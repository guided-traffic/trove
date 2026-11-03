package link_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/link"
)

// TestR_BAS_026_TheHistoryStartsOnlyWhenTheStartReadReachedItsEnd: a starting replica reads the feed in pages; the
// start of its history and the end of its unreachable state need the read to reach the end of the feed. When the
// second page fails, the first page's entries are applied, the replica stays unreachable and has no history; the
// next tick reads on from the cursor and ends both (Q-BAS-126, security finding B3).
func TestR_BAS_026_TheHistoryStartsOnlyWhenTheStartReadReachedItsEnd(t *testing.T) {
	r := newRig(t, rigOpts{mutate: func(c *link.Config) { c.FeedLimit = 1 }})
	for i := 0; i < 3; i++ {
		r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{fmt.Sprintf("t%d", i)}})
	}
	r.faulty.FailAfter(2) // the handshake and the first page pass, the second page does not
	r.tick()
	st := r.l.State()
	if st.Reachable {
		t.Fatalf("the replica turned reachable although the start read did not reach its end:\n%s", r.logs.String())
	}
	if !st.HistoryStart.IsZero() {
		t.Fatalf("the history started at %v after a start read that did not reach its end", st.HistoryStart)
	}
	if got := r.applied("token"); got != 1 {
		t.Fatalf("the first page's entry was applied %v times, want 1", got)
	}
	r.faulty.SetDown(false)
	r.tickAfter(2 * time.Second)
	st = r.l.State()
	if !st.Reachable || st.HistoryStart.IsZero() {
		t.Fatalf("the next tick did not finish the start read: reachable %v, history %v", st.Reachable, st.HistoryStart)
	}
	if got := r.applied("token"); got != 3 {
		t.Fatalf("%v entries applied, want 3", got)
	}
}
