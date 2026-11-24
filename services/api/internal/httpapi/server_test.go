package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{"database up", nil, http.StatusOK, "ok"},
		{"database down is not reported healthy", errors.New("down"), http.StatusServiceUnavailable, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewRouter(fakePinger{tc.pingErr}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["status"] != tc.wantBody {
				t.Fatalf("status field = %q, want %q", body["status"], tc.wantBody)
			}
		})
	}
}
