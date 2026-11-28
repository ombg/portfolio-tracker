// Package holdings answers the holdings and security views (FR-7, FR-8, D-38): it loads the signed-in user's ledger
// under RLS, replays it with calc and shapes the result per account and per security. Values nobody knows yet are
// null with a reason or a flag, never 0 (NFR-4); totals say how many positions they leave out.
package holdings

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/ombg/portfolio-tracker/services/api/internal/calc"
	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
)

// NoPrice is the market-value reason until market data exists (M4, D-38).
const NoPrice = "no_price"

// Error is a refusal with an HTTP status and a stable code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var (
	// Another user's account or a security the user never held looks exactly like a missing one (FR-1).
	ErrAccountNotFound  = &Error{Status: 404, Code: "not_found", Message: "account not found"}
	ErrSecurityNotFound = &Error{Status: 404, Code: "not_found", Message: "security not found"}
)

// Scope selects accounts (calculations.md, Reporting scopes).
type Scope struct {
	Kind      string // all, registered, non_registered, account
	AccountID uuid.UUID
}

func (s Scope) String() string {
	if s.Kind == "account" {
		return "account:" + s.AccountID.String()
	}
	return s.Kind
}

// ParseScope reads the scope query parameter; empty means all.
func ParseScope(v string) (Scope, error) {
	switch v {
	case "", "all":
		return Scope{Kind: "all"}, nil
	case "registered", "non_registered":
		return Scope{Kind: v}, nil
	}
	if id, ok := strings.CutPrefix(v, "account:"); ok {
		if u, err := uuid.Parse(id); err == nil {
			return Scope{Kind: "account", AccountID: u}, nil
		}
	}
	return Scope{}, &Error{Status: 400, Code: "invalid_scope",
		Message: "scope must be all, registered, non_registered or account:<id>"}
}

type Broker struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type Account struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Broker       Broker    `json:"broker"`
	IsRegistered bool      `json:"is_registered"`
	Management   string    `json:"management"`
	Status       string    `json:"status"`
}

// managed accounts are reported at account level only (D-14, FR-12).
func (a Account) managed() bool { return a.Management == "robo" }

type Security struct {
	ID              uuid.UUID `json:"id"`
	Symbol          *string   `json:"symbol"`
	Exchange        *string   `json:"exchange"`
	Name            string    `json:"name"`
	Type            *string   `json:"type"` // null: not classified yet (D-37)
	TradingCurrency string    `json:"trading_currency"`
}

type Money struct {
	Currency string          `json:"currency"`
	Amount   decimal.Decimal `json:"amount"`
}

type Flag struct {
	Code          string    `json:"code"`
	Date          string    `json:"date"`
	TransactionID uuid.UUID `json:"transaction_id"`
}

// Position is one account's holding of one security.
type Position struct {
	Security          Security         `json:"security"`
	Quantity          decimal.Decimal  `json:"quantity"`
	BookCurrency      string           `json:"book_currency"`
	AvgCost           *decimal.Decimal `json:"avg_cost"`
	BookValue         *decimal.Decimal `json:"book_value"`
	AvgCostCAD        *decimal.Decimal `json:"avg_cost_cad"`
	BookValueCAD      *decimal.Decimal `json:"book_value_cad"`
	RealizedGain      *decimal.Decimal `json:"realized_gain"`
	RealizedGainCAD   *decimal.Decimal `json:"realized_gain_cad"`
	Income            []Money          `json:"income"`
	IncomeCAD         *decimal.Decimal `json:"income_cad"`
	MarketValue       *decimal.Decimal `json:"market_value"`
	MarketValueReason string           `json:"market_value_reason"`
	Flags             []Flag           `json:"flags"`
	Open              bool             `json:"open"`
}

type AccountHoldings struct {
	Account   Account    `json:"account"`
	Positions []Position `json:"positions"`
	Cash      []Money    `json:"cash"`
}

// Combined is one security across the scope's accounts. The average cost is informational (FR-8); the native
// figures appear only when every account keeps the same book currency.
type Combined struct {
	Security        Security         `json:"security"`
	Quantity        decimal.Decimal  `json:"quantity"`
	BookCurrency    *string          `json:"book_currency"`
	AvgCost         *decimal.Decimal `json:"avg_cost"`
	BookValue       *decimal.Decimal `json:"book_value"`
	AvgCostCAD      *decimal.Decimal `json:"avg_cost_cad"`
	BookValueCAD    *decimal.Decimal `json:"book_value_cad"`
	RealizedGainCAD *decimal.Decimal `json:"realized_gain_cad"`
	IncomeCAD       *decimal.Decimal `json:"income_cad"`
	Accounts        int              `json:"accounts"`
	Flags           []string         `json:"flags"`
	Open            bool             `json:"open"`
}

// Excluded names an account left out of the security view and why.
type Excluded struct {
	AccountID uuid.UUID `json:"account_id"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"` // managed_account
}

// Total sums the known values and counts the positions it leaves out.
type Total struct {
	Amount   *decimal.Decimal `json:"amount"`
	Excludes int              `json:"excludes"`
	Reason   string           `json:"reason,omitempty"`
}

type Totals struct {
	BookValueCAD    Total   `json:"book_value_cad"`
	RealizedGainCAD Total   `json:"realized_gain_cad"`
	IncomeCAD       Total   `json:"income_cad"`
	MarketValue     Total   `json:"market_value"`
	Cash            []Money `json:"cash"`
}

// Report is GET /api/holdings.
type Report struct {
	Scope      string            `json:"scope"`
	AsOf       *string           `json:"as_of"` // latest trade date in the scope
	Accounts   []AccountHoldings `json:"accounts"`
	Securities []Combined        `json:"securities"`
	Excluded   []Excluded        `json:"excluded"`
	Totals     Totals            `json:"totals"`
}

type Store struct{ Pool *pgxpool.Pool }

// state is everything one request needs, read in one RLS transaction.
type state struct {
	accounts   []Account
	byID       map[uuid.UUID]Account
	securities map[uuid.UUID]Security
	listings   map[uuid.UUID][]dbq.HeldListingsRow
	result     calc.Result
	lastDate   map[uuid.UUID]time.Time // per account
}

func (s *Store) load(ctx context.Context, userID uuid.UUID, extra func(q *dbq.Queries) error) (*state, error) {
	st := &state{byID: map[uuid.UUID]Account{}, securities: map[uuid.UUID]Security{},
		listings: map[uuid.UUID][]dbq.HeldListingsRow{}, lastDate: map[uuid.UUID]time.Time{}}
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		accts, err := q.ListAccounts(ctx, true)
		if err != nil {
			return fmt.Errorf("accounts: %w", err)
		}
		for _, a := range accts {
			acct := Account{ID: a.CoreAccount.ID, Name: a.CoreAccount.Name, Type: a.CoreAccount.Type,
				Broker: Broker{Code: a.BrokerCode, Name: a.BrokerName}, IsRegistered: a.CoreAccount.IsRegistered,
				Management: a.CoreAccount.Management, Status: a.CoreAccount.Status}
			st.accounts = append(st.accounts, acct)
			st.byID[acct.ID] = acct
		}
		rows, err := q.LedgerForReplay(ctx)
		if err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
		txs := make([]calc.Tx, 0, len(rows))
		for _, r := range rows {
			txs = append(txs, toTx(r))
			if r.TradeDate.After(st.lastDate[r.AccountID]) {
				st.lastDate[r.AccountID] = r.TradeDate
			}
		}
		st.result = calc.Replay(txs)
		secs, err := q.HeldSecurities(ctx)
		if err != nil {
			return fmt.Errorf("securities: %w", err)
		}
		listings, err := q.HeldListings(ctx)
		if err != nil {
			return fmt.Errorf("listings: %w", err)
		}
		for _, l := range listings {
			st.listings[l.SecurityID] = append(st.listings[l.SecurityID], l)
		}
		for _, sec := range secs {
			v := Security{ID: sec.ID, Name: sec.Name, Type: sec.SecurityType, TradingCurrency: sec.TradingCurrency}
			if cur := current(st.listings[sec.ID]); cur != nil {
				v.Symbol, v.Exchange = &cur.Symbol, &cur.Exchange
			}
			st.securities[sec.ID] = v
		}
		if extra != nil {
			return extra(q)
		}
		return nil
	})
	return st, err
}

// current is the open symbol period (the latest-starting one), else the period that ended last.
func current(ls []dbq.HeldListingsRow) *dbq.HeldListingsRow {
	var best *dbq.HeldListingsRow
	later := func(a, b *time.Time) bool { return a != nil && (b == nil || a.After(*b)) }
	for i := range ls {
		l := &ls[i]
		switch {
		case best == nil:
			best = l
		case l.ValidTo == nil && best.ValidTo != nil:
			best = l
		case l.ValidTo == nil && best.ValidTo == nil && later(l.ValidFrom, best.ValidFrom):
			best = l
		case l.ValidTo != nil && best.ValidTo != nil && l.ValidTo.After(*best.ValidTo):
			best = l
		}
	}
	return best
}

func toTx(r dbq.LedgerForReplayRow) calc.Tx {
	t := calc.Tx{ID: r.ID.String(), AccountID: r.AccountID.String(), Type: r.Type, TradeDate: r.TradeDate,
		Quantity: opt(r.Quantity), SplitRatio: opt(r.SplitRatio), Price: opt(r.Price), Commission: r.Commission,
		GrossAmount: opt(r.GrossAmount), NetAmount: r.NetAmount, Currency: r.Currency, FXRateToCAD: opt(r.FxRateToCad)}
	if r.SecurityID != nil {
		t.SecurityID = r.SecurityID.String()
	}
	if r.GroupID != nil {
		t.GroupID = r.GroupID.String()
	}
	if r.CounterpartyAccountID != nil {
		t.Counterparty = r.CounterpartyAccountID.String()
	}
	return t
}

func opt(n decimal.NullDecimal) *decimal.Decimal {
	if !n.Valid {
		return nil
	}
	return &n.Decimal
}

func (st *state) inScope(a Account, sc Scope) bool {
	switch sc.Kind {
	case "registered":
		return a.IsRegistered
	case "non_registered":
		return !a.IsRegistered
	case "account":
		return a.ID == sc.AccountID
	}
	return true
}

func (st *state) position(p *calc.Position) Position {
	out := Position{Security: st.securities[uuid.MustParse(p.SecurityID)], Quantity: p.Quantity,
		BookCurrency: p.BookCurrency, AvgCost: p.AvgCost(), BookValue: p.Cost, AvgCostCAD: p.AvgCostCAD(),
		BookValueCAD: p.CostCAD, RealizedGain: p.Realized, RealizedGainCAD: p.RealizedCAD, IncomeCAD: p.IncomeCAD,
		MarketValueReason: NoPrice, Open: p.Open(), Income: []Money{}, Flags: []Flag{}}
	for cur, amt := range p.Income {
		out.Income = append(out.Income, Money{Currency: cur, Amount: amt})
	}
	sort.Slice(out.Income, func(i, j int) bool { return out.Income[i].Currency < out.Income[j].Currency })
	for _, f := range p.Flags {
		out.Flags = append(out.Flags, Flag{Code: f.Code, Date: f.Date.Format(time.DateOnly),
			TransactionID: uuid.MustParse(f.TxID)})
	}
	if !out.Open { // nothing held: no market value to miss
		out.MarketValueReason = ""
	}
	return out
}

// Holdings builds the report for one scope.
func (s *Store) Holdings(ctx context.Context, userID uuid.UUID, sc Scope) (*Report, error) {
	st, err := s.load(ctx, userID, nil)
	if err != nil {
		return nil, err
	}
	if sc.Kind == "account" {
		if _, ok := st.byID[sc.AccountID]; !ok {
			return nil, ErrAccountNotFound
		}
	}
	rep := &Report{Scope: sc.String(), Accounts: []AccountHoldings{}, Securities: []Combined{}, Excluded: []Excluded{},
		Totals: Totals{Cash: []Money{}}}
	byAccount := map[string]*AccountHoldings{}
	var last time.Time
	for _, a := range st.accounts {
		if !st.inScope(a, sc) {
			continue
		}
		d, active := st.lastDate[a.ID]
		if a.Status == "archived" && !active {
			continue
		}
		if d.After(last) {
			last = d
		}
		rep.Accounts = append(rep.Accounts, AccountHoldings{Account: a, Positions: []Position{}, Cash: []Money{}})
		if a.managed() {
			rep.Excluded = append(rep.Excluded, Excluded{AccountID: a.ID, Name: a.Name, Reason: "managed_account"})
		}
	}
	for i := range rep.Accounts {
		byAccount[rep.Accounts[i].Account.ID.String()] = &rep.Accounts[i]
	}
	if !last.IsZero() {
		v := last.Format(time.DateOnly)
		rep.AsOf = &v
	}

	var combined []*calc.Position
	for _, p := range st.result.Positions {
		ah := byAccount[p.AccountID]
		if ah == nil {
			continue
		}
		pos := st.position(p)
		ah.Positions = append(ah.Positions, pos)
		rep.Totals.add(pos)
		if !ah.Account.managed() {
			combined = append(combined, p)
		}
	}
	cash := map[string]decimal.Decimal{}
	for _, c := range st.result.Cash {
		if ah := byAccount[c.AccountID]; ah != nil {
			ah.Cash = append(ah.Cash, Money{Currency: c.Currency, Amount: c.Amount})
			cash[c.Currency] = cash[c.Currency].Add(c.Amount)
		}
	}
	for cur, amt := range cash {
		rep.Totals.Cash = append(rep.Totals.Cash, Money{Currency: cur, Amount: amt})
	}
	sort.Slice(rep.Totals.Cash, func(i, j int) bool { return rep.Totals.Cash[i].Currency < rep.Totals.Cash[j].Currency })
	for i := range rep.Accounts {
		sortPositions(rep.Accounts[i].Positions)
	}
	rep.Securities = st.combine(combined)
	rep.Totals.MarketValue.Reason = NoPrice
	return rep, nil
}

func sortPositions(ps []Position) {
	sort.SliceStable(ps, func(i, j int) bool { return label(ps[i].Security) < label(ps[j].Security) })
}

func label(s Security) string {
	if s.Symbol != nil {
		return *s.Symbol + " " + s.Name
	}
	return "~" + s.Name // unlisted (GICs) after listed securities
}

func (t *Totals) add(p Position) {
	sum := func(tot *Total, v *decimal.Decimal) {
		if v == nil {
			tot.Excludes++
			return
		}
		if tot.Amount == nil {
			zero := decimal.Zero
			tot.Amount = &zero
		}
		s := tot.Amount.Add(*v)
		tot.Amount = &s
	}
	if p.Open {
		sum(&t.BookValueCAD, p.BookValueCAD)
		sum(&t.MarketValue, p.MarketValue)
	}
	sum(&t.RealizedGainCAD, p.RealizedGainCAD)
	sum(&t.IncomeCAD, p.IncomeCAD)
}

// combine sums positions per security (FR-8: informational average across accounts).
func (st *state) combine(ps []*calc.Position) []Combined {
	type acc struct {
		c          Combined
		currencies map[string]bool
		cost       *decimal.Decimal
		flags      map[string]bool
	}
	groups := map[string]*acc{}
	var order []string
	for _, p := range ps {
		g := groups[p.SecurityID]
		if g == nil {
			zero := decimal.Zero
			g = &acc{c: Combined{Security: st.securities[uuid.MustParse(p.SecurityID)], Flags: []string{},
				BookValueCAD: ptr(zero), RealizedGainCAD: ptr(zero), IncomeCAD: ptr(zero)},
				currencies: map[string]bool{}, cost: ptr(zero), flags: map[string]bool{}}
			groups[p.SecurityID] = g
			order = append(order, p.SecurityID)
		}
		g.c.Accounts++
		g.c.Quantity = g.c.Quantity.Add(p.Quantity)
		g.c.Open = g.c.Open || p.Open()
		if p.Open() { // a closed position carries no cost into the combined line
			g.currencies[p.BookCurrency] = true
			g.cost = add(g.cost, p.Cost)
			g.c.BookValueCAD = add(g.c.BookValueCAD, p.CostCAD)
		}
		g.c.RealizedGainCAD = add(g.c.RealizedGainCAD, p.RealizedCAD)
		g.c.IncomeCAD = add(g.c.IncomeCAD, p.IncomeCAD)
		for _, f := range p.Flags {
			g.flags[f.Code] = true
		}
	}
	out := make([]Combined, 0, len(order))
	for _, id := range order {
		g := groups[id]
		if len(g.currencies) == 1 {
			for cur := range g.currencies {
				c := cur
				g.c.BookCurrency = &c
			}
			g.c.BookValue = g.cost
			g.c.AvgCost = avg(g.cost, g.c.Quantity)
		}
		if !g.c.Open {
			g.c.BookValueCAD = ptr(decimal.Zero)
		}
		g.c.AvgCostCAD = avg(g.c.BookValueCAD, g.c.Quantity)
		for f := range g.flags {
			g.c.Flags = append(g.c.Flags, f)
		}
		sort.Strings(g.c.Flags)
		out = append(out, g.c)
	}
	sort.SliceStable(out, func(i, j int) bool { return label(out[i].Security) < label(out[j].Security) })
	return out
}

func ptr(d decimal.Decimal) *decimal.Decimal { return &d }

func add(a, b *decimal.Decimal) *decimal.Decimal {
	if a == nil || b == nil {
		return nil
	}
	return ptr(a.Add(*b))
}

func avg(cost *decimal.Decimal, qty decimal.Decimal) *decimal.Decimal {
	if cost == nil || !qty.IsPositive() {
		return nil
	}
	return ptr(cost.DivRound(qty, calc.AvgPlaces))
}

// ---- security detail ----------------------------------------------------------------------------------------

type Symbol struct {
	Symbol    string  `json:"symbol"`
	Exchange  string  `json:"exchange"`
	ValidFrom *string `json:"valid_from"`
	ValidTo   *string `json:"valid_to"`
}

type AccountRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type AccountPosition struct {
	Account  Account  `json:"account"`
	Position Position `json:"position"`
}

// Source is where a ledger row came from (NFR-2).
type Source struct {
	FileID   *uuid.UUID `json:"file_id"`
	FileName *string    `json:"file_name"`
	RowNo    *int32     `json:"row_no"`
	ImportID *uuid.UUID `json:"import_id"`
}

type LedgerRow struct {
	ID          uuid.UUID        `json:"id"`
	Account     AccountRef       `json:"account"`
	Type        string           `json:"type"`
	TradeDate   string           `json:"trade_date"`
	Quantity    *decimal.Decimal `json:"quantity"`
	SplitRatio  *decimal.Decimal `json:"split_ratio"`
	Price       *decimal.Decimal `json:"price"`
	Commission  decimal.Decimal  `json:"commission"`
	GrossAmount *decimal.Decimal `json:"gross_amount"`
	NetAmount   decimal.Decimal  `json:"net_amount"`
	Currency    string           `json:"currency"`
	FXRateToCAD *decimal.Decimal `json:"fx_rate_to_cad"`
	Origin      string           `json:"origin"`
	Corrected   bool             `json:"corrected"`
	Source      Source           `json:"source"`
}

// SecurityDetail is GET /api/securities/{id}.
type SecurityDetail struct {
	Security     Security          `json:"security"`
	Symbols      []Symbol          `json:"symbols"`
	Positions    []AccountPosition `json:"positions"`
	Combined     *Combined         `json:"combined"`
	Excluded     []Excluded        `json:"excluded"`
	Transactions []LedgerRow       `json:"transactions"`
}

// Security answers the detail of one security the user holds or held, across all their accounts.
func (s *Store) Security(ctx context.Context, userID, securityID uuid.UUID) (*SecurityDetail, error) {
	var ledger []dbq.SecurityLedgerRow
	var symbols []dbq.SecuritySymbolsRow
	st, err := s.load(ctx, userID, func(q *dbq.Queries) error {
		var err error
		if ledger, err = q.SecurityLedger(ctx, &securityID); err != nil {
			return fmt.Errorf("security ledger: %w", err)
		}
		if symbols, err = q.SecuritySymbols(ctx, securityID); err != nil {
			return fmt.Errorf("security symbols: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sec, ok := st.securities[securityID]
	if !ok {
		return nil, ErrSecurityNotFound
	}
	out := &SecurityDetail{Security: sec, Symbols: []Symbol{}, Positions: []AccountPosition{}, Excluded: []Excluded{},
		Transactions: []LedgerRow{}}
	for _, sy := range symbols {
		out.Symbols = append(out.Symbols, Symbol{Symbol: sy.Symbol, Exchange: sy.Exchange, ValidFrom: date(sy.ValidFrom),
			ValidTo: date(sy.ValidTo)})
	}
	var combined []*calc.Position
	excluded := map[uuid.UUID]bool{}
	for _, p := range st.result.Positions {
		if p.SecurityID != securityID.String() {
			continue
		}
		a := st.byID[uuid.MustParse(p.AccountID)]
		out.Positions = append(out.Positions, AccountPosition{Account: a, Position: st.position(p)})
		if a.managed() {
			if !excluded[a.ID] {
				out.Excluded = append(out.Excluded, Excluded{AccountID: a.ID, Name: a.Name, Reason: "managed_account"})
				excluded[a.ID] = true
			}
			continue
		}
		combined = append(combined, p)
	}
	sort.SliceStable(out.Positions, func(i, j int) bool { return out.Positions[i].Account.Name < out.Positions[j].Account.Name })
	if c := st.combine(combined); len(c) == 1 {
		out.Combined = &c[0]
	}
	for _, r := range ledger {
		a := st.byID[r.AccountID]
		out.Transactions = append(out.Transactions, LedgerRow{ID: r.ID, Account: AccountRef{ID: a.ID, Name: a.Name},
			Type: r.Type, TradeDate: r.TradeDate.Format(time.DateOnly), Quantity: opt(r.Quantity),
			SplitRatio: opt(r.SplitRatio), Price: opt(r.Price), Commission: r.Commission, GrossAmount: opt(r.GrossAmount),
			NetAmount: r.NetAmount, Currency: r.Currency, FXRateToCAD: opt(r.FxRateToCad), Origin: r.Origin,
			Corrected: r.Corrected, Source: Source{FileID: r.FileID, FileName: r.FileName, RowNo: r.SourceRowNo,
				ImportID: r.ImportBatchID}})
	}
	return out, nil
}

func date(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.Format(time.DateOnly)
	return &v
}

// IsError reports whether err is a refusal with a status for the client.
func IsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// ---- for the market status (FR-6) ------------------------------------------------------------------------------

// HeldSecurity is a security in the user's ledger and whether any account still holds it.
type HeldSecurity struct {
	Security
	Open bool
}

// LatestPrice is the newest provider price of a security.
type LatestPrice struct {
	Price      decimal.Decimal
	Currency   string
	ObservedAt time.Time
	Source     string
	Kind       string
}

// Securities returns every security in the user's ledger, with open ones marked, and their latest prices.
func (s *Store) Securities(ctx context.Context, userID uuid.UUID) ([]HeldSecurity, map[uuid.UUID]LatestPrice, error) {
	prices := map[uuid.UUID]LatestPrice{}
	var out []HeldSecurity
	st, err := s.load(ctx, userID, func(q *dbq.Queries) error { return nil })
	if err != nil {
		return nil, nil, err
	}
	open := map[uuid.UUID]bool{}
	for _, p := range st.result.Positions {
		if p.Open() {
			open[uuid.MustParse(p.SecurityID)] = true
		}
	}
	ids := make([]uuid.UUID, 0, len(st.securities))
	for id, sec := range st.securities {
		out = append(out, HeldSecurity{Security: sec, Open: open[id]})
		ids = append(ids, id)
	}
	sort.Slice(out, func(i, j int) bool { return label(out[i].Security) < label(out[j].Security) })
	err = db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		rows, err := dbq.New(tx).LatestPrices(ctx, ids)
		if err != nil {
			return fmt.Errorf("latest prices: %w", err)
		}
		for _, r := range rows {
			prices[r.SecurityID] = LatestPrice{Price: r.Price, Currency: r.Currency, ObservedAt: r.ObservedAt,
				Source: r.Source, Kind: r.Kind}
		}
		return nil
	})
	return out, prices, err
}
