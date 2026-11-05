package mirror

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// R-REPO-054: the window starts at 1, doubles after w successes in a row until the ceiling (slow start), halves once per wait
// and then grows by one after w successes in a row.
func TestR_REPO_054_TheWindowStartsSlowHalvesOncePerWaitAndGrowsByOne(t *testing.T) {
	clock := newClock()
	var seen []int
	th := newThrottle(clock, fixedJitter(0), 8, func(w int) { seen = append(seen, w) })
	ctx := context.Background()
	succeed := func(n int) {
		t.Helper()
		for range n {
			tk, err := th.acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			th.success(tk)
		}
	}
	mustEqual(t, "the window of a new attempt", th.Window(), 1)
	succeed(1)
	mustEqual(t, "after 1 success", th.Window(), 2)
	succeed(2)
	mustEqual(t, "after 2 more", th.Window(), 4)
	succeed(4)
	mustEqual(t, "after 4 more", th.Window(), 8)
	succeed(20)
	mustEqual(t, "the ceiling holds", th.Window(), 8)

	// Eight requests in flight; the first answer is a rate limit: the window halves, the other answers do not halve it again.
	var tks []ticket
	for range 8 {
		tk, err := th.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tks = append(tks, tk)
	}
	wait, started := th.rateLimited(tks[0], 7*time.Second, true)
	if !started || wait != 7*time.Second {
		t.Fatalf("first answer: started %v wait %v", started, wait)
	}
	mustEqual(t, "after the first answer", th.Window(), 4)
	for _, tk := range tks[1:] {
		if _, started := th.rateLimited(tk, 7*time.Second, true); started {
			t.Fatal("an answer of a request that was in flight when the wait began started another wait")
		}
	}
	mustEqual(t, "answers of requests in flight do not halve again", th.Window(), 4)

	// Nothing is sent during the wait: the next acquire sleeps through it.
	before := clock.Now()
	tk, err := th.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.Now().Sub(before); got != 7*time.Second {
		t.Fatalf("the wait lasted %v, want 7s", got)
	}
	th.success(tk)

	// After a rate limit the window grows by one after w successes (4 here), no longer by doubling.
	succeed(3)
	mustEqual(t, "additive increase after 4 successes", th.Window(), 5)
	succeed(5)
	mustEqual(t, "additive increase after 5 more", th.Window(), 6)
	if len(seen) == 0 || seen[0] != 1 {
		t.Fatalf("the observer saw %v", seen)
	}
}

// R-REPO-054: the window a wait halved stays halved while the wait lasts: requests that were in flight when it began answer
// successfully and do not grow it (trove_mirror_concurrency is at most half of its value before the answer).
func TestR_REPO_054_SuccessesOfRequestsInFlightWhenTheWaitBeganDoNotGrowTheHalvedWindow(t *testing.T) {
	th := newThrottle(newClock(), fixedJitter(0), 4, nil)
	ctx := context.Background()
	for range 3 { // slow start: 1, 2, 4
		tk, _ := th.acquire(ctx)
		th.success(tk)
	}
	mustEqual(t, "the window before the answer", th.Window(), 4)
	var tks []ticket
	for range 4 {
		tk, err := th.acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tks = append(tks, tk)
	}
	if _, started := th.rateLimited(tks[0], 7*time.Second, true); !started {
		t.Fatal("no wait started")
	}
	mustEqual(t, "the window after the answer", th.Window(), 2)
	for _, tk := range tks[1:] {
		th.success(tk)
	}
	mustEqual(t, "the window after the answers of the requests in flight", th.Window(), 2)
}

// R-REPO-054: a request that sleeps through a wait when the pass stops taking work (a stop, a bound or an error during the
// wait) is woken by the halt at once, so that the job does not sit through the rest of the wait. A wait over 300 s halts the
// throttle itself before a request can sleep through it (Q-ARCH-323), so the wait here is a short one.
func TestR_REPO_054_AHaltWakesTheRequestsThatSleepThroughAWait(t *testing.T) {
	th := newThrottle(blockingClock{newClock()}, fixedJitter(0), 4, nil)
	ctx := context.Background()
	tk, err := th.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, started := th.rateLimited(tk, 200*time.Second, true); !started {
		t.Fatal("no wait started")
	}
	got := make(chan error, 1)
	go func() { _, err := th.acquire(ctx); got <- err }()
	select {
	case err := <-got:
		t.Fatalf("acquire returned %v during the wait", err)
	case <-time.After(100 * time.Millisecond):
	}
	th.halt()
	select {
	case err := <-got:
		if !errors.Is(err, errHalted) {
			t.Fatalf("acquire returned %v, want errHalted", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the halt did not wake the request that sleeps through the wait")
	}
}

// R-REPO-054: the window never exceeds its ceiling, and a halving stops at 1.
func TestR_REPO_054_TheWindowStaysBetweenOneAndTheCeiling(t *testing.T) {
	th := newThrottle(newClock(), fixedJitter(0), 3, nil)
	ctx := context.Background()
	for range 40 {
		tk, _ := th.acquire(ctx)
		th.success(tk)
	}
	mustEqual(t, "the ceiling", th.Window(), 3)
	for range 6 {
		tk, _ := th.acquire(ctx)
		th.rateLimited(tk, 0, false)
	}
	mustEqual(t, "the floor", th.Window(), 1)
}

// R-REPO-054: without a hint the wait after the k-th answer in a row is drawn from [d/2, d] with d = min(300 s, 1 s x 2^(k-1));
// a success resets k.
func TestR_REPO_054_TheBackOffWithoutAHintIsDrawnFromHalfToFullAndCapped(t *testing.T) {
	cases := []struct {
		k      int
		lo, hi time.Duration
	}{
		{1, 500 * time.Millisecond, time.Second},
		{2, time.Second, 2 * time.Second},
		{3, 2 * time.Second, 4 * time.Second},
		{8, 64 * time.Second, 128 * time.Second},
		{9, 128 * time.Second, 256 * time.Second},
		{10, 150 * time.Second, 300 * time.Second},
		{40, 150 * time.Second, 300 * time.Second},
	}
	for _, c := range cases {
		if got := RateLimitBackoff(c.k, 0); got != c.lo {
			t.Errorf("k=%d, draw 0: %v, want %v", c.k, got, c.lo)
		}
		if got := RateLimitBackoff(c.k, 0.999999); got < c.lo || got > c.hi {
			t.Errorf("k=%d, draw 1: %v, want within [%v, %v]", c.k, got, c.lo, c.hi)
		}
	}
	th := newThrottle(newClock(), fixedJitter(0), 4, nil)
	ctx := context.Background()
	// The draw is 0, so each wait is d/2 of the k-th answer in a row; the floor of a second applies to the first.
	for k, want := range []time.Duration{time.Second, time.Second, 2 * time.Second} {
		tk, _ := th.acquire(ctx)
		if wait, _ := th.rateLimited(tk, 0, false); wait != want {
			t.Errorf("answer %d: wait %v, want %v", k+1, wait, want)
		}
	}
	tk, _ := th.acquire(ctx)
	th.success(tk)
	tk, _ = th.acquire(ctx)
	wait, _ := th.rateLimited(tk, 0, false)
	if wait != time.Second {
		t.Errorf("after a success the next wait is drawn from k=1 again: %v", wait)
	}
}

// R-REPO-054: a wait the upstream asked for is capped at 6 h; an answer that asks for nothing still waits a second.
func TestR_REPO_054_ARetryAfterIsCappedAndNeverZero(t *testing.T) {
	th := newThrottle(newClock(), fixedJitter(0), 4, nil)
	tk, _ := th.acquire(context.Background())
	if wait, _ := th.rateLimited(tk, 100*time.Hour, true); wait != 6*time.Hour {
		t.Errorf("a hint of 100 h waits %v, want 6h", wait)
	}
	th = newThrottle(newClock(), fixedJitter(0), 4, nil)
	tk, _ = th.acquire(context.Background())
	if wait, _ := th.rateLimited(tk, 0, true); wait != time.Second {
		t.Errorf("a hint of 0 waits %v, want 1s", wait)
	}
}

// R-REPO-053 errors: the wait after a failed pass attempt is 60 s, 120 s, 240 s ... capped at 6 h, with a jitter of 20 %.
func TestR_REPO_053_TheFailureBackOffDoublesFromAMinuteAndIsCappedAtSixHours(t *testing.T) {
	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second}
	for i, w := range want {
		mustEqual(t, "back-off after failure "+string(rune('1'+i)), FailureBackoff(i+1), w)
	}
	mustEqual(t, "before the cap", FailureBackoff(9), 15360*time.Second)
	mustEqual(t, "the cap", FailureBackoff(10), 6*time.Hour)
	mustEqual(t, "the cap holds", FailureBackoff(500), 6*time.Hour)
	if f := JitterFactor(0); f != 0.8 {
		t.Errorf("lowest factor %v", f)
	}
	if f := JitterFactor(0.9999999); f < 1.19 || f > 1.2 {
		t.Errorf("highest factor %v", f)
	}
	if f := JitterFactor(0.5); f != 1.0 {
		t.Errorf("middle factor %v", f)
	}
}

// R-REPO-051: the versions a pass stores: prereleases go when they are not wanted, the newest n of the rest stay, a pinned
// version stays besides them (Q-NPM-71).
func TestR_REPO_051_TheVersionBoundsKeepTheNewestAndThePinned(t *testing.T) {
	item := func(v string, pre, pin bool) discover.Item {
		return discover.Item{Version: v, Path: v, Prerelease: pre, Pinned: pin}
	}
	versions := func(items []discover.Item) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Version)
		}
		return out
	}
	// The example of the acceptance criterion: 1.0.0, 1.1.0 (latest), 2.0.0-rc.1, 1.2.0 with maxVersionsPerPackage 2 and no prereleases.
	plan := []discover.Item{item("2.0.0-rc.1", true, false), item("1.2.0", false, false), item("1.1.0", false, true), item("1.0.0", false, false)}
	mustContain(t, "n=2 without prereleases", versions(SelectItems(plan, false, 2)), "1.2.0", "1.1.0")
	mustContain(t, "n=2 with prereleases", versions(SelectItems(plan, true, 2)), "2.0.0-rc.1", "1.2.0", "1.1.0")
	mustContain(t, "n=0 is all", versions(SelectItems(plan, true, 0)), "2.0.0-rc.1", "1.2.0", "1.1.0", "1.0.0")
	mustContain(t, "n=0 without prereleases", versions(SelectItems(plan, false, 0)), "1.2.0", "1.1.0", "1.0.0")
	// A pinned version beyond the bound stays besides the newest n, and a pinned prerelease stays although prereleases are off.
	plan = []discover.Item{item("3.0.0", false, false), item("2.0.0", false, false), item("1.0.0", false, true), item("0.9.0-beta", true, true)}
	mustContain(t, "pinned beyond the bound", versions(SelectItems(plan, false, 1)), "3.0.0", "1.0.0", "0.9.0-beta")
	mustContain(t, "no items", versions(SelectItems(nil, true, 3)))
}

// R-REPO-051: a pass is full when there is no cursor of a complete feed pass, the method differs, the patterns changed, the
// last full pass is older than fullPassIntervalSeconds, or it is requested; otherwise a feed pass is incremental.
func TestR_REPO_051_APassIsFullOrIncrementalByTheRules(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	fp := Fingerprint([]string{"@acme/*"}, nil)
	base := State{FeedCursor: []byte(`{"seq":5}`), FeedMethod: discover.MethodFeed, PatternsFingerprint: fp, LastFullPassAt: now.Add(-time.Hour)}
	week := 7 * 24 * time.Hour
	cases := []struct {
		name string
		s    func(State) State
		fp   []byte
		full time.Duration
		req  bool
		want discover.PassKind
	}{
		{"incremental after a complete feed pass", func(s State) State { return s }, fp, week, false, discover.PassIncremental},
		{"no cursor", func(s State) State { s.FeedCursor = nil; return s }, fp, week, false, discover.PassFull},
		{"the patterns changed", func(s State) State { return s }, Fingerprint([]string{"@acme/*", "@acme2/*"}, nil), week, false, discover.PassFull},
		{"the exclude patterns changed", func(s State) State { return s }, Fingerprint([]string{"@acme/*"}, []string{"x"}), week, false, discover.PassFull},
		{"the last full pass is too old", func(s State) State { s.LastFullPassAt = now.Add(-8 * 24 * time.Hour); return s }, fp, week, false, discover.PassFull},
		{"no full pass interval", func(s State) State { s.LastFullPassAt = now.Add(-800 * 24 * time.Hour); return s }, fp, 0, false, discover.PassIncremental},
		{"requested by the arguments", func(s State) State { return s }, fp, week, true, discover.PassFull},
		{"requested in the row", func(s State) State { s.PassRequestedFull = true; return s }, fp, week, false, discover.PassFull},
	}
	for _, c := range cases {
		if got := DecideKind(c.s(base), c.fp, c.full, now, c.req); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// The method: only a feed that is the feed of the last complete pass is incremental.
	mustEqual(t, "a listing is always full", FinalKind(discover.PassIncremental, discover.MethodListing, base), discover.PassFull)
	other := base
	other.FeedMethod = discover.MethodListing
	mustEqual(t, "a method that differs from the cursor's", FinalKind(discover.PassIncremental, discover.MethodFeed, other), discover.PassFull)
	mustEqual(t, "the same feed", FinalKind(discover.PassIncremental, discover.MethodFeed, base), discover.PassIncremental)
	mustEqual(t, "a full pass stays full", FinalKind(discover.PassFull, discover.MethodFeed, base), discover.PassFull)
}

// R-REPO-056: an error message names hosts and no URL: nothing of a path, a query or a credential stays.
func TestR_REPO_056_AMessageKeepsTheHostOfAnURLAndNothingElse(t *testing.T) {
	got := sanitize(`GET https://user:secret@registry.example.org/@acme%2fa?token=abc failed: dial tcp (see http://10.0.0.1:8080/x)`)
	want := `GET registry.example.org failed: dial tcp (see 10.0.0.1`
	if got != want+`)` {
		t.Fatalf("sanitize = %q", got)
	}
}
