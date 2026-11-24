// Package admin holds the ptctl operations. They connect as the database owner, which D-32
// reserves for user creation and password resets.
package admin

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ombg/portfolio-tracker/services/api/internal/auth"
)

var (
	ErrEmailTaken   = errors.New("a user with this email already exists")
	ErrUserNotFound = errors.New("no user with this email")
	ErrInvalidEmail = errors.New("invalid email address")
)

// DefaultPortfolioName is the single portfolio every user gets (D-2).
const DefaultPortfolioName = "Main"

type NewUser struct {
	Email             string
	DisplayName       string
	ReportingCurrency string // CAD, USD or EUR; empty means CAD
	Password          string
}

// CreateUser adds a user and their portfolio in one transaction.
func CreateUser(ctx context.Context, pool *pgxpool.Pool, u NewUser) (uuid.UUID, error) {
	email, err := normalizeEmail(u.Email)
	if err != nil {
		return uuid.Nil, err
	}
	name := strings.TrimSpace(u.DisplayName)
	if name == "" {
		return uuid.Nil, errors.New("display name is required")
	}
	currency := strings.ToUpper(strings.TrimSpace(u.ReportingCurrency))
	if currency == "" {
		currency = "CAD"
	}
	if err := auth.CheckPasswordPolicy(u.Password); err != nil {
		return uuid.Nil, err
	}
	hash, err := auth.HashPassword(u.Password)
	if err != nil {
		return uuid.Nil, err
	}

	var id uuid.UUID
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO core.users (email, password_hash, display_name, reporting_currency)
			VALUES ($1, $2, $3, $4) RETURNING id`, email, hash, name, currency).Scan(&id); err != nil {
			return err
		}
		// The owner may not bypass RLS (it is forced), so set the user for the portfolio insert.
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", id.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO core.portfolios (user_id, name) VALUES ($1, $2)", id, DefaultPortfolioName)
		return err
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return uuid.Nil, ErrEmailTaken
	case errors.As(err, &pgErr) && pgErr.Code == "23503":
		return uuid.Nil, fmt.Errorf("unknown reporting currency %q", currency)
	case err != nil:
		return uuid.Nil, fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

// SetPassword replaces a user's password and ends all of their sessions.
func SetPassword(ctx context.Context, pool *pgxpool.Pool, email, password string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if err := auth.CheckPasswordPolicy(password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, "UPDATE core.users SET password_hash = $1 WHERE lower(email) = lower($2) RETURNING id",
			hash, email).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		_, err = tx.Exec(ctx, "DELETE FROM core.sessions WHERE user_id = $1", id)
		return err
	})
}

func normalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" {
		return "", ErrInvalidEmail
	}
	return s, nil
}
