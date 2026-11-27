package imports

// Commit: a staged batch becomes ledger rows in one transaction (FR-3, D-7, D-8, D-26, D-27, D-28, D-35, D-37).
//
// Ledger rows are never updated (D-7), so every link is worked out before the insert: ids of new rows are generated
// here, links to rows already in the ledger are written as corrections ("linked automatically").

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ombg/portfolio-tracker/services/api/internal/db"
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
)

// TransferWindowDays is how far apart the two legs of an internal transfer may be (D-27).
const TransferWindowDays = 3

const linkReason = "linked automatically at import commit (D-37)"

// exchangeCurrency is the currency a listing trades in (core.security_symbols.exchange).
var exchangeCurrency = map[string]string{"TSX": "CAD", "TSXV": "CAD", "NEO": "CAD", "CSE": "CAD", "US": "USD"}

// CommitResult says what the commit did; the batch's counts keep the same numbers.
type CommitResult struct {
	Inserted        int `json:"inserted"`
	Duplicate       int `json:"duplicate"`
	LinkedTransfers int `json:"linked_transfers"`
}

// normalized mirrors the importer's normalized row (data-model.md, "Normalized import row"). Decimals arrive as
// strings and stay decimals.
type normalized struct {
	AccountID   uuid.UUID           `json:"account_id"`
	Type        string              `json:"type"`
	TradeDate   string              `json:"trade_date"`
	SettleDate  *string             `json:"settle_date"`
	RecordDate  *string             `json:"record_date"`
	Security    *normSecurity       `json:"security"`
	Quantity    decimal.NullDecimal `json:"quantity"`
	SplitRatio  decimal.NullDecimal `json:"split_ratio"`
	Price       decimal.NullDecimal `json:"price"`
	Commission  decimal.Decimal     `json:"commission"`
	GrossAmount decimal.NullDecimal `json:"gross_amount"`
	NetAmount   decimal.Decimal     `json:"net_amount"`
	Currency    string              `json:"currency"`
	FxRate      decimal.NullDecimal `json:"fx_rate"`
	FxSource    *string             `json:"fx_source"`
	Group       *struct{ Kind, Key string }
	RelatedRow  *int32  `json:"related_row"`
	LinkBasis   *string `json:"link_basis"`
	Allocations []struct {
		Row    int32           `json:"row"`
		Amount decimal.Decimal `json:"amount"`
	} `json:"allocations"`
	CounterpartyAccountID *uuid.UUID `json:"counterparty_account_id"`
	TransferStatus        *string    `json:"transfer_status"`
	NaturalKey            string     `json:"natural_key"`
}

type normSecurity struct {
	Symbol          *string    `json:"symbol"`
	Exchange        *string    `json:"exchange"`
	BrokerCode      *string    `json:"broker_code"`
	SecurityID      *uuid.UUID `json:"security_id"`
	Proposed        *struct{ Symbol, Exchange string }
	Name            *string `json:"name"`
	SecurityType    *string `json:"security_type"`
	TradingCurrency *string `json:"trading_currency"`
	GIC             *struct {
		Issuer   string          `json:"issuer"`
		Rate     decimal.Decimal `json:"rate"`
		Maturity string          `json:"maturity"`
	} `json:"gic"`
}

// item is one staged row on its way into the ledger.
type item struct {
	rowNo     int32
	raw       []byte
	n         normalized
	tradeDate time.Time

	dup    *dbq.StoredByNaturalKeyRow // set: the row is already in the ledger as this transaction
	id     uuid.UUID                  // new rows: generated here
	secID  *uuid.UUID
	group  *uuid.UUID
	rel    *uuid.UUID
	basis  *string
	wht    decimal.NullDecimal
	status *string // transfer_status
	paired bool
}

func (it *item) txID() uuid.UUID {
	if it.dup != nil {
		return it.dup.ID
	}
	return it.id
}

type unresolved struct {
	Row    int32  `json:"row"`
	Reason string `json:"reason"`
}

type committer struct {
	ctx     context.Context
	q       *dbq.Queries
	userID  uuid.UUID
	batchID uuid.UUID
	broker  uuid.UUID
	srcFile uuid.UUID
	items   []*item
	byRow   map[int32]*item
	fresh   []*item // rows to insert, in row order
	missing []unresolved
	result  CommitResult
}

// Commit inserts the batch's ok and warning rows into the ledger, skips rows already there (D-8) and links what
// belongs together. Rows with errors block it; so do securities that cannot be placed (409 unresolved_securities).
func (s *Store) Commit(ctx context.Context, userID, batchID uuid.UUID) (CommitResult, error) {
	var out CommitResult
	err := db.WithUser(ctx, s.Pool, userID, func(tx pgx.Tx) error {
		c := &committer{ctx: ctx, q: dbq.New(tx), userID: userID, batchID: batchID, byRow: map[int32]*item{}}
		var err error
		out, err = c.run()
		return err
	})
	return out, err
}

func (c *committer) run() (CommitResult, error) {
	if err := c.q.CommitLock(c.ctx); err != nil {
		return c.result, err
	}
	b, err := c.q.GetBatchForCommit(c.ctx, c.batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.result, ErrNotFound
	} else if err != nil {
		return c.result, err
	}
	if !isOpen(b.Status) {
		return c.result, closed(b.Status)
	}
	if b.Errors > 0 {
		return c.result, &Error{Status: 409, Code: "not_committable",
			Message: fmt.Sprintf("%d rows have errors; fix or exclude them first", b.Errors)}
	}
	c.broker, c.srcFile = b.BrokerID, b.SourceFileID
	if err := c.load(); err != nil {
		return c.result, err
	}
	if len(c.items) == 0 {
		return c.result, &Error{Status: 409, Code: "not_committable", Message: "no rows to commit"}
	}
	steps := []func() error{c.duplicates, c.securities, c.groups, c.withholding, c.transfers, c.insert}
	for _, step := range steps {
		if err := step(); err != nil {
			return c.result, err
		}
	}
	counts := map[string]any{}
	if len(b.Counts) > 0 {
		if err := json.Unmarshal(b.Counts, &counts); err != nil {
			return c.result, err
		}
	}
	counts["inserted"], counts["duplicate"] = c.result.Inserted, c.result.Duplicate
	cj, _ := json.Marshal(counts)
	return c.result, c.q.MarkCommitted(c.ctx, dbq.MarkCommittedParams{ID: c.batchID, Counts: cj})
}

func (c *committer) load() error {
	rows, err := c.q.ListRowsToCommit(c.ctx, c.batchID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		it := &item{rowNo: r.RowNo, raw: r.Raw}
		if err := json.Unmarshal(r.Normalized, &it.n); err != nil {
			return fmt.Errorf("row %d: normalized row: %w", r.RowNo, err)
		}
		if it.tradeDate, err = time.Parse(time.DateOnly, it.n.TradeDate); err != nil {
			return fmt.Errorf("row %d: trade_date: %w", r.RowNo, err)
		}
		c.items = append(c.items, it)
		c.byRow[r.RowNo] = it
	}
	return nil
}

// duplicates applies D-8: of k rows with one natural key, the first m match the m rows the ledger already has
// (stored rows, voided or not: D-35); the rest are new.
func (c *committer) duplicates() error {
	keys := []string{}
	seen := map[string]bool{}
	for _, it := range c.items {
		if !seen[it.n.NaturalKey] {
			seen[it.n.NaturalKey] = true
			keys = append(keys, it.n.NaturalKey)
		}
	}
	stored, err := c.q.StoredByNaturalKey(c.ctx, keys)
	if err != nil {
		return err
	}
	existing := map[string][]dbq.StoredByNaturalKeyRow{}
	for _, s := range stored {
		existing[s.NaturalKey] = append(existing[s.NaturalKey], s)
	}
	used := map[string]int{}
	for _, it := range c.items {
		k := it.n.NaturalKey
		if i := used[k]; i < len(existing[k]) {
			it.dup = &existing[k][i]
			used[k]++
			c.result.Duplicate++
			continue
		}
		it.id = uuid.New()
		c.fresh = append(c.fresh, it)
	}
	return nil
}

// securities places every new row's security in the master, creating securities and symbols that are missing and
// saving broker codes as the user's aliases (D-15, D-26, D-36). Symbol changes go first, so the new ticker joins the
// old security before any other row looks it up.
func (c *committer) securities() error {
	var changes, rest []*item
	for _, it := range c.fresh {
		if it.n.Security == nil {
			continue
		}
		if it.n.Type == "SYMBOL_CHANGE" && it.n.Group != nil {
			changes = append(changes, it)
		} else {
			rest = append(rest, it)
		}
	}
	// Old legs (shares out) before new legs, oldest change first.
	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if !a.tradeDate.Equal(b.tradeDate) {
			return a.tradeDate.Before(b.tradeDate)
		}
		return a.n.Quantity.Decimal.IsNegative() && !b.n.Quantity.Decimal.IsNegative()
	})
	oldLeg := map[string]*item{}
	for _, it := range changes {
		key := it.n.Group.Kind + "\x00" + it.n.Group.Key
		if old, ok := oldLeg[key]; ok && old.secID != nil && !it.n.Quantity.Decimal.IsNegative() {
			if err := c.resolveNewTicker(it, *old.secID); err != nil {
				return err
			}
			continue
		}
		if err := c.resolve(it); err != nil {
			return err
		}
		if it.n.Quantity.Decimal.IsNegative() {
			oldLeg[key] = it
		}
	}
	for _, it := range rest {
		if err := c.resolve(it); err != nil {
			return err
		}
	}
	if len(c.missing) > 0 {
		return &Error{Status: 409, Code: "unresolved_securities",
			Message: "some securities cannot be placed; choose them in the preview",
			Extra:   map[string]any{"rows": c.missing}}
	}
	return nil
}

func (c *committer) resolve(it *item) error {
	sec := it.n.Security
	switch {
	case sec.SecurityID != nil:
		it.secID = sec.SecurityID
		return nil
	case sec.SecurityType != nil && *sec.SecurityType == "gic":
		return c.resolveGIC(it)
	}
	symbol, exchange := listing(sec)
	if symbol == "" {
		c.unresolved(it, "no symbol, alias or rule for this security")
		return nil
	}
	id, reason, err := c.findListing(symbol, exchange, listingDate(it))
	if err != nil {
		return err
	}
	if id == nil && reason == "" {
		if id, err = c.createListed(symbol, *exchange, sec, it.n.Currency); err != nil {
			return err
		}
	}
	if id == nil {
		c.unresolved(it, reason)
		return nil
	}
	it.secID = id
	if sec.BrokerCode != nil {
		return c.q.UpsertAlias(c.ctx, dbq.UpsertAliasParams{UserID: &c.userID, BrokerID: c.broker,
			Alias: *sec.BrokerCode, SecurityID: *id})
	}
	return nil
}

// listingDate is the day a row's symbol is looked up on. The old leg of a symbol change names the ticker as it was
// until the change, and a known change ends that ticker's period on the change date (valid_to is exclusive), so
// that leg is looked up on the day before.
func listingDate(it *item) time.Time {
	if it.n.Type == "SYMBOL_CHANGE" && it.n.Quantity.Valid && it.n.Quantity.Decimal.IsNegative() {
		return it.tradeDate.AddDate(0, 0, -1)
	}
	return it.tradeDate
}

// resolveNewTicker: the new leg of a symbol change. A ticker the master does not know yet becomes the old
// security's symbol from the change date on (D-15); a ticker it knows keeps its own security.
func (c *committer) resolveNewTicker(it *item, oldID uuid.UUID) error {
	symbol, exchange := listing(it.n.Security)
	if symbol != "" && exchange != nil {
		id, _, err := c.findListing(symbol, exchange, it.tradeDate)
		if err != nil {
			return err
		}
		if id == nil {
			n, err := c.q.ListingPeriods(c.ctx, dbq.ListingPeriodsParams{Symbol: symbol, Exchange: *exchange})
			if err != nil {
				return err
			}
			if n == 0 {
				d := it.tradeDate
				if err := c.q.CloseSecuritySymbols(c.ctx, dbq.CloseSecuritySymbolsParams{OnDate: d, SecurityID: oldID}); err != nil {
					return err
				}
				if err := c.q.InsertSecuritySymbol(c.ctx, dbq.InsertSecuritySymbolParams{SecurityID: oldID,
					Symbol: symbol, Exchange: *exchange, ValidFrom: &d}); err != nil {
					return err
				}
				it.secID = &oldID
				return nil
			}
		}
	}
	return c.resolve(it)
}

// resolveGIC: one security per GIC code (D-26), found again through the user's alias for the code.
func (c *committer) resolveGIC(it *item) error {
	sec := it.n.Security
	if sec.BrokerCode == nil {
		c.unresolved(it, "GIC without a broker code")
		return nil
	}
	id, err := c.q.AliasSecurity(c.ctx, dbq.AliasSecurityParams{BrokerID: c.broker, Alias: *sec.BrokerCode})
	if err == nil {
		it.secID = &id
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	gic := "gic"
	p := dbq.InsertSecurityParams{Name: deref(sec.Name, *sec.BrokerCode), SecurityType: &gic,
		TradingCurrency: deref(sec.TradingCurrency, it.n.Currency)}
	if sec.GIC != nil {
		p.GicIssuer = &sec.GIC.Issuer
		p.GicRate = decimal.NullDecimal{Decimal: sec.GIC.Rate, Valid: true}
		if m, err := time.Parse(time.DateOnly, sec.GIC.Maturity); err == nil {
			p.GicMaturity = &m
		}
	}
	if id, err = c.q.InsertSecurity(c.ctx, p); err != nil {
		return err
	}
	it.secID = &id
	return c.q.UpsertAlias(c.ctx, dbq.UpsertAliasParams{UserID: &c.userID, BrokerID: c.broker,
		Alias: *sec.BrokerCode, SecurityID: id})
}

// findListing returns the security a symbol names on a date. With no exchange (Wealthsimple leaves it open when the
// file cannot tell), exactly one listing in the master must match. A nil id with no reason means "not in the master
// yet, may be created".
func (c *committer) findListing(symbol string, exchange *string, on time.Time) (*uuid.UUID, string, error) {
	rows, err := c.q.SecurityByListing(c.ctx, dbq.SecurityByListingParams{Symbol: symbol, Exchange: exchange, OnDate: on})
	if err != nil {
		return nil, "", err
	}
	ids := map[uuid.UUID]bool{}
	for _, r := range rows {
		ids[r.SecurityID] = true
	}
	switch {
	case len(ids) == 1:
		return &rows[0].SecurityID, "", nil
	case len(ids) > 1:
		return nil, fmt.Sprintf("%s is listed on several exchanges; choose one", symbol), nil
	case exchange == nil:
		return nil, fmt.Sprintf("the exchange of %s is unknown; choose it", symbol), nil
	}
	n, err := c.q.ListingPeriods(c.ctx, dbq.ListingPeriodsParams{Symbol: symbol, Exchange: *exchange})
	if err != nil {
		return nil, "", err
	}
	if n > 0 {
		return nil, fmt.Sprintf("%s (%s) did not name a security on %s", symbol, *exchange, on.Format(time.DateOnly)), nil
	}
	return nil, "", nil
}

// createListed adds a security the master does not know. Its type stays unknown until market data classifies it
// (D-37, NFR-4); the symbol is valid from the beginning.
func (c *committer) createListed(symbol, exchange string, sec *normSecurity, rowCurrency string) (*uuid.UUID, error) {
	cur := deref(sec.TradingCurrency, exchangeCurrency[exchange])
	if cur == "" {
		cur = rowCurrency
	}
	id, err := c.q.InsertSecurity(c.ctx, dbq.InsertSecurityParams{Name: deref(sec.Name, symbol), TradingCurrency: cur})
	if err != nil {
		return nil, err
	}
	err = c.q.InsertSecuritySymbol(c.ctx, dbq.InsertSecuritySymbolParams{SecurityID: id, Symbol: symbol, Exchange: exchange})
	return &id, err
}

func (c *committer) unresolved(it *item, reason string) {
	c.missing = append(c.missing, unresolved{Row: it.rowNo, Reason: reason})
}

// listing: the listed symbol of a row's security, the user's rule included (it sets symbol and exchange), else the
// importer's proposal; committing a row with a proposal accepts it (D-37).
func listing(sec *normSecurity) (string, *string) {
	if sec.Symbol != nil && *sec.Symbol != "" {
		return *sec.Symbol, sec.Exchange
	}
	if sec.Proposed != nil && sec.Proposed.Symbol != "" {
		ex := sec.Proposed.Exchange
		return sec.Proposed.Symbol, &ex
	}
	return "", nil
}

// groups gives the legs of one in-file group (FX, symbol change, journal, split) one group_id. A leg already in the
// ledger lends its group_id; one without a group gets the new one as a correction.
func (c *committer) groups() error {
	type grp struct{ members []*item }
	order := []string{}
	all := map[string]*grp{}
	for _, it := range c.items {
		if it.n.Group == nil {
			continue
		}
		k := it.n.Group.Kind + "\x00" + it.n.Group.Key
		if all[k] == nil {
			all[k] = &grp{}
			order = append(order, k)
		}
		all[k].members = append(all[k].members, it)
	}
	for _, k := range order {
		g := all[k]
		var id *uuid.UUID
		anyNew := false
		for _, m := range g.members {
			if m.dup == nil {
				anyNew = true
			} else if id == nil && m.dup.GroupID != nil {
				id = m.dup.GroupID
			}
		}
		if !anyNew {
			continue
		}
		if id == nil {
			nid := uuid.New()
			id = &nid
		}
		for _, m := range g.members {
			if m.dup == nil {
				m.group = id
			} else if m.dup.GroupID == nil {
				if err := c.correct(m.dup.ID, "group_id", nil, id.String()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// withholding links each new tax row to its dividend (related_id, link_basis) and records the tax on the dividend
// (withholding_tax). An allocated tax row points at the dividend with the largest share; every dividend it was
// split over carries its share (D-37).
func (c *committer) withholding() error {
	attributed := map[uuid.UUID]decimal.Decimal{}
	attribute := func(target *item, amount decimal.Decimal) {
		attributed[target.txID()] = attributed[target.txID()].Add(amount)
	}
	for _, it := range c.fresh {
		if it.n.Type != "WITHHOLDING_TAX" {
			continue
		}
		if len(it.n.Allocations) > 0 {
			var best *item
			var bestAmt decimal.Decimal
			for _, a := range it.n.Allocations {
				t := c.byRow[a.Row]
				if t == nil {
					continue // the dividend is not part of this commit (excluded)
				}
				attribute(t, a.Amount)
				if best == nil || a.Amount.Abs().GreaterThan(bestAmt) {
					best, bestAmt = t, a.Amount.Abs()
				}
			}
			if best != nil {
				id, basis := best.txID(), "allocated"
				it.rel, it.basis = &id, &basis
			}
			continue
		}
		if it.n.RelatedRow == nil {
			continue
		}
		if t := c.byRow[*it.n.RelatedRow]; t != nil {
			attribute(t, it.n.NetAmount)
			id := t.txID()
			it.rel, it.basis = &id, it.n.LinkBasis
		}
	}
	var stored []uuid.UUID
	for _, it := range c.items {
		if amt, ok := attributed[it.txID()]; ok && it.dup == nil {
			it.wht = decimal.NullDecimal{Decimal: amt, Valid: true}
		} else if ok {
			stored = append(stored, it.dup.ID)
		}
	}
	if len(stored) == 0 {
		return nil
	}
	current, err := c.q.LedgerWithholding(c.ctx, stored)
	if err != nil {
		return err
	}
	for _, r := range current {
		var old any
		total := attributed[r.ID]
		if r.WithholdingTax.Valid {
			old = r.WithholdingTax.Decimal.String()
			total = total.Add(r.WithholdingTax.Decimal)
		}
		if err := c.correct(r.ID, "withholding_tax", old, total.String()); err != nil {
			return err
		}
	}
	return nil
}

// candidate is one side an internal transfer may pair with: a new row of this commit or a ledger row still
// awaiting its counterpart.
type candidate struct {
	it            *item // nil for a ledger row
	ledger        *dbq.AwaitingTransfersRow
	account       uuid.UUID
	counterparty  *uuid.UUID
	typ, currency string
	date          time.Time
	net, qty      decimal.Decimal
	security      *uuid.UUID
	used          bool
}

// transfers pairs internal transfer legs (D-27): the other account, the opposite direction, the same currency and
// amount (in kind: the same security and quantity), at most three days apart; the nearest wins. Both legs get one
// group_id and the status linked; a leg already in the ledger gets them as corrections.
func (c *committer) transfers() error {
	var cands []*candidate
	for _, it := range c.fresh {
		if it.n.TransferStatus != nil && *it.n.TransferStatus == "awaiting_counterpart" && it.n.CounterpartyAccountID != nil {
			cands = append(cands, &candidate{it: it, account: it.n.AccountID, counterparty: it.n.CounterpartyAccountID,
				typ: it.n.Type, currency: it.n.Currency, date: it.tradeDate, net: it.n.NetAmount,
				qty: it.n.Quantity.Decimal, security: it.secID})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	ledger, err := c.q.AwaitingTransfers(c.ctx)
	if err != nil {
		return err
	}
	for i := range ledger {
		l := &ledger[i]
		cands = append(cands, &candidate{ledger: l, account: l.AccountID, counterparty: l.CounterpartyAccountID,
			typ: l.Type, currency: l.Currency, date: l.TradeDate, net: l.NetAmount, qty: l.Quantity.Decimal,
			security: l.SecurityID})
	}
	for _, r := range cands {
		if r.it == nil || r.used {
			continue
		}
		var best *candidate
		bestDays := TransferWindowDays + 1
		for _, o := range cands {
			if o == r || o.used || !pairs(r, o) {
				continue
			}
			if d := days(r.date, o.date); d < bestDays {
				best, bestDays = o, d
			}
		}
		if best == nil {
			continue
		}
		r.used, best.used = true, true
		g, linked := uuid.New(), "linked"
		r.it.group, r.it.status = &g, &linked
		if best.it != nil {
			best.it.group, best.it.status = &g, &linked
		} else {
			if err := c.correct(best.ledger.ID, "group_id", nil, g.String()); err != nil {
				return err
			}
			if err := c.correct(best.ledger.ID, "transfer_status", "awaiting_counterpart", linked); err != nil {
				return err
			}
		}
		c.result.LinkedTransfers++
	}
	return nil
}

func pairs(a, b *candidate) bool {
	if a.counterparty == nil || b.counterparty == nil || *a.counterparty != b.account || *b.counterparty != a.account ||
		a.currency != b.currency || days(a.date, b.date) > TransferWindowDays {
		return false
	}
	if !(a.typ == "TRANSFER_OUT" && b.typ == "TRANSFER_IN" || a.typ == "TRANSFER_IN" && b.typ == "TRANSFER_OUT") {
		return false
	}
	if !a.net.IsZero() || !b.net.IsZero() {
		return a.net.Equal(b.net.Neg())
	}
	// In kind: no cash moves, so the shares must match.
	return a.security != nil && b.security != nil && *a.security == *b.security && a.qty.Equal(b.qty)
}

func days(a, b time.Time) int {
	d := int(a.Sub(b).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

// insert writes the new rows: everything else first, then tax rows, whose related_id points at a dividend.
func (c *committer) insert() error {
	ordered := make([]*item, 0, len(c.fresh))
	for _, tax := range []bool{false, true} {
		for _, it := range c.fresh {
			if (it.n.Type == "WITHHOLDING_TAX") == tax {
				ordered = append(ordered, it)
			}
		}
	}
	for _, it := range ordered {
		n := it.n
		settle, err := date(n.SettleDate)
		if err != nil {
			return fmt.Errorf("row %d: settle_date: %w", it.rowNo, err)
		}
		record, err := date(n.RecordDate)
		if err != nil {
			return fmt.Errorf("row %d: record_date: %w", it.rowNo, err)
		}
		p := dbq.InsertTransactionParams{
			ID: it.id, UserID: c.userID, AccountID: n.AccountID, Type: n.Type, TradeDate: it.tradeDate,
			SettleDate: settle, RecordDate: record, SecurityID: it.secID,
			Quantity: n.Quantity, SplitRatio: n.SplitRatio, Price: n.Price, Commission: n.Commission,
			GrossAmount: n.GrossAmount, NetAmount: n.NetAmount, Currency: n.Currency, WithholdingTax: it.wht,
			RelatedID: it.rel, LinkBasis: it.basis, GroupID: it.group, TransferStatus: n.TransferStatus,
			CounterpartyAccountID: n.CounterpartyAccountID, SourceFileID: &c.srcFile, SourceRowNo: &it.rowNo,
			ImportBatchID: &c.batchID, NaturalKey: n.NaturalKey, Raw: it.raw,
		}
		if it.status != nil {
			p.TransferStatus = it.status
		}
		// A broker rate is a rate to CAD only on a USD row; on a CAD row it described the listing's price and stays
		// in raw (D-37).
		if n.FxRate.Valid && n.Currency == "USD" {
			p.FxRateToCad = n.FxRate
			p.FxSource = n.FxSource
			if p.FxSource == nil {
				broker := "broker"
				p.FxSource = &broker
			}
		}
		if err := c.q.InsertTransaction(c.ctx, p); err != nil {
			return fmt.Errorf("insert row %d: %w", it.rowNo, err)
		}
		c.result.Inserted++
	}
	return nil
}

func (c *committer) correct(txID uuid.UUID, field string, old, new any) error {
	var oldJSON []byte
	if old != nil {
		oldJSON, _ = json.Marshal(old)
	}
	newJSON, _ := json.Marshal(new)
	return c.q.InsertCorrection(c.ctx, dbq.InsertCorrectionParams{UserID: c.userID, TransactionID: txID, Field: field,
		OldValue: oldJSON, NewValue: newJSON, Reason: linkReason})
}

func date(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func deref(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}
