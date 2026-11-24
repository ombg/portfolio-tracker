// Package httpapi holds the portfolio-api HTTP routes. Caddy proxies /api/* here.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewRouter returns the API router.
func NewRouter(db Pinger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Get("/api/health", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		// A broken database is reported as such, never as healthy (NFR-4).
		if err := db.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "unreachable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "ok"})
	})
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
