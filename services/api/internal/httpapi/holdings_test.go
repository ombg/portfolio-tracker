package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ombg/portfolio-tracker/services/api/internal/holdings"
)

// Holdings API (#23, FR-7, D-38). Synthetic ledger written as the owner (D-20); the replay itself is tested in
// internal/calc against the golden ledgers.

var (
	tfsa  = uuid.MustParse("21000000-0000-0000-0000-000000000001")
	nonr  = uuid.MustParse("21000000-0000-0000-0000-000000000002")
	robo  = uuid.MustParse("21000000-0000-0000-0000-000000000003")
	secS1 = uuid.MustParse("31000000-0000-0000-0000-000000000001")
	secG  = uuid.MustParse("31000000-0000-0000-0000-000000000002")
	secB  = uuid.MustParse("31000000-0000-0000-0000-000000000003") // only user B holds it
)

type holdingsHarness struct {
	*importHarness
	nk int
}

func newHoldingsHarness(t *testing.T) *holdingsHarness {
	ih := newImportHarness(t)
	ih.deps.Holdings = &holdings.Store{Pool: ih.store.Pool}
	ih.router = NewRouter(ih.deps)
	h := &holdingsHarness{importHarness: ih}
	// Market data is global and outlives the per-test truncate of users.
	h.exec("TRUNCATE market.prices, market.fx_rates")
	return h
}

func (h *holdingsHarness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.store.Pool.Exec(context.Background(), q, args...); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

func (h *holdingsHarness) account(user, id uuid.UUID, name, typ, management string, registered bool) {
	h.t.Helper()
	analysis := "security"
	if management == "robo" {
		analysis = "account"
	}
	h.exec(`INSERT INTO core.accounts (id, user_id, portfolio_id, broker_id, name, type, is_registered, base_currency,
		management, analysis_level)
		SELECT $1, $2, p.id, b.id, $3, $4, $5, 'CAD', $6, $7 FROM core.portfolios p, core.brokers b
		WHERE p.user_id = $2 AND b.code = 'questrade'`, id, user, name, typ, registered, management, analysis)
}

func (h *holdingsHarness) security(id uuid.UUID, name, symbol, typ string) {
	h.t.Helper()
	var t any
	if typ != "" {
		t = typ
	}
	// The security master is global (D-37) and outlives the per-test truncate, so this is idempotent.
	h.exec(`INSERT INTO core.securities (id, name, security_type, trading_currency) VALUES ($1, $2, $3, 'CAD')
		ON CONFLICT (id) DO NOTHING`, id, name, t)
	if symbol != "" {
		h.exec(`INSERT INTO core.security_symbols (security_id, symbol, exchange) SELECT $1, $2, 'TSX'
			WHERE NOT EXISTS (SELECT 1 FROM core.security_symbols WHERE security_id = $1)`, id, symbol)
	}
}

// tx writes a manual ledger row; amounts as text.
func (h *holdingsHarness) tx(user, account uuid.UUID, typ, date string, sec *uuid.UUID, qty, price, net string) uuid.UUID {
	h.t.Helper()
	h.nk++
	key := strings.Repeat(string("0123456789abcdef"[h.nk%16]), 63) + string("0123456789abcdef"[h.nk/16%16])
	var q, p any
	if qty != "" {
		q = qty
	}
	if price != "" {
		p = price
	}
	var id uuid.UUID
	if err := h.store.Pool.QueryRow(context.Background(), `INSERT INTO core.transactions (user_id, account_id, type,
		trade_date, security_id, quantity, price, net_amount, currency, origin, natural_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'CAD', 'manual', $9) RETURNING id`,
		user, account, typ, date, sec, q, p, net, key).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func dEq(t *testing.T, what string, got *decimal.Decimal, want string) {
	t.Helper()
	if want == "null" {
		if got != nil {
			t.Errorf("%s = %s, want null", what, got)
		}
		return
	}
	if got == nil || !got.Equal(decimal.RequireFromString(want)) {
		t.Errorf("%s = %v, want %s", what, got, want)
	}
}

func (h *holdingsHarness) seedPortfolio() {
	a := h.userA
	h.account(a, tfsa, "Synthetic TFSA", "TFSA", "self_directed", true)
	h.account(a, nonr, "Synthetic Cash", "NON_REGISTERED", "self_directed", false)
	h.account(a, robo, "Synthetic Managed RRSP", "RRSP", "robo", true)
	h.security(secS1, "SYNTHETIC GROWTH ETF", "QZGR", "")
	h.security(secG, "SYNTHETIC BANK GIC 3.5% 2026-01-15", "", "gic")
	h.tx(a, tfsa, "DEPOSIT", "2025-01-02", nil, "", "", "1000")
	h.tx(a, tfsa, "BUY", "2025-01-10", &secS1, "10", "20", "-200")
	h.tx(a, tfsa, "BUY", "2025-01-15", &secG, "500", "1", "-500")
	h.tx(a, nonr, "DEPOSIT", "2025-01-02", nil, "", "", "300")
	h.tx(a, nonr, "BUY", "2025-02-10", &secS1, "5", "30", "-150")
	h.tx(a, nonr, "DIVIDEND", "2025-03-31", &secS1, "", "", "1.50")
	h.tx(a, robo, "DEPOSIT", "2025-01-02", nil, "", "", "100")
	h.tx(a, robo, "BUY", "2025-02-12", &secS1, "3", "25", "-75")
}

func TestHoldingsByAccountAndSecurity(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()

	rec := h.do(http.MethodGet, "/api/holdings", "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	rep := decodeJSON[holdings.Report](t, rec)
	if rep.Scope != "all" || rep.AsOf == nil || *rep.AsOf != "2025-03-31" || len(rep.Accounts) != 3 {
		t.Fatalf("scope %s as_of %v accounts %d", rep.Scope, rep.AsOf, len(rep.Accounts))
	}
	byName := map[string]holdings.AccountHoldings{}
	for _, a := range rep.Accounts {
		byName[a.Account.Name] = a
	}
	tf := byName["Synthetic TFSA"]
	if len(tf.Positions) != 2 || tf.Positions[0].Security.Symbol == nil || *tf.Positions[0].Security.Symbol != "QZGR" {
		t.Fatalf("TFSA positions %+v", tf.Positions)
	}
	s1 := tf.Positions[0]
	dEq(t, "TFSA S1 quantity", &s1.Quantity, "10")
	dEq(t, "TFSA S1 avg", s1.AvgCost, "20")
	dEq(t, "TFSA S1 book CAD", s1.BookValueCAD, "200")
	if s1.MarketValue != nil || s1.MarketValueReason != holdings.NoPrice || !s1.Open || s1.BookCurrency != "CAD" {
		t.Errorf("TFSA S1 market value %v reason %q open %v", s1.MarketValue, s1.MarketValueReason, s1.Open)
	}
	gic := tf.Positions[1] // unlisted securities after listed ones
	if gic.Security.Symbol != nil || gic.Security.Type == nil || *gic.Security.Type != "gic" {
		t.Errorf("GIC security %+v", gic.Security)
	}
	if len(tf.Cash) != 1 || !tf.Cash[0].Amount.Equal(decimal.RequireFromString("300")) {
		t.Errorf("TFSA cash %+v", tf.Cash)
	}
	nr := byName["Synthetic Cash"].Positions[0]
	dEq(t, "non-registered income CAD", nr.IncomeCAD, "1.5")
	if s := byName["Synthetic Managed RRSP"]; len(s.Positions) != 1 || s.Account.Management != "robo" {
		t.Errorf("managed account %+v", s)
	}

	// By security: the managed account is left out and named (D-38, FR-12); the average is informational.
	if len(rep.Excluded) != 1 || rep.Excluded[0].AccountID != robo || rep.Excluded[0].Reason != "managed_account" {
		t.Fatalf("excluded %+v", rep.Excluded)
	}
	if len(rep.Securities) != 2 {
		t.Fatalf("securities %+v", rep.Securities)
	}
	c := rep.Securities[0]
	dEq(t, "combined quantity", &c.Quantity, "15")
	dEq(t, "combined book CAD", c.BookValueCAD, "350")
	dEq(t, "combined avg CAD", c.AvgCostCAD, "23.33333333")
	dEq(t, "combined income CAD", c.IncomeCAD, "1.5")
	if c.Accounts != 2 || c.BookCurrency == nil || *c.BookCurrency != "CAD" {
		t.Errorf("combined accounts %d book currency %v", c.Accounts, c.BookCurrency)
	}

	// Totals never turn a missing market value into 0 (NFR-4).
	tot := rep.Totals
	dEq(t, "total book CAD", tot.BookValueCAD.Amount, "925")
	// No prices stored: only the GIC has a value (principal, D-26); the 4 others are left out and counted.
	if tot.MarketValue.Amount == nil || !tot.MarketValue.Amount.Equal(decimal.RequireFromString("500")) ||
		tot.MarketValue.Excludes != 3 || tot.MarketValue.Reason != holdings.NoPrice {
		t.Errorf("market value total %+v", tot.MarketValue)
	}
	if len(tot.Cash) != 1 || !tot.Cash[0].Amount.Equal(decimal.RequireFromString("476.5")) {
		t.Errorf("cash total %+v", tot.Cash)
	}
}

func TestHoldingsScopes(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	names := func(path string) []string {
		rec := h.do(http.MethodGet, path, "", h.a)
		wantStatus(t, rec, http.StatusOK, "")
		var out []string
		for _, a := range decodeJSON[holdings.Report](t, rec).Accounts {
			out = append(out, a.Account.Name)
		}
		return out
	}
	if got := strings.Join(names("/api/holdings?scope=registered"), ","); got != "Synthetic Managed RRSP,Synthetic TFSA" {
		t.Errorf("registered: %s", got)
	}
	if got := strings.Join(names("/api/holdings?scope=non_registered"), ","); got != "Synthetic Cash" {
		t.Errorf("non_registered: %s", got)
	}
	if got := strings.Join(names("/api/holdings?scope=account:"+tfsa.String()), ","); got != "Synthetic TFSA" {
		t.Errorf("account: %s", got)
	}
	wantStatus(t, h.do(http.MethodGet, "/api/holdings?scope=everything", "", h.a), http.StatusBadRequest, "invalid_scope")
	wantStatus(t, h.do(http.MethodGet, "/api/holdings?scope=account:"+uuid.NewString(), "", h.a), http.StatusNotFound, "not_found")
}

func TestSecurityDetailTracesEveryRow(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	// One imported row with its source file (NFR-2).
	batch := h.stage([]stagedRow{{RowNo: 7, Status: "ok", Normalized: []byte("null")}})
	h.exec(`INSERT INTO core.transactions (user_id, account_id, type, trade_date, security_id, quantity, price,
		net_amount, currency, origin, natural_key, source_file_id, source_row_no, import_batch_id)
		SELECT $1, $2, 'SELL', '2025-04-01', $3, 4, 25, 100, 'CAD', 'import', repeat('e', 64), b.source_file_id, 7, b.id
		FROM imports.import_batches b WHERE b.id = $4`, h.userA, tfsa, secS1, batch)

	rec := h.do(http.MethodGet, "/api/securities/"+secS1.String(), "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	d := decodeJSON[holdings.SecurityDetail](t, rec)
	if *d.Security.Symbol != "QZGR" || len(d.Symbols) != 1 || len(d.Positions) != 3 || len(d.Transactions) != 5 {
		t.Fatalf("detail: symbols %d positions %d transactions %d", len(d.Symbols), len(d.Positions), len(d.Transactions))
	}
	for _, p := range d.Positions {
		if p.Account.ID == tfsa {
			dEq(t, "TFSA quantity", &p.Position.Quantity, "6")
			dEq(t, "TFSA realized", p.Position.RealizedGainCAD, "20") // 100 - 4 x 20
		}
	}
	if d.Combined == nil || len(d.Excluded) != 1 {
		t.Fatalf("combined %v excluded %v", d.Combined, d.Excluded)
	}
	dEq(t, "combined quantity", &d.Combined.Quantity, "11")
	var sold holdings.LedgerRow
	for _, r := range d.Transactions {
		if r.Type == "SELL" {
			sold = r
		} else if r.Origin != "manual" || r.Source.FileID != nil {
			t.Errorf("manual row %+v", r)
		}
	}
	if sold.Origin != "import" || sold.Source.FileName == nil || *sold.Source.FileName != "synthetic.csv" ||
		sold.Source.RowNo == nil || *sold.Source.RowNo != 7 || sold.Source.ImportID == nil || *sold.Source.ImportID != batch {
		t.Errorf("imported row source %+v", sold.Source)
	}

	wantStatus(t, h.do(http.MethodGet, "/api/securities/"+uuid.NewString(), "", h.a), http.StatusNotFound, "not_found")
	wantStatus(t, h.do(http.MethodGet, "/api/securities/not-a-uuid", "", h.a), http.StatusNotFound, "not_found")
}

func TestHoldingsAreIsolatedBetweenUsers(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	b := h.secondUser()
	userB := h.userID("second@example.test")
	acctB := uuid.MustParse("21000000-0000-0000-0000-0000000000b1")
	h.account(userB, acctB, "Second TFSA", "TFSA", "self_directed", true)
	h.security(secB, "SYNTHETIC OTHER CORP", "QZOT", "")
	h.tx(userB, acctB, "BUY", "2025-05-05", &secB, "1", "10", "-10")

	rec := h.do(http.MethodGet, "/api/holdings", "", b)
	wantStatus(t, rec, http.StatusOK, "")
	rep := decodeJSON[holdings.Report](t, rec)
	if len(rep.Accounts) != 1 || rep.Accounts[0].Account.ID != acctB || len(rep.Securities) != 1 ||
		rep.Securities[0].Security.ID != secB {
		t.Fatalf("user B sees %+v", rep)
	}
	// The security master is global, positions are not: A's security is not found for B, and B's for A.
	wantStatus(t, h.do(http.MethodGet, "/api/securities/"+secS1.String(), "", b), http.StatusNotFound, "not_found")
	wantStatus(t, h.do(http.MethodGet, "/api/securities/"+secB.String(), "", h.a), http.StatusNotFound, "not_found")
	wantStatus(t, h.do(http.MethodGet, "/api/holdings?scope=account:"+tfsa.String(), "", b), http.StatusNotFound, "not_found")
}

func TestHoldingsRequireLogin(t *testing.T) {
	h := newHoldingsHarness(t)
	for _, p := range []string{"/api/holdings", "/api/securities/" + secS1.String()} {
		if rec := h.do(http.MethodGet, p, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d", p, rec.Code)
		}
	}
}
