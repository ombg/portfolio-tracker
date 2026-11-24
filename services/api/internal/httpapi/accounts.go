package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/accounts"
)

type accountHandlers struct{ store *accounts.Store }

func (h *accountHandlers) routes(r chi.Router) {
	r.Get("/api/brokers", h.brokers)
	r.Get("/api/currencies", h.currencies)
	r.Get("/api/accounts", h.list)
	r.Post("/api/accounts", h.create)
	r.Get("/api/accounts/{id}", h.get)
	r.Patch("/api/accounts/{id}", h.update)
}

func (h *accountHandlers) list(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	out, err := h.store.List(r.Context(), userID, includeArchived)
	if err != nil {
		internalError(w, "list accounts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (h *accountHandlers) get(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := accountID(w, r)
	if !ok {
		return
	}
	a, err := h.store.Get(r.Context(), userID, id)
	if err != nil {
		h.writeStoreError(w, "get account", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *accountHandlers) create(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	var in accounts.CreateInput
	if !decodeBody(w, r, &in) {
		return
	}
	a, err := h.store.Create(r.Context(), userID, in)
	if err != nil {
		h.writeStoreError(w, "create account", err)
		return
	}
	w.Header().Set("Location", "/api/accounts/"+a.ID.String())
	writeJSON(w, http.StatusCreated, a)
}

func (h *accountHandlers) update(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, ok := accountID(w, r)
	if !ok {
		return
	}
	var p accounts.PatchInput
	if !decodeBody(w, r, &p) {
		return
	}
	a, err := h.store.Update(r.Context(), userID, id, p)
	if err != nil {
		h.writeStoreError(w, "update account", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *accountHandlers) brokers(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	out, err := h.store.Brokers(r.Context(), userID)
	if err != nil {
		internalError(w, "list brokers", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"brokers": out})
}

func (h *accountHandlers) currencies(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	out, err := h.store.Currencies(r.Context(), userID)
	if err != nil {
		internalError(w, "list currencies", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"currencies": out})
}

func (h *accountHandlers) writeStoreError(w http.ResponseWriter, op string, err error) {
	var ve *accounts.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "validation_failed", "field": ve.Field, "message": ve.Message})
	case errors.Is(err, accounts.ErrNotFound):
		// Another user's account looks exactly like a missing one (FR-1).
		writeError(w, http.StatusNotFound, "not_found", "account not found")
	case errors.Is(err, accounts.ErrDuplicateNumber):
		writeError(w, http.StatusConflict, "duplicate_account_number", err.Error())
	default:
		internalError(w, op, err)
	}
}

func accountID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "account not found")
		return uuid.Nil, false
	}
	return id, true
}

// decodeBody reads a JSON object of at most 128 KiB and rejects unknown fields.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		msg := "request body must be a JSON object with known fields"
		if s := err.Error(); strings.HasPrefix(s, "json: unknown field") || strings.Contains(s, "must be a date") {
			msg = s
		}
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "bad_request", "request body must be a single JSON object")
		return false
	}
	return true
}

func internalError(w http.ResponseWriter, op string, err error) {
	slog.Error(op+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal", op+" failed")
}
