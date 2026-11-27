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
