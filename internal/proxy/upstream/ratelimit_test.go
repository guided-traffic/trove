package upstream

import (
	"net/http"
	"testing"
	"time"
)

func TestRateLimitOf(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	slow := []byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>`)
	for _, tc := range []struct {
		name   string
		status int
		h      http.Header
		body   []byte
		ok     bool
		reason string
		wait   time.Duration
		hinted bool
	}{
		{"R-REPO-054 429 with Retry-After seconds", 429, hdr("Retry-After", "7"), nil, true, ReasonTooManyRequests, 7 * time.Second, true},
		{"R-REPO-054 429 with Retry-After as an HTTP date", 429, hdr("Retry-After", now.Add(5*time.Second).Format(http.TimeFormat)), nil, true, ReasonTooManyRequests, 5 * time.Second, true},
		{"R-REPO-054 429 without a hint", 429, hdr(), nil, true, ReasonTooManyRequests, 0, false},
		{"R-REPO-054 429 with a date in the past is no hint", 429, hdr("Retry-After", now.Add(-time.Minute).Format(http.TimeFormat)), nil, true, ReasonTooManyRequests, 0, false},
		{"R-REPO-054 503 SlowDown without Retry-After", 503, hdr(), slow, true, ReasonSlowDown, 0, false},
		{"R-REPO-054 503 SlowDown with Retry-After", 503, hdr("Retry-After", "2"), slow, true, ReasonSlowDown, 2 * time.Second, true},
		{"R-REPO-054 503 with Retry-After", 503, hdr("Retry-After", "30"), nil, true, ReasonRetryAfter, 30 * time.Second, true},
		{"R-REPO-054 503 with an unreadable Retry-After is a failure", 503, hdr("Retry-After", "soon"), nil, false, "", 0, false},
		{"R-REPO-054 a plain 503 is a failure, not a rate limit", 503, hdr(), []byte("<html>busy</html>"), false, "", 0, false},
		{"R-REPO-054 403 with X-RateLimit-Remaining 0 and a reset", 403, hdr("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1793786404"), nil, true, ReasonQuotaHeader, 4 * time.Second, true},
		{"R-REPO-054 429 with RateLimit-Remaining 0 and RateLimit-Reset", 429, hdr("RateLimit-Remaining", "0", "RateLimit-Reset", "12"), nil, true, ReasonQuotaHeader, 12 * time.Second, true},
		{"R-REPO-054 403 with remaining requests is no rate limit", 403, hdr("X-RateLimit-Remaining", "10"), nil, false, "", 0, false},
		{"R-REPO-054 a plain 403 is no rate limit", 403, hdr(), nil, false, "", 0, false},
		{"R-REPO-054 a wait above six hours is capped", 429, hdr("Retry-After", "999999"), nil, true, ReasonTooManyRequests, MaxRateLimitWait, true},
		{"R-REPO-054 500 is no rate limit", 500, hdr("Retry-After", "5"), nil, false, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rl, ok := RateLimitOf(tc.status, tc.h, tc.body, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, rl)
			}
			if !ok {
				return
			}
			if rl.Status != tc.status || rl.Reason != tc.reason || rl.Wait != tc.wait || rl.Hinted != tc.hinted {
				t.Fatalf("RateLimitOf = %+v, want reason %s wait %v hinted %v", rl, tc.reason, tc.wait, tc.hinted)
			}
			e := &RateLimitError{RateLimit: rl, Repo: "npm-remote"}
			if d, h := e.RetryAfter(); d != tc.wait || h != tc.hinted || e.RateLimitReason() != tc.reason || e.Error() == "" {
				t.Fatalf("the error does not carry the outcome: %v %v %q", d, h, e.Error())
			}
		})
	}
}
