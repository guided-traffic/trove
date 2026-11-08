package upstream

import (
	"net/http"
	"testing"
	"time"
)

// TestR_REPO_054_QuotaHeaderParse: RateLimit-Remaining is read by its integer before the first ';', so a registry's
// "0;w=21600" counts as 0 (R-REPO-054).
func TestR_REPO_054_QuotaHeaderParse(t *testing.T) {
	for _, tc := range []struct {
		in string
		n  int64
		ok bool
	}{
		{"0", 0, true},
		{"0;w=21600", 0, true},
		{" 0 ; w=21600", 0, true},
		{"76;w=21600", 76, true},
		{"100;w=21600;comment=\"x\"", 100, true},
		{"-1", -1, true},
		{"", 0, false},
		{";w=21600", 0, false},
		{"abc;w=1", 0, false},
		{"1.5", 0, false},
		{"99999999999999999999", 0, false},
	} {
		n, ok := QuotaRemaining(tc.in)
		if n != tc.n || ok != tc.ok {
			t.Errorf("QuotaRemaining(%q) = %d, %v; want %d, %v", tc.in, n, ok, tc.n, tc.ok)
		}
	}
}

// TestR_REPO_054_QuotaHeaderWithWindowIsARateLimit: the answers that carry "0;w=…" are rate-limit answers with the reason
// quota_header and no wait of their own; one with requests left is none (R-REPO-054).
func TestR_REPO_054_QuotaHeaderWithWindowIsARateLimit(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header, value string
		status              int
		limited             bool
	}{
		{"429 RateLimit-Remaining 0 with a window", "RateLimit-Remaining", "0;w=21600", 429, true},
		{"403 X-RateLimit-Remaining 0 with a window", "X-RateLimit-Remaining", "0;w=60", 403, true},
		{"503 RateLimit-Remaining 0 with a window", "RateLimit-Remaining", "0;w=21600", 503, true},
		{"403 with requests left", "RateLimit-Remaining", "12;w=21600", 403, false},
		{"403 with an unreadable value", "RateLimit-Remaining", "soon;w=21600", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set(tc.header, tc.value)
			rl, ok := RateLimitOf(tc.status, h, nil, now)
			if ok != tc.limited {
				t.Fatalf("limited = %v, want %v (%+v)", ok, tc.limited, rl)
			}
			if ok && (rl.Reason != ReasonQuotaHeader || rl.Hinted) {
				t.Errorf("RateLimitOf = %+v, want the reason %s and no hint", rl, ReasonQuotaHeader)
			}
		})
	}
}
