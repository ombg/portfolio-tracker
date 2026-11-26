package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/testdb"
)

// connectAsImporter logs in to the test database as pt_importer with password.
func connectAsImporter(t *testing.T, password string) (*pgx.Conn, error) {
	t.Helper()
	cfg, err := pgx.ParseConfig(testdb.URL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = db.ImporterRole, password
	return pgx.ConnectConfig(context.Background(), cfg)
}

// TestSetImporterLogin: the importer can log in only with the configured password, as a role that cannot bypass
// RLS, and not at all once the password is removed (D-35).
func TestSetImporterLogin(t *testing.T) {
	ctx := context.Background()
	pool := ownerPool(t)
	const password = `synthetic 'quoted' importer password`
	t.Cleanup(func() { _ = db.SetImporterLogin(ctx, pool, "") })

	if err := db.SetImporterLogin(ctx, pool, "too short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := db.SetImporterLogin(ctx, pool, password); err != nil {
		t.Fatal(err)
	}
	conn, err := connectAsImporter(t, password)
	if err != nil {
		t.Fatalf("login with the configured password: %v", err)
	}
	var user string
	var super, bypass bool
	err = conn.QueryRow(ctx, `SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).
		Scan(&user, &super, &bypass)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if user != db.ImporterRole || super || bypass {
		t.Fatalf("logged in as %s super=%v bypassrls=%v", user, super, bypass)
	}

	if c, err := connectAsImporter(t, "wrong password, long enough to pass"); err == nil {
		_ = c.Close(ctx)
		t.Fatal("login with a wrong password succeeded")
	}

	if err := db.SetImporterLogin(ctx, pool, ""); err != nil {
		t.Fatal(err)
	}
	if c, err := connectAsImporter(t, password); err == nil {
		_ = c.Close(ctx)
		t.Fatal("login still possible after the password was removed")
	}
}
