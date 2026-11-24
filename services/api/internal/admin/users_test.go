package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ombg/portfolio-tracker/services/api/internal/admin"
	"github.com/ombg/portfolio-tracker/services/api/internal/auth"
	"github.com/ombg/portfolio-tracker/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const goodPassword = "a long enough passphrase"

func TestCreateUserAddsUserAndPortfolio(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OwnerPool(t)

	id, err := admin.CreateUser(ctx, pool, admin.NewUser{Email: "new@example.test", DisplayName: "New User", Password: goodPassword})
	if err != nil {
		t.Fatal(err)
	}
	var currency, hash, portfolio string
	err = pool.QueryRow(ctx, `SELECT u.reporting_currency, u.password_hash, p.name
		FROM core.users u JOIN core.portfolios p ON p.user_id = u.id WHERE u.id = $1`, id).Scan(&currency, &hash, &portfolio)
	if err != nil {
		t.Fatal(err)
	}
	if currency != "CAD" || portfolio != admin.DefaultPortfolioName {
		t.Fatalf("currency %q, portfolio %q; want CAD, %q", currency, portfolio, admin.DefaultPortfolioName)
	}
	if ok, _ := auth.VerifyPassword(goodPassword, hash); !ok {
		t.Fatal("stored hash does not verify the password")
	}

	if _, err := admin.CreateUser(ctx, pool, admin.NewUser{Email: "NEW@example.test", DisplayName: "Again", Password: goodPassword}); !errors.Is(err, admin.ErrEmailTaken) {
		t.Fatalf("duplicate email (other case): %v, want ErrEmailTaken", err)
	}
}

func TestCreateUserValidates(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OwnerPool(t)
	for name, u := range map[string]admin.NewUser{
		"bad email":        {Email: "not-an-email", DisplayName: "X", Password: goodPassword},
		"email with name":  {Email: "X <x@example.test>", DisplayName: "X", Password: goodPassword},
		"no display name":  {Email: "x1@example.test", DisplayName: " ", Password: goodPassword},
		"weak password":    {Email: "x2@example.test", DisplayName: "X", Password: "short"},
		"unknown currency": {Email: "x3@example.test", DisplayName: "X", ReportingCurrency: "GBP", Password: goodPassword},
	} {
		if _, err := admin.CreateUser(ctx, pool, u); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM core.users WHERE email LIKE 'x_@example.test'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected users were stored: %d %v", n, err)
	}
}

func TestSetPasswordEndsSessions(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OwnerPool(t)
	store := auth.NewStore(pool)

	if _, err := admin.CreateUser(ctx, pool, admin.NewUser{Email: "reset@example.test", DisplayName: "Reset", Password: goodPassword}); err != nil {
		t.Fatal(err)
	}
	sess, err := store.Login(ctx, "reset@example.test", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	const newPassword = "an entirely new passphrase"
	if err := admin.SetPassword(ctx, pool, "Reset@example.test", newPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserID(ctx, sess.ID); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("old session after reset: %v, want ErrNoSession", err)
	}
	if _, err := store.Login(ctx, "reset@example.test", goodPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password after reset: %v", err)
	}
	if _, err := store.Login(ctx, "reset@example.test", newPassword); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if err := admin.SetPassword(ctx, pool, "missing@example.test", newPassword); !errors.Is(err, admin.ErrUserNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}
