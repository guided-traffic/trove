package discover

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type throttled struct{ wait time.Duration }

func (t throttled) Error() string                     { return "throttled" }
func (t throttled) RetryAfter() (time.Duration, bool) { return t.wait, t.wait > 0 }
func (t throttled) RateLimitReason() string           { return "too_many_requests" }

func TestDiscoverContract(t *testing.T) {
	t.Run("R-REPO-054 a wrapped rate-limit error is found with its wait", func(t *testing.T) {
		err := fmt.Errorf("listing page 3: %w", throttled{wait: 7 * time.Second})
		rl, ok := AsRateLimited(err)
		if !ok {
			t.Fatal("AsRateLimited did not find the wrapped error")
		}
		if d, hinted := rl.RetryAfter(); !hinted || d != 7*time.Second {
			t.Fatalf("RetryAfter = %v, %v", d, hinted)
		}
		if _, ok := AsRateLimited(errors.New("connection refused")); ok {
			t.Fatal("a transport error counts as a rate limit")
		}
	})
	t.Run("R-REPO-051 auto tries feed, listing and names in that order", func(t *testing.T) {
		got := Order()
		if len(got) != 3 || got[0] != MethodFeed || got[1] != MethodListing || got[2] != MethodNames {
			t.Fatalf("Order() = %v", got)
		}
		for _, m := range got {
			if !m.Valid() {
				t.Fatalf("%q is not valid", m)
			}
		}
		if Method("crawl").Valid() {
			t.Fatal("an unknown method is valid")
		}
	})
	t.Run("R-REPO-052 the literal prefix and the bounding rule of include patterns", func(t *testing.T) {
		for _, tc := range []struct {
			pattern  string
			prefix   string
			wildcard bool
			bounding bool
		}{
			{"lodash", "lodash", false, true},
			{"@types/*", "@types/", true, true},
			{"Microsoft.Extensions.*", "Microsoft.Extensions.", true, true},
			{"*", "", true, false},
			{"**", "", true, false},
			{"@*/*", "@", true, false},
			{"?x", "", true, false},
			{`a\*b*`, "a*b", true, true},
			{"@acme/", "@acme/", false, true},
		} {
			prefix, wildcard := LiteralPrefix(tc.pattern)
			if prefix != tc.prefix || wildcard != tc.wildcard {
				t.Errorf("LiteralPrefix(%q) = %q, %v; want %q, %v", tc.pattern, prefix, wildcard, tc.prefix, tc.wildcard)
			}
			if got := Bounding(tc.pattern); got != tc.bounding {
				t.Errorf("Bounding(%q) = %v, want %v", tc.pattern, got, tc.bounding)
			}
		}
	})
}
