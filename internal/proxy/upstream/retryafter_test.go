package upstream

import (
	"net/http"
	"testing"
	"time"
)

// TestParseRetryAfter: one reader of Retry-After for the client classification and RateLimitOf (R-REPO-054, R-REPO-029); each caller
// applies its own rule for an absent value.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		in   string
		want time.Duration
		ok   bool
	}{
		"seconds":            {"7", 7 * time.Second, true},
		"zero":               {"0", 0, true},
		"a date ahead":       {now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second, true},
		"a date in the past": {now.Add(-time.Minute).Format(http.TimeFormat), -time.Minute, true},
		"negative":           {"-1", 0, false},
		"unreadable":         {"soon", 0, false},
		"empty":              {"", 0, false},
		"overflowing":        {"99999999999999999", time.Duration(1<<31) * time.Second, true},
	} {
		t.Run("R-REPO-054 "+name, func(t *testing.T) {
			d, ok := ParseRetryAfter(tc.in, now)
			if ok != tc.ok || (ok && d != tc.want) {
				t.Fatalf("%v %v, want %v %v", d, ok, tc.want, tc.ok)
			}
		})
	}
	t.Run("R-REPO-054 the error names its HTTP status", func(t *testing.T) {
		var s interface{ HTTPStatus() int } = &RateLimitError{RateLimit: RateLimit{Status: 503}}
		if s.HTTPStatus() != 503 {
			t.Fatal(s.HTTPStatus())
		}
	})
}
