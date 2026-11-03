package link_test

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/link"
)

// TestR_BAS_027_ACallThatTheMainInstallationNeverAnswersIsCutAtUnreachableAfter: a connection that swallows the
// request holds a call of the link until response_header_timeout; the link caps every call at unreachable_after,
// so a tick returns, and the replica is unreachable, after that time and not after the longer timeout
// (Q-BAS-126, security finding B8). The delay of the main installation stands for the black hole.
func TestR_BAS_027_ACallThatTheMainInstallationNeverAnswersIsCutAtUnreachableAfter(t *testing.T) {
	r := newRig(t, rigOpts{
		realTime: true, timeout: 30 * time.Second,
		mutate: func(c *link.Config) { c.UnreachableAfter = time.Second; c.PollInterval = 200 * time.Millisecond },
	})
	r.connect()
	r.main.SetDelay(20 * time.Second)
	start := time.Now()
	r.tick()
	took := time.Since(start)
	if took > 10*time.Second {
		t.Fatalf("a tick against a black hole took %v, the cap is unreachable_after (1 s)", took)
	}
	if r.l.State().Reachable {
		t.Fatalf("the replica is still reachable after %v of no feed answer:\n%s", took, r.logs.String())
	}
}
