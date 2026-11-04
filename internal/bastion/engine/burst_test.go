package engine

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound: the link
// records the whole page and the engine deletes what the page matches in one pass over the answers, so a burst page
// costs one pass and not one per entry (Q-BAS-126, security finding B7). The 5 s of R-BAS-026 is a wall-clock target
// of the reference environment: the page takes about 0.1 s natively, 0.65 s under -race with -coverpkg=./... and
// about 1 s in the pinned image on 2 CPUs (a headroom of 5 to 50, not two orders of magnitude), and 5.5 s on a
// runner that ran the packages of `make test` in parallel (run 41797860424). It is enforced with TROVE_PERF_GATE=1
// (`make test-performance`, Q-BAS-140). Per change the bound is relative to the load: the page may cost at most
// maxPassesPerPage times one matching pass over the same answers measured right before it, and a per-entry pass
// would cost a thousand of them.
func TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound(t *testing.T) {
	// The configuration of a bastion refuses a TROVE_ variable that is no setting of it (R-BAS-002), so the gate is
	// read first and the variable is out of the environment while the harness loads its configuration.
	gated := os.Getenv("TROVE_PERF_GATE") == "1"
	if v, ok := os.LookupEnv("TROVE_PERF_GATE"); ok {
		os.Unsetenv("TROVE_PERF_GATE")
		t.Cleanup(func() { os.Setenv("TROVE_PERF_GATE", v) })
	}
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

	pass := medianOf(5, func() {
		m := newEntryMatcher(entries)
		s.e.answers.mem.retain(func(_ string, v *answerVal) bool { _ = m.matches(v.Answer); return true })
	})
	start := time.Now()
	s.tick()
	took := time.Since(start)
	t.Logf("the page took %v, one matching pass over the answers %v (%.1f passes)", took, pass, float64(took)/float64(pass))
	if limit := maxPassesPerPage * pass; took > limit {
		t.Fatalf("the page took %v to apply, %d passes over the answers (%v) are the bound: a pass per entry is a thousand", took, maxPassesPerPage, limit)
	}
	switch {
	case took <= 5*time.Second:
	case gated:
		t.Fatalf("the page took %v to apply, the bound of R-BAS-026 is 5 s", took)
	default:
		t.Logf("the page took %v, above the 5 s of R-BAS-026; enforced with TROVE_PERF_GATE=1 (Q-BAS-140)", took)
	}
	if got, want := s.e.answers.mem.len(), answersHeld/2; got != want {
		t.Fatalf("%d answers left, want %d: the answers of the 1000 named tokens go and only those", got, want)
	}
	if got := readCounter(s.m.FeedApplied.WithLabelValues("token")); got != revoked {
		t.Fatalf("%v entries counted, want %d", got, revoked)
	}
}

// maxPassesPerPage is the per-change bound of a burst page in matching passes over the answers (Q-BAS-140): the
// page is one pass that also deletes and measures 2 to 3 passes (natively and under -race with -coverpkg=./...);
// the per-entry design was 1,000.
const maxPassesPerPage = 100

// medianOf runs f n times and returns the median duration.
func medianOf(n int, f func()) time.Duration {
	d := make([]time.Duration, n)
	for i := range d {
		start := time.Now()
		f()
		d[i] = time.Since(start)
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[n/2]
}
