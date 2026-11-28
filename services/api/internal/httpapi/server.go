// Package httpapi holds the portfolio-api HTTP routes. Caddy proxies /api/* here.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ombg/portfolio-tracker/services/api/internal/accounts"
	"github.com/ombg/portfolio-tracker/services/api/internal/auth"
	"github.com/ombg/portfolio-tracker/services/api/internal/holdings"
	"github.com/ombg/portfolio-tracker/services/api/internal/importer"
	"github.com/ombg/portfolio-tracker/services/api/internal/imports"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

type Deps struct {
	DB           Pinger
	Auth         *auth.Store
	Signer       *auth.Signer
	Accounts     *accounts.Store
	Imports      *imports.Store
	Holdings     *holdings.Store
	MarketData   *marketdata.Client // nil: no backfill request after a commit
	Importer     *importer.Client   // nil: uploads and corrections answer 503
	CookieSecure bool               // false only for plain-HTTP development outside localhost
}

// NewRouter returns the API router.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(requireJSONForWrites)

	r.Get("/api/health", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		// A broken database is reported as such, never as healthy (NFR-4).
		if err := d.DB.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "unreachable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "database": "ok"})
	})

	if d.Auth != nil && d.Signer != nil {
		a := &authHandlers{store: d.Auth, signer: d.Signer, secure: d.CookieSecure}
		r.Post("/api/auth/login", a.login)
		r.Post("/api/auth/logout", a.logout)
		r.Group(func(r chi.Router) {
			r.Use(a.requireUser)
			r.Get("/api/me", a.me)
			if d.Accounts != nil {
				(&accountHandlers{store: d.Accounts}).routes(r)
			}
			if d.Imports != nil {
				(&importHandlers{store: d.Imports, importer: d.Importer, marketData: d.MarketData}).routes(r)
			}
			if d.Holdings != nil {
				(&holdingHandlers{store: d.Holdings}).routes(r)
			}
		})
	}
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends {"error": code, "message": text}; code is stable for the web client.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
