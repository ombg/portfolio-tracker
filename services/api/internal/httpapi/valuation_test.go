package httpapi

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/holdings"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

// Market values on read (#28, D-39): prices, rates, weights, stale flags and the gaps of M3 filled.

var (
	secV = uuid.MustParse("31000000-0000-0000-0000-000000000005") // US, bought in USD without a broker rate
	secW = uuid.MustParse("31000000-0000-0000-0000-000000000006") // TSX, arrived in kind without a value
)

func TestHoldingsValuation(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	h.exec(`INSERT INTO core.securities (id, name, trading_currency) VALUES ($1, 'ZEDCO VALUE INC', 'USD'),
		($2, 'SYNTHETIC TRANSFER FUND', 'CAD') ON CONFLICT (id) DO NOTHING`, secV, secW)
	h.exec(`INSERT INTO core.security_symbols (security_id, symbol, exchange) SELECT $1, 'ZZVL', 'US'
		WHERE NOT EXISTS (SELECT 1 FROM core.security_symbols WHERE security_id = $1)`, secV)
	h.exec(`INSERT INTO core.security_symbols (security_id, symbol, exchange) SELECT $1, 'QZTW', 'TSX'
		WHERE NOT EXISTS (SELECT 1 FROM core.security_symbols WHERE security_id = $1)`, secW)
	h.exec(`INSERT INTO core.transactions (user_id, account_id, type, trade_date, security_id, quantity, price,
		net_amount, currency, origin, natural_key) VALUES
		($1, $2, 'BUY', '2025-03-03', $3, 4, 50, -200, 'USD', 'manual', repeat('7', 64)),
		($1, $4, 'TRANSFER_IN', '2025-06-02', $5, 2, NULL, 0, 'CAD', 'manual', repeat('8', 64))`,
		h.userA, nonr, secV, tfsa, secW)
	h.exec(`INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price, currency, source) VALUES
		($1, 'intraday', '2025-11-27', '2025-11-27 17:30Z', 25, 'CAD', 'yahoo'),
		($2, 'intraday', '2025-11-26', '2025-11-26 19:00Z', 60, 'USD', 'yahoo'),
		($3, 'close', '2025-05-30', '2025-05-30 20:00Z', 12, 'CAD', 'yahoo')`, secS1, secV, secW)
	h.exec(`INSERT INTO market.fx_rates (base, quote, kind, rate_date, observed_at, rate, source) VALUES
		('USD', 'CAD', 'daily', '2025-03-03', '2025-03-03 21:30Z', 1.43, 'boc'),
		('USD', 'CAD', 'intraday', '2025-11-27', '2025-11-27 17:30Z', 1.40, 'yahoo')`)
	body, code := runningStatus, http.StatusOK // stale_before: TSX 2025-11-27 17:00Z, US 2025-11-26 20:00Z
	srv := fakeMarketData(t, &body, &code)
	h.deps.Holdings.Market = marketdata.New(srv.URL, "synthetic-token")
	h.router = NewRouter(h.deps)

	rec := h.do(http.MethodGet, "/api/holdings", "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	rep := decodeJSON[holdings.Report](t, rec)
	pos := map[string]holdings.Position{}
	for _, a := range rep.Accounts {
		for _, p := range a.Positions {
			pos[a.Account.Name+"/"+p.Security.Name] = p
		}
	}
	s1 := pos["Synthetic TFSA/SYNTHETIC GROWTH ETF"]
	dEq(t, "TFSA S1 market value", s1.MarketValue, "250")
	dEq(t, "TFSA S1 unrealized", s1.UnrealizedGainCAD, "50")
	dEq(t, "TFSA S1 weight", s1.Weight, "0.190840") // 250 / 1310
	if b := s1.Valuation; b == nil || b.PriceSource != "yahoo" || b.FXRate != nil || b.Stale == nil || *b.Stale ||
		s1.MarketValueReason != "" {
		t.Errorf("TFSA S1 basis %+v reason %q", s1.Valuation, s1.MarketValueReason)
	}
	gic := pos["Synthetic TFSA/SYNTHETIC BANK GIC 3.5% 2026-01-15"]
	dEq(t, "GIC market value (principal)", gic.MarketValue, "500")
	if gic.Valuation.PriceSource != "principal" {
		t.Errorf("GIC basis %+v", gic.Valuation)
	}
	// USD bought without a broker rate: the CAD cost uses BoC of the trade date (D-6); the value uses the latest
	// rate. 4 x 50 x 1.43 = 286; 4 x 60 x 1.40 = 336. Its price is older than the US cut-off: stale.
	v := pos["Synthetic Cash/ZEDCO VALUE INC"]
	dEq(t, "USD cost CAD (BoC)", v.BookValueCAD, "286")
	dEq(t, "USD market value", v.MarketValue, "336")
	dEq(t, "USD unrealized", v.UnrealizedGainCAD, "50")
	if len(v.Flags) != 0 || v.Valuation.FXSource != "yahoo" || v.Valuation.FXRate.String() != "1.4" ||
		v.Valuation.Stale == nil || !*v.Valuation.Stale {
		t.Errorf("USD flags %+v basis %+v", v.Flags, v.Valuation)
	}
	// In kind on Monday without a value: Friday's close x 2 = 24, labelled estimated (D-23, D-31).
	w := pos["Synthetic TFSA/SYNTHETIC TRANSFER FUND"]
	dEq(t, "estimated cost", w.BookValueCAD, "24")
	if len(w.Flags) != 1 || w.Flags[0].Code != "estimated_cost" {
		t.Errorf("transfer flags %+v", w.Flags)
	}

	tot := rep.Totals
	dEq(t, "total market value", tot.MarketValue.Amount, "1310") // 250 + 500 + 125 + 75 + 336 + 24
	dEq(t, "total unrealized", tot.UnrealizedGainCAD.Amount, "75")
	if tot.MarketValue.Excludes != 0 || tot.MarketValue.Reason != "" {
		t.Errorf("market value total %+v", tot.MarketValue)
	}
	for _, c := range rep.Securities {
		if c.Security.ID == secS1 { // managed account left out: 10 + 5
			dEq(t, "combined market value", c.MarketValue, "375")
			dEq(t, "combined unrealized", c.UnrealizedGainCAD, "25")
			dEq(t, "combined weight", c.Weight, "0.286260")
		}
	}

	rec = h.do(http.MethodGet, "/api/securities/"+secV.String(), "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	d := decodeJSON[holdings.SecurityDetail](t, rec)
	if r := d.Transactions[0]; r.FXRateToCAD != nil || r.FXRate == nil || r.FXRate.String() != "1.43" || r.FXSource != "boc" {
		t.Errorf("ledger row rate %+v %v %q", r.FXRateToCAD, r.FXRate, r.FXSource)
	}
	dEq(t, "detail market value", d.Positions[0].Position.MarketValue, "336")
}

func TestValuationWithoutMarketDataStillShowsPricesButNoStaleness(t *testing.T) {
	h := newHoldingsHarness(t)
	h.seedPortfolio()
	h.exec(`INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price, currency, source)
		VALUES ($1, 'close', '2025-11-26', '2025-11-26 21:00Z', 26, 'CAD', 'yahoo')`, secS1)
	rec := h.do(http.MethodGet, "/api/holdings?scope=account:"+tfsa.String(), "", h.a)
	wantStatus(t, rec, http.StatusOK, "")
	p := decodeJSON[holdings.Report](t, rec).Accounts[0].Positions[0]
	dEq(t, "market value", p.MarketValue, "260")
	if p.Valuation.Stale != nil {
		t.Errorf("market-data unreachable: staleness must be unknown, got %v", *p.Valuation.Stale)
	}
}
