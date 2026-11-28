package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ombg/portfolio-tracker/services/api/internal/market"
)

// Market data status (FR-6, AC-12, D-39).
type marketHandlers struct{ store *market.Store }

func (h *marketHandlers) routes(r chi.Router) {
	r.Get("/api/market/status", h.status)
}

func (h *marketHandlers) status(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	out, err := h.store.Status(r.Context(), userID)
	if err != nil {
		internalError(w, "market status", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
