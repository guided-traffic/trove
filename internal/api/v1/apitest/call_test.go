package apitest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const staleBody = `{"type":"/api/v1/problems/authz_stale","status":503,"code":"authz_stale"}`

// These tests pin the read-back after a create (R-API-004, R-API-052, Q-API-238): which answers are repeated,
// and that the other calls of the contract are not.

// resourceOver is a Resource whose calls go to h.
func resourceOver(h http.Handler) (Resource, *httptest.Server) {
	srv := httptest.NewServer(h)
	return Resource{Do: srv.Client().Do}, srv
}

func setStaleBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := staleBudget
	staleBudget = d
	t.Cleanup(func() { staleBudget = old })
}

func TestReadBackIsRepeatedAfterRetryAfterOfAnUnconfirmedState(t *testing.T) {
	setStaleBudget(t, 3*time.Second)
	var n atomic.Int32
	res, srv := resourceOver(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(staleBody))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if a := (&suite{res: res}).readBack(t, srv.URL); a.status != 200 || n.Load() != 3 {
		t.Fatalf("status %d after %d requests, want 200 after 3", a.status, n.Load())
	}
}

func TestReadBackRepeatsOnlyAStaleAnswerWithRetryAfter(t *testing.T) {
	setStaleBudget(t, 3*time.Second)
	for _, c := range []struct {
		name        string
		status      int
		code, retry string
	}{
		{"another 503", 503, "unavailable", "1"},
		{"a stale 503 without Retry-After", 503, "authz_stale", ""},
		{"a 404", 404, "not_found", ""},
		{"a 503 unavailable with Retry-After 0", 503, "unavailable", "0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var n atomic.Int32
			res, srv := resourceOver(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n.Add(1)
				if c.retry != "" {
					w.Header().Set("Retry-After", c.retry)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(`{"code":"` + c.code + `"}`))
			}))
			defer srv.Close()
			if a := (&suite{res: res}).readBack(t, srv.URL); a.status != c.status || n.Load() != 1 {
				t.Fatalf("status %d after %d requests, want %d after 1", a.status, n.Load(), c.status)
			}
		})
	}
}

func TestReadBackEndsWithTheAnswerOfAReplicaThatNeverConfirms(t *testing.T) {
	setStaleBudget(t, 300*time.Millisecond)
	res, srv := resourceOver(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(staleBody))
	}))
	defer srv.Close()
	start := time.Now()
	if a := (&suite{res: res}).readBack(t, srv.URL); a.status != 503 || time.Since(start) > 2*time.Second {
		t.Fatalf("status %d after %s, want the 503 once the budget is spent", a.status, time.Since(start))
	}
}

func TestOtherCallsOfTheContractAreNotRepeated(t *testing.T) {
	setStaleBudget(t, 3*time.Second)
	var n atomic.Int32
	res, srv := resourceOver(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(staleBody))
	}))
	defer srv.Close()
	for _, m := range []string{"GET", "POST"} {
		n.Store(0)
		if a := res.call(t, m, srv.URL, nil); a.status != 503 || n.Load() != 1 {
			t.Fatalf("%s: status %d after %d requests, want 503 after 1", m, a.status, n.Load())
		}
	}
}
