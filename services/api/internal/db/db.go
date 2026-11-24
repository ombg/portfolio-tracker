// Package db opens the Postgres pool, applies migrations and runs request work
// inside transactions scoped to one user (D-1, D-32).
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5:// driver
	_ "github.com/golang-migrate/migrate/v4/source/file"     // file:// source
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequestRole is the database role every request transaction runs as. It has no
// BYPASSRLS, so the row-level security policies always apply.
const RequestRole = "pt_app"

// Open connects to Postgres as the database owner and checks the connection.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies every pending migration in dir to the database at url.
// url is a postgres:// URL; dir is the path to db/migrations.
func Migrate(url, dir string) error {
	m, err := newMigrate(url, dir)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// MigrateDown reverts every migration. Used by tests to prove down migrations work.
func MigrateDown(url, dir string) error {
	m, err := newMigrate(url, dir)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("revert migrations: %w", err)
	}
	return nil
}

func newMigrate(url, dir string) (*migrate.Migrate, error) {
	dbURL, err := pgx5URL(url)
	if err != nil {
		return nil, err
	}
	m, err := migrate.New("file://"+dir, dbURL)
	if err != nil {
		return nil, fmt.Errorf("load migrations from %s: %w", dir, err)
	}
	return m, nil
}

// pgx5URL rewrites a postgres:// URL to the pgx5:// scheme golang-migrate expects.
func pgx5URL(url string) (string, error) {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			return "pgx5://" + url[len(prefix):], nil
		}
	}
	return "", errors.New("database url must start with postgres:// or postgresql://")
}

// AsRequestRole runs fn in a transaction as RequestRole with no user set. RLS then hides
// every user-owned row; it is used only for login and session lookup, which go through
// the D-32 functions.
func AsRequestRole(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+RequestRole); err != nil {
			return fmt.Errorf("set request role: %w", err)
		}
		return fn(tx)
	})
}

// WithUser runs fn in a transaction as RequestRole with app.user_id set to userID,
// so RLS limits every query to that user's rows. The transaction commits when fn
// returns nil and rolls back otherwise.
func WithUser(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, fn func(pgx.Tx) error) error {
	if userID == uuid.Nil {
		return errors.New("WithUser: user id is required")
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+RequestRole); err != nil {
			return fmt.Errorf("set request role: %w", err)
		}
		// SET LOCAL takes no parameters; set_config(..., true) is the transaction-local equivalent.
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", userID.String()); err != nil {
			return fmt.Errorf("set app.user_id: %w", err)
		}
		return fn(tx)
	})
}
