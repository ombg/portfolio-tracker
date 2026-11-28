// Package config reads portfolio-api settings from the environment (specs/architecture.md).
package config

import (
	"errors"
	"os"
)

type Config struct {
	DatabaseURL   string // postgres:// URL; the API connects as the database owner
	MigrationsDir string // path to db/migrations
	ListenAddr    string
	SessionSecret string // signs session cookies; at least 32 bytes
	CookieSecure  bool   // true unless COOKIE_SECURE=false; Compose sets false for plain HTTP on loopback (D-34)
	// ImporterDBPassword is the pt_importer role's password, set after migrations; empty disables its login (D-35).
	ImporterDBPassword string
	// MarketDBPassword is the pt_market role's password, likewise (D-39).
	MarketDBPassword string
	// MarketDataURL reaches market-data's internal API (backfill after a commit); needs ServiceToken.
	MarketDataURL string
	// ImporterURL and ServiceToken reach the importer's internal API; without a token, uploads answer 503.
	ImporterURL  string
	ServiceToken string
}

// FromEnv reads the configuration. DATABASE_URL is always required; SESSION_SECRET is
// required to serve (checked when the signer is built), not for `api migrate`.
func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		MigrationsDir: getenv("MIGRATIONS_DIR", "/migrations"),
		ListenAddr:    getenv("LISTEN_ADDR", ":8080"),
		SessionSecret: os.Getenv("SESSION_SECRET"),
		CookieSecure:  os.Getenv("COOKIE_SECURE") != "false",

		ImporterDBPassword: os.Getenv("IMPORTER_DB_PASSWORD"),
		MarketDBPassword:   os.Getenv("MARKET_DB_PASSWORD"),
		MarketDataURL:      getenv("MARKET_DATA_URL", "http://market-data:8000"),
		ImporterURL:        getenv("IMPORTER_URL", "http://importer:8000"),
		ServiceToken:       os.Getenv("SERVICE_TOKEN"),
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
