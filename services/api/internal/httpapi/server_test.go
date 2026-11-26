package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ombg/portfolio-tracker/services/api/internal/accounts"
	"github.com/ombg/portfolio-tracker/services/api/internal/admin"
	"github.com/ombg/portfolio-tracker/services/api/internal/auth"
	"github.com/ombg/portfolio-tracker/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{"database up", nil, http.StatusOK, "ok"},
		{"database down is not reported healthy", errors.New("down"), http.StatusServiceUnavailable, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewRouter(Deps{DB: fakePinger{tc.pingErr}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["status"] != tc.wantBody {
				t.Fatalf("status field = %q, want %q", body["status"], tc.wantBody)
			}
		})
	}
}

// --- login, session and logout against a real database ---

const (
	testEmail    = "login@example.test"
	testPassword = "synthetic passphrase 1"
)

type harness struct {
	t      *testing.T
	router http.Handler
	store  *auth.Store
	deps   Deps // rebuild the router with more dependencies: h.router = NewRouter(h.deps)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testdb.OwnerPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE core.accounts, core.portfolios, core.sessions, core.users CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CreateUser(ctx, pool, admin.NewUser{Email: testEmail, DisplayName: "Login User", Password: testPassword}); err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(pool)
	deps := Deps{DB: pool, Auth: store, Signer: signer, CookieSecure: true, Accounts: &accounts.Store{Pool: pool}}
	return &harness{t: t, store: store, deps: deps, router: NewRouter(deps)}
}

func (h *harness) do(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *harness) login(email, password string) (*httptest.ResponseRecorder, *http.Cookie) {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	rec := h.do(http.MethodPost, "/api/auth/login", string(body), nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return rec, c
		}
	}
	return rec, nil
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body["error"]
}

func TestLoginMeLogout(t *testing.T) {
	h := newHarness(t)

	rec, cookie := h.login("Login@Example.test", testPassword)
	if rec.Code != http.StatusNoContent || cookie == nil {
		t.Fatalf("login: status %d, cookie %v", rec.Code, cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	if until := time.Until(cookie.Expires); until < 29*24*time.Hour || until > 31*24*time.Hour {
		t.Fatalf("cookie expires in %v, want about 30 days (D-33)", until)
	}

	rec = h.do(http.MethodGet, "/api/me", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: status %d", rec.Code)
	}
	var p auth.Profile
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Email != testEmail || p.DisplayName != "Login User" || p.ReportingCurrency != "CAD" {
		t.Fatalf("profile = %+v", p)
	}

	rec = h.do(http.MethodPost, "/api/auth/logout", "", cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == auth.CookieName && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}
	if rec = h.do(http.MethodGet, "/api/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: status %d, want 401", rec.Code)
	}
}

func TestLoginFailuresLookTheSame(t *testing.T) {
	h := newHarness(t)
	for name, creds := range map[string][2]string{
		"wrong password": {testEmail, "not the password at all"},
		"unknown email":  {"nobody@example.test", testPassword},
	} {
		rec, cookie := h.login(creds[0], creds[1])
		if rec.Code != http.StatusUnauthorized || cookie != nil || errorCode(t, rec) != "invalid_credentials" {
			t.Errorf("%s: status %d, cookie %v", name, rec.Code, cookie)
		}
	}
}

func TestLoginRejectsBadRequests(t *testing.T) {
	h := newHarness(t)
	for name, body := range map[string]string{
		"not json":        "email=x",
		"missing fields":  `{"email":"login@example.test"}`,
		"unknown field":   `{"email":"login@example.test","password":"x","admin":true}`,
		"empty object":    `{}`,
		"oversized input": `{"email":"` + strings.Repeat("a", 9<<10) + `","password":"x"}`,
	} {
		if rec := h.do(http.MethodPost, "/api/auth/login", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
}

func TestWritesMustBeJSON(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("email=a&password=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: status %d, want 415", rec.Code)
	}
}

func TestMeRequiresAValidSession(t *testing.T) {
	h := newHarness(t)
	_, good := h.login(testEmail, testPassword)
	if good == nil {
		t.Fatal("login failed")
	}
	forged := *good
	forged.Value = strings.Replace(good.Value, good.Value[:8], "ffffffff", 1)
	for name, c := range map[string]*http.Cookie{
		"no cookie":        nil,
		"garbage":          {Name: auth.CookieName, Value: "garbage"},
		"forged signature": &forged,
	} {
		if rec := h.do(http.MethodGet, "/api/me", "", c); rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthenticated" {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
	}
}

func TestDisabledUserIsSignedOut(t *testing.T) {
	h := newHarness(t)
	_, cookie := h.login(testEmail, testPassword)
	if _, err := h.store.Pool.Exec(context.Background(), "UPDATE core.users SET disabled_at = now() WHERE email = $1", testEmail); err != nil {
		t.Fatal(err)
	}
	if rec := h.do(http.MethodGet, "/api/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user: status %d, want 401", rec.Code)
	}
	if rec, c := h.login(testEmail, testPassword); rec.Code != http.StatusUnauthorized || c != nil {
		t.Fatalf("disabled user can log in: status %d", rec.Code)
	}
}
