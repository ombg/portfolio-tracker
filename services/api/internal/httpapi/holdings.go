package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/holdings"
)

// Holdings and security detail (FR-7, D-38).
type holdingHandlers struct{ store *holdings.Store }

func (h *holdingHandlers) routes(r chi.Router) {
	r.Get("/api/holdings", h.holdings)
	r.Get("/api/securities/{id}", h.security)
}

func (h *holdingHandlers) holdings(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	scope, err := holdings.ParseScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeHoldingsError(w, "parse scope", err)
		return
	}
	out, err := h.store.Holdings(r.Context(), userID, scope)
	if err != nil {
		writeHoldingsError(w, "holdings", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *holdingHandlers) security(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "security not found")
		return
	}
	out, err := h.store.Security(r.Context(), userID, id)
	if err != nil {
		writeHoldingsError(w, "security", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeHoldingsError(w http.ResponseWriter, op string, err error) {
	if e, ok := holdings.IsError(err); ok {
		writeError(w, e.Status, e.Code, e.Message)
		return
	}
	internalError(w, op, err)
}
