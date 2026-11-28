// Package market answers GET /api/market/status (FR-6, AC-12, D-39): market-data's refresh status, seen through the
// caller's own securities. Runs cover every user's securities, so each run is cut down to the caller's symbols:
// another user's tickers, and failures that only concern them, never show (FR-1).
package market

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ombg/portfolio-tracker/services/api/internal/holdings"
	"github.com/ombg/portfolio-tracker/services/api/internal/marketdata"
)

// States of the status card. Unavailable, never, off and failed are never shown as success (NFR-4).
const (
	StateSuccess     = "success"
	StatePartial     = "partial"
	StateFailed      = "failed"
	StateStale       = "stale"
	StateOff         = "off"
	StateUnavailable = "unavailable"
	StateNever       = "never"
)

type Store struct {
	Holdings *holdings.Store
	Client   *marketdata.Client
}

type PriceRef struct {
	Security   holdings.Security `json:"security"`
	Price      decimal.Decimal   `json:"price"`
	Currency   string            `json:"currency"`
	ObservedAt time.Time         `json:"observed_at"`
	Source     string            `json:"source"`
}

type Run struct {
	Kind       string            `json:"kind"`
	StartedAt  *time.Time        `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	Status     string            `json:"status"`
	Errors     map[string]string `json:"errors"` // the caller's provider symbols only
}

type Status struct {
	State         string              `json:"state"`
	Provider      *string             `json:"provider"`
	MarketOpen    *bool               `json:"market_open"`
	LastCloseDate *string             `json:"last_close_date"`
	LastSuccessAt *time.Time          `json:"last_success_at"`
	LastRunAt     *time.Time          `json:"last_run_at"`
	LastRunStatus *string             `json:"last_run_status"`
	NextRunAt     *time.Time          `json:"next_run_at"`
	Stale         []PriceRef          `json:"stale"`
	Unpriced      []holdings.Security `json:"unpriced"`
	Runs          []Run               `json:"runs"`
}

// Status combines market-data's report with the caller's holdings and the stored prices.
func (s *Store) Status(ctx context.Context, userID uuid.UUID) (*Status, error) {
	secs, prices, err := s.Holdings.Securities(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &Status{Stale: []PriceRef{}, Unpriced: []holdings.Security{}, Runs: []Run{}}
	mine := map[string]bool{} // the caller's provider symbols
	for _, h := range secs {
		if h.Symbol != nil && h.Exchange != nil {
			mine[marketdata.YahooSymbol(*h.Symbol, *h.Exchange)] = true
		}
		if !h.Open || priced(h.Security) {
			continue
		}
		if _, ok := prices[h.ID]; !ok {
			out.Unpriced = append(out.Unpriced, h.Security)
		}
	}

	md, err := s.Client.Status(ctx)
	if errors.Is(err, marketdata.ErrUnavailable) {
		out.State = StateUnavailable
		return out, nil
	} else if err != nil {
		return nil, err
	}
	out.Provider, out.MarketOpen, out.LastCloseDate, out.NextRunAt = &md.Provider, &md.MarketOpen,
		&md.LastCloseDate, md.NextRunAt
	if md.Provider == "off" {
		out.State = StateOff
		return out, nil
	}
	for _, h := range secs {
		p, ok := prices[h.ID]
		if !h.Open || !ok || priced(h.Security) || h.Exchange == nil {
			continue
		}
		if before, ok := md.StaleBefore[marketdata.CalendarOf(*h.Exchange)]; ok && p.ObservedAt.Before(before) {
			out.Stale = append(out.Stale, PriceRef{Security: h.Security, Price: p.Price, Currency: p.Currency,
				ObservedAt: p.ObservedAt, Source: p.Source})
		}
	}
	sort.SliceStable(out.Stale, func(i, j int) bool { return out.Stale[i].ObservedAt.Before(out.Stale[j].ObservedAt) })

	var latest *Run
	for _, r := range md.Runs {
		run := forCaller(r, mine)
		out.Runs = append(out.Runs, run)
		if r.Kind == "backfill" || r.Status == "running" {
			continue
		}
		if latest == nil {
			l := run
			latest = &l
		}
		if run.Status == StateSuccess && out.LastSuccessAt == nil {
			out.LastSuccessAt = run.FinishedAt
		}
	}
	switch {
	case latest == nil:
		out.State = StateNever
	case latest.Status == StateFailed:
		out.State = StateFailed
	case latest.Status == StatePartial || len(out.Unpriced) > 0: // a held security without any price is not covered
		out.State = StatePartial
	case len(out.Stale) > 0:
		out.State = StateStale
	default:
		out.State = StateSuccess
	}
	if latest != nil {
		out.LastRunAt, out.LastRunStatus = latest.FinishedAt, &latest.Status
	}
	return out, nil
}

// priced: a GIC is valued at principal and never requested (D-26).
func priced(s holdings.Security) bool { return s.Type != nil && *s.Type == "gic" }

// forCaller keeps the caller's symbols' errors and derives the run's status from them: a run that refreshed
// nothing, or failed inside market-data ("*"), failed for everyone.
func forCaller(r marketdata.Run, mine map[string]bool) Run {
	out := Run{Kind: r.Kind, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Errors: map[string]string{}}
	for sym, msg := range r.Errors {
		if mine[sym] {
			out.Errors[sym] = msg
		}
	}
	_, internal := r.Errors["*"]
	switch {
	case r.Status == "running":
		out.Status = "running"
	case internal || (r.Status == StateFailed && r.SymbolsOK == 0):
		out.Status = StateFailed
	case len(out.Errors) > 0:
		out.Status = StatePartial
	default:
		out.Status = StateSuccess
	}
	return out
}
