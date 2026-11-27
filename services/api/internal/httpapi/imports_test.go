package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/importer"
	"github.com/ombg/portfolio-tracker/services/api/internal/imports"
)

// Synthetic batches only (D-20): invented account numbers, file names and rows.

const testServiceToken = "test-service-token-of-at-least-32-bytes"

// fakeImporter records what portfolio-api sends to the importer and answers with a canned response.
type fakeImporter struct {
	mu       sync.Mutex
	calls    []fakeCall
	status   int
	response string
	srv      *httptest.Server
}

type fakeCall struct {
	Method, Path, Token, UserID, ContentType string
	Body                                     []byte
}

func newFakeImporter(t *testing.T) *fakeImporter {
	f := &fakeImporter{status: http.StatusOK, response: `{"status":"parsed"}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{r.Method, r.URL.Path, r.Header.Get("X-Service-Token"),
			r.Header.Get("X-User-Id"), r.Header.Get("Content-Type"), body})
		status, resp := f.status, f.response
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeImporter) answer(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.response = status, body
}

func (f *fakeImporter) recorded() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

type importHarness struct {
	*harness
	fake  *fakeImporter
	userA uuid.UUID
	a     *http.Cookie
}

func newImportHarness(t *testing.T) *importHarness {
	h := newHarness(t)
	fake := newFakeImporter(t)
	h.deps.Imports = &imports.Store{Pool: h.store.Pool}
	h.deps.Importer = importer.New(fake.srv.URL, testServiceToken)
	h.router = NewRouter(h.deps)
	ih := &importHarness{harness: h, fake: fake, a: h.signIn(testEmail, testPassword)}
	ih.userA = ih.userID(testEmail)
	return ih
}

func (h *importHarness) userID(email string) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	if err := h.store.Pool.QueryRow(context.Background(), "SELECT id FROM core.users WHERE email = $1", email).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

type seedRow struct {
	no                 int
	status, normalized string
}

// seedBatch stores a batch as the importer would have, as the owner.
func (h *importHarness) seedBatch(user uuid.UUID, broker, status string, rows ...seedRow) uuid.UUID {
	h.t.Helper()
	ctx := context.Background()
	pool := h.store.Pool
	var fileID, batchID uuid.UUID
	sha := strings.Repeat(uuid.NewString()[:8], 8)
	if err := pool.QueryRow(ctx, `INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type,
		stored_path) VALUES ($1, $2, 'synthetic.csv', 42, 'text/csv', 'ab/x.csv') RETURNING id`, user, sha).Scan(&fileID); err != nil {
		h.t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.status]++
	}
	cj, _ := json.Marshal(counts)
	if err := pool.QueryRow(ctx, `INSERT INTO imports.import_batches (user_id, source_file_id, broker_id, parser,
		parser_version, status, counts) SELECT $1, $2, id, 'synthetic', '1', $3, $4 FROM core.brokers WHERE code = $5
		RETURNING id`, user, fileID, status, cj, broker).Scan(&batchID); err != nil {
		h.t.Fatal(err)
	}
	for _, r := range rows {
		msgs := "[]"
		if r.status == "error" {
			msgs = `[{"level":"error","code":"unmapped_action","text":"no mapping"}]`
		}
		var normalized any
		if r.normalized != "" {
			normalized = r.normalized
		}
		if _, err := pool.Exec(ctx, `INSERT INTO imports.import_rows (batch_id, row_no, user_id, raw, normalized, status,
			messages) VALUES ($1, $2, $3, '{"Action":"synthetic"}', $4, $5, $6)`, batchID, r.no, user, normalized,
			r.status, msgs); err != nil {
			h.t.Fatal(err)
		}
	}
	return batchID
}

const untrackedRow = `{"untracked_account":{"number":"90000009","account_type":"Individual TFSA","proposed_type":"TFSA","proposed_management":"self_directed"}}`

// previewBatch: one ok row for the account, one error, two rows of an untracked account.
func (h *importHarness) previewBatch(accountID uuid.UUID) uuid.UUID {
	return h.seedBatch(h.userA, "questrade", "needs_review",
		seedRow{2, "ok", `{"account_id":"` + accountID.String() + `","type":"DEPOSIT"}`},
		seedRow{3, "error", ""},
		seedRow{4, "skipped", untrackedRow},
		seedRow{5, "skipped", untrackedRow},
	)
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if code != "" {
		if got := errorCode(t, rec); got != code {
			t.Fatalf("error = %q, want %q", got, code)
		}
	}
}

func TestImportHistoryAndPreview(t *testing.T) {
	h := newImportHarness(t)
	acct := h.create(h.a, `{"broker":"questrade","name":"Synthetic TFSA","type":"TFSA","external_number":"90000001"}`)
	other := h.create(h.a, `{"broker":"questrade","name":"Synthetic RRSP","type":"RRSP","external_number":"90000002"}`)
	id := h.seedBatch(h.userA, "questrade", "needs_review",
		seedRow{2, "ok", `{"account_id":"` + acct.ID.String() + `","type":"DEPOSIT"}`},
		seedRow{3, "error", ""},
		seedRow{4, "skipped", untrackedRow},
		seedRow{5, "skipped", untrackedRow},
		seedRow{6, "excluded", `{"account_id":"` + other.ID.String() + `","type":"FEE"}`}, // left out: not affected
	)

	rec := h.do(http.MethodGet, "/api/imports", "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	list := decodeJSON[struct{ Imports []imports.Summary }](t, rec).Imports
	if len(list) != 1 {
		t.Fatalf("history has %d imports, want 1", len(list))
	}
	s := list[0]
	if s.ID != id || s.Status != "needs_review" || s.Broker.Code != "questrade" || s.File.Name != "synthetic.csv" || s.CanCommit {
		t.Errorf("summary = %+v", s)
	}
	var accts []struct {
		ID   uuid.UUID
		Name string
	}
	if err := json.Unmarshal(s.Accounts, &accts); err != nil || len(accts) != 1 || accts[0].ID != acct.ID {
		t.Errorf("accounts = %s, want only %s", s.Accounts, acct.ID)
	}
	if string(s.Counts) != `{"ok":1,"error":1,"skipped":2,"excluded":1}` {
		t.Errorf("counts = %s", s.Counts)
	}

	rec = h.do(http.MethodGet, "/api/imports/"+id.String(), "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	d := decodeJSON[imports.Detail](t, rec)
	if len(d.Rows) != 5 || d.Rows[0].RowNo != 2 || d.Rows[1].Status != "error" || string(d.Rows[1].Normalized) != "null" {
		t.Errorf("rows = %+v", d.Rows)
	}
	if len(d.Untracked) != 1 || d.Untracked[0].Number != "90000009" || d.Untracked[0].Rows != 2 ||
		d.Untracked[0].Proposal["proposed_type"] != "TFSA" {
		t.Errorf("untracked = %+v", d.Untracked)
	}

	// Another user sees nothing and gets 404 for this batch, exactly like a missing one.
	b := h.secondUser()
	rec = h.do(http.MethodGet, "/api/imports", "", b)
	if got := decodeJSON[struct{ Imports []imports.Summary }](t, rec).Imports; len(got) != 0 {
		t.Errorf("second user sees %d imports", len(got))
	}
	for _, path := range []string{"/api/imports/" + id.String(), "/api/imports/" + uuid.NewString(), "/api/imports/not-a-uuid"} {
		wantStatus(t, h.do(http.MethodGet, path, "", b), http.StatusNotFound, "not_found")
	}
	wantStatus(t, h.do(http.MethodGet, "/api/imports", "", nil), http.StatusUnauthorized, "unauthenticated")
}

func TestCanCommitOnceErrorsAreGone(t *testing.T) {
	h := newImportHarness(t)
	acct := h.create(h.a, `{"broker":"questrade","name":"Synthetic TFSA","type":"TFSA","external_number":"90000001"}`)
	ok := `{"account_id":"` + acct.ID.String() + `"}`
	for _, tc := range []struct {
		name   string
		status string
		rows   []seedRow
		want   bool
	}{
		{"error row blocks", "needs_review", []seedRow{{2, "ok", ok}, {3, "error", ""}}, false},
		{"excluded error does not", "parsed", []seedRow{{2, "ok", ok}, {3, "excluded", ""}}, true},
		{"warnings do not", "needs_review", []seedRow{{2, "warning", ok}}, true},
		{"nothing to commit", "parsed", []seedRow{{2, "skipped", untrackedRow}}, false},
		{"discarded", "discarded", []seedRow{{2, "ok", ok}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := h.seedBatch(h.userA, "questrade", tc.status, tc.rows...)
			rec := h.do(http.MethodGet, "/api/imports/"+id.String(), "", h.a)
			if got := decodeJSON[imports.Detail](t, rec).CanCommit; got != tc.want {
				t.Errorf("can_commit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscardImport(t *testing.T) {
	h := newImportHarness(t)
	id := h.seedBatch(h.userA, "questrade", "needs_review", seedRow{2, "error", ""})
	b := h.secondUser()
	path := "/api/imports/" + id.String() + "/discard"

	wantStatus(t, h.do(http.MethodPost, path, "", b), http.StatusNotFound, "not_found")
	rec := h.do(http.MethodPost, path, "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	if s := decodeJSON[imports.Summary](t, rec); s.Status != "discarded" || s.CanCommit {
		t.Errorf("after discard: %+v", s)
	}
	wantStatus(t, h.do(http.MethodPost, path, "", h.a), http.StatusConflict, "batch_closed")
	if n := len(h.fake.recorded()); n != 0 {
		t.Errorf("discard called the importer %d times", n)
	}
}

func (h *importHarness) upload(cookie *http.Cookie, withHeader bool, build func(*multipart.Writer)) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	build(mw)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/imports", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if withHeader {
		req.Header.Set(UploadHeader, UploadHeaderValue)
	}
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func synthFile(name, content string) func(*multipart.Writer) {
	return func(mw *multipart.Writer) {
		_ = mw.WriteField("broker", "questrade")
		_ = mw.WriteField("account_id", "20000000-0000-0000-0000-0000000000a1")
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = io.WriteString(fw, content)
	}
}

func TestUploadIsStreamedToTheImporter(t *testing.T) {
	h := newImportHarness(t)
	batch := uuid.New()
	h.fake.answer(http.StatusCreated, `{"batch_id":"`+batch.String()+`","source_file_id":"x","status":"parsed","counts":{"ok":2}}`)
	const content = "Transaction Date,Action\n2025-01-02,CON\n"

	rec := h.upload(h.a, true, synthFile(`act"ivities.csv`, content))
	wantStatus(t, rec, http.StatusCreated, "")
	if loc := rec.Header().Get("Location"); loc != "/api/imports/"+batch.String() {
		t.Errorf("Location = %q", loc)
	}
	if body := decodeJSON[map[string]any](t, rec); body["status"] != "parsed" {
		t.Errorf("body = %v", body)
	}

	calls := h.fake.recorded()
	if len(calls) != 1 {
		t.Fatalf("importer called %d times", len(calls))
	}
	c := calls[0]
	if c.Method != http.MethodPost || c.Path != "/internal/imports" || c.Token != testServiceToken || c.UserID != h.userA.String() {
		t.Errorf("call = %s %s token %q user %q", c.Method, c.Path, c.Token, c.UserID)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(c.Body))
	req.Header.Set("Content-Type", c.ContentType)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	if req.FormValue("broker") != "questrade" || req.FormValue("account_id") != "20000000-0000-0000-0000-0000000000a1" {
		t.Errorf("fields = %v", req.MultipartForm.Value)
	}
	f, fh, err := req.FormFile("file")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	if string(got) != content || fh.Filename != "act_ivities.csv" {
		t.Errorf("file %q = %q", fh.Filename, got)
	}
}

func TestUploadRefusals(t *testing.T) {
	h := newImportHarness(t)

	t.Run("CSRF: multipart without the upload header", func(t *testing.T) {
		wantStatus(t, h.upload(h.a, false, synthFile("a.csv", "x")), http.StatusUnsupportedMediaType, "unsupported_media_type")
	})
	t.Run("CSRF: the header does not open other routes to multipart", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("name", "x")
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/accounts", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set(UploadHeader, UploadHeaderValue)
		req.AddCookie(h.a)
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, req)
		wantStatus(t, rec, http.StatusUnsupportedMediaType, "unsupported_media_type")
	})
	t.Run("unknown field", func(t *testing.T) {
		rec := h.upload(h.a, true, func(mw *multipart.Writer) {
			_ = mw.WriteField("user_id", uuid.NewString())
			synthFile("a.csv", "x")(mw)
		})
		wantStatus(t, rec, http.StatusBadRequest, "bad_request")
	})
	t.Run("no file", func(t *testing.T) {
		rec := h.upload(h.a, true, func(mw *multipart.Writer) { _ = mw.WriteField("broker", "questrade") })
		wantStatus(t, rec, http.StatusBadRequest, "bad_request")
	})
	t.Run("two files", func(t *testing.T) {
		rec := h.upload(h.a, true, func(mw *multipart.Writer) {
			synthFile("a.csv", "x")(mw)
			fw, _ := mw.CreateFormFile("file", "b.csv")
			_, _ = io.WriteString(fw, "y")
		})
		wantStatus(t, rec, http.StatusBadRequest, "bad_request")
	})
	t.Run("too large", func(t *testing.T) {
		big := strings.Repeat("x", MaxUploadBytes+(2<<20))
		wantStatus(t, h.upload(h.a, true, synthFile("a.csv", big)), http.StatusRequestEntityTooLarge, "file_too_large")
	})
	t.Run("importer refusal is passed on", func(t *testing.T) {
		h.fake.answer(http.StatusConflict, `{"error":"duplicate_file","message":"this file was already imported","batch_id":"b1"}`)
		rec := h.upload(h.a, true, synthFile("a.csv", "x"))
		wantStatus(t, rec, http.StatusConflict, "")
		if body := decodeJSON[map[string]any](t, rec); body["error"] != "duplicate_file" || body["batch_id"] != "b1" {
			t.Errorf("body = %v", body)
		}
	})
	t.Run("importer failure is 502 without details", func(t *testing.T) {
		h.fake.answer(http.StatusInternalServerError, `{"detail":"traceback with internals"}`)
		rec := h.upload(h.a, true, synthFile("a.csv", "x"))
		wantStatus(t, rec, http.StatusBadGateway, "")
		if strings.Contains(rec.Body.String(), "traceback") {
			t.Errorf("importer internals leaked: %s", rec.Body.String())
		}
	})
	t.Run("importer down", func(t *testing.T) {
		h.deps.Importer = importer.New("http://127.0.0.1:1", testServiceToken)
		h.router = NewRouter(h.deps)
		wantStatus(t, h.upload(h.a, true, synthFile("a.csv", "x")), http.StatusBadGateway, "importer_unavailable")
	})
	t.Run("importer not configured", func(t *testing.T) {
		h.deps.Importer = nil
		h.router = NewRouter(h.deps)
		wantStatus(t, h.upload(h.a, true, synthFile("a.csv", "x")), http.StatusServiceUnavailable, "importer_unavailable")
	})
}

func TestEditRowGoesThroughTheImporter(t *testing.T) {
	h := newImportHarness(t)
	acct := h.create(h.a, `{"broker":"questrade","name":"Synthetic TFSA","type":"TFSA","external_number":"90000001"}`)
	id := h.previewBatch(acct.ID)
	path := "/api/imports/" + id.String() + "/rows/3"

	for _, body := range []string{`{"type":"FEE"}`, `{"security":{"symbol":"QZPF","exchange":"TSX"}}`, `{"excluded":true}`} {
		rec := h.do(http.MethodPatch, path, body, h.a)
		wantStatus(t, rec, http.StatusOK, "")
		out := decodeJSON[struct {
			Import imports.Summary
			Row    imports.Row
		}](t, rec)
		if out.Import.ID != id || out.Row.RowNo != 3 {
			t.Errorf("response = %+v", out)
		}
		calls := h.fake.recorded()
		c := calls[len(calls)-1]
		if c.Method != http.MethodPatch || c.Path != "/internal/imports/"+id.String()+"/rows/3" ||
			c.UserID != h.userA.String() || string(c.Body) != body {
			t.Errorf("forwarded %s %s %s, want body %s", c.Method, c.Path, c.Body, body)
		}
	}

	h.fake.answer(http.StatusUnprocessableEntity, `{"error":"invalid_type","message":"'TIP' is not a transaction type"}`)
	wantStatus(t, h.do(http.MethodPatch, path, `{"type":"TIP"}`, h.a), http.StatusUnprocessableEntity, "invalid_type")

	before := len(h.fake.recorded())
	wantStatus(t, h.do(http.MethodPatch, path, `{}`, h.a), http.StatusBadRequest, "bad_request")
	wantStatus(t, h.do(http.MethodPatch, path, `{"note":"x"}`, h.a), http.StatusBadRequest, "bad_request")
	wantStatus(t, h.do(http.MethodPatch, "/api/imports/"+id.String()+"/rows/x", `{"excluded":true}`, h.a), http.StatusNotFound, "not_found")
	b := h.secondUser()
	wantStatus(t, h.do(http.MethodPatch, path, `{"excluded":true}`, b), http.StatusNotFound, "not_found")
	wantStatus(t, h.do(http.MethodPost, "/api/imports/"+id.String()+"/reparse", ``, b), http.StatusNotFound, "not_found")

	closed := h.seedBatch(h.userA, "questrade", "discarded", seedRow{2, "error", ""})
	wantStatus(t, h.do(http.MethodPatch, "/api/imports/"+closed.String()+"/rows/2", `{"excluded":true}`, h.a), http.StatusConflict, "batch_closed")
	wantStatus(t, h.do(http.MethodPost, "/api/imports/"+closed.String()+"/reparse", ``, h.a), http.StatusConflict, "batch_closed")
	if after := len(h.fake.recorded()); after != before {
		t.Errorf("refused requests reached the importer %d times", after-before)
	}
}

func TestReparse(t *testing.T) {
	h := newImportHarness(t)
	id := h.seedBatch(h.userA, "questrade", "needs_review", seedRow{2, "skipped", untrackedRow})
	rec := h.do(http.MethodPost, "/api/imports/"+id.String()+"/reparse", ``, h.a)
	wantStatus(t, rec, http.StatusOK, "")
	if s := decodeJSON[imports.Summary](t, rec); s.ID != id {
		t.Errorf("summary = %+v", s)
	}
	if c := h.fake.recorded(); len(c) != 1 || c[0].Path != "/internal/imports/"+id.String()+"/reparse" || c[0].UserID != h.userA.String() {
		t.Errorf("calls = %+v", c)
	}
}

func TestLinkUntrackedAccountNumber(t *testing.T) {
	h := newImportHarness(t)
	tracked := h.create(h.a, `{"broker":"questrade","name":"Synthetic TFSA","type":"TFSA","external_number":"90000001"}`)
	id := h.previewBatch(tracked.ID)
	path := "/api/imports/" + id.String() + "/rows/4"
	numberless := h.create(h.a, `{"broker":"questrade","name":"Synthetic second TFSA","type":"TFSA"}`)
	otherBroker := h.create(h.a, `{"broker":"wealthsimple","name":"Synthetic WS","type":"TFSA"}`)
	b := h.secondUser()
	bAccount := h.create(b, `{"broker":"questrade","name":"B TFSA","type":"TFSA"}`)

	for _, tc := range []struct {
		name, path string
		account    uuid.UUID
		status     int
		code       string
	}{
		{"row of a tracked account", "/api/imports/" + id.String() + "/rows/2", numberless.ID, 422, "not_applicable"},
		{"account at another broker", path, otherBroker.ID, 422, "account_broker_mismatch"},
		{"account with another number", path, tracked.ID, 409, "account_has_number"},
		{"another user's account", path, bAccount.ID, 422, "unknown_account"},
		{"missing row", "/api/imports/" + id.String() + "/rows/99", numberless.ID, 404, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantStatus(t, h.do(http.MethodPatch, tc.path, `{"account_id":"`+tc.account.String()+`"}`, h.a), tc.status, tc.code)
		})
	}
	if n := len(h.fake.recorded()); n != 0 {
		t.Fatalf("refused links reached the importer %d times", n)
	}

	rec := h.do(http.MethodPatch, path, `{"account_id":"`+numberless.ID.String()+`"}`, h.a)
	wantStatus(t, rec, http.StatusOK, "")
	got := decodeAccount(t, h.do(http.MethodGet, "/api/accounts/"+numberless.ID.String(), "", h.a))
	if got.ExternalNumber == nil || *got.ExternalNumber != "90000009" {
		t.Errorf("external_number = %v, want 90000009", got.ExternalNumber)
	}
	if c := h.fake.recorded(); len(c) != 1 || c[0].Path != "/internal/imports/"+id.String()+"/reparse" {
		t.Errorf("after linking, importer calls = %+v, want one reparse", c)
	}
	// Linking the same number again is harmless.
	wantStatus(t, h.do(http.MethodPatch, path, `{"account_id":"`+numberless.ID.String()+`"}`, h.a), http.StatusOK, "")
}
