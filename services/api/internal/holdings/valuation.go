package holdings

// Valuation on read (D-39): market value = quantity x latest price x latest rate to CAD, with its basis (NFR-2);
// Bank of Canada rates fill ledger rows without a broker rate and closes value in-kind transfers without a value.
// The daily valuation cache arrives with M5 (D-13).

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ombg/portfolio-tracker/services/api/internal/calc"
	"github.com/ombg/portfolio-tracker/services/api/internal/db/dbq"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

// Market-value reasons: why a held position has no market value. Never shown as 0 (NFR-4).
const (
	NoPrice        = "no_price"        // the provider has no price for it yet
	NoFX           = "no_fx"           // its price currency has no rate to CAD yet
	MissingHistory = "missing_history" // more shares left than the ledger held (negative_holding)
)

const (
	rateLookback = 7 * 24 * time.Hour // a weekend or holiday has no Bank of Canada rate (D-6)
	valuePlaces  = 6
)

// Basis says where a market value comes from (NFR-2, FR-6): price, its source and time, and the FX rate used.
type Basis struct {
	Price           decimal.Decimal  `json:"price"`
	PriceCurrency   string           `json:"price_currency"`
	PriceSource     string           `json:"price_source"` // yahoo | principal (GICs, D-26)
	PriceKind       string           `json:"price_kind"`   // intraday | close | principal
	PriceObservedAt *time.Time       `json:"price_observed_at"`
	Stale           *bool            `json:"stale"`   // null when market-data could not say (unavailable)
	FXRate          *decimal.Decimal `json:"fx_rate"` // null for a CAD price
	FXSource        string           `json:"fx_source,omitempty"`
	FXObservedAt    *time.Time       `json:"fx_observed_at"`
}

type closeKey struct {
	security string
	day      time.Time
}

type closeVal struct {
	price    decimal.Decimal
	currency string
}

// inputs is the market data one request uses, read in the same RLS transaction as the ledger.
type inputs struct {
	daily       map[string][]dbq.DailyRatesToCADRow // per currency, by date
	latestFX    map[string]dbq.LatestRatesToCADRow
	prices      map[uuid.UUID]LatestPrice
	closes      map[closeKey]closeVal
	staleBefore map[string]time.Time // per calendar; nil when market-data did not answer
}

func newInputs() *inputs {
	return &inputs{daily: map[string][]dbq.DailyRatesToCADRow{}, latestFX: map[string]dbq.LatestRatesToCADRow{},
		prices: map[uuid.UUID]LatestPrice{}, closes: map[closeKey]closeVal{}}
}

// rateOn is the Bank of Canada rate of day, else of the last business day in the week before (D-6).
func (m *inputs) rateOn(currency string, day time.Time) *decimal.Decimal {
	rs := m.daily[currency]
	i := sort.Search(len(rs), func(i int) bool { return rs[i].RateDate.After(day) }) - 1
	if i < 0 || day.Sub(rs[i].RateDate) > rateLookback {
		return nil
	}
	return &rs[i].Rate
}

func (m *inputs) close(securityID string, day time.Time) (*decimal.Decimal, string) {
	c, ok := m.closes[closeKey{securityID, day}]
	if !ok {
		return nil, ""
	}
	return &c.price, c.currency
}

func (m *inputs) options() calc.Options {
	return calc.Options{RateToCAD: m.rateOn, Close: m.close}
}

// value is a position's market value in CAD now, or why it has none.
func (st *state) value(p *calc.Position) (*decimal.Decimal, string, *Basis) {
	if !p.Open() {
		return nil, "", nil
	}
	if p.Quantity.IsNegative() || hasFlag(p, calc.FlagNegativeHolding) {
		return nil, MissingHistory, nil
	}
	sec := st.securities[uuid.MustParse(p.SecurityID)]
	var b Basis
	if sec.Type != nil && *sec.Type == "gic" {
		b = Basis{Price: decimal.NewFromInt(1), PriceCurrency: sec.TradingCurrency, PriceSource: "principal",
			PriceKind: "principal"}
		f := false
		b.Stale = &f
	} else {
		lp, ok := st.market.prices[sec.ID]
		if !ok {
			return nil, NoPrice, nil
		}
		at := lp.ObservedAt
		b = Basis{Price: lp.Price, PriceCurrency: lp.Currency, PriceSource: lp.Source, PriceKind: lp.Kind,
			PriceObservedAt: &at}
		if st.market.staleBefore != nil && sec.Exchange != nil {
			if before, ok := st.market.staleBefore[marketdata.CalendarOf(*sec.Exchange)]; ok {
				stale := at.Before(before)
				b.Stale = &stale
			}
		}
	}
	fx := decimal.NewFromInt(1)
	if b.PriceCurrency != "CAD" {
		r, ok := st.market.latestFX[b.PriceCurrency]
		if !ok {
			return nil, NoFX, &b
		}
		at := r.ObservedAt
		fx, b.FXRate, b.FXSource, b.FXObservedAt = r.Rate, &r.Rate, r.Source, &at
	}
	mv := p.Quantity.Mul(b.Price).Mul(fx).Round(valuePlaces)
	return &mv, "", &b
}

func hasFlag(p *calc.Position, code string) bool {
	for _, f := range p.Flags {
		if f.Code == code {
			return true
		}
	}
	return false
}

// rowRate is the rate to CAD a ledger row is converted with, and where it comes from (D-6, D-37).
func (st *state) rowRate(currency string, broker *decimal.Decimal, day time.Time) (*decimal.Decimal, string) {
	switch {
	case currency == "CAD":
		return nil, ""
	case broker != nil:
		return broker, "broker"
	}
	if r := st.market.rateOn(currency, day); r != nil {
		return r, "boc"
	}
	return nil, ""
}

// weigh sets each valued position's share of the scope's priced market value.
func weigh(ps []*Position) {
	total := decimal.Zero
	for _, p := range ps {
		if p.MarketValue != nil {
			total = total.Add(*p.MarketValue)
		}
	}
	if !total.IsPositive() {
		return
	}
	for _, p := range ps {
		if p.MarketValue != nil {
			w := p.MarketValue.DivRound(total, valuePlaces)
			p.Weight = &w
		}
	}
}
