// Package marketdata calls market-data's internal API on the Docker network with the shared service token (D-39).
package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client reaches market-data. A nil *Client is valid and does nothing (no SERVICE_TOKEN configured).
type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 5 * time.Second}}
}

// Backfill asks market-data to fetch missing history now (new securities after a commit). market-data decides what
// is missing from the database; nothing about the user is sent.
func (c *Client) Backfill(ctx context.Context) error {
	if c == nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/internal/backfill", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Service-Token", c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted && res.StatusCode != http.StatusConflict { // 409: provider off
		return fmt.Errorf("market-data backfill: HTTP %d", res.StatusCode)
	}
	return nil
}

// BackfillSoon runs Backfill in the background: a commit never waits for, or fails because of, market data.
func (c *Client) BackfillSoon() {
	if c == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Backfill(ctx); err != nil {
			slog.Warn("market-data backfill request failed; the next scheduled run will catch up", "error", err)
		}
	}()
}

// Run is one refresh run as market-data reports it; Errors maps provider symbols to what went wrong.
type Run struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	StartedAt     *time.Time        `json:"started_at"`
	FinishedAt    *time.Time        `json:"finished_at"`
	Status        string            `json:"status"`
	SymbolsOK     int               `json:"symbols_ok"`
	SymbolsFailed int               `json:"symbols_failed"`
	Errors        map[string]string `json:"errors"`
}

// Status is market-data's GET /internal/status.
type Status struct {
	Provider      string               `json:"provider"`
	Clock         string               `json:"clock"`
	Now           time.Time            `json:"now"`
	MarketOpen    bool                 `json:"market_open"`
	LastCloseDate string               `json:"last_close_date"`
	StaleBefore   map[string]time.Time `json:"stale_before"`
	LastSuccessAt *time.Time           `json:"last_success_at"`
	LastRunStatus *string              `json:"last_run_status"`
	NextRunAt     *time.Time           `json:"next_run_at"`
	Runs          []Run                `json:"runs"`
}

// Status asks market-data how refreshes are going (FR-6). A nil client reports ErrUnavailable.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	if c == nil {
		return nil, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/internal/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Token", c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, res.StatusCode)
	}
	var s Status
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return &s, nil
}

// ErrUnavailable: market-data did not answer; the status is shown as unavailable, never as success (NFR-4).
var ErrUnavailable = errors.New("market-data unavailable")

var yahooSuffix = map[string]string{"TSX": ".TO", "TSXV": ".V", "NEO": ".NE", "CSE": ".CN", "US": ""}

// YahooSymbol mirrors market-data's mapping (marketdata/symbols.py): AP.UN on TSX -> AP-UN.TO, BRK.B -> BRK-B.
func YahooSymbol(symbol, exchange string) string {
	suffix, ok := yahooSuffix[exchange]
	if !ok || symbol == "" {
		return ""
	}
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(symbol)), ".", "-") + suffix
}

// CalendarOf is the trading calendar a listing follows (market-data's calendar.py has TSX and US).
func CalendarOf(exchange string) string {
	if exchange == "US" {
		return "US"
	}
	return "TSX"
}
