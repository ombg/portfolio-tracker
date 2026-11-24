package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
)

// Synthetic fixture ids (D-20): two users, each with one portfolio and one account.
var (
	userA      = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	userB      = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	portfolioA = uuid.MustParse("10000000-0000-0000-0000-00000000000a")
	portfolioB = uuid.MustParse("10000000-0000-0000-0000-00000000000b")
	accountA   = uuid.MustParse("20000000-0000-0000-0000-00000000000a")
	accountB   = uuid.MustParse("20000000-0000-0000-0000-00000000000b")
)

var (
	dbURL         string
	migrationsDir string
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	ctx := context.Background()
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir = filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "db", "migrations")

	ctr, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("portfolio_test"),
		tcpostgres.WithUsername("owner"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres container (is Docker running?):", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(ctr) }()

	dbURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "connection string:", err)
		return 1
	}
	if err := db.Migrate(dbURL, migrationsDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

// ownerPool connects as the database owner (a superuser in the test container, so RLS
// does not apply). Used only to seed fixtures and inspect the catalog.
func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	b := &pgx.Batch{}
	b.Queue("TRUNCATE core.accounts, core.portfolios, core.sessions, core.users CASCADE")
	b.Queue(`INSERT INTO core.users (id, email, password_hash, display_name)
		VALUES ($1, 'a@example.test', 'x', 'User A'), ($2, 'b@example.test', 'x', 'User B')`, userA, userB)
	b.Queue("INSERT INTO core.portfolios (id, user_id, name) VALUES ($1, $2, 'Main'), ($3, $4, 'Main')",
		portfolioA, userA, portfolioB, userB)
	b.Queue(`INSERT INTO core.accounts (id, user_id, portfolio_id, broker_id, name, type, is_registered, base_currency)
		SELECT v.id, v.u, v.p, b.id, v.n, 'TFSA', true, 'CAD'
		FROM (VALUES ($1::uuid, $2::uuid, $3::uuid, 'A tfsa'), ($4::uuid, $5::uuid, $6::uuid, 'B tfsa')) v(id, u, p, n)
		CROSS JOIN (SELECT id FROM core.brokers WHERE code = 'questrade') b`,
		accountA, userA, portfolioA, accountB, userB, portfolioB)
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestMigrationsDownAndUpAgain(t *testing.T) {
	if err := db.MigrateDown(dbURL, migrationsDir); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(dbURL, migrationsDir); err != nil {
		t.Fatal(err)
	}
}

// TestEveryUserTableHasForcedRLS guards rule 2: any table with a user_id column must have
// RLS enabled and forced, unless D-32 exempts it, in which case pt_app may not touch it.
func TestEveryUserTableHasForcedRLS(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	exempt := map[string]bool{"core.users": true, "core.sessions": true} // D-32

	rows, err := pool.Query(ctx, `
		SELECT n.nspname || '.' || c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       has_table_privilege('pt_app', c.oid, 'SELECT,INSERT,UPDATE,DELETE')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
		  AND n.nspname IN ('core', 'imports', 'market')
		  AND (EXISTS (SELECT 1 FROM pg_attribute a
		               WHERE a.attrelid = c.oid AND a.attname = 'user_id' AND NOT a.attisdropped)
		       OR c.relname = 'users')`)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for rows.Next() {
		var name string
		var rls, forced, appPriv bool
		if err := rows.Scan(&name, &rls, &forced, &appPriv); err != nil {
			t.Fatal(err)
		}
		checked++
		switch {
		case exempt[name] && appPriv:
			t.Errorf("%s: pt_app must have no privileges (D-32)", name)
		case !exempt[name] && (!rls || !forced):
			t.Errorf("%s: has user_id but RLS enabled=%v forced=%v (D-1)", name, rls, forced)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 4 {
		t.Fatalf("checked %d tables, expected at least users, sessions, portfolios, accounts", checked)
	}
}

func TestRequestRoleWithoutUserSeesNothing(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	seed(t, pool)

	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+db.RequestRole); err != nil {
			return err
		}
		for _, table := range []string{"core.accounts", "core.portfolios"} {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Errorf("%s: %d rows visible with app.user_id unset, want 0", table, n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWithUserRejectsNilUser(t *testing.T) {
	err := db.WithUser(context.Background(), ownerPool(t), uuid.Nil, func(pgx.Tx) error { return nil })
	if err == nil {
		t.Fatal("expected an error for the nil user id")
	}
}

func TestUserSeesOnlyOwnRows(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	seed(t, pool)

	for _, tc := range []struct {
		user    uuid.UUID
		account uuid.UUID
		name    string
	}{{userA, accountA, "A tfsa"}, {userB, accountB, "B tfsa"}} {
		err := db.WithUser(ctx, pool, tc.user, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "SELECT id, name FROM core.accounts")
			if err != nil {
				return err
			}
			type acct struct {
				ID   uuid.UUID
				Name string
			}
			got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[acct])
			if err != nil {
				return err
			}
			if len(got) != 1 || got[0].ID != tc.account || got[0].Name != tc.name {
				t.Errorf("user %s sees %+v, want only %s", tc.user, got, tc.name)
			}
			var portfolios int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM core.portfolios").Scan(&portfolios); err != nil {
				return err
			}
			if portfolios != 1 {
				t.Errorf("user %s sees %d portfolios, want 1", tc.user, portfolios)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

const insertAccount = `
	INSERT INTO core.accounts (user_id, portfolio_id, broker_id, name, type, is_registered, base_currency, management, analysis_level)
	SELECT $1, $2, id, 'new', $3, $4, 'CAD', $5, $6 FROM core.brokers WHERE code = 'wealthsimple'`

// TestUserCannotWriteOtherUsersRows runs each statement as user A and expects the given
// SQLSTATE: 42501 (RLS or privilege), 23503 (foreign key), 23514 (check). "" = must succeed.
func TestUserCannotWriteOtherUsersRows(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)

	cases := []struct {
		name     string
		sql      string
		args     []any
		wantCode string
		wantRows int64 // rows affected when wantCode is ""
	}{
		{"insert account owned by B", insertAccount,
			[]any{userB, portfolioB, "TFSA", true, "self_directed", "security"}, "42501", 0},
		{"insert own account into B's portfolio", insertAccount,
			[]any{userA, portfolioB, "TFSA", true, "self_directed", "security"}, "23503", 0},
		{"insert portfolio owned by B", "INSERT INTO core.portfolios (user_id, name) VALUES ($1, 'x')",
			[]any{userB}, "42501", 0},
		{"rename B's account", "UPDATE core.accounts SET name = 'changed' WHERE id = $1",
			[]any{accountB}, "", 0},
		{"hand own account to B", "UPDATE core.accounts SET user_id = $1 WHERE id = $2",
			[]any{userB, accountA}, "42501", 0},
		{"move own account to B's portfolio", "UPDATE core.accounts SET portfolio_id = $1 WHERE id = $2",
			[]any{portfolioB, accountA}, "23503", 0},
		{"delete own account", "DELETE FROM core.accounts WHERE id = $1",
			[]any{accountA}, "42501", 0},
		{"read users table", "SELECT 1 FROM core.users", nil, "42501", 0},
		{"read sessions table", "SELECT 1 FROM core.sessions", nil, "42501", 0},
		{"insert valid own account", insertAccount,
			[]any{userA, portfolioA, "NON_REGISTERED", false, "self_directed", "security"}, "", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, pool)
			err := db.WithUser(ctx, pool, userA, func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, tc.sql, tc.args...)
				if err == nil && tc.wantCode == "" && tag.RowsAffected() != tc.wantRows {
					t.Errorf("rows affected = %d, want %d", tag.RowsAffected(), tc.wantRows)
				}
				return err
			})
			assertSQLState(t, err, tc.wantCode)
		})
	}

	// User B's account is untouched after all of the above.
	seed(t, pool)
	var name string
	if err := pool.QueryRow(ctx, "SELECT name FROM core.accounts WHERE id = $1", accountB).Scan(&name); err != nil || name != "B tfsa" {
		t.Fatalf("B's account = %q, %v", name, err)
	}
}

// TestAccountRules covers the account constraints from data-model.md and D-14.
func TestAccountRules(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)

	cases := []struct {
		name          string
		accountType   string
		registered    bool
		management    string
		analysisLevel string
		wantCode      string
	}{
		{"TFSA is registered", "TFSA", true, "self_directed", "security", ""},
		{"TFSA marked non-registered", "TFSA", false, "self_directed", "security", "23514"},
		{"FHSA marked non-registered", "FHSA", false, "self_directed", "security", "23514"},
		{"non-registered marked registered", "NON_REGISTERED", true, "self_directed", "security", "23514"},
		{"OTHER may be registered", "OTHER", true, "self_directed", "security", ""},
		{"OTHER may be non-registered", "OTHER", false, "self_directed", "security", ""},
		{"robo at account level", "RRSP", true, "robo", "account", ""},
		{"robo at security level", "RRSP", true, "robo", "security", "23514"},
		{"unknown account type", "LIRA", true, "self_directed", "security", "23514"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, pool)
			err := db.WithUser(ctx, pool, userA, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, insertAccount, userA, portfolioA, tc.accountType, tc.registered, tc.management, tc.analysisLevel)
				return err
			})
			assertSQLState(t, err, tc.wantCode)
		})
	}

	t.Run("external number unique per user and broker", func(t *testing.T) {
		seed(t, pool)
		const q = `INSERT INTO core.accounts (user_id, portfolio_id, broker_id, external_number, name, type, is_registered, base_currency)
			SELECT $1, $2, id, '00000001', 'dup', 'TFSA', true, 'CAD' FROM core.brokers WHERE code = 'questrade'`
		err := db.WithUser(ctx, pool, userA, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, q, userA, portfolioA); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, q, userA, portfolioA)
			return err
		})
		assertSQLState(t, err, "23505")
	})
}

func assertSQLState(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s, got %v", want, err)
	}
	if pgErr.Code != want {
		t.Fatalf("want SQLSTATE %s, got %s: %s", want, pgErr.Code, pgErr.Message)
	}
}
