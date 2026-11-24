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
	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
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
		q := dbq.New(tx)
		var err error
		id, err = q.CreateUser(ctx, dbq.CreateUserParams{Email: email, PasswordHash: hash, DisplayName: name, ReportingCurrency: currency})
		if err != nil {
			return err
		}
		// The owner may not bypass RLS (it is forced), so set the user for the portfolio insert.
		if err := db.SetUser(ctx, tx, id); err != nil {
			return err
		}
		return q.CreatePortfolio(ctx, dbq.CreatePortfolioParams{UserID: id, Name: DefaultPortfolioName})
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
		q := dbq.New(tx)
		id, err := q.SetPasswordHash(ctx, dbq.SetPasswordHashParams{PasswordHash: hash, Email: email})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		return q.DeleteUserSessions(ctx, id)
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
