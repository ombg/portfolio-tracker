package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrNoSession          = errors.New("no valid session")
)

// Session lifetimes (D-33).
const (
	DefaultSessionTTL  = 30 * 24 * time.Hour // absolute lifetime
	DefaultSessionIdle = 7 * 24 * time.Hour  // ends after this long without a request
)

type Session struct {
	ID        uuid.UUID
	ExpiresAt time.Time
}

type Profile struct {
	ID                uuid.UUID `json:"id"`
	Email             string    `json:"email"`
	DisplayName       string    `json:"display_name"`
	ReportingCurrency string    `json:"reporting_currency"`
}

// Store reaches core.users and core.sessions only through the D-32 functions, always as pt_app.
type Store struct {
	Pool *pgxpool.Pool
	TTL  time.Duration
	Idle time.Duration
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, TTL: DefaultSessionTTL, Idle: DefaultSessionIdle}
}

// Login checks the credentials and creates a session. Unknown email, disabled user and wrong
// password all return ErrInvalidCredentials after the same amount of hashing work.
func (s *Store) Login(ctx context.Context, email, password string) (Session, error) {
	email = strings.TrimSpace(email)
	var userID uuid.UUID
	var hash string
	err := db.AsRequestRole(ctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT user_id, password_hash FROM core.auth_user_for_login($1)", email).Scan(&userID, &hash)
	})
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("look up user: %w", err)
	}
	if !found {
		hash = dummyHash()
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil {
		return Session{}, fmt.Errorf("verify password: %w", err)
	}
	if !found || !ok {
		return Session{}, ErrInvalidCredentials
	}

	var sess Session
	err = db.AsRequestRole(ctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT session_id, expires_at FROM core.session_create($1, $2)",
			userID, int32(s.TTL/time.Second)).Scan(&sess.ID, &sess.ExpiresAt)
	})
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// UserID resolves a session to its user and records activity.
func (s *Store) UserID(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	var userID *uuid.UUID
	err := db.AsRequestRole(ctx, s.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT core.session_user_id($1, $2)", sessionID, int32(s.Idle/time.Second)).Scan(&userID)
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve session: %w", err)
	}
	if userID == nil {
		return uuid.Nil, ErrNoSession
	}
	return *userID, nil
}

func (s *Store) Revoke(ctx context.Context, sessionID uuid.UUID) error {
	return db.AsRequestRole(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT core.session_revoke($1)", sessionID)
		return err
	})
}

// Profile returns the signed-in user's profile; the database selects it by app.user_id.
func (s *Store) Profile(ctx context.Context, userID uuid.UUID) (Profile, error) {
	var p Profile
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT id, email, display_name, reporting_currency FROM core.current_user_profile()").
			Scan(&p.ID, &p.Email, &p.DisplayName, &p.ReportingCurrency)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNoSession
	}
	return p, err
}
