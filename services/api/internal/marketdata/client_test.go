package marketdata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackfill(t *testing.T) {
	var got []string
	status := http.StatusAccepted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("X-Service-Token"))
		if r.ContentLength > 0 {
			t.Errorf("backfill sent a body; it must send nothing about the user")
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := New(srv.URL+"/", "synthetic-token")
	if err := c.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = http.StatusConflict // provider off: not an error
	if err := c.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = http.StatusUnauthorized
	if err := c.Backfill(context.Background()); err == nil {
		t.Fatal("401 accepted")
	}
	if len(got) != 3 || got[0] != "POST /internal/backfill synthetic-token" {
		t.Fatalf("requests %v", got)
	}
	var none *Client
	if err := none.Backfill(context.Background()); err != nil {
		t.Fatal("a nil client must do nothing")
	}
	none.BackfillSoon()
}

func TestYahooSymbol(t *testing.T) {
	for _, c := range [][3]string{{"QZGR", "TSX", "QZGR.TO"}, {"AP.UN", "TSX", "AP-UN.TO"}, {"BRK.B", "US", "BRK-B"},
		{"QZV", "TSXV", "QZV.V"}, {"QZ", "", ""}, {"", "TSX", ""}} {
		if got := YahooSymbol(c[0], c[1]); got != c[2] {
			t.Errorf("YahooSymbol(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestStatusUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t").Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
	var none *Client
	if _, err := none.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil client err %v", err)
	}
}
