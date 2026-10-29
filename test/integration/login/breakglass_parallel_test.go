//go:build integration

package login

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
)

// TestR_AUTH_038_ParallelAttemptsAreCountedBeforeTheyAreVerified sends many wrong
// passwords at once across three replicas. Each attempt is counted before its
// password is verified, so no more than the threshold (5) are verified; the rest
// are refused with 429 unverified, and the right password is refused afterwards
// (R-AUTH-038, R-AUTH-014).
func TestR_AUTH_038_ParallelAttemptsAreCountedBeforeTheyAreVerified(t *testing.T) {
	e, _, _ := breakGlassEnv(t, 3, nil)
	const attempts = 30
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := e.browser().localLogin("admin", "wrong-password-"+strconv.Itoa(i))
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	counts := map[int]int{}
	for _, c := range codes {
		counts[c]++
	}
	if counts[http.StatusUnauthorized] > 5 {
		t.Fatalf("%d wrong passwords were verified, the threshold is 5: %v", counts[http.StatusUnauthorized], counts)
	}
	if counts[http.StatusUnauthorized] == 0 {
		t.Fatalf("no attempt was verified: %v", counts)
	}
	for c := range counts {
		if c != http.StatusUnauthorized && c != http.StatusTooManyRequests && c != http.StatusServiceUnavailable {
			t.Fatalf("unexpected answers %v", counts)
		}
	}
	// A verification that found no slot is taken back, so the count equals the
	// failures: the account is locked once five were verified, otherwise the
	// remaining attempts bring it there.
	for i := 0; counts[http.StatusUnauthorized] < 5 && i < 10; i++ {
		if resp, _ := e.browser().localLogin("admin", "wrong-again-"+strconv.Itoa(i)); resp.StatusCode == http.StatusUnauthorized {
			counts[http.StatusUnauthorized]++
		}
	}
	resp, body := e.browser().localLogin("admin", bgPassword)
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "break_glass_locked") {
		t.Fatalf("the right password after the burst: %d %s", resp.StatusCode, body)
	}
	if n := len(e.audit.events(audit.EventAuthBreakGlassLocked)); n != 1 {
		t.Fatalf("auth.breakglass.locked events = %d", n)
	}
}
