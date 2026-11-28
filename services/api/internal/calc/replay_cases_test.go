package calc

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d(s string) *decimal.Decimal { v := decimal.RequireFromString(s); return &v }

func day(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }

func find(t *testing.T, r Result, account, security string) *Position {
	t.Helper()
	for _, p := range r.Positions {
		if p.AccountID == account && p.SecurityID == security {
			return p
		}
	}
	t.Fatalf("no position %s/%s", account, security)
	return nil
}

func hasFlag(p *Position, code, tx string) bool {
	for _, f := range p.Flags {
		if f.Code == code && f.TxID == tx {
			return true
		}
	}
	return false
}

func TestSaleWithoutHistoryIsFlaggedNotValued(t *testing.T) {
	r := Replay([]Tx{
		{ID: "s", AccountID: "A", Type: "SELL", TradeDate: day("2025-01-05"), SecurityID: "S", Quantity: d("3"),
			Price: d("10"), NetAmount: *d("30"), Currency: "CAD"},
		{ID: "b", AccountID: "A", Type: "BUY", TradeDate: day("2025-02-05"), SecurityID: "S", Quantity: d("5"),
			Price: d("10"), NetAmount: *d("-50"), Currency: "CAD"},
	})
	p := find(t, r, "A", "S")
	if !p.Quantity.Equal(*d("2")) || p.Cost != nil || p.Realized != nil || p.AvgCost() != nil {
		t.Fatalf("got qty %s cost %v realized %v", p.Quantity, p.Cost, p.Realized)
	}
	if !hasFlag(p, FlagNegativeHolding, "s") || hasFlag(p, FlagFXMissing, "s") {
		t.Fatalf("flags %v", p.Flags)
	}
}

func TestSymbolChangeOntoAnotherSecurityCarriesCost(t *testing.T) {
	// D-37: the new ticker was already a separate security, so the legs name two ids within one group.
	r := Replay([]Tx{
		{ID: "b", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-05"), SecurityID: "OLD", Quantity: d("30"),
			Price: d("10"), Commission: *d("6"), NetAmount: *d("-306"), Currency: "CAD"},
		{ID: "in", AccountID: "A", Type: "SYMBOL_CHANGE", TradeDate: day("2025-03-03"), SecurityID: "NEW",
			Quantity: d("30"), NetAmount: decimal.Zero, Currency: "CAD", GroupID: "g"},
		{ID: "out", AccountID: "A", Type: "SYMBOL_CHANGE", TradeDate: day("2025-03-03"), SecurityID: "OLD",
			Quantity: d("-30"), NetAmount: decimal.Zero, Currency: "CAD", GroupID: "g"},
	})
	old, nu := find(t, r, "A", "OLD"), find(t, r, "A", "NEW")
	if !old.Quantity.IsZero() || !old.Cost.IsZero() || !old.Realized.IsZero() {
		t.Fatalf("old: qty %s cost %s realized %s", old.Quantity, old.Cost, old.Realized)
	}
	if !nu.Quantity.Equal(*d("30")) || !nu.Cost.Equal(*d("306")) || !nu.AvgCost().Equal(*d("10.2")) || len(nu.Flags) > 0 {
		t.Fatalf("new: qty %s cost %v flags %v", nu.Quantity, nu.Cost, nu.Flags)
	}
}

func TestJournalWithinOnePositionChangesNothing(t *testing.T) {
	// Questrade BRW: the same security moves from the CAD to the USD side of the account.
	r := Replay([]Tx{
		{ID: "b", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-05"), SecurityID: "S", Quantity: d("10"),
			Price: d("50"), NetAmount: *d("-500"), Currency: "CAD"},
		{ID: "j1", AccountID: "A", Type: "JOURNAL", TradeDate: day("2025-02-05"), SecurityID: "S", Quantity: d("-10"),
			Price: d("50"), NetAmount: decimal.Zero, Currency: "CAD", GroupID: "j"},
		{ID: "j2", AccountID: "A", Type: "JOURNAL", TradeDate: day("2025-02-05"), SecurityID: "S", Quantity: d("10"),
			Price: d("36"), NetAmount: decimal.Zero, Currency: "USD", GroupID: "j"},
	})
	p := find(t, r, "A", "S")
	if !p.Quantity.Equal(*d("10")) || !p.Cost.Equal(*d("500")) || p.BookCurrency != "CAD" || len(p.Flags) > 0 {
		t.Fatalf("qty %s cost %v book %s flags %v", p.Quantity, p.Cost, p.BookCurrency, p.Flags)
	}
}

func TestTransferAwaitingItsCounterpartUsesTheBrokerValue(t *testing.T) {
	// D-27: detected as internal, but the other account's leg is not imported yet.
	r := Replay([]Tx{
		{ID: "in", AccountID: "B", Type: "TRANSFER_IN", TradeDate: day("2025-03-03"), SecurityID: "S",
			Quantity: d("4"), GrossAmount: d("88"), NetAmount: decimal.Zero, Currency: "CAD", GroupID: "g",
			Counterparty: "A"},
	})
	p := find(t, r, "B", "S")
	if !p.Cost.Equal(*d("88")) || len(p.Flags) > 0 {
		t.Fatalf("cost %v flags %v", p.Cost, p.Flags)
	}
}

func TestUSSecurityBoughtInCADKeepsACADBook(t *testing.T) {
	// D-38: Wealthsimple settles US trades in CAD; the broker's rate stays in raw on CAD rows (D-37).
	r := Replay([]Tx{
		{ID: "b", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-05"), SecurityID: "US1", Quantity: d("2"),
			Price: d("140"), NetAmount: *d("-280"), Currency: "CAD"},
		{ID: "dv", AccountID: "A", Type: "DIVIDEND", TradeDate: day("2025-02-05"), SecurityID: "US1",
			NetAmount: *d("1.40"), Currency: "CAD"},
	})
	p := find(t, r, "A", "US1")
	if p.BookCurrency != "CAD" || !p.Cost.Equal(*d("280")) || !p.CostCAD.Equal(*d("280")) ||
		!p.IncomeCAD.Equal(*d("1.4")) || len(p.Flags) > 0 {
		t.Fatalf("book %s cost %v cad %v income %v flags %v", p.BookCurrency, p.Cost, p.CostCAD, p.IncomeCAD, p.Flags)
	}
}

func TestReplayDoesNotDependOnInputOrderWithinADayBeyondIncreasesFirst(t *testing.T) {
	a := []Tx{
		{ID: "1", AccountID: "A", Type: "SELL", TradeDate: day("2025-01-05"), SecurityID: "S", Quantity: d("1"),
			Price: d("12"), NetAmount: *d("12"), Currency: "CAD"},
		{ID: "2", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-05"), SecurityID: "S", Quantity: d("3"),
			Price: d("10"), NetAmount: *d("-30"), Currency: "CAD"},
	}
	p := find(t, Replay(a), "A", "S")
	if !p.Quantity.Equal(*d("2")) || !p.Cost.Equal(*d("20")) || !p.Realized.Equal(*d("2")) || len(p.Flags) > 0 {
		t.Fatalf("qty %s cost %v realized %v flags %v", p.Quantity, p.Cost, p.Realized, p.Flags)
	}
}

func TestSplitDeltaBelowZeroIsFlagged(t *testing.T) {
	// A reverse split's legs (Wealthsimple: -9 and +3) on shares the ledger never saw arrive.
	r := Replay([]Tx{
		{ID: "a", AccountID: "A", Type: "SPLIT", TradeDate: day("2025-11-02"), SecurityID: "S", Quantity: d("-9"),
			NetAmount: decimal.Zero, Currency: "CAD", GroupID: "g"},
		{ID: "b", AccountID: "A", Type: "SPLIT", TradeDate: day("2025-11-02"), SecurityID: "S", Quantity: d("3"),
			NetAmount: decimal.Zero, Currency: "CAD", GroupID: "g"},
	})
	p := find(t, r, "A", "S")
	if !p.Quantity.Equal(*d("-6")) || p.Cost != nil || p.CostCAD != nil || !hasFlag(p, FlagNegativeHolding, "a") {
		t.Fatalf("qty %s cost %v flags %v", p.Quantity, p.Cost, p.Flags)
	}
}

func boc(rates map[string]string) func(string, time.Time) *decimal.Decimal {
	return func(cur string, day time.Time) *decimal.Decimal {
		if v, ok := rates[cur+" "+day.Format(time.DateOnly)]; ok {
			return d(v)
		}
		return nil
	}
}

func TestBankOfCanadaRatesFillRowsWithoutABrokerRate(t *testing.T) {
	// D-39: the USD row without a rate takes the BoC rate of its date; the row with a broker rate keeps it.
	txs := []Tx{
		{ID: "b1", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-06"), SecurityID: "U", Quantity: d("10"),
			Price: d("100"), NetAmount: *d("-1000"), Currency: "USD"},
		{ID: "b2", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-07"), SecurityID: "U", Quantity: d("10"),
			Price: d("110"), NetAmount: *d("-1100"), Currency: "USD", FXRateToCAD: d("1.50")},
		{ID: "dv", AccountID: "A", Type: "DIVIDEND", TradeDate: day("2025-01-08"), SecurityID: "U",
			NetAmount: *d("2"), Currency: "USD"},
	}
	opts := Options{RateToCAD: boc(map[string]string{"USD 2025-01-06": "1.4", "USD 2025-01-07": "1.43",
		"USD 2025-01-08": "1.45"})}
	p := find(t, ReplayWith(txs, opts), "A", "U")
	// 1000 x 1.4 + 1100 x 1.50 (the broker's rate wins over BoC's 1.43) = 3050; dividend 2 x 1.45.
	if !p.CostCAD.Equal(*d("3050")) || !p.IncomeCAD.Equal(*d("2.9")) || len(p.Flags) > 0 {
		t.Fatalf("cost CAD %v income CAD %v flags %v", p.CostCAD, p.IncomeCAD, p.Flags)
	}
	// Without the BoC rate for the 6th the CAD cost stays unknown and flagged, as before.
	p = find(t, ReplayWith(txs, Options{RateToCAD: boc(map[string]string{"USD 2025-01-08": "1.45"})}), "A", "U")
	if p.CostCAD != nil || !hasFlag(p, FlagFXMissing, "b1") {
		t.Fatalf("cost CAD %v flags %v", p.CostCAD, p.Flags)
	}
}

func TestTransferWithoutValueIsEstimatedAtThatDaysClose(t *testing.T) {
	// D-23, D-31: shares in without a value take the close of the transfer date (in the security's currency).
	close := func(sec string, day time.Time) (*decimal.Decimal, string) {
		if sec == "U" && day.Equal(time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)) {
			return d("20.5"), "USD"
		}
		return nil, ""
	}
	txs := []Tx{{ID: "in", AccountID: "A", Type: "TRANSFER_IN", TradeDate: day("2025-06-02"), SecurityID: "U",
		Quantity: d("4"), NetAmount: decimal.Zero, Currency: "CAD"}}
	opts := Options{Close: close, RateToCAD: boc(map[string]string{"USD 2025-06-02": "1.4"})}
	p := find(t, ReplayWith(txs, opts), "A", "U")
	if p.BookCurrency != "USD" || !p.Cost.Equal(*d("82")) || !p.CostCAD.Equal(*d("114.8")) ||
		!hasFlag(p, FlagEstimatedCost, "in") || hasFlag(p, FlagCostUnknown, "in") {
		t.Fatalf("book %s cost %v cad %v flags %v", p.BookCurrency, p.Cost, p.CostCAD, p.Flags)
	}
	// No close known: unknown, as before.
	p = find(t, ReplayWith(txs, Options{}), "A", "U")
	if p.Cost != nil || !hasFlag(p, FlagCostUnknown, "in") {
		t.Fatalf("cost %v flags %v", p.Cost, p.Flags)
	}
}

func TestCADRowOfAUSDBookUsesTheBookCurrencyRate(t *testing.T) {
	// A sale settled in CAD of shares bought in USD: proceeds convert to USD with BoC USD of the sale date.
	txs := []Tx{
		{ID: "b", AccountID: "A", Type: "BUY", TradeDate: day("2025-01-06"), SecurityID: "U", Quantity: d("10"),
			Price: d("100"), NetAmount: *d("-1000"), Currency: "USD", FXRateToCAD: d("1.4")},
		{ID: "s", AccountID: "A", Type: "SELL", TradeDate: day("2025-02-06"), SecurityID: "U", Quantity: d("5"),
			Price: d("150"), NetAmount: *d("750"), Currency: "CAD"},
	}
	p := find(t, ReplayWith(txs, Options{RateToCAD: boc(map[string]string{"USD 2025-02-06": "1.5"})}), "A", "U")
	// 750 CAD = 500 USD; cost out 500 USD -> realized 0 USD; CAD: 750 - 700 = 50.
	if !p.Realized.Equal(decimal.Zero) || !p.RealizedCAD.Equal(*d("50")) || len(p.Flags) > 0 {
		t.Fatalf("realized %v CAD %v flags %v", p.Realized, p.RealizedCAD, p.Flags)
	}
}
