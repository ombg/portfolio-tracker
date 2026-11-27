package calc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// Golden ledgers in testdata/synthetic (specs/testing.md): hand-computed, compared exactly as decimals.

func fixture(t *testing.T, name, file string, into any) {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(f), "..", "..", "..", "..", "testdata", "synthetic", name, file)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

type jsonTx struct {
	ID, Account, Type, Date, Security, Currency, Group, Counterparty string
	Quantity, SplitRatio, Price, GrossAmount, FXRateToCAD            *string
	Commission                                                       *string
	NetAmount                                                        string `json:"net_amount"`
}

func (j *jsonTx) UnmarshalJSON(b []byte) error {
	var m map[string]*string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	get := func(k string) string {
		if v := m[k]; v != nil {
			return *v
		}
		return ""
	}
	*j = jsonTx{ID: get("id"), Account: get("account"), Type: get("type"), Date: get("date"),
		Security: get("security"), Currency: get("currency"), Group: get("group"), Counterparty: get("counterparty"),
		Quantity: m["quantity"], SplitRatio: m["split_ratio"], Price: m["price"], GrossAmount: m["gross_amount"],
		FXRateToCAD: m["fx_rate_to_cad"], Commission: m["commission"], NetAmount: get("net_amount")}
	return nil
}

func dec(t *testing.T, s *string) *decimal.Decimal {
	t.Helper()
	if s == nil {
		return nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func ledger(t *testing.T, name string) []Tx {
	var in struct{ Transactions []jsonTx }
	fixture(t, name, "input/ledger.json", &in)
	var out []Tx
	for _, j := range in.Transactions {
		day, err := time.Parse(time.DateOnly, j.Date)
		if err != nil {
			t.Fatal(err)
		}
		tx := Tx{ID: j.ID, AccountID: j.Account, Type: j.Type, TradeDate: day, SecurityID: j.Security,
			Quantity: dec(t, j.Quantity), SplitRatio: dec(t, j.SplitRatio), Price: dec(t, j.Price),
			GrossAmount: dec(t, j.GrossAmount), NetAmount: *dec(t, &j.NetAmount), Currency: j.Currency,
			FXRateToCAD: dec(t, j.FXRateToCAD), GroupID: j.Group, Counterparty: j.Counterparty}
		if c := dec(t, j.Commission); c != nil {
			tx.Commission = *c
		}
		out = append(out, tx)
	}
	return out
}

type wantPos struct {
	Account, Security, Quantity string
	BookCurrency                string `json:"book_currency"`
	Cost, CostCAD               *string
	AvgCost, AvgCostCAD         *string
	Realized, RealizedCAD       *string
	Income                      map[string]string
	IncomeCAD                   *string `json:"income_cad"`
	Flags                       []struct{ Code, Date, Tx string }
}

func (w *wantPos) UnmarshalJSON(b []byte) error {
	type plain wantPos
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if err := json.Unmarshal(b, (*plain)(w)); err != nil {
		return err
	}
	opt := func(k string) (*string, error) {
		var s *string
		err := json.Unmarshal(m[k], &s)
		return s, err
	}
	var err error
	for k, dst := range map[string]**string{"cost": &w.Cost, "cost_cad": &w.CostCAD, "avg_cost": &w.AvgCost,
		"avg_cost_cad": &w.AvgCostCAD, "realized": &w.Realized, "realized_cad": &w.RealizedCAD} {
		if *dst, err = opt(k); err != nil {
			return err
		}
	}
	return nil
}

// same compares a computed optional decimal with the expected text ("null" = unknown).
func same(got *decimal.Decimal, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Equal(decimal.RequireFromString(*want))
}

func show(d *decimal.Decimal) string {
	if d == nil {
		return "null"
	}
	return d.String()
}

func showWant(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

func TestGoldenLedgers(t *testing.T) {
	for _, name := range []string{"avg_cost", "internal_transfer", "untracked_transfer"} {
		t.Run(name, func(t *testing.T) {
			var want struct {
				Positions []wantPos
				Cash      []struct{ Account, Currency, Amount string }
			}
			fixture(t, name, "expected/holdings.json", &want)
			got := Replay(ledger(t, name))

			if len(got.Positions) != len(want.Positions) {
				t.Fatalf("positions: got %d, want %d", len(got.Positions), len(want.Positions))
			}
			for i, w := range want.Positions {
				p := got.Positions[i]
				where := fmt.Sprintf("%s/%s", w.Account, w.Security)
				if p.AccountID != w.Account || p.SecurityID != w.Security {
					t.Fatalf("position %d: got %s/%s, want %s", i, p.AccountID, p.SecurityID, where)
				}
				checks := []struct {
					field string
					got   *decimal.Decimal
					want  *string
				}{
					{"quantity", &p.Quantity, &w.Quantity}, {"cost", p.Cost, w.Cost}, {"cost_cad", p.CostCAD, w.CostCAD},
					{"avg_cost", p.AvgCost(), w.AvgCost}, {"avg_cost_cad", p.AvgCostCAD(), w.AvgCostCAD},
					{"realized", p.Realized, w.Realized}, {"realized_cad", p.RealizedCAD, w.RealizedCAD},
					{"income_cad", p.IncomeCAD, w.IncomeCAD},
				}
				for _, c := range checks {
					if !same(c.got, c.want) {
						t.Errorf("%s %s: got %s, want %s", where, c.field, show(c.got), showWant(c.want))
					}
				}
				if p.BookCurrency != w.BookCurrency {
					t.Errorf("%s book_currency: got %s, want %s", where, p.BookCurrency, w.BookCurrency)
				}
				if len(p.Income) != len(w.Income) {
					t.Errorf("%s income: got %v, want %v", where, p.Income, w.Income)
				}
				for cur, amt := range w.Income {
					if !p.Income[cur].Equal(decimal.RequireFromString(amt)) {
						t.Errorf("%s income %s: got %s, want %s", where, cur, p.Income[cur], amt)
					}
				}
				var gotFlags, wantFlags []string
				for _, f := range p.Flags {
					gotFlags = append(gotFlags, fmt.Sprintf("%s@%s/%s", f.Code, f.Date.Format(time.DateOnly), f.TxID))
				}
				for _, f := range w.Flags {
					wantFlags = append(wantFlags, fmt.Sprintf("%s@%s/%s", f.Code, f.Date, f.Tx))
				}
				sort.Strings(gotFlags)
				sort.Strings(wantFlags)
				if strings.Join(gotFlags, ",") != strings.Join(wantFlags, ",") {
					t.Errorf("%s flags: got %v, want %v", where, gotFlags, wantFlags)
				}
			}
			if len(got.Cash) != len(want.Cash) {
				t.Fatalf("cash: got %v, want %v", got.Cash, want.Cash)
			}
			for i, w := range want.Cash {
				c := got.Cash[i]
				if c.AccountID != w.Account || c.Currency != w.Currency || !c.Amount.Equal(decimal.RequireFromString(w.Amount)) {
					t.Errorf("cash %d: got %s %s %s, want %s %s %s", i, c.AccountID, c.Currency, c.Amount, w.Account, w.Currency, w.Amount)
				}
			}
		})
	}
}
