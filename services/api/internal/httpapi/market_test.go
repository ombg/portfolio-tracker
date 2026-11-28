package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/market"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

// Market status (#28, FR-6, AC-12, D-39) against a fake market-data and stored prices.

var secU = uuid.MustParse("31000000-0000-0000-0000-000000000004") // held by A, never priced

func fakeMarketData(t *testing.T, body *string, status *int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/status" || r.Header.Get("X-Service-Token") != "synthetic-token" {
			t.Errorf("unexpected request %s %s", r.URL.Path, r.Header.Get("X-Service-Token"))
		}
		w.WriteHeader(*status)
		_, _ = w.Write([]byte(*body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const runningStatus = `{"provider": "yahoo", "clock": "live", "now": "2025-11-27T18:00:00Z", "market_open": true,
  "last_close_date": "2025-11-26",
  "stale_before": {"TSX": "2025-11-27T17:00:00Z", "US": "2025-11-26T20:00:00Z"},
  "next_run_at": "2025-11-27T18:30:00Z",
  "runs": [
    {"id": "r3", "kind": "backfill", "started_at": "2025-11-27T17:59:00Z", "finished_at": "2025-11-27T17:59:30Z",
     "status": "partial", "symbols_ok": 1, "symbols_failed": 1, "errors": {"QZOT.TO": "not found"}},
    {"id": "r2", "kind": "intraday", "started_at": "2025-11-27T17:30:00Z", "finished_at": "2025-11-27T17:30:05Z",
     "status": "partial", "symbols_ok": 2, "symbols_failed": 2,
     "errors": {"QZUP.TO": "not found: No data found", "QZOT.TO": "HTTP 503"}},
    {"id": "r1", "kind": "intraday", "started_at": "2025-11-27T17:00:00Z", "finished_at": "2025-11-27T17:00:04Z",
     "status": "success", "symbols_ok": 3, "symbols_failed": 0, "errors": {}}
  ]}`

func TestMarketStatusIsSeenThroughTheCallersSecurities(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	h.security(secU, "SYNTHETIC UNPRICED CORP", "QZUP", "")
	h.tx(h.userA, tfsa, "BUY", "2025-03-03", &secU, "2", "10", "-20")
	// User B holds another TSX security whose refresh failed.
	b := h.secondUser()
	userB := h.userID("second@example.test")
	acctB := uuid.MustParse("21000000-0000-0000-0000-0000000000b1")
	h.account(userB, acctB, "Second TFSA", "TFSA", "self_directed", true)
	h.security(secB, "SYNTHETIC OTHER CORP", "QZOT", "")
	h.tx(userB, acctB, "BUY", "2025-05-05", &secB, "1", "10", "-10")
	// QZGR's latest price is from 16:30 UTC, before the TSX cut-off of 17:00: stale.
	h.exec(`INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price, currency, source) VALUES
		($1, 'close', '2025-11-26', '2025-11-26 21:00Z', 27.10, 'CAD', 'yahoo'),
		($1, 'intraday', '2025-11-27', '2025-11-27 16:30Z', 27.51, 'CAD', 'yahoo')
		ON CONFLICT DO NOTHING`, secS1)

	body, code := runningStatus, http.StatusOK
	srv := fakeMarketData(t, &body, &code)
	h.deps.MarketData = marketdata.New(srv.URL, "synthetic-token")
	h.router = NewRouter(h.deps)
	get := func(c *http.Cookie) market.Status {
		rec := h.do(http.MethodGet, "/api/market/status", "", c)
		wantStatus(t, rec, http.StatusOK, "")
		return decodeJSON[market.Status](t, rec)
	}

	a := get(h.a)
	if a.State != market.StatePartial || *a.LastRunStatus != "partial" || a.LastSuccessAt == nil ||
		a.LastSuccessAt.Format("15:04") != "17:00" || a.NextRunAt == nil || !*a.MarketOpen {
		t.Fatalf("A: state %s last run %v last success %v", a.State, a.LastRunStatus, a.LastSuccessAt)
	}
	if len(a.Runs) != 3 || len(a.Runs[1].Errors) != 1 || a.Runs[1].Errors["QZUP.TO"] == "" {
		t.Fatalf("A's runs must hold A's symbols only: %+v", a.Runs)
	}
	if a.Runs[0].Status != "success" || len(a.Runs[0].Errors) != 0 {
		t.Errorf("A: B's backfill failure must not show: %+v", a.Runs[0])
	}
	if len(a.Stale) != 1 || a.Stale[0].Security.ID != secS1 || a.Stale[0].Price.String() != "27.51" {
		t.Errorf("A stale %+v", a.Stale)
	}
	if len(a.Unpriced) != 1 || a.Unpriced[0].ID != secU {
		t.Errorf("A unpriced %+v (the GIC is valued at principal and never unpriced)", a.Unpriced)
	}

	bs := get(b)
	if bs.Runs[1].Errors["QZOT.TO"] != "HTTP 503" || bs.Runs[1].Errors["QZUP.TO"] != "" {
		t.Errorf("B's runs %+v", bs.Runs[1])
	}
	raw, _ := json.Marshal(bs)
	for _, leak := range []string{"QZUP", "QZGR", secS1.String(), secU.String()} {
		if contains(string(raw), leak) {
			t.Errorf("B's status names A's %s: %s", leak, raw)
		}
	}

	body = `{"provider": "off", "clock": "live", "now": "2025-11-27T18:00:00Z", "market_open": true,
		"last_close_date": "2025-11-26", "stale_before": {}, "runs": []}`
	if s := get(h.a); s.State != market.StateOff || len(s.Stale) != 0 {
		t.Errorf("off: %+v", s)
	}
	// The latest run succeeded for A, but A holds a security that has never been priced: not up to date.
	body = runningStatus
	var st map[string]any
	_ = json.Unmarshal([]byte(body), &st)
	st["runs"] = st["runs"].([]any)[2:]
	raw, _ = json.Marshal(st)
	body = string(raw)
	if s := get(h.a); s.State != market.StatePartial || *s.LastRunStatus != "success" {
		t.Errorf("unpriced holding: state %s, last run %v", s.State, s.LastRunStatus)
	}
	code = http.StatusBadGateway
	if s := get(h.a); s.State != market.StateUnavailable || len(s.Unpriced) != 1 {
		t.Errorf("unavailable: %+v", s)
	}
	if rec := h.do(http.MethodGet, "/api/market/status", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", rec.Code)
	}
}

func TestMarketStatusBeforeAnyRun(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	body, code := `{"provider": "yahoo", "clock": "live", "now": "2025-11-27T18:00:00Z", "market_open": false,
		"last_close_date": "2025-11-26", "stale_before": {"TSX": "2025-11-26T20:00:00Z"}, "runs": []}`, http.StatusOK
	srv := fakeMarketData(t, &body, &code)
	h.deps.MarketData = marketdata.New(srv.URL, "synthetic-token")
	h.router = NewRouter(h.deps)
	rec := h.do(http.MethodGet, "/api/market/status", "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	if s := decodeJSON[market.Status](t, rec); s.State != market.StateNever || s.LastSuccessAt != nil {
		t.Fatalf("before any run: %+v", s)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
