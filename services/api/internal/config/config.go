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
}

// FromEnv reads the configuration. DATABASE_URL is required.
func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		MigrationsDir: getenv("MIGRATIONS_DIR", "/migrations"),
		ListenAddr:    getenv("LISTEN_ADDR", ":8080"),
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
