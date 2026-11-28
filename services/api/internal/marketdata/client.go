// Package marketdata calls market-data's internal API on the Docker network with the shared service token (D-39).
package marketdata

import (
	"context"
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
