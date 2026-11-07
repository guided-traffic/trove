package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The engine's unit tests over the fakes of rig_test.go: a fake clock, a fake host and a fake lister. Each names the
// requirement it checks.

func (g *rig) item(result string) float64 {
	return val(g.met.Items.WithLabelValues("it/npm-mirror", result))
}

func (g *rig) passes(kind, outcome string) float64 {
	return val(g.met.Passes.WithLabelValues("it/npm-mirror", kind, outcome))
}

func (g *rig) lastPass() map[string]any {
	g.t.Helper()
	g.store.mu.Lock()
	raw := g.store.lastPass[rigRepoID]
	g.store.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		g.t.Fatalf("last_pass %q: %v", raw, err)
	}
	return m
}

func (g *rig) paths(names ...string) []string {
	var out []string
	for _, n := range names {
		out = append(out, n+"/-/1.0.0.tgz")
	}
	return out
}

// R-REPO-053: a pass claims, discovers, fills in name order, ends with the summary, clears the pass and sets the next one to its
// start plus the interval; the pending table is empty and the counters count what was stored.
func TestR_REPO_053_APassDiscoversFillsAndEnds(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("c", "a", "b")
	for _, n := range []string{"a", "b", "c"} {
		g.plan(n, "1.0.0")
	}
	start := g.clock.Now()
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "stored in name order", g.host.stored(), g.paths("a", "b", "c")...)
	if left := g.store.names(rigRepoID); len(left) != 0 {
		t.Fatalf("pending names left: %v", left)
	}
	st := g.store.state(rigRepoID)
	if st.InProgress() || st.PassJobID != "" {
		t.Fatalf("the pass is not cleared: %+v", st)
	}
	mustEqual(t, "next pass", st.NextPassAt, start.Add(time.Hour))
	mustEqual(t, "failures", st.ConsecutiveFailures, 0)
	sum := g.lastPass()
	mustEqual(t, "summary outcome", fmt.Sprint(sum["outcome"]), "complete")
	mustEqual(t, "summary packages", sum["packages"].(float64), 3)
	mustEqual(t, "summary itemsStored", sum["itemsStored"].(float64), 3)
	mustEqual(t, "summary bytesStored", sum["bytesStored"].(float64), 300)
	mustEqual(t, "summary kind", fmt.Sprint(sum["kind"]), "full")
	mustEqual(t, "summary method", fmt.Sprint(sum["method"]), "feed")
	mustEqual(t, "summary discoveryComplete", sum["discoveryComplete"].(bool), true)
	mustEqual(t, "stored items counted", g.item(ResultStored), 3)
	mustEqual(t, "bytes counted", val(g.met.Bytes.WithLabelValues("it/npm-mirror")), 300)
	mustEqual(t, "complete passes counted", g.passes("full", OutcomeComplete), 1)
	evs := g.rec.of(audit.EventMirrorPassCompleted)
	if len(evs) != 1 || evs[0].Repository != "it/npm-mirror" || evs[0].Actor.Kind != "system" {
		t.Fatalf("pass_completed events: %+v", evs)
	}
}

// R-REPO-051: after a complete feed pass the next pass is incremental and asks the feed after the stored cursor; a change of the
// patterns makes the next pass full; a requested pass is full.
func TestR_REPO_051_TheNextPassIsIncrementalAndAPatternChangeMakesItFull(t *testing.T) {
	g := newRig(t, []string{"@acme/*"}, "")
	g.lister.pages = []discover.Page{{Names: []string{"@acme/a"}, Done: true, Cursor: []byte(`{"seq":5}`)}}
	g.plan("@acme/a", "1.0.0")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	st := g.store.state(rigRepoID)
	mustEqual(t, "feed cursor", string(st.FeedCursor), `{"seq":5}`)
	mustEqual(t, "feed method", st.FeedMethod, discover.MethodFeed)
	if st.LastFullPassAt.IsZero() {
		t.Fatal("the time of the full pass is not recorded")
	}
	g.clock.advance(2 * time.Hour)
	g.lister.pages = []discover.Page{{Names: []string{"@acme/a"}, Done: true, Cursor: []byte(`{"seq":6}`)}}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the second pass", fmt.Sprint(res.get("kind")), "incremental")
	rq := g.lister.requests[len(g.lister.requests)-1]
	mustEqual(t, "request kind", rq.Kind, discover.PassIncremental)
	mustEqual(t, "request since", string(rq.Since), `{"seq":5}`)
	g.clock.advance(2 * time.Hour)
	g.update([]string{"@acme/*", "@acme2/*"}, "")
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "after a pattern change", fmt.Sprint(res.get("kind")), "full")
	g.clock.advance(2 * time.Hour)
	res, err = g.run(PassArgs{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "requested", fmt.Sprint(res.get("kind")), "full")
	g.clock.advance(2 * time.Hour)
	res, _ = g.run()
	mustEqual(t, "and incremental again", fmt.Sprint(res.get("kind")), "incremental")
}

// R-REPO-051: a method that is not the feed makes every pass full, whatever the cursor says.
func TestR_REPO_051_AListingPassIsAlwaysFull(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":1,"discovery":"listing"}`)
	g.lister.methods = []discover.Method{discover.MethodFeed, discover.MethodListing}
	g.lister.pages = []discover.Page{{Names: []string{"a"}, Done: true, Cursor: []byte(`{"c":1}`)}}
	g.plan("a", "1.0.0")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	g.clock.advance(2 * time.Hour)
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "kind", fmt.Sprint(res.get("kind")), "full")
	mustEqual(t, "method", g.lister.requests[len(g.lister.requests)-1].Method, discover.MethodListing)
}

// R-REPO-051: a method set explicitly that the upstream does not offer ends the pass with discovery_unavailable; `auto` takes
// the first method that is offered, and `names` is always offered.
func TestR_REPO_051_AMethodThatIsNotOfferedFailsTheAttemptAndAutoPicksTheFirst(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":1,"discovery":"feed"}`)
	g.lister.methods = []discover.Method{discover.MethodListing}
	_, err := g.run()
	if err == nil || !strings.Contains(err.Error(), CodeDiscoveryUnavailable) {
		t.Fatalf("err = %v", err)
	}
	evs := g.rec.of(audit.EventMirrorPassFailed)
	if len(evs) != 1 || evs[0].Reason != CodeDiscoveryUnavailable {
		t.Fatalf("pass_failed events: %+v", evs)
	}
	g2 := newRig(t, nil, `{"concurrency":1,"discovery":"auto"}`)
	g2.lister.methods = nil
	g2.names("a")
	if _, err := g2.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "auto falls back to names", g2.lister.requests[0].Method, discover.MethodNames)
}

// R-REPO-051: a full pass also takes the exact names of the include patterns and the names of the packages the repository
// holds, so that a package that clients pulled is kept current.
func TestR_REPO_051_AFullPassAddsTheExactIncludeNamesAndTheHeldPackages(t *testing.T) {
	g := newRig(t, []string{"Lodash", "@acme/*"}, "")
	g.store.held[rigRepoID] = []string{"@acme/held", "@other/held"}
	g.host.admit = func(n string) bool { return n != "@other/held" }
	g.names("x")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "packages planned", g.lister.planOrder, "@acme/held", "lodash", "x")
}

// R-REPO-051: the versions a pass stores follow maxVersionsPerPackage and includePrereleases, and a pinned version stays.
func TestR_REPO_051_APassStoresTheBoundedVersions(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":1,"maxVersionsPerPackage":2,"includePrereleases":false}`)
	g.names("@acme/a")
	g.lister.plans = map[string]discover.Plan{"@acme/a": {Name: "@acme/a", Items: []discover.Item{
		{Version: "2.0.0-rc.1", Path: "p/2.0.0-rc.1", Prerelease: true},
		{Version: "1.2.0", Path: "p/1.2.0"},
		{Version: "1.1.0", Path: "p/1.1.0", Pinned: true},
		{Version: "1.0.0", Path: "p/1.0.0"},
	}}}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stored versions", g.host.stored(), "p/1.2.0", "p/1.1.0")
}

// R-REPO-051: a name the feed reports as deleted makes the pass refresh its document and store nothing.
func TestR_REPO_051_ARemovedNameRefreshesTheDocumentAndStoresNothing(t *testing.T) {
	g := newRig(t, nil, "")
	g.lister.pages = []discover.Page{{Names: []string{"a"}, Removed: []string{"gone"}, Done: true}}
	g.plan("a", "1.0.0")
	g.plan("gone", "1.0.0")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stored", g.host.stored(), g.paths("a")...)
	if g.lister.planCalls["gone"] != 1 {
		t.Fatalf("the document of a deleted package was refreshed %d times", g.lister.planCalls["gone"])
	}
}

// R-REPO-051: a listing the upstream truncates completes the pass with discoveryComplete false and the reason.
func TestR_REPO_051_ATruncatedListingCompletesWithTheNote(t *testing.T) {
	g := newRig(t, nil, "")
	g.lister.pages = []discover.Page{{Names: []string{"a"}, Done: true, Truncated: true, Note: "the search stops at skip 3000"}}
	g.plan("a", "1.0.0")
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	sum := g.lastPass()
	mustEqual(t, "discoveryComplete", sum["discoveryComplete"].(bool), false)
	mustEqual(t, "discoveryNote", fmt.Sprint(sum["discoveryNote"]), "the search stops at skip 3000")
}

// R-REPO-053: discovery pauses at 10,000 pending names: the next page is not asked for while the pass has that many pending.
func TestR_REPO_053_DiscoveryStopsAskingWhileTenThousandNamesArePending(t *testing.T) {
	g := newRig(t, nil, "")
	var big []string
	for i := range MaxPending {
		big = append(big, fmt.Sprintf("p%05d", i))
	}
	g.lister.pages = []discover.Page{{Names: big}, {Names: []string{"last"}, Done: true}}
	var asked []int
	g.lister.namesErr = func(call int) error { asked = append(asked, call); return nil }
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) { return FillResult{Present: true}, nil }
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "pages asked", len(asked), 2)
	rq := g.lister.requests
	mustEqual(t, "the first page may hold 10,000 names", rq[0].Limit, MaxPending)
	mustEqual(t, "the second page is asked after the first was worked off", rq[1].Limit, MaxPending)
}

// R-REPO-053 errors: a failure of the pass itself is recorded, and the next pass is due after 60 s, 120 s and 240 s with a
// jitter of 20 %; the pass in progress stays; a complete pass resets the count.
func TestR_REPO_053_AFailedPassBacksOffAndAnEndedPassResetsTheCount(t *testing.T) {
	for _, tc := range []struct {
		jitter   float64
		scale    float64
		failures int
	}{{0, 0.8, 3}, {0.5, 1.0, 3}, {0.9999999, 1.2, 3}} {
		g := newRig(t, nil, "")
		g.eng.o.Jitter = fixedJitter(tc.jitter)
		g.lister.namesErr = func(call int) error { return errors.New("500 from https://registry.example.org/-/all") }
		g.names("a")
		g.plan("a", "1.0.0")
		for i := 1; i <= tc.failures; i++ {
			now := g.clock.Now()
			_, err := g.run()
			if err == nil || !strings.Contains(err.Error(), CodeDiscoveryFailed) || strings.Contains(err.Error(), "/-/all") {
				t.Fatalf("failure %d: %v", i, err)
			}
			st := g.store.state(rigRepoID)
			mustEqual(t, "failures", st.ConsecutiveFailures, i)
			want := time.Duration(float64(60*time.Second<<(i-1)) * tc.scale)
			if got := st.NextPassAt.Sub(now); got < want-time.Millisecond || got > want+time.Millisecond {
				t.Fatalf("failure %d: next pass in %v, want %v", i, got, want)
			}
			if !st.InProgress() {
				t.Fatal("the pass in progress was cleared by a failure")
			}
			g.clock.advance(time.Hour)
		}
		evs := g.rec.of(audit.EventMirrorPassFailed)
		mustEqual(t, "failure events", len(evs), tc.failures)
		mustEqual(t, "failed passes counted", g.passes("full", OutcomeFailed), float64(tc.failures))
		g.lister.namesErr = nil
		res, err := g.run()
		if err != nil {
			t.Fatal(err)
		}
		mustEqual(t, "outcome after the upstream recovers", fmt.Sprint(res.get("outcome")), "complete")
		mustEqual(t, "failures after a complete pass", g.store.state(rigRepoID).ConsecutiveFailures, 0)
	}
}

// R-REPO-053 step 3: a package that fails keeps its row with one more attempt and a retry a minute later per attempt; after
// three attempts it is left to the next pass and counted as failed; the pass ends partial, the next pass tries it again.
func TestR_REPO_053_AFailingPackageIsRetriedThreeTimesThenLeftToTheNextPass(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	broken := true
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "a/") && broken {
			return FillResult{}, errors.New("the upstream answered 500")
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "partial")
	if got := g.clock.slept(); len(got) != 2 || got[0] != time.Minute || got[1] != 2*time.Minute {
		t.Fatalf("waits for the retries: %v", got)
	}
	mustEqual(t, "failed items", g.item(ResultFailed), 3)
	sum := g.lastPass()
	mustEqual(t, "packagesFailed", sum["packagesFailed"].(float64), 1)
	mustEqual(t, "packages", sum["packages"].(float64), 1)
	mustContain(t, "left pending", g.store.names(rigRepoID), "a")
	mustEqual(t, "no failure of the pass itself", g.store.state(rigRepoID).ConsecutiveFailures, 0)
	broken = false
	g.clock.advance(time.Hour)
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the next pass", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "no name left", g.store.names(rigRepoID))
}

// R-REPO-054: a rate-limit answer with Retry-After makes the pass send nothing for that long, halves its window and repeats
// the request; nothing is a failure, and the item is stored once.
func TestR_REPO_054_ARateLimitWaitsForTheHintAndRepeatsTheRequest(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, call int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "a/") && call == 1 {
			return FillResult{}, fmt.Errorf("fill: %w", &rateLimitErr{status: 429, after: 7 * time.Second, hinted: true})
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	if got := g.clock.slept(); len(got) != 1 || got[0] != 7*time.Second {
		t.Fatalf("waits: %v", got)
	}
	mustContain(t, "each tarball stored once", g.host.stored(), g.paths("a", "b")...)
	mustEqual(t, "failed items", g.item(ResultFailed), 0)
	mustEqual(t, "rate limits counted", val(g.met.RateLimited.WithLabelValues("it/npm-mirror", "too_many_requests")), 1)
	mustEqual(t, "wait seconds counted", val(g.met.RateLimitWait.WithLabelValues("it/npm-mirror")), 7)
	evs := g.rec.of(audit.EventMirrorRateLimited)
	if len(evs) != 1 {
		t.Fatalf("rate_limited events: %+v", evs)
	}
	var d map[string]any
	_ = json.Unmarshal(evs[0].Details, &d)
	mustEqual(t, "event hinted", d["hinted"].(bool), true)
	mustEqual(t, "event host", fmt.Sprint(d["host"]), "registry.example.org")
}

// R-REPO-054: a wait longer than 300 s ends the job with the result deferred and keeps the pass; the dispatch does not enqueue
// the next job before the wait is over and enqueues it at that time, and the job continues the pass.
func TestR_REPO_054_ALongWaitEndsTheJobAndTheNextJobContinuesAtThatTime(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	limited := true
	g.host.fill = func(item discover.Item, call int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "b/") && limited {
			return FillResult{}, &rateLimitErr{status: 429, after: time.Hour, hinted: true}
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.eng.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the first dispatch enqueues the pass", g.queue.count(), 1)
	g.queue.finish(PassKey(rigRepoID))
	start := g.clock.Now()
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "result", fmt.Sprint(res.get("deferred")), "rate_limited")
	mustEqual(t, "until", fmt.Sprint(res.get("until")), start.Add(time.Hour).Format(time.RFC3339))
	if len(g.clock.slept()) != 0 {
		t.Fatalf("the job slept through the wait: %v", g.clock.slept())
	}
	st := g.store.state(rigRepoID)
	if !st.InProgress() || !st.RateLimitedUntil.Equal(start.Add(time.Hour)) {
		t.Fatalf("the pass does not stay in progress with the wait: %+v", st)
	}
	mustEqual(t, "deferred passes counted", g.passes("full", OutcomeDeferred), 1)
	g.clock.advance(59 * time.Minute)
	if out, _ := g.eng.Dispatch(t.Context()); out.Enqueued != 0 {
		t.Fatal("the dispatch enqueued a pass before the wait was over")
	}
	g.clock.advance(time.Minute + time.Second)
	out, err := g.eng.Dispatch(t.Context())
	if err != nil || out.Enqueued != 1 {
		t.Fatalf("the dispatch after the wait: %+v, %v", out, err)
	}
	limited = false
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the next job", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "each tarball stored once", g.host.stored(), g.paths("a", "b")...)
	mustEqual(t, "one rate-limit event", len(g.rec.of(audit.EventMirrorRateLimited)), 1)
}

// R-REPO-054: a pre-emption by a client and a *WaitError are neither failures nor rate limits: the item comes back later, the
// request is repeated after the wait, and the pass completes with no failed item.
func TestR_REPO_054_APreEmptedFetchAndAWaitAreNotFailures(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, call int) (FillResult, error) {
		switch {
		case strings.HasPrefix(item.Path, "a/") && call == 1:
			return FillResult{}, fmt.Errorf("fill: %w", ErrPreempted)
		case strings.HasPrefix(item.Path, "b/") && call == 1:
			return FillResult{}, &WaitError{After: 5 * time.Second, Reason: WaitCircuitOpen}
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustEqual(t, "packagesFailed", g.lastPass()["packagesFailed"].(float64), 0)
	mustEqual(t, "failed items", g.item(ResultFailed), 0)
	mustEqual(t, "pre-empted items", g.item(ResultPreempted), 1)
	mustEqual(t, "pre-empted counter", val(g.met.Preempted.WithLabelValues("it/npm-mirror")), 1)
	mustEqual(t, "no rate limit", val(g.met.RateLimited.WithLabelValues("it/npm-mirror", "too_many_requests")), 0)
	got := g.host.stored()
	sort.Strings(got)
	mustContain(t, "each tarball stored once", got, g.paths("a", "b")...)
	mustEqual(t, "no attempt counted", len(g.store.names(rigRepoID)), 0)
}

// R-REPO-052: the pass fetches no artifact that would cross maxBytes, ends with bound_reached keeping its pending names,
// writes mirror.bound_reached once, ends later passes at once without an upstream request while the bound holds, and continues
// when the bound is raised.
func TestR_REPO_052_TheByteBoundStopsThePassAndHoldsUntilItIsRaised(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":1,"maxBytes":1048576}`)
	g.update(nil, `{"concurrency":1,"maxBytes":250}`)
	g.names("a", "b", "c", "d")
	for _, n := range []string{"a", "b", "c", "d"} {
		g.plan(n, "1.0.0")
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "bound_reached")
	mustContain(t, "stored before the bound", g.host.stored(), g.paths("a", "b")...)
	mustContain(t, "pending names kept", g.store.names(rigRepoID), "c", "d")
	st := g.store.state(rigRepoID)
	mustEqual(t, "the bound in the state", st.BoundReached, BoundBytes)
	mustEqual(t, "bounded items", g.item(ResultBounded), 1)
	mustEqual(t, "passes counted", g.passes("full", OutcomeBoundReached), 1)
	evs := g.rec.of(audit.EventMirrorBoundReached)
	if len(evs) != 1 {
		t.Fatalf("bound_reached events: %d", len(evs))
	}
	var d map[string]any
	_ = json.Unmarshal(evs[0].Details, &d)
	mustEqual(t, "usedBytes", d["usedBytes"].(float64), 200)
	mustEqual(t, "limitBytes", d["limitBytes"].(float64), 250)

	// The counter has folded what the pass stored: the bound holds, and the next pass asks the upstream for nothing.
	g.store.usage = func(int64) Usage { return Usage{LogicalBytes: 300, FoldedAt: g.clock.Now()} }
	g.clock.advance(2 * time.Hour)
	calls, plans := g.lister.namesCalls, len(g.lister.planOrder)
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome while it holds", fmt.Sprint(res.get("outcome")), "bound_reached")
	if g.lister.namesCalls != calls || len(g.lister.planOrder) != plans || len(g.host.stored()) != 2 {
		t.Fatal("a pass under a bound that holds sent a request")
	}
	mustEqual(t, "no second event", len(g.rec.of(audit.EventMirrorBoundReached)), 1)

	// Raising maxBytes lets the next pass continue with the pending names; the bound is cleared when it completes.
	g.update(nil, `{"concurrency":1,"maxBytes":1073741824}`)
	g.clock.advance(2 * time.Hour)
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome after the bound was raised", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "the rest stored", g.host.stored(), g.paths("a", "b", "c", "d")...)
	mustEqual(t, "the bound is cleared", g.store.state(rigRepoID).BoundReached, "")
}

// R-REPO-052: the bound counts the folded counter plus what the pass stored since its last fold, and an artifact without an
// announced size is fetched and counted afterwards.
func TestR_REPO_052_TheBoundAddsWhatThePassStoredSinceTheLastFold(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":1,"maxBytes":1048576}`)
	g.update(nil, `{"concurrency":1,"maxBytes":1000}`)
	g.names("a", "b", "c")
	g.lister.plans = map[string]discover.Plan{
		"a": {Name: "a", Items: []discover.Item{{Version: "1", Path: "a/1", Size: 0}}},
		"b": {Name: "b", Items: []discover.Item{{Version: "1", Path: "b/1", Size: 100}}},
		"c": {Name: "c", Items: []discover.Item{{Version: "1", Path: "c/1", Size: 100}}},
	}
	// The folded counter holds 700 bytes from before; the fold was a minute ago. a has no announced size and stores 250: the
	// pass counts it afterwards, so b (700 + 250 + 100 > 1000) is left by the bound.
	g.store.usage = func(int64) Usage { return Usage{LogicalBytes: 700, FoldedAt: g.clock.Now().Add(-time.Minute)} }
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) { return FillResult{Bytes: 250}, nil }
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "bound_reached")
	mustContain(t, "stored", g.host.stored(), "a/1")
	// A fold after the stored bytes counts them in the counter, and the pass stops counting them twice.
	g.store.usage = func(int64) Usage { return Usage{LogicalBytes: 950, FoldedAt: g.clock.Now().Add(time.Minute)} }
	b := newByteBound(g.store, g.clock, rigRepoID, 1000)
	b.add(250)
	if used, _ := b.used(t.Context()); used != 950 {
		t.Fatalf("used = %d, want the folded 950 without the 250 the fold has seen", used)
	}
}

// R-REPO-053 stops: a pause stops the pass before its next package and keeps it; resuming continues from the pending names;
// switching the mode back to pull-through clears it.
func TestR_REPO_053_APauseKeepsThePassAndAModeChangeClearsIt(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b", "c")
	for _, n := range []string{"a", "b", "c"} {
		g.plan(n, "1.0.0")
	}
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "b/") {
			g.update(nil, `{"concurrency":1,"paused":true}`)
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "stopped")
	mustContain(t, "stored before the pause", g.host.stored(), g.paths("a", "b")...)
	mustContain(t, "pending", g.store.names(rigRepoID), "c")
	if !g.store.state(rigRepoID).InProgress() {
		t.Fatal("a pause cleared the pass")
	}
	// A job that starts while the mirror is paused does nothing.
	if res, _ := g.run(); fmt.Sprint(res.get("skipped")) != "paused" {
		t.Fatalf("a job under a pause: %+v", res.v)
	}
	// Resume.
	g.host.fill = nil
	g.update(nil, `{"concurrency":1}`)
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "after the resume", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "stored once each", g.host.stored(), g.paths("a", "b", "c")...)
	// A mode change during a pass clears it.
	g.clock.advance(2 * time.Hour)
	g.lister.pages = nil
	g.names("d", "e")
	g.plan("d", "1.0.0")
	g.plan("e", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		g.repos.set(buildRepo(t, "npm-mirror", rigRepoID, []byte(`{"remote":{"url":"https://registry.example.org/","mode":"pullThrough"}}`)))
		return FillResult{Bytes: 100}, nil
	}
	res, err = g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "after the mode change", fmt.Sprint(res.get("outcome")), "stopped")
	st := g.store.state(rigRepoID)
	if st.InProgress() || len(g.store.names(rigRepoID)) != 0 {
		t.Fatalf("a mode change did not clear the pass: %+v, %v", st, g.store.names(rigRepoID))
	}
	mustEqual(t, "the last complete pass stays", string(st.FeedCursor), string(st.FeedCursor))
}

// R-REPO-053 stops: offline mode and the global offline switch end the pass with offline, without a request and without
// counting a failure.
func TestR_REPO_053_OfflineEndsThePassWithoutAFailure(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "1.0.0")
	off := true
	e, err := New(Options{
		Share: testShare, Store: g.store, Repos: g.repos, Host: g.host, Queue: g.queue, Audit: g.rec, Metrics: g.met, Holder: "holder-a",
		Clock: g.clock, Jitter: fixedJitter(0.5), GlobalOffline: func() bool { return off },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.runAs(e)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "offline")
	if g.lister.namesCalls != 0 || len(g.host.stored()) != 0 {
		t.Fatal("an offline pass sent a request")
	}
	st := g.store.state(rigRepoID)
	mustEqual(t, "failures", st.ConsecutiveFailures, 0)
	mustEqual(t, "recheck", st.NextPassAt, g.clock.Now().Add(OfflineRecheck))
	if !st.InProgress() {
		t.Fatal("offline cleared the pass")
	}
	off = false
	g.clock.advance(10 * time.Minute)
	res, err = g.runAs(e)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "online again", fmt.Sprint(res.get("outcome")), "complete")
}

// R-REPO-053 and Q-ARCH-287: every write of a pass is conditional on the fence; a write that matches no row stops the job at
// once without writing; the next job takes over and ends the pass from the pending names, and a late write under the old fence
// changes no row.
func TestR_REPO_053_AWriteUnderALostFenceStopsTheJobAndLeavesTheRowAlone(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b", "c")
	for _, n := range []string{"a", "b", "c"} {
		g.plan(n, "1.0.0")
	}
	other := Fence{RepoID: rigRepoID, JobID: "job-of-another-process", Holder: "holder-b"}
	var once sync.Once
	g.store.beforeWrite = func(op string) {
		if op == "done" {
			once.Do(func() {
				// The lease of this job expired and the next dispatch's job took the pass over.
				if _, err := g.store.Claim(t.Context(), Claim{Fence: other, KindIfNew: discover.PassFull, Now: g.clock.Now()}); err != nil {
					t.Error(err)
				}
			})
		}
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "reason", fmt.Sprint(res.get("reason")), "fence_lost")
	g.store.beforeWrite = nil
	writes := len(g.store.writes)
	st := g.store.state(rigRepoID)
	mustEqual(t, "the row names the other job", st.PassJobID, other.JobID)
	mustEqual(t, "nothing of the stale job was written after it lost the fence", len(g.store.writes), writes)
	if err := g.store.PackageDone(t.Context(), Fence{RepoID: rigRepoID, JobID: "00000000-0000-0000-0000-000000000001", Holder: "holder-a"}, PackageResult{Name: "b"}); !errors.Is(err, ErrFenceLost) {
		t.Fatalf("a late write under the old fence: %v", err)
	}
	mustContain(t, "pending after the late write", g.store.names(rigRepoID), "a", "b", "c")
	// The job that owns the pass ends it: another engine continues from the pending names.
	e2 := g.newEngine("holder-b")
	g.jobs += 10
	res, err = g.runAs(e2)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the next job", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "no name left", g.store.names(rigRepoID))
}

// R-REPO-053: a pass attempt that is released at shutdown writes nothing more and leaves the pass in progress with its pending
// names; another engine takes it over and ends it.
func TestR_REPO_053_AReleasedAttemptLeavesThePassForAnotherProcess(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b", "c")
	for _, n := range []string{"a", "b", "c"} {
		g.plan(n, "1.0.0")
	}
	rctx, release := newReleasable(t.Context())
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "b/") {
			release()
			return FillResult{}, rctx.Err()
		}
		return FillResult{Bytes: 100}, nil
	}
	g.jobs++
	res := &result{}
	err := g.eng.RunPass(rctx, "00000000-0000-0000-0000-000000000099", []byte(`{"repository":"it/npm-mirror","repositoryId":7}`), res)
	if err == nil {
		t.Fatal("a released attempt returned nil: the job would be completed")
	}
	st := g.store.state(rigRepoID)
	if !st.InProgress() {
		t.Fatal("the pass was cleared by a release")
	}
	mustContain(t, "pending after the release", g.store.names(rigRepoID), "b", "c")
	mustEqual(t, "no failure", st.ConsecutiveFailures, 0)
	g.host.fill = nil
	e2 := g.newEngine("holder-b")
	res2, err := g.runAs(e2)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the second engine", fmt.Sprint(res2.get("outcome")), "complete")
	mustEqual(t, "failed items", g.item(ResultFailed), 0)
}

// R-REPO-053: the dispatch ensures a row for every remote in mode mirror, enqueues a pass for each that is due and not paused
// under the dedup key of the repository, and two dispatches in one minute never yield two unfinished passes for one repository.
func TestR_REPO_053_TheDispatchEnqueuesTheDuePassesOnce(t *testing.T) {
	g := newRig(t, nil, "")
	g.repos.set(buildRepo(t, "npm-paused", 8, mirrorDoc(t, nil, `{"paused":true}`)))
	g.repos.set(buildRepo(t, "npm-pull", 9, []byte(`{"remote":{"url":"https://registry.example.org/"}}`)))
	e2 := g.newEngine("holder-b")
	for _, e := range []*Engine{g.eng, e2, g.eng} {
		if _, err := e.Dispatch(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	mustEqual(t, "enqueued", g.queue.count(), 1)
	if got := g.queue.specs[0]; got.Kind != KindPass || got.DedupKey != PassKey(rigRepoID) || got.TargetKey != "it/npm-mirror" {
		t.Fatalf("spec %+v", got)
	}
	var a PassArgs
	b, _ := json.Marshal(g.queue.specs[0].Args)
	_ = json.Unmarshal(b, &a)
	mustEqual(t, "args", a, PassArgs{Repository: qr("npm-mirror"), RepositoryID: rigRepoID})
	for _, id := range []int64{rigRepoID, 8} {
		if _, ok, _ := g.store.State(t.Context(), id); !ok {
			t.Errorf("no state row for %d", id)
		}
	}
	if _, ok, _ := g.store.State(t.Context(), 9); ok {
		t.Error("a state row for a repository in mode pullThrough")
	}
	// A request makes a row due before its time.
	g.queue.finish(PassKey(rigRepoID))
	g.store.mu.Lock()
	g.store.states[rigRepoID].NextPassAt = g.clock.Now().Add(time.Hour)
	g.store.mu.Unlock()
	if out, _ := g.eng.Dispatch(t.Context()); out.Enqueued != 0 {
		t.Fatal("a pass that is not due was enqueued")
	}
	g.store.mu.Lock()
	g.store.states[rigRepoID].PassRequestedAt = g.clock.Now()
	g.store.mu.Unlock()
	if out, _ := g.eng.Dispatch(t.Context()); out.Enqueued != 1 {
		t.Fatal("a requested pass was not enqueued")
	}
}

// R-REPO-053: a repository that left mode mirror loses its pass in progress and its pending names at the next dispatch and
// keeps the cursor of its last complete pass (Q-REPO-148); a format without discovery gets no pass.
func TestR_REPO_053_TheDispatchClearsThePassOfARepositoryThatLeftTheMode(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		g.update(nil, `{"concurrency":1,"paused":true}`)
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	g.store.mu.Lock()
	g.store.states[rigRepoID].FeedCursor = []byte(`{"seq":1}`)
	g.store.mu.Unlock()
	g.repos.set(buildRepo(t, "npm-mirror", rigRepoID, []byte(`{"remote":{"url":"https://registry.example.org/","mode":"pullThrough"}}`)))
	if _, err := g.eng.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := g.store.state(rigRepoID)
	if s.InProgress() || len(g.store.names(rigRepoID)) != 0 || string(s.FeedCursor) != `{"seq":1}` {
		t.Fatalf("state after the mode change: %+v, pending %v", s, g.store.names(rigRepoID))
	}
	g.update(nil, "")
	g.host.noLists = true
	g.queue = &memQueue{}
	g.eng.o.Queue = g.queue
	if out, _ := g.eng.Dispatch(t.Context()); out.Enqueued != 0 || g.queue.count() != 0 {
		t.Fatal("a pass was enqueued for a format without discovery")
	}
}

// R-REPO-055: a pass hands the host the background share B of the repository as its ceiling: min(concurrency, max(1,
// maxConcurrentFetches / 4)).
func TestR_REPO_055_ThePassCarriesTheBackgroundShareAsItsCeiling(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want int
	}{
		{`{"concurrency":8}`, 4},
		{`{"concurrency":2}`, 2},
		{`{}`, 4},
	} {
		g := newRig(t, nil, tc.doc)
		g.names("a")
		if _, err := g.run(); err != nil {
			t.Fatal(err)
		}
		if len(g.host.ceilings) != 1 || g.host.ceilings[0] != tc.want {
			t.Errorf("settings %s: ceilings %v, want %d", tc.doc, g.host.ceilings, tc.want)
		}
	}
	g := newRig(t, nil, `{"concurrency":8}`)
	g.repo = buildRepo(t, "npm-mirror", rigRepoID, []byte(`{"remote":{"url":"https://registry.example.org/","mode":"mirror","limits":{"maxConcurrentFetches":2},"mirror":{"concurrency":8}}}`))
	g.repos.set(g.repo)
	g.names("a")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "with maxConcurrentFetches 2", g.host.ceilings[0], 1)
}

// R-REPO-053, R-REPO-054: a pass with several workers stores every item once, never has more requests in flight than its
// ceiling, and ends complete.
func TestR_REPO_054_SeveralWorkersStoreEveryItemOnceWithinTheCeiling(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":4}`)
	var names []string
	for i := range 40 {
		n := fmt.Sprintf("p%02d", i)
		names = append(names, n)
		g.plan(n, "1.0.0", "1.1.0")
	}
	g.names(names...)
	var mu sync.Mutex
	inflight, peak := 0, 0
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustEqual(t, "stored", len(g.host.stored()), 80)
	seen := map[string]bool{}
	for _, p := range g.host.stored() {
		if seen[p] {
			t.Fatalf("%s stored twice", p)
		}
		seen[p] = true
	}
	if peak > 4 {
		t.Fatalf("%d requests in flight, the ceiling is 4", peak)
	}
	mustEqual(t, "the window gauge is cleared at the end", val(g.met.Concurrency.WithLabelValues("it/npm-mirror")), 0)
}

// R-REPO-057: an artifact that is stored already is counted as present and adds no bytes.
func TestR_REPO_057_APresentArtifactIsCountedAndAddsNoBytes(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "1.0.0", "1.1.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasSuffix(item.Path, "1.0.0.tgz") {
			return FillResult{Present: true}, nil
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "present", g.item(ResultPresent), 1)
	mustEqual(t, "stored", g.item(ResultStored), 1)
	mustEqual(t, "bytes", val(g.met.Bytes.WithLabelValues("it/npm-mirror")), 100)
}

// R-REPO-053: a repository that is gone, one that is no mirror and a format without discovery start no pass.
func TestR_REPO_053_ANoopJobStartsNoPass(t *testing.T) {
	g := newRig(t, nil, "")
	g.repos.remove(qr("npm-mirror"))
	res, err := g.run()
	if err != nil || fmt.Sprint(res.get("skipped")) != "repository_gone" {
		t.Fatalf("a deleted repository: %v, %+v", err, res.v)
	}
	g.repos.set(buildRepo(t, "npm-mirror", rigRepoID, []byte(`{"remote":{"url":"https://registry.example.org/"}}`)))
	res, err = g.run()
	if err != nil || fmt.Sprint(res.get("skipped")) != "not_mirror" {
		t.Fatalf("a pull-through repository: %v, %+v", err, res.v)
	}
	g.repos.set(buildRepo(t, "npm-mirror", 99, mirrorDoc(t, nil, "")))
	res, err = g.run()
	if err != nil || fmt.Sprint(res.get("skipped")) != "repository_gone" {
		t.Fatalf("a repository whose key was reused: %v, %+v", err, res.v)
	}
}

var _ repository.Reader = (*memRepos)(nil)

// R-REPO-060: a move changes the name and not the id, and the running job holds the repository by its id: a queued pass whose
// arguments carry the old name still runs, and a running pass is not halted by the move.
func TestR_REPO_060_APassSurvivesAMoveOfItsRepository(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	to := qname.Repo{Project: "other", Key: "npm-mirror"}
	moved := false
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if !moved {
			moved = true
			g.repos.move(rigRepoID, to, mirrorDoc(t, nil, `{"concurrency":1}`))
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "stored", g.item(ResultStored), 2)

	// A job queued before the move carries the old name; the pass starts on the repository of the id.
	res, err := g.run()
	if err != nil || fmt.Sprint(res.get("skipped")) == "repository_gone" {
		t.Fatalf("a queued pass after the move: %v, %+v", err, res.v)
	}
}

// R-REPO-057: the series of a repository exist from the first pass attempt on, at 0, so that increase() of the counters the
// alerts read sees the first failure; every family is labelled by repository and no package.
func TestR_REPO_057_TheSeriesExistAtZeroFromTheFirstAttempt(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	fams, err := g.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, f := range fams {
		got[f.GetName()] = len(f.GetMetric())
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() != "repo" && l.GetName() != "kind" && l.GetName() != "outcome" && l.GetName() != "result" && l.GetName() != "reason" {
					t.Errorf("%s has the label %s", f.GetName(), l.GetName())
				}
			}
		}
	}
	want := map[string]int{
		MetricPasses: 14, MetricItems: 6, MetricBytes: 1, MetricRateLimited: 4, MetricRateLimitWait: 1, MetricConcurrency: 1, MetricPreempted: 1,
	}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s has %d series, want %d", name, got[name], n)
		}
	}
	mustEqual(t, "failed passes", g.passes("full", OutcomeFailed), 0)
}

// R-REPO-053: a snapshot that lists no repository (a reader that has not loaded) clears no pass: only a remote repository that is
// in the snapshot and left the mode loses its pass.
func TestR_REPO_053_AnEmptySnapshotClearsNoPass(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		g.update(nil, `{"concurrency":1,"paused":true}`)
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	g.repos.remove(qr("npm-mirror"))
	if _, err := g.eng.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !g.store.state(rigRepoID).InProgress() || len(g.store.names(rigRepoID)) == 0 {
		t.Fatal("an empty snapshot cleared the pass in progress")
	}
}
