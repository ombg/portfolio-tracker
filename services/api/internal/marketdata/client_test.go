package marketdata

import (
	"context"
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
