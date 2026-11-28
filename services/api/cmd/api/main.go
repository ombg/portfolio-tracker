// Command api runs portfolio-api: it applies migrations at start-up, then serves /api.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ombg/portfolio-tracker/services/api/internal/accounts"
	"github.com/ombg/portfolio-tracker/services/api/internal/auth"
	"github.com/ombg/portfolio-tracker/services/api/internal/config"
	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/holdings"
	"github.com/ombg/portfolio-tracker/services/api/internal/httpapi"
	"github.com/ombg/portfolio-tracker/services/api/internal/importer"
	"github.com/ombg/portfolio-tracker/services/api/internal/imports"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

func main() {
	if err := run(); err != nil {
		slog.Error("portfolio-api stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(cfg.DatabaseURL, cfg.MigrationsDir); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.SetImporterLogin(ctx, pool, cfg.ImporterDBPassword); err != nil {
		return err
	}
	if err := db.SetServiceLogin(ctx, pool, db.MarketRole, "MARKET_DB_PASSWORD", cfg.MarketDBPassword); err != nil {
		return err
	}
	// `api migrate` applies migrations and exits (make migrate); otherwise serve.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		slog.Info("migrations applied")
		return nil
	}
	signer, err := auth.NewSigner(cfg.SessionSecret)
	if err != nil {
		return err
	}

	var imp *importer.Client
	var md *marketdata.Client
	if cfg.ServiceToken != "" {
		imp = importer.New(cfg.ImporterURL, cfg.ServiceToken)
		md = marketdata.New(cfg.MarketDataURL, cfg.ServiceToken)
	} else {
		slog.Warn("SERVICE_TOKEN is not set: uploads and import corrections are unavailable")
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			DB: pool, Auth: auth.NewStore(pool), Signer: signer, CookieSecure: cfg.CookieSecure,
			Accounts: &accounts.Store{Pool: pool}, Imports: &imports.Store{Pool: pool}, Importer: imp,
			Holdings: &holdings.Store{Pool: pool, Market: md}, MarketData: md,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("portfolio-api listening", "addr", cfg.ListenAddr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
