package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/importer"
	"github.com/ombg/portfolio-tracker/services/api/internal/imports"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

// MaxUploadBytes matches the importer's limit (25 MiB); the importer enforces it exactly, the API stops a body
// that is clearly larger (plus room for the multipart framing).
const MaxUploadBytes = 25 << 20

// UploadHeader must accompany every multipart upload. A cross-site form cannot set a header and a cross-site
// fetch with one needs a CORS preflight this API never grants, so it replaces the application/json rule for this
// one route (D-33, D-36).
const UploadHeader = "X-Requested-With"
const UploadHeaderValue = "portfolio-tracker"

type importHandlers struct {
	store      *imports.Store
	importer   *importer.Client   // nil when SERVICE_TOKEN is not configured
	marketData *marketdata.Client // nil: no backfill request after a commit
}

func (h *importHandlers) routes(r chi.Router) {
	r.Get("/api/imports", h.list)
	r.Post("/api/imports", h.upload)
	r.Get("/api/imports/{id}", h.get)
	r.Patch("/api/imports/{id}/rows/{row}", h.editRow)
	r.Post("/api/imports/{id}/reparse", h.reparse)
	r.Post("/api/imports/{id}/discard", h.discard)
	r.Post("/api/imports/{id}/commit", h.commit)
}

func (h *importHandlers) list(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	out, err := h.store.List(r.Context(), userID)
	if err != nil {
		internalError(w, "list imports", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": out})
}

func (h *importHandlers) get(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := importID(w, r)
	if !ok {
		return
	}
	d, err := h.store.Get(r.Context(), userID, id)
	if err != nil {
		writeImportError(w, "get import", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *importHandlers) discard(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := importID(w, r)
	if !ok {
		return
	}
	s, err := h.store.Discard(r.Context(), userID, id)
	if err != nil {
		writeImportError(w, "discard import", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// commit writes the batch into the ledger (D-8, D-37); it needs no importer.
func (h *importHandlers) commit(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := importID(w, r)
	if !ok {
		return
	}
	res, err := h.store.Commit(r.Context(), userID, id)
	if err != nil {
		writeImportError(w, "commit import", err)
		return
	}
	h.marketData.BackfillSoon() // new securities get their history now, not at the next scheduled run (D-39)
	s, err := h.store.Summary(r.Context(), userID, id)
	if err != nil {
		writeImportError(w, "commit import", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"import": s, "result": res})
}

func (h *importHandlers) reparse(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := importID(w, r)
	if !ok || !h.available(w) {
		return
	}
	if err := h.store.CheckOpen(r.Context(), userID, id); err != nil {
		writeImportError(w, "reparse import", err)
		return
	}
	res, err := h.importer.Reparse(r.Context(), userID, id)
	if importerRefused(w, "reparse import", res, err) {
		return
	}
	h.writeSummary(w, r, userID, id)
}

// rowEdit is the PATCH body. type, security and excluded go to the importer (saved as rules or a row edit);
// account_id links an untracked account number to an existing account here, then the batch is parsed again.
type rowEdit struct {
	Type      *string          `json:"type,omitempty"`
	Security  *json.RawMessage `json:"security,omitempty"`
	Excluded  *bool            `json:"excluded,omitempty"`
	AccountID *uuid.UUID       `json:"account_id,omitempty"`
}

func (h *importHandlers) editRow(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := importID(w, r)
	if !ok {
		return
	}
	rowNo, err := strconv.ParseInt(chi.URLParam(r, "row"), 10, 32)
	if err != nil || rowNo < 1 {
		writeError(w, http.StatusNotFound, "not_found", "row not found")
		return
	}
	var in rowEdit
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Type == nil && in.Security == nil && in.Excluded == nil && in.AccountID == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "nothing to change: give type, security, excluded or account_id")
		return
	}
	if !h.available(w) {
		return
	}
	ctx := r.Context()
	if in.AccountID != nil {
		err = h.store.LinkAccount(ctx, userID, id, int32(rowNo), *in.AccountID)
	} else {
		err = h.store.CheckOpen(ctx, userID, id)
	}
	if err != nil {
		writeImportError(w, "edit import row", err)
		return
	}

	var res importer.Response
	if in.Type != nil || in.Security != nil || in.Excluded != nil {
		body, _ := json.Marshal(rowEdit{Type: in.Type, Security: in.Security, Excluded: in.Excluded})
		res, err = h.importer.EditRow(ctx, userID, id, int32(rowNo), body)
	} else {
		res, err = h.importer.Reparse(ctx, userID, id)
	}
	if importerRefused(w, "edit import row", res, err) {
		return
	}
	s, err := h.store.Summary(ctx, userID, id)
	if err != nil {
		writeImportError(w, "edit import row", err)
		return
	}
	row, err := h.store.Row(ctx, userID, id, int32(rowNo))
	if err != nil {
		writeImportError(w, "edit import row", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"import": s, "row": row})
}

// upload re-encodes the browser's multipart body part by part into a pipe to the importer, so the file is never
// held in memory or on the API's disk. Only the fields broker and account_id and one part file are passed on.
func (h *importHandlers) upload(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	if !h.available(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected a multipart/form-data body")
		return
	}
	pr, pw := io.Pipe()
	out := multipart.NewWriter(pw)
	copied := make(chan error, 1)
	go func() {
		err := copyParts(mr, out)
		if err == nil {
			err = out.Close()
		}
		_ = pw.CloseWithError(err)
		copied <- err
	}()
	res, err := h.importer.Upload(r.Context(), userID, out.FormDataContentType(), pr)
	_ = pr.CloseWithError(errors.New("importer finished")) // unblocks the copy if the importer stopped reading
	copyErr := <-copied

	var tooBig *http.MaxBytesError
	var bad badPartError
	switch {
	case errors.As(copyErr, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "files are limited to 25 MiB")
		return
	case errors.As(copyErr, &bad):
		writeError(w, http.StatusBadRequest, "bad_request", bad.Error())
		return
	}
	if importerRefused(w, "upload import", res, err) {
		return
	}
	var created struct {
		BatchID uuid.UUID `json:"batch_id"`
	}
	if err := json.Unmarshal(res.Body, &created); err != nil || res.Status != http.StatusCreated {
		internalError(w, "upload import", fmt.Errorf("unexpected importer answer %d", res.Status))
		return
	}
	w.Header().Set("Location", "/api/imports/"+created.BatchID.String())
	relay(w, res)
}

type badPartError string

func (e badPartError) Error() string { return string(e) }

func copyParts(mr *multipart.Reader, out *multipart.Writer) error {
	files := 0
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				return err
			}
			return badPartError("malformed multipart body")
		}
		switch name := p.FormName(); name {
		case "broker", "account_id":
			v, err := io.ReadAll(io.LimitReader(p, 256))
			if err != nil {
				return err
			}
			if err := out.WriteField(name, string(v)); err != nil {
				return err
			}
		case "file":
			if files++; files > 1 {
				return badPartError("send one file per upload")
			}
			hdr := textproto.MIMEHeader{}
			hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, quoteFilename(p.FileName())))
			ct := p.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/octet-stream"
			}
			hdr.Set("Content-Type", ct)
			dst, err := out.CreatePart(hdr)
			if err != nil {
				return err
			}
			if _, err := io.Copy(dst, p); err != nil {
				return err
			}
		default:
			return badPartError(fmt.Sprintf("unknown field %q", name))
		}
	}
	if files == 0 {
		return badPartError("invalid or missing: file")
	}
	return nil
}

// quoteFilename keeps the browser's file name readable but unable to break out of the header.
func quoteFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, s)
	if s == "" {
		return "upload"
	}
	return s
}

func (h *importHandlers) available(w http.ResponseWriter) bool {
	if h.importer == nil {
		writeError(w, http.StatusServiceUnavailable, "importer_unavailable", "the importer is not configured (SERVICE_TOKEN)")
		return false
	}
	return true
}

// importerRefused answers for the importer when it failed (502) or refused (its own 4xx, same error shape) and
// reports whether it did.
func importerRefused(w http.ResponseWriter, op string, res importer.Response, err error) bool {
	if err != nil {
		slog.Error(op+" failed", "error", err)
		writeError(w, http.StatusBadGateway, "importer_unavailable", "the importer did not answer; try again")
		return true
	}
	if res.Status >= 300 {
		relay(w, res)
		return true
	}
	return false
}

func relay(w http.ResponseWriter, res importer.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (h *importHandlers) writeSummary(w http.ResponseWriter, r *http.Request, userID, id uuid.UUID) {
	s, err := h.store.Summary(r.Context(), userID, id)
	if err != nil {
		writeImportError(w, "get import", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func writeImportError(w http.ResponseWriter, op string, err error) {
	var e *imports.Error
	if errors.As(err, &e) {
		body := map[string]any{"error": e.Code, "message": e.Message}
		for k, v := range e.Extra {
			body[k] = v
		}
		writeJSON(w, e.Status, body)
		return
	}
	internalError(w, op, err)
}

func importID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "import not found")
		return uuid.Nil, false
	}
	return id, true
}
