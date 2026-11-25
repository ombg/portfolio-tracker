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
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
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
		row, err := dbq.New(tx).AuthUserForLogin(ctx, email)
		userID, hash = row.UserID, row.PasswordHash
		return err
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
		row, err := dbq.New(tx).SessionCreate(ctx, dbq.SessionCreateParams{UserID: userID, TtlSeconds: int32(s.TTL / time.Second)})
		sess = Session{ID: row.SessionID, ExpiresAt: row.ExpiresAt}
		return err
	})
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// UserID resolves a session to its user and records activity.
func (s *Store) UserID(ctx context.Context, sessionID uuid.UUID) (uuid.UUID, error) {
	var userID uuid.UUID
	err := db.AsRequestRole(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		userID, err = dbq.New(tx).SessionUserID(ctx, dbq.SessionUserIDParams{SessionID: sessionID, IdleSeconds: int32(s.Idle / time.Second)})
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve session: %w", err)
	}
	// session_user_id returns NULL for no valid session, which scans as the zero UUID.
	if userID == uuid.Nil {
		return uuid.Nil, ErrNoSession
	}
	return userID, nil
}

func (s *Store) Revoke(ctx context.Context, sessionID uuid.UUID) error {
	return db.AsRequestRole(ctx, s.Pool, func(tx pgx.Tx) error {
		return dbq.New(tx).SessionRevoke(ctx, sessionID)
	})
}

// Profile returns the signed-in user's profile; the database selects it by app.user_id.
func (s *Store) Profile(ctx context.Context, userID uuid.UUID) (Profile, error) {
	var p Profile
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		row, err := dbq.New(tx).CurrentUserProfile(ctx)
		p = Profile{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, ReportingCurrency: row.ReportingCurrency}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNoSession
	}
	return p, err
}
