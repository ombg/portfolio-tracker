// Package calc derives holdings, average cost, realized gain, income and cash from the ledger
// (specs/calculations.md, FR-7, FR-8, D-17, D-23, D-38). It is pure: callers pass v_ledger rows and get results; no
// database, no clock. Money and quantities are decimals throughout (rule 3); an unknown value is a nil pointer with a
// flag on the position, never 0 (NFR-4).
package calc

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Tx is one v_ledger row, reduced to what the replay reads.
type Tx struct {
	ID           string
	AccountID    string
	Type         string
	TradeDate    time.Time
	SecurityID   string // "" when the row has none
	Quantity     *decimal.Decimal
	SplitRatio   *decimal.Decimal
	Price        *decimal.Decimal
	Commission   decimal.Decimal
	GrossAmount  *decimal.Decimal
	NetAmount    decimal.Decimal
	Currency     string
	FXRateToCAD  *decimal.Decimal
	GroupID      string // "" when none
	Counterparty string // counterparty_account_id, "" when none
}

// Flag codes (data-quality.md, D-38).
const (
	FlagNegativeHolding  = "negative_holding"
	FlagFXMissing        = "fx_missing"
	FlagCostUnknown      = "cost_unknown"
	FlagUnsupportedEvent = "unsupported_event"
	FlagEstimatedCost    = "estimated_cost"
)

// Options supply market data to the replay (D-39); a nil function means none is known yet.
type Options struct {
	// RateToCAD is the rate of one unit of currency in CAD on day (Bank of Canada, previous business day when the
	// day has none, D-6). Used when a row carries no broker rate, or for another currency than the row's.
	RateToCAD func(currency string, day time.Time) *decimal.Decimal
	// Close is a security's close on day (or the last one before it) and its currency, to value an in-kind transfer
	// that came without a value (D-23, D-31: the cost is then labelled estimated).
	Close func(securityID string, day time.Time) (*decimal.Decimal, string)
}

// Flag marks a condition on a position from the row that first raised it.
type Flag struct {
	Code string
	Date time.Time
	TxID string
}

// Position is one account's holding of one security. Cost and realized gain are in BookCurrency (the currency the
// position was paid in) and in CAD; nil means unknown (see Flags).
type Position struct {
	AccountID    string
	SecurityID   string
	Quantity     decimal.Decimal
	BookCurrency string
	Cost         *decimal.Decimal
	CostCAD      *decimal.Decimal
	Realized     *decimal.Decimal
	RealizedCAD  *decimal.Decimal
	Income       map[string]decimal.Decimal // income net of withholding, per currency
	IncomeCAD    *decimal.Decimal
	Flags        []Flag
}

// AvgCost is Cost / Quantity, nil when either is unknown or the quantity is not positive.
func (p *Position) AvgCost() *decimal.Decimal { return avg(p.Cost, p.Quantity) }

// AvgCostCAD is CostCAD / Quantity.
func (p *Position) AvgCostCAD() *decimal.Decimal { return avg(p.CostCAD, p.Quantity) }

// Open reports whether the position still holds shares (or is short after missing history).
func (p *Position) Open() bool { return !p.Quantity.IsZero() }

// Cash is one account's balance in one currency: the sum of net_amount (calculations.md, Cash).
type Cash struct {
	AccountID string
	Currency  string
	Amount    decimal.Decimal
}

// Result of a replay, sorted by account, then security or currency.
type Result struct {
	Positions []*Position
	Cash      []Cash
}

// AvgPlaces is the precision averages are rounded to for display (NUMERIC(24,8)).
const AvgPlaces = 8

func avg(cost *decimal.Decimal, qty decimal.Decimal) *decimal.Decimal {
	if cost == nil || !qty.IsPositive() {
		return nil
	}
	v := cost.DivRound(qty, AvgPlaces)
	return &v
}

// carryTypes move cost between positions within a group instead of buying or selling (D-27, D-37).
func carries(t *Tx) bool {
	switch t.Type {
	case "SYMBOL_CHANGE", "JOURNAL":
		return t.GroupID != "" && t.SecurityID != ""
	case "TRANSFER_IN", "TRANSFER_OUT":
		return t.GroupID != "" && t.SecurityID != "" && t.Counterparty != ""
	}
	return false
}

// out reports whether a carry leg gives shares away.
func out(t *Tx) bool {
	if t.Type == "TRANSFER_OUT" {
		return true
	}
	if t.Type == "TRANSFER_IN" {
		return false
	}
	return t.Quantity != nil && t.Quantity.IsNegative()
}

// increases reports whether a row adds shares; on one day those go first, so a same-day buy and sell never looks
// like a sale without history (calculations.md).
func increases(t *Tx) bool {
	switch t.Type {
	case "BUY", "DRIP", "TRANSFER_IN":
		return true
	case "SPLIT", "SYMBOL_CHANGE", "JOURNAL":
		return t.SplitRatio != nil || (t.Quantity != nil && t.Quantity.IsPositive())
	}
	return false
}

type posKey struct{ account, security string }

type pool struct {
	qty, cost, costCAD decimal.Decimal
	known, knownCAD    bool
	currency           string
	pendingOut         int
	waiting            []*Tx
}

type replay struct {
	opts  Options
	pos   map[posKey]*Position
	cash  map[[2]string]decimal.Decimal
	pools map[string]*pool
}

// Replay runs the ledger in (trade date, increases first, input order) order. Callers pass rows in a stable order
// (v_ledger by trade date, import time, source row, id).
func Replay(txs []Tx) Result { return ReplayWith(txs, Options{}) }

// ReplayWith is Replay with market data for rates and closes (D-39).
func ReplayWith(txs []Tx, opts Options) Result {
	order := make([]*Tx, len(txs))
	for i := range txs {
		order[i] = &txs[i]
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if !a.TradeDate.Equal(b.TradeDate) {
			return a.TradeDate.Before(b.TradeDate)
		}
		return increases(a) && !increases(b)
	})
	r := &replay{opts: opts, pos: map[posKey]*Position{}, cash: map[[2]string]decimal.Decimal{}, pools: map[string]*pool{}}
	for _, t := range order {
		if carries(t) && out(t) {
			r.pool(t.GroupID).pendingOut++
		}
	}
	for _, t := range order {
		if k := [2]string{t.AccountID, t.Currency}; !t.NetAmount.IsZero() { // in-kind rows move no cash
			r.cash[k] = r.cash[k].Add(t.NetAmount)
		}
		if t.SecurityID == "" {
			continue
		}
		if carries(t) && !out(t) {
			if p := r.pools[t.GroupID]; p != nil && p.pendingOut > 0 {
				p.waiting = append(p.waiting, t) // the shares have not left the other position yet
				continue
			}
		}
		r.apply(t)
	}
	for _, p := range r.pools { // only when an out leg never ran, which the pending count rules out
		for _, t := range p.waiting {
			r.apply(t)
		}
	}
	return r.result()
}

func (r *replay) pool(group string) *pool {
	p := r.pools[group]
	if p == nil {
		p = &pool{known: true, knownCAD: true}
		r.pools[group] = p
	}
	return p
}

func (r *replay) position(t *Tx) *Position {
	k := posKey{t.AccountID, t.SecurityID}
	p := r.pos[k]
	if p == nil {
		zero := decimal.Zero
		p = &Position{AccountID: t.AccountID, SecurityID: t.SecurityID, Cost: ptr(zero), CostCAD: ptr(zero),
			Realized: ptr(zero), RealizedCAD: ptr(zero), Income: map[string]decimal.Decimal{}, IncomeCAD: ptr(zero)}
		r.pos[k] = p
	}
	return p
}

func (r *replay) apply(t *Tx) {
	p := r.position(t)
	r.event(p, t)
	// Whatever took the quantity below zero (a sale, a transfer, a split delta), history is missing: the cost and
	// value are unknown from here on (calculations.md, data-quality.md).
	if p.Quantity.IsNegative() && !p.has(FlagNegativeHolding) {
		p.flag(FlagNegativeHolding, t)
		p.Cost, p.CostCAD = nil, nil
	}
}

func (p *Position) has(code string) bool {
	for _, f := range p.Flags {
		if f.Code == code {
			return true
		}
	}
	return false
}

func (r *replay) event(p *Position, t *Tx) {
	q := decimal.Zero
	if t.Quantity != nil {
		q = t.Quantity.Abs()
	}
	switch t.Type {
	case "BUY", "DRIP":
		if t.Price == nil {
			r.acquire(p, t, q, nil)
			p.flag(FlagCostUnknown, t)
			return
		}
		amount := q.Mul(*t.Price).Add(t.Commission)
		r.acquire(p, t, q, ptr(amount))
	case "SELL":
		taken, takenCAD := r.dispose(p, t, q)
		if t.Price == nil {
			p.Realized, p.RealizedCAD = nil, nil
			p.flag(FlagCostUnknown, t)
			return
		}
		proceeds := q.Mul(*t.Price).Sub(t.Commission)
		native, cad := r.native(p, proceeds, t.Currency, t), r.toCAD(proceeds, t.Currency, t)
		if native == nil || cad == nil {
			p.flag(FlagFXMissing, t)
		}
		// An unknown cost (missing history, already flagged) leaves the realized gain unknown too.
		p.Realized = sub(add(p.Realized, native), taken)
		p.RealizedCAD = sub(add(p.RealizedCAD, cad), takenCAD)
	case "TRANSFER_IN", "SYMBOL_CHANGE", "JOURNAL":
		if carries(t) {
			if out(t) {
				r.giveToPool(p, t, q)
			} else {
				r.takeFromPool(p, t, q)
			}
			return
		}
		if t.Type != "TRANSFER_IN" { // a lone leg: shares appear without a known cost
			if out(t) {
				r.dispose(p, t, q)
			} else {
				r.acquire(p, t, q, nil)
				p.flag(FlagCostUnknown, t)
			}
			return
		}
		if t.GrossAmount == nil { // D-23: value at the transfer date from that day's close
			r.acquireEstimated(p, t, q)
			return
		}
		r.acquire(p, t, q, ptr(t.GrossAmount.Abs()))
	case "TRANSFER_OUT":
		if carries(t) {
			r.giveToPool(p, t, q)
			return
		}
		r.dispose(p, t, q)
	case "SPLIT":
		if t.SplitRatio != nil {
			p.Quantity = p.Quantity.Mul(*t.SplitRatio)
		} else if t.Quantity != nil {
			p.Quantity = p.Quantity.Add(*t.Quantity)
		}
	case "RETURN_OF_CAPITAL":
		amount := t.NetAmount.Abs()
		p.Cost = sub(p.Cost, r.native(p, amount, t.Currency, t))
		p.CostCAD = sub(p.CostCAD, r.toCAD(amount, t.Currency, t))
		if p.Cost == nil || p.CostCAD == nil {
			p.flag(FlagFXMissing, t)
		}
	case "DIVIDEND", "DISTRIBUTION", "INTEREST", "WITHHOLDING_TAX":
		p.Income[t.Currency] = p.Income[t.Currency].Add(t.NetAmount)
		p.IncomeCAD = add(p.IncomeCAD, r.toCAD(t.NetAmount, t.Currency, t))
		if p.IncomeCAD == nil {
			p.flag(FlagFXMissing, t)
		}
	case "MERGER", "SPIN_OFF":
		p.Cost, p.CostCAD = nil, nil
		p.flag(FlagUnsupportedEvent, t)
	}
}

// acquire adds q shares at amount (in the row's currency); a nil amount makes the cost unknown.
func (r *replay) acquire(p *Position, t *Tx, q decimal.Decimal, amount *decimal.Decimal) {
	r.acquireIn(p, t, q, amount, t.Currency)
}

// acquireIn adds q shares at amount in currency (an estimated value may be in the security's currency).
func (r *replay) acquireIn(p *Position, t *Tx, q decimal.Decimal, amount *decimal.Decimal, currency string) {
	if p.BookCurrency == "" {
		p.BookCurrency = currency
	}
	p.Quantity = p.Quantity.Add(q)
	if amount == nil {
		p.Cost, p.CostCAD = nil, nil
		return
	}
	p.Cost = add(p.Cost, r.native(p, *amount, currency, t))
	p.CostCAD = add(p.CostCAD, r.toCAD(*amount, currency, t))
	if p.Cost == nil || p.CostCAD == nil {
		p.flag(FlagFXMissing, t)
	}
}

// dispose removes q shares at average cost and returns the cost removed (native, CAD). Selling more than is held
// means missing history: the position is flagged and its cost becomes unknown (calculations.md).
func (r *replay) dispose(p *Position, t *Tx, q decimal.Decimal) (*decimal.Decimal, *decimal.Decimal) {
	if p.BookCurrency == "" {
		p.BookCurrency = t.Currency
	}
	if q.GreaterThan(p.Quantity) {
		p.flag(FlagNegativeHolding, t)
		p.Quantity = p.Quantity.Sub(q)
		p.Cost, p.CostCAD = nil, nil
		return nil, nil
	}
	taken, takenCAD := share(p.Cost, q, p.Quantity), share(p.CostCAD, q, p.Quantity)
	p.Quantity = p.Quantity.Sub(q)
	if p.Quantity.IsZero() { // exactly what was left: no rounding residue
		taken, takenCAD = p.Cost, p.CostCAD
		p.Cost, p.CostCAD = zeroIfKnown(p.Cost), zeroIfKnown(p.CostCAD)
	} else {
		p.Cost, p.CostCAD = sub(p.Cost, taken), sub(p.CostCAD, takenCAD)
	}
	return taken, takenCAD
}

func (r *replay) giveToPool(p *Position, t *Tx, q decimal.Decimal) {
	pl := r.pool(t.GroupID)
	if pl.currency == "" {
		pl.currency = p.BookCurrency
	}
	taken, takenCAD := r.dispose(p, t, q)
	pl.qty = pl.qty.Add(q)
	if taken == nil {
		pl.known = false
	} else {
		pl.cost = pl.cost.Add(*taken)
	}
	if takenCAD == nil {
		pl.knownCAD = false
	} else {
		pl.costCAD = pl.costCAD.Add(*takenCAD)
	}
	pl.pendingOut--
	if pl.pendingOut == 0 {
		waiting := pl.waiting
		pl.waiting = nil
		for _, w := range waiting {
			r.apply(w)
		}
	}
}

// takeFromPool receives q shares with their share of the cost given in the same group.
func (r *replay) takeFromPool(p *Position, t *Tx, q decimal.Decimal) {
	pl := r.pools[t.GroupID]
	if pl == nil || pl.qty.IsZero() { // the other side is not in the ledger (awaiting counterpart)
		if t.GrossAmount != nil {
			r.acquire(p, t, q, ptr(t.GrossAmount.Abs()))
		} else {
			r.acquireEstimated(p, t, q)
		}
		return
	}
	if p.BookCurrency == "" {
		p.BookCurrency = pl.currency
	}
	var cost, costCAD *decimal.Decimal
	if pl.known {
		cost = share(&pl.cost, q, pl.qty)
	}
	if pl.knownCAD {
		costCAD = share(&pl.costCAD, q, pl.qty)
	}
	if q.Equal(pl.qty) {
		if pl.known {
			cost = ptr(pl.cost)
		}
		if pl.knownCAD {
			costCAD = ptr(pl.costCAD)
		}
	}
	if pl.known {
		pl.cost = pl.cost.Sub(*cost)
	}
	if pl.knownCAD {
		pl.costCAD = pl.costCAD.Sub(*costCAD)
	}
	pl.qty = pl.qty.Sub(q)
	if cost != nil && p.BookCurrency != pl.currency {
		if cost = r.convert(*cost, pl.currency, p.BookCurrency, t); cost == nil {
			p.flag(FlagFXMissing, t)
		}
	}
	p.Quantity = p.Quantity.Add(q)
	p.Cost, p.CostCAD = add(p.Cost, cost), add(p.CostCAD, costCAD)
	if cost == nil || costCAD == nil {
		p.flag(FlagCostUnknown, t)
	}
}

// acquireEstimated values shares that arrived without a value at that day's close (D-23, D-31), else leaves the
// cost unknown until market data has the close.
func (r *replay) acquireEstimated(p *Position, t *Tx, q decimal.Decimal) {
	if r.opts.Close != nil {
		if price, currency := r.opts.Close(t.SecurityID, t.TradeDate); price != nil {
			r.acquireIn(p, t, q, ptr(price.Mul(q)), currency)
			p.flag(FlagEstimatedCost, t)
			return
		}
	}
	r.acquire(p, t, q, nil)
	p.flag(FlagCostUnknown, t)
}

// rate is one unit of currency in CAD for row t: CAD is 1; the row's own broker rate for its currency (D-37); else
// the Bank of Canada rate of the row's date (D-6); nil when none is known yet.
func (r *replay) rate(currency string, t *Tx) *decimal.Decimal {
	if currency == "CAD" {
		one := decimal.NewFromInt(1)
		return &one
	}
	if currency == t.Currency && t.FXRateToCAD != nil {
		return t.FXRateToCAD
	}
	if r.opts.RateToCAD != nil {
		return r.opts.RateToCAD(currency, t.TradeDate)
	}
	return nil
}

// native converts an amount to the position's book currency (calculations.md, D-38).
func (r *replay) native(p *Position, amount decimal.Decimal, currency string, t *Tx) *decimal.Decimal {
	return r.convert(amount, currency, p.BookCurrency, t)
}

// convert changes currency through CAD with the rates of row t.
func (r *replay) convert(amount decimal.Decimal, currency, target string, t *Tx) *decimal.Decimal {
	if currency == target {
		return &amount
	}
	from, to := r.rate(currency, t), r.rate(target, t)
	if from == nil || to == nil {
		return nil
	}
	return ptr(amount.Mul(*from).Div(*to))
}

// toCAD converts with the row's rate, else the Bank of Canada rate of its date; nil when neither is known.
func (r *replay) toCAD(amount decimal.Decimal, currency string, t *Tx) *decimal.Decimal {
	from := r.rate(currency, t)
	if from == nil {
		return nil
	}
	return ptr(amount.Mul(*from))
}

func (p *Position) flag(code string, t *Tx) {
	for _, f := range p.Flags {
		if f.Code == code {
			return
		}
	}
	p.Flags = append(p.Flags, Flag{Code: code, Date: t.TradeDate, TxID: t.ID})
}

func (r *replay) result() Result {
	var out Result
	for _, p := range r.pos {
		out.Positions = append(out.Positions, p)
	}
	sort.Slice(out.Positions, func(i, j int) bool {
		a, b := out.Positions[i], out.Positions[j]
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.SecurityID < b.SecurityID
	})
	for k, v := range r.cash {
		out.Cash = append(out.Cash, Cash{AccountID: k[0], Currency: k[1], Amount: v})
	}
	sort.Slice(out.Cash, func(i, j int) bool {
		a, b := out.Cash[i], out.Cash[j]
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.Currency < b.Currency
	})
	return out
}

func ptr(d decimal.Decimal) *decimal.Decimal { return &d }

func add(a, b *decimal.Decimal) *decimal.Decimal {
	if a == nil || b == nil {
		return nil
	}
	return ptr(a.Add(*b))
}

func sub(a, b *decimal.Decimal) *decimal.Decimal {
	if a == nil || b == nil {
		return nil
	}
	return ptr(a.Sub(*b))
}

// share is cost * q / of, the part of a cost that q of `of` shares carry.
func share(cost *decimal.Decimal, q, of decimal.Decimal) *decimal.Decimal {
	if cost == nil || of.IsZero() {
		return nil
	}
	return ptr(cost.Mul(q).Div(of))
}

func zeroIfKnown(d *decimal.Decimal) *decimal.Decimal {
	if d == nil {
		return nil
	}
	return ptr(decimal.Zero)
}
