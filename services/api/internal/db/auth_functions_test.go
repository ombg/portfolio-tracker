package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
)

// asApp runs fn as pt_app with no user set, as login and session lookup do.
func asApp(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	return db.AsRequestRole(ctx, ownerPool(t), func(tx pgx.Tx) error { return fn(ctx, tx) })
}

func createSession(t *testing.T, user uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT session_id FROM core.session_create($1, 3600)", user).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func resolve(t *testing.T, session uuid.UUID) *uuid.UUID {
	t.Helper()
	var got *uuid.UUID
	err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT core.session_user_id($1, 600)", session).Scan(&got)
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLoginLookupIsCaseInsensitiveAndSkipsDisabledUsers(t *testing.T) {
	pool := ownerPool(t)
	seed(t, pool)
	lookup := func(email string) int {
		var n int
		if err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM core.auth_user_for_login($1)", email).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := lookup("A@Example.TEST"); n != 1 {
		t.Fatalf("mixed-case lookup found %d users, want 1", n)
	}
	if n := lookup("nobody@example.test"); n != 0 {
		t.Fatalf("unknown email found %d users", n)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE core.users SET disabled_at = now() WHERE id = $1", userA); err != nil {
		t.Fatal(err)
	}
	if n := lookup("a@example.test"); n != 0 {
		t.Fatal("disabled user is still returned for login")
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)

	t.Run("valid session resolves to its user", func(t *testing.T) {
		seed(t, pool)
		if got := resolve(t, createSession(t, userA)); got == nil || *got != userA {
			t.Fatalf("resolved to %v, want user A", got)
		}
	})
	t.Run("unknown session resolves to nothing", func(t *testing.T) {
		seed(t, pool)
		if got := resolve(t, uuid.New()); got != nil {
			t.Fatalf("unknown session resolved to %v", got)
		}
	})
	t.Run("expired session resolves to nothing", func(t *testing.T) {
		seed(t, pool)
		s := createSession(t, userA)
		mustExec(t, "UPDATE core.sessions SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second' WHERE id = $1", s)
		if got := resolve(t, s); got != nil {
			t.Fatal("expired session still resolves")
		}
	})
	t.Run("idle session resolves to nothing", func(t *testing.T) {
		seed(t, pool)
		s := createSession(t, userA)
		mustExec(t, "UPDATE core.sessions SET last_seen_at = now() - interval '11 minutes' WHERE id = $1", s)
		if got := resolve(t, s); got != nil {
			t.Fatal("session idle past the limit still resolves")
		}
	})
	t.Run("disabled user's session resolves to nothing", func(t *testing.T) {
		seed(t, pool)
		s := createSession(t, userA)
		mustExec(t, "UPDATE core.users SET disabled_at = now() WHERE id = $1", userA)
		if got := resolve(t, s); got != nil {
			t.Fatal("disabled user's session still resolves")
		}
	})
	t.Run("revoked session resolves to nothing", func(t *testing.T) {
		seed(t, pool)
		s := createSession(t, userA)
		if err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT core.session_revoke($1)", s)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := resolve(t, s); got != nil {
			t.Fatal("revoked session still resolves")
		}
	})
	t.Run("no session for a disabled user", func(t *testing.T) {
		seed(t, pool)
		mustExec(t, "UPDATE core.users SET disabled_at = now() WHERE id = $1", userA)
		err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT * FROM core.session_create($1, 3600)", userA)
			return err
		})
		assertSQLState(t, err, "28000")
	})
	t.Run("ttl outside 1 minute to 90 days is refused", func(t *testing.T) {
		seed(t, pool)
		for _, ttl := range []int{0, 59, 90*86400 + 1} {
			err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT * FROM core.session_create($1, $2)", userA, ttl)
				return err
			})
			assertSQLState(t, err, "22023")
		}
	})
	t.Run("creating a session drops the user's expired ones", func(t *testing.T) {
		seed(t, pool)
		old := createSession(t, userA)
		mustExec(t, "UPDATE core.sessions SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second' WHERE id = $1", old)
		createSession(t, userA)
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM core.sessions WHERE id = $1", old).Scan(&n); err != nil || n != 0 {
			t.Fatalf("expired session still stored: %d %v", n, err)
		}
	})
}

func TestProfileIsOnlyTheSignedInUser(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	seed(t, pool)

	var email string
	err := db.WithUser(ctx, pool, userB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT email FROM core.current_user_profile()").Scan(&email)
	})
	if err != nil || email != "b@example.test" {
		t.Fatalf("profile for B = %q, %v", email, err)
	}
	var n int
	if err := asApp(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM core.current_user_profile()").Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("profile without a user returned %d rows, %v", n, err)
	}
}

// TestAuthFunctionsAreNotPublic: only pt_app may call the D-32 functions.
func TestAuthFunctionsAreNotPublic(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	mustExec(t, "DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pt_other') THEN CREATE ROLE pt_other NOLOGIN; END IF; END $$")

	rows, err := pool.Query(ctx, `
		SELECT p.oid::regprocedure::text, has_function_privilege('pt_app', p.oid, 'EXECUTE'),
		       has_function_privilege('pt_other', p.oid, 'EXECUTE'), p.prosecdef,
		       coalesce(array_to_string(p.proconfig, ','), '')
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'core' AND p.prosecdef`)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for rows.Next() {
		var name, config string
		var app, other, definer bool
		if err := rows.Scan(&name, &app, &other, &definer, &config); err != nil {
			t.Fatal(err)
		}
		seen++
		if !app {
			t.Errorf("%s: pt_app cannot execute", name)
		}
		if other {
			t.Errorf("%s: executable by any role (EXECUTE not revoked from PUBLIC)", name)
		}
		if config != "search_path=pg_catalog, pg_temp" {
			t.Errorf("%s: SECURITY DEFINER without a pinned search_path (got %q)", name, config)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 5 {
		t.Fatalf("found %d SECURITY DEFINER functions in core, want 5 (update this test when adding one)", seen)
	}
}

func mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := ownerPool(t).Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
