// Package testdb starts a migrated Postgres 16 container for database tests (specs/testing.md).
// Fixtures are synthetic (D-20).
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
)

var url string

// URL is the owner connection string of the running test database.
func URL() string { return url }

// MigrationsDir is the repository's db/migrations folder.
func MigrationsDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "db", "migrations")
}

// Main starts the container, applies migrations, runs the tests and exits. Call it from TestMain.
func Main(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
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

	url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "connection string:", err)
		return 1
	}
	if err := db.Migrate(url, MigrationsDir()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

// OwnerPool connects as the database owner (a superuser in the container, so RLS does not
// apply). Use it only to seed fixtures and inspect the catalog.
func OwnerPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
