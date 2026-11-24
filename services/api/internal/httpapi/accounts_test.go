package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ombg/portfolio-tracker/services/api/internal/accounts"
	"github.com/ombg/portfolio-tracker/services/api/internal/admin"
)

// Synthetic accounts only (D-20): invented names and numbers.

func (h *harness) signIn(email, password string) *http.Cookie {
	h.t.Helper()
	_, c := h.login(email, password)
	if c == nil {
		h.t.Fatalf("login as %s failed", email)
	}
	return c
}

func (h *harness) secondUser() *http.Cookie {
	h.t.Helper()
	const email, pw = "second@example.test", "second synthetic pass"
	if _, err := admin.CreateUser(context.Background(), h.store.Pool, admin.NewUser{Email: email, DisplayName: "Second", Password: pw}); err != nil {
		h.t.Fatal(err)
	}
	return h.signIn(email, pw)
}

func decodeAccount(t *testing.T, rec *httptest.ResponseRecorder) accounts.Account {
	t.Helper()
	var a accounts.Account
	if err := json.NewDecoder(rec.Body).Decode(&a); err != nil {
		t.Fatalf("decode account: %v (body %s)", err, rec.Body.String())
	}
	return a
}

func (h *harness) create(c *http.Cookie, body string) accounts.Account {
	h.t.Helper()
	rec := h.do(http.MethodPost, "/api/accounts", body, c)
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create %s: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	return decodeAccount(h.t, rec)
}

func (h *harness) patch(c *http.Cookie, id, body string, wantStatus int) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := h.do(http.MethodPatch, "/api/accounts/"+id, body, c)
	if rec.Code != wantStatus {
		h.t.Fatalf("patch %s: status %d, want %d, body %s", body, rec.Code, wantStatus, rec.Body.String())
	}
	return rec
}

func validationField(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "validation_failed" {
		t.Fatalf("error = %q, want validation_failed (%v)", body["error"], body)
	}
	return body["field"]
}

// TestAC1CreateAccountsAtBothBrokers: PRD AC-1, separate Wealthsimple and Questrade accounts,
// registered and non-registered.
func TestAC1CreateAccountsAtBothBrokers(t *testing.T) {
	h := newHarness(t)
	c := h.signIn(testEmail, testPassword)

	for _, tc := range []struct {
		body           string
		wantRegistered bool
		wantLevel      string
	}{
		{`{"broker":"questrade","name":"QT TFSA","type":"TFSA","external_number":"00000001","opened_on":"2020-01-02"}`, true, "security"},
		{`{"broker":"questrade","name":"QT Margin","type":"NON_REGISTERED","base_currency":"USD"}`, false, "security"},
		{`{"broker":"wealthsimple","name":"WS FHSA","type":"FHSA"}`, true, "security"},
		{`{"broker":"wealthsimple","name":"WS Managed RRSP","type":"RRSP","management":"robo"}`, true, "account"},
		{`{"broker":"wealthsimple","name":"WS Cash","type":"OTHER","is_registered":false,"notes":"chequing-style account"}`, false, "security"},
	} {
		a := h.create(c, tc.body)
		if a.IsRegistered != tc.wantRegistered || a.AnalysisLevel != tc.wantLevel || a.Status != "open" {
			t.Errorf("%s -> registered %v, level %s, status %s", tc.body, a.IsRegistered, a.AnalysisLevel, a.Status)
		}
	}

	rec := h.do(http.MethodGet, "/api/accounts", "", c)
	var list struct{ Accounts []accounts.Account }
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list: %d %v", rec.Code, err)
	}
	if len(list.Accounts) != 5 {
		t.Fatalf("listed %d accounts, want 5", len(list.Accounts))
	}
	brokers := map[string]int{}
	for _, a := range list.Accounts {
		brokers[a.Broker.Code]++
	}
	if brokers["questrade"] != 2 || brokers["wealthsimple"] != 3 {
		t.Fatalf("accounts per broker = %v", brokers)
	}
	first := list.Accounts[0]
	if first.Name != "QT Margin" || first.BaseCurrency != "USD" {
		t.Fatalf("list not sorted by name or currency wrong: first = %s %s", first.Name, first.BaseCurrency)
	}
}

func TestCreateAccountValidation(t *testing.T) {
	h := newHarness(t)
	c := h.signIn(testEmail, testPassword)
	for _, tc := range []struct{ body, field string }{
		{`{"name":"x","type":"TFSA"}`, "broker"},
		{`{"broker":"rbc","name":"x","type":"TFSA"}`, "broker"},
		{`{"broker":"questrade","name":"  ","type":"TFSA"}`, "name"},
		{`{"broker":"questrade","name":"x","type":"LIRA"}`, "type"},
		{`{"broker":"questrade","name":"x","type":"TFSA","is_registered":false}`, "is_registered"},
		{`{"broker":"questrade","name":"x","type":"NON_REGISTERED","is_registered":true}`, "is_registered"},
		{`{"broker":"questrade","name":"x","type":"RRSP","management":"robo","analysis_level":"security"}`, "analysis_level"},
		{`{"broker":"questrade","name":"x","type":"TFSA","base_currency":"GBP"}`, "base_currency"},
		{`{"broker":"questrade","name":"x","type":"TFSA","base_currency":"CADX"}`, "base_currency"},
		{`{"broker":"questrade","name":"x","type":"TFSA","migration_meta":[1,2]}`, "migration_meta"},
	} {
		rec := h.do(http.MethodPost, "/api/accounts", tc.body, c)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422 (%s)", tc.body, rec.Code, rec.Body.String())
			continue
		}
		if f := validationField(t, rec); f != tc.field {
			t.Errorf("%s: field %q, want %q", tc.body, f, tc.field)
		}
	}
	for _, body := range []string{
		`{"broker":"questrade","name":"x","type":"TFSA","user_id":"00000000-0000-0000-0000-00000000000b"}`,
		`{"broker":"questrade","name":"x","type":"TFSA","opened_on":"02/01/2020"}`,
		`[]`,
	} {
		if rec := h.do(http.MethodPost, "/api/accounts", body, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, rec.Code)
		}
	}
}

func TestDuplicateAccountNumberPerBroker(t *testing.T) {
	h := newHarness(t)
	c := h.signIn(testEmail, testPassword)
	h.create(c, `{"broker":"questrade","name":"One","type":"TFSA","external_number":"00000009"}`)
	rec := h.do(http.MethodPost, "/api/accounts", `{"broker":"questrade","name":"Two","type":"RRSP","external_number":" 00000009 "}`, c)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "duplicate_account_number" {
		t.Fatalf("same number, same broker: status %d", rec.Code)
	}
	// The same number at another broker, or for another user, is fine.
	h.create(c, `{"broker":"wealthsimple","name":"Three","type":"TFSA","external_number":"00000009"}`)
	h.create(h.secondUser(), `{"broker":"questrade","name":"Other user","type":"TFSA","external_number":"00000009"}`)
}

func TestPatchAccount(t *testing.T) {
	h := newHarness(t)
	c := h.signIn(testEmail, testPassword)
	a := h.create(c, `{"broker":"wealthsimple","name":"Original","type":"TFSA","notes":"keep","opened_on":"2021-03-04"}`)
	id := a.ID.String()

	got := decodeAccount(t, h.patch(c, id, `{"name":"Renamed"}`, http.StatusOK))
	if got.Name != "Renamed" || got.Notes == nil || *got.Notes != "keep" || got.Type != "TFSA" {
		t.Fatalf("rename changed other fields: %+v", got)
	}
	if !got.UpdatedAt.After(a.UpdatedAt) && !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("updated_at went backwards")
	}

	got = decodeAccount(t, h.patch(c, id, `{"notes":null}`, http.StatusOK))
	if got.Notes != nil {
		t.Fatalf("notes not cleared: %v", *got.Notes)
	}

	// Changing the type re-derives is_registered; robo re-derives analysis level (D-14).
	got = decodeAccount(t, h.patch(c, id, `{"type":"NON_REGISTERED"}`, http.StatusOK))
	if got.IsRegistered {
		t.Fatal("type NON_REGISTERED kept is_registered = true")
	}
	got = decodeAccount(t, h.patch(c, id, `{"management":"robo"}`, http.StatusOK))
	if got.AnalysisLevel != "account" {
		t.Fatalf("robo kept analysis level %s", got.AnalysisLevel)
	}
	got = decodeAccount(t, h.patch(c, id, `{"management":"self_directed"}`, http.StatusOK))
	if got.AnalysisLevel != "security" {
		t.Fatalf("self_directed kept analysis level %s", got.AnalysisLevel)
	}

	// Close, reopen (clears closed_on), archive.
	got = decodeAccount(t, h.patch(c, id, `{"status":"closed","closed_on":"2024-06-30"}`, http.StatusOK))
	if got.Status != "closed" || got.ClosedOn == nil || got.ClosedOn.Format("2006-01-02") != "2024-06-30" {
		t.Fatalf("close: %+v", got)
	}
	got = decodeAccount(t, h.patch(c, id, `{"status":"open"}`, http.StatusOK))
	if got.Status != "open" || got.ClosedOn != nil {
		t.Fatalf("reopen: status %s closed_on %v", got.Status, got.ClosedOn)
	}
	h.patch(c, id, `{"status":"archived"}`, http.StatusOK)

	for _, tc := range []struct{ body, field string }{
		{`{"status":"closed","closed_on":"2020-01-01"}`, "closed_on"}, // before opened_on
		{`{"name":null}`, "name"},
		{`{"broker":"questrade"}`, "broker"},
		{`{"status":"deleted"}`, "status"},
		{`{"type":"RRSP","is_registered":false}`, "is_registered"},
	} {
		if f := validationField(t, h.patch(c, id, tc.body, http.StatusUnprocessableEntity)); f != tc.field {
			t.Errorf("%s: field %q, want %q", tc.body, f, tc.field)
		}
	}
	h.patch(c, id, `{"nmae":"typo"}`, http.StatusBadRequest)
}

func TestArchivedAccountsAreHiddenUnlessAsked(t *testing.T) {
	h := newHarness(t)
	c := h.signIn(testEmail, testPassword)
	h.create(c, `{"broker":"questrade","name":"Active","type":"TFSA"}`)
	old := h.create(c, `{"broker":"questrade","name":"Old","type":"RRSP"}`)
	h.patch(c, old.ID.String(), `{"status":"archived"}`, http.StatusOK)

	count := func(path string) int {
		var list struct{ Accounts []accounts.Account }
		if err := json.NewDecoder(h.do(http.MethodGet, path, "", c).Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		return len(list.Accounts)
	}
	if n := count("/api/accounts"); n != 1 {
		t.Fatalf("default list has %d accounts, want 1", n)
	}
	if n := count("/api/accounts?include_archived=true"); n != 2 {
		t.Fatalf("list with archived has %d accounts, want 2", n)
	}
	if rec := h.do(http.MethodGet, "/api/accounts/"+old.ID.String(), "", c); rec.Code != http.StatusOK {
		t.Fatalf("archived account by id: status %d", rec.Code)
	}
}

// TestAccountsAreIsolatedBetweenUsers: FR-1 through the API. Another user's account is
// indistinguishable from a missing one.
func TestAccountsAreIsolatedBetweenUsers(t *testing.T) {
	h := newHarness(t)
	a := h.signIn(testEmail, testPassword)
	b := h.secondUser()
	mine := h.create(a, `{"broker":"questrade","name":"A only","type":"TFSA"}`)
	path := "/api/accounts/" + mine.ID.String()

	if rec := h.do(http.MethodGet, path, "", b); rec.Code != http.StatusNotFound {
		t.Fatalf("B reads A's account: status %d", rec.Code)
	}
	if rec := h.do(http.MethodPatch, path, `{"name":"taken"}`, b); rec.Code != http.StatusNotFound {
		t.Fatalf("B patches A's account: status %d", rec.Code)
	}
	var list struct{ Accounts []accounts.Account }
	if err := json.NewDecoder(h.do(http.MethodGet, "/api/accounts?include_archived=true", "", b).Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Accounts) != 0 {
		t.Fatalf("B lists %d accounts, want 0", len(list.Accounts))
	}
	if got := decodeAccount(t, h.do(http.MethodGet, path, "", a)); got.Name != "A only" {
		t.Fatalf("A's account changed: %q", got.Name)
	}
	if rec := h.do(http.MethodGet, "/api/accounts/not-a-uuid", "", a); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id: status %d", rec.Code)
	}
}

func TestAccountRoutesRequireLogin(t *testing.T) {
	h := newHarness(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/accounts"}, {http.MethodPost, "/api/accounts"},
		{http.MethodGet, "/api/brokers"}, {http.MethodGet, "/api/currencies"},
		{http.MethodPatch, "/api/accounts/00000000-0000-0000-0000-000000000001"},
	} {
		if rec := h.do(r.method, r.path, `{}`, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without login: status %d", r.method, r.path, rec.Code)
		}
	}
}

func TestReferenceData(t *testing.T) {
	h := newHarness(t)
	c := h.signIn(testEmail, testPassword)
	var b struct{ Brokers []accounts.Broker }
	if err := json.NewDecoder(h.do(http.MethodGet, "/api/brokers", "", c).Body).Decode(&b); err != nil || len(b.Brokers) != 2 {
		t.Fatalf("brokers = %+v, %v", b, err)
	}
	var cur struct{ Currencies []accounts.Currency }
	if err := json.NewDecoder(h.do(http.MethodGet, "/api/currencies", "", c).Body).Decode(&cur); err != nil || len(cur.Currencies) != 3 || cur.Currencies[0].Code != "CAD" {
		t.Fatalf("currencies = %+v, %v", cur, err)
	}
}
