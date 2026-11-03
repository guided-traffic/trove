package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound: the link
// records the whole page and the engine deletes what the page matches in one pass over the answers, so a burst page
// costs one pass and not one per entry. The bound of R-BAS-026 is 5 s from the poll; the test runs on the
// slowest runner we know and has the headroom of two orders of magnitude (Q-BAS-126, security finding B7).
func TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound(t *testing.T) {
	s := newSiteHarness(t, options{})
	const answersHeld, tokens, revoked = 200000, 2000, 1000
	for i := 0; i < answersHeld; i++ {
		a := answers.Answer{Kind: answers.URL, ObtainedAt: s.clock.now(), TokenID: fmt.Sprintf("tok-%d", i%tokens), Repository: "npm-all"}
		s.e.answers.mem.put(fmt.Sprintf("url|k%d", i), &answerVal{Answer: a, Allowed: true}, 0)
	}
	if got := s.e.answers.mem.len(); got != answersHeld {
		t.Fatalf("%d answers held, want %d", got, answersHeld)
	}
	entries := make([]iface.FeedEntry, revoked)
	for i := range entries {
		entries[i] = iface.FeedEntry{ID: fmt.Sprintf("e%d", i), At: s.clock.now(), Kind: iface.FeedToken, Tokens: []string{fmt.Sprintf("tok-%d", i)}}
	}
	s.main.mu.Lock()
	s.main.feedAns = iface.FeedAnswer{Entries: entries, Cursor: iface.Cursor{At: epoch, ID: "e999"}.String()}
	s.main.mu.Unlock()
	s.clock.advance(time.Second)

	start := time.Now()
	s.tick()
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the page took %v to apply, the bound is 5 s", took)
	}
	if got, want := s.e.answers.mem.len(), answersHeld/2; got != want {
		t.Fatalf("%d answers left, want %d: the answers of the 1000 named tokens go and only those", got, want)
	}
	if got := readCounter(s.m.FeedApplied.WithLabelValues("token")); got != revoked {
		t.Fatalf("%v entries counted, want %d", got, revoked)
	}
}
