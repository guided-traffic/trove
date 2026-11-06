package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// The tests of the fix wave over the mirror engine (docs/developer/mirror.md, Q-REPO-184 to Q-REPO-192): each fails on the
// engine as it was merged.

// blockingClock is a clock whose Sleep takes the time it is asked for: it returns only when its context ends. A worker that
// sleeps a wait of hours on it holds the job until the job's context ends.
type blockingClock struct{ *fakeClock }

func (c blockingClock) Sleep(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// withTimeout runs f and fails the test when it does not return in time.
func withTimeout(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return in 10 s", what)
	}
}

// R-REPO-057, R-REPO-053 step 3: an artifact the host refuses is counted refused and the next item goes on; a transient failure of
// one item does not keep the others from being stored, and the package fails once, after them.
func TestR_REPO_057_ARefusedItemIsCountedAndTheOthersGoOn(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "3.0.0", "2.0.0", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.Contains(item.Path, "2.0.0") {
			return FillResult{}, &RefusedError{Reason: RefusedBlocked}
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "stored around the refused one", g.host.stored(), "a/-/3.0.0.tgz", "a/-/1.0.0.tgz")
	mustEqual(t, "refused items", g.item(ResultRefused), 1)
	mustEqual(t, "failed items", g.item(ResultFailed), 0)
	mustEqual(t, "packagesFailed", g.lastPass()["packagesFailed"].(float64), 0)
	mustContain(t, "nothing pending", g.store.names(rigRepoID))
}

func TestR_REPO_053_AFailingItemFinishesTheOthersAndTheFailureIsOneAttempt(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "3.0.0", "2.0.0", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.Contains(item.Path, "3.0.0") {
			return FillResult{}, errors.New("the upstream answered 500")
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	// The first item fails in every attempt, and the two after it are stored in the first one.
	mustContain(t, "the items after the failing one", g.host.stored(), "a/-/2.0.0.tgz", "a/-/1.0.0.tgz")
	mustEqual(t, "failed items, one per attempt", g.item(ResultFailed), float64(MaxPackageAttempts))
	mustEqual(t, "packagesFailed", g.lastPass()["packagesFailed"].(float64), 1)
}

// R-REPO-053 (drain): a request the process's shutdown cancelled stops the pass as if its context had ended: nothing is written,
// no item and no package counts as failed, and the job does not complete.
func TestR_REPO_053_AShutdownErrorWritesNothingAndFailsNoItem(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		return FillResult{}, fmt.Errorf("fill: %w", ErrShuttingDown)
	}
	_, err := g.run()
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("err = %v, want the shutdown: the job is not completed", err)
	}
	for _, w := range g.store.writes {
		if w == "failed" || w == "kept" || w == "end" || w == "failure" || w == "done" {
			t.Fatalf("a write %q after the shutdown; writes %v", w, g.store.writes)
		}
	}
	mustEqual(t, "failed items", g.item(ResultFailed), 0)
	if !g.store.state(rigRepoID).InProgress() {
		t.Fatal("the pass was cleared")
	}
}

// Q-ARCH-312: a host that is not ready fails the dispatch and the pass with its cause instead of skipping the repositories.
func TestR_REPO_053_AHostThatIsNotReadyFailsTheDispatchAndThePass(t *testing.T) {
	g := newRig(t, nil, "")
	g.host.readyErr = errors.New("the remote engine is not built in this process")
	if _, err := g.eng.Dispatch(t.Context()); err == nil || !strings.Contains(err.Error(), "not built") {
		t.Fatalf("dispatch err = %v", err)
	}
	if _, err := g.run(); err == nil || !strings.Contains(err.Error(), "not built") {
		t.Fatalf("pass err = %v", err)
	}
	if g.queue.count() != 0 {
		t.Fatal("a pass was enqueued")
	}
	if _, ok, _ := g.store.State(t.Context(), rigRepoID); ok {
		t.Fatal("a state row was created for a host that cannot serve")
	}
}

// R-REPO-055: a client that pre-empts a discovery request (the methods, a page, the document of a deleted package) makes the pass
// wait and send it again; it is no failure of the pass.
func TestR_REPO_055_APreEmptedDiscoveryRequestIsRetriedAndNeverFailsThePass(t *testing.T) {
	g := newRig(t, nil, "")
	g.lister.pages = []discover.Page{{Names: []string{"a"}, Removed: []string{"gone"}, Done: true}}
	g.plan("a", "1.0.0")
	preempt := fmt.Errorf("get: %w", ErrPreempted)
	g.lister.methodsFn = func(call int) error {
		if call == 1 {
			return preempt
		}
		return nil
	}
	g.lister.namesErr = func(call int) error {
		if call == 1 {
			return preempt
		}
		return nil
	}
	g.lister.planErr = func(name string, call int) error {
		if name == "gone" && call == 1 {
			return preempt
		}
		return nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustEqual(t, "failures", g.store.state(rigRepoID).ConsecutiveFailures, 0)
	mustEqual(t, "pre-emptions counted", val(g.met.Preempted.WithLabelValues("npm-mirror")), 3)
	if got := g.clock.slept(); len(got) != 3 || got[0] != PreemptRetry {
		t.Fatalf("waits %v, want three of %v", got, PreemptRetry)
	}
	mustContain(t, "stored", g.host.stored(), g.paths("a")...)
}

// R-REPO-054: a wait over 300 s ends the attempt although several workers are blocked behind the wait: none of them sleeps it, and
// the job does not hold its slot through it.
func TestR_REPO_054_ALongWaitReleasesEveryWorkerAtOnce(t *testing.T) {
	g := newRig(t, nil, `{"concurrency":4}`)
	var names []string
	for i := range 12 {
		n := fmt.Sprintf("p%02d", i)
		names = append(names, n)
		g.plan(n, "1.0.0")
	}
	g.names(names...)
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		return FillResult{}, &rateLimitErr{status: 429, after: time.Hour, hinted: true}
	}
	// The writes of the wait take a moment, as they do against PostgreSQL: the workers behind the wait wake in that time.
	g.store.beforeWrite = func(op string) {
		if op == "ratelimit" || op == "defer" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	e, err := New(Options{
		Share: testShare, Store: g.store, Repos: g.repos, Host: g.host, Queue: g.queue, Audit: g.rec, Metrics: g.met, Holder: "holder-a",
		Clock: blockingClock{g.clock}, Jitter: fixedJitter(0.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	var res *result
	withTimeout(t, "the pass", func() {
		res, err = g.runAs(e)
	})
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "result", fmt.Sprint(res.get("deferred")), "rate_limited")
	st := g.store.state(rigRepoID)
	if !st.InProgress() || st.RateLimitedUntil.IsZero() {
		t.Fatalf("the deferral is not in the state: %+v", st)
	}
}

// R-REPO-054: a job that starts while the wait of an earlier attempt lies ahead sends nothing: it is skipped, the claim leaves the
// wait alone, and the dispatch continues the pass when it is over.
func TestR_REPO_054_AJobDuringADeferralIsSkippedAndTheClaimKeepsTheWait(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		return FillResult{}, &rateLimitErr{status: 429, after: time.Hour, hinted: true}
	}
	if res, err := g.run(); err != nil || fmt.Sprint(res.get("deferred")) != "rate_limited" {
		t.Fatalf("first run: %v %+v", err, res.v)
	}
	until := g.store.state(rigRepoID).RateLimitedUntil
	calls := g.lister.namesCalls
	plans := len(g.lister.planOrder)
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "skipped", fmt.Sprint(res.get("skipped")), "rate_limited")
	if g.lister.namesCalls != calls || len(g.lister.planOrder) != plans || len(g.host.stored()) != 0 {
		t.Fatal("a job during the deferral sent a request")
	}
	mustEqual(t, "the wait is kept", g.store.state(rigRepoID).RateLimitedUntil, until)
	// The claim of a job that does run leaves a wait alone too (it is the end's to clear).
	g.clock.advance(2 * time.Hour)
	g.host.fill = nil
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	if !g.store.state(rigRepoID).RateLimitedUntil.IsZero() {
		t.Fatal("the end did not clear the wait")
	}
}

// R-REPO-054: a wait of 300 s or less is written too, so that a job released in the middle of it does not resume at once in
// another process.
func TestR_REPO_054_AShortWaitIsPersistedWhileItLastsAndTheEndClearsIt(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a")
	g.plan("a", "1.0.0")
	start := g.clock.Now()
	var seen time.Time
	g.host.fill = func(item discover.Item, call int) (FillResult, error) {
		if call == 1 {
			return FillResult{}, &rateLimitErr{status: 429, after: 7 * time.Second, hinted: true}
		}
		seen = g.store.state(rigRepoID).RateLimitedUntil
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the wait in the state during the pass", seen, start.Add(7*time.Second))
	if !g.store.state(rigRepoID).RateLimitedUntil.IsZero() {
		t.Fatal("the end left the wait")
	}
}

// R-REPO-056: a request for a full pass that arrives while a pass is in progress survives the takeover of that pass and makes the
// pass after it full.
func TestR_REPO_056_AFullRequestSurvivesTheTakeoverOfAPassInProgress(t *testing.T) {
	g := newRig(t, nil, "")
	g.lister.pages = []discover.Page{{Names: []string{"a", "b"}, Done: true, Cursor: []byte(`{"seq":1}`)}}
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "a/") {
			g.update(nil, `{"concurrency":1,"paused":true}`)
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	// The pass is in progress (paused); a request for a full pass arrives and the pass is resumed.
	g.store.mu.Lock()
	g.store.states[rigRepoID].PassRequestedAt, g.store.states[rigRepoID].PassRequestedFull = g.clock.Now(), true
	g.store.mu.Unlock()
	g.host.fill = nil
	g.update(nil, `{"concurrency":1}`)
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	st := g.store.state(rigRepoID)
	if st.PassRequestedAt.IsZero() || !st.PassRequestedFull {
		t.Fatalf("the takeover consumed the request: %+v", st)
	}
	ids, _ := g.store.Due(t.Context(), []int64{rigRepoID}, g.clock.Now())
	if len(ids) != 1 {
		t.Fatal("the request does not make the row due")
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "the pass after it", fmt.Sprint(res.get("kind")), "full")
	if g.store.state(rigRepoID).PassRequestedAt != (time.Time{}) {
		t.Fatal("the fresh pass did not consume the request")
	}
}

// R-REPO-051: a pending name that the current patterns refuse is dropped when a pass is claimed and is never asked for, not even
// its document; a change of the document during the pass applies to the packages after it.
func TestR_REPO_051_APendingNameIsReAdmittedAndARefusedOneIsNeverAskedFor(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b", "c")
	for _, n := range []string{"a", "b", "c"} {
		g.plan(n, "1.0.0")
	}
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "a/") {
			// The patterns change while the pass runs, and the pass is stopped by a pause at the next package.
			g.host.admit = func(n string) bool { return n != "c" }
			g.update(nil, `{"concurrency":1,"paused":true}`)
		}
		return FillResult{Bytes: 100}, nil
	}
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "pending after the pause", g.store.names(rigRepoID), "b", "c")
	// Resume: c is refused now; the claim drops it before anything is asked.
	g.host.fill = nil
	g.update(nil, `{"concurrency":1}`)
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "stored", g.host.stored(), g.paths("a", "b")...)
	if g.lister.planCalls["c"] != 0 {
		t.Fatalf("the plan of a refused name was asked %d times", g.lister.planCalls["c"])
	}
	mustContain(t, "pending", g.store.names(rigRepoID))
}

func TestR_REPO_051_ANewDocumentDuringThePassRebuildsTheAdmissionBeforeTheNextPackage(t *testing.T) {
	g := newRig(t, nil, "")
	g.names("a", "b")
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) {
		if strings.HasPrefix(item.Path, "a/") {
			g.host.admit = func(n string) bool { return n != "b" }
			g.update(nil, `{"concurrency":1}`) // a new config version, nothing else changes
		}
		return FillResult{Bytes: 100}, nil
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustContain(t, "stored", g.host.stored(), g.paths("a")...)
	if g.lister.planCalls["b"] != 0 {
		t.Fatal("the document of a name the new patterns refuse was asked for")
	}
	mustContain(t, "pending", g.store.names(rigRepoID))
}

func TestR_REPO_051_ARemovedNameThePatternsRefuseIsNotRefreshed(t *testing.T) {
	g := newRig(t, nil, "")
	g.lister.pages = []discover.Page{{Names: []string{"a"}, Removed: []string{"gone"}, Done: true}}
	g.host.admit = func(n string) bool { return n != "gone" }
	g.plan("a", "1.0.0")
	if _, err := g.run(); err != nil {
		t.Fatal(err)
	}
	if g.lister.planCalls["gone"] != 0 {
		t.Fatal("the document of a refused removed name was asked for")
	}
}

// R-REPO-053 step 2: the bound of 10,000 pending names counts every row, the ones left after three attempts too; a pass whose
// table is full of them does not ask for the next page and ends partial with the reason.
func TestR_REPO_053_TheBoundCountsTheRowsThatFailedAndEndsThePassPartial(t *testing.T) {
	g := newRig(t, nil, "")
	var names []string
	for i := range MaxPending {
		n := fmt.Sprintf("p%05d", i)
		names = append(names, n)
	}
	g.lister.pages = []discover.Page{{Names: names}, {Names: []string{"last"}, Done: true}}
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) { return FillResult{}, errors.New("500") }
	g.lister.plans = map[string]discover.Plan{}
	for _, n := range names {
		g.lister.plans[n] = discover.Plan{Name: n, Items: []discover.Item{{Version: "1", Path: n + "/1"}}}
	}
	res, err := g.run()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "partial")
	mustEqual(t, "pages asked", g.lister.namesCalls, 1)
	sum := g.lastPass()
	mustEqual(t, "discoveryComplete", sum["discoveryComplete"].(bool), false)
	if !strings.Contains(fmt.Sprint(sum["discoveryNote"]), "bound of 10,000") {
		t.Fatalf("discoveryNote %v", sum["discoveryNote"])
	}
	mustEqual(t, "rows kept", len(g.store.names(rigRepoID)), MaxPending)
	if len(g.store.state(rigRepoID).FeedCursor) != 0 {
		t.Fatal("the cursor advanced although discovery did not end")
	}
}

// R-REPO-051: the end of the phase that adds the held names is recorded as its own cursor, so that a repository with E exact
// include names and H held names whose sum reaches 10,000 still gets to the format's pages.
func TestR_REPO_051_TheEndOfTheHeldNamesPhaseIsRecordedAndTheFormatsPagesFollow(t *testing.T) {
	g := newRig(t, []string{"exact-one"}, "")
	for i := range MaxPending - 1 {
		g.store.held[rigRepoID] = append(g.store.held[rigRepoID], fmt.Sprintf("h%05d", i))
	}
	g.host.fill = func(item discover.Item, _ int) (FillResult, error) { return FillResult{Present: true}, nil }
	g.lister.pages = []discover.Page{{Names: []string{"format-name"}, Done: true}}
	g.lister.plans = map[string]discover.Plan{}
	var res *result
	var err error
	withTimeout(t, "the pass", func() { res, err = g.run() })
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "outcome", fmt.Sprint(res.get("outcome")), "complete")
	mustEqual(t, "the format's page was asked", g.lister.namesCalls, 1)
	if g.lister.planCalls["format-name"] != 1 {
		t.Fatal("the name of the format's page was not worked off")
	}
}

// Q-REPO-186: an engine cursor is told from a format's by its structure, not by a substring.
func TestR_REPO_051_AnEngineCursorIsRecognizedByItsStructure(t *testing.T) {
	cases := []struct {
		in             string
		after          string
		done, isEngine bool
	}{
		{`{"mirrorHeldAfter":"abc"}`, "abc", false, true},
		{`{"mirrorHeldDone":true}`, "", true, true},
		{`{"q":"mirrorHeldAfter"}`, "", false, false},
		{`{"mirrorHeldAfter":"a","other":1}`, "", false, false},
		{`{"mirrorHeldDone":false}`, "", false, false},
		{`["mirrorHeldAfter"]`, "", false, false},
		{``, "", false, false},
	}
	for _, c := range cases {
		after, done, ok := parseEngineCursor(json.RawMessage(c.in))
		if after != c.after || done != c.done || ok != c.isEngine {
			t.Errorf("%q: %q %v %v", c.in, after, done, ok)
		}
	}
}

// R-REPO-053 stops: offline switched on while discovery runs ends the attempt before the next page, without a failure.
func TestR_REPO_053_OfflineDuringDiscoveryStopsBeforeTheNextPage(t *testing.T) {
	g := newRig(t, nil, "")
	g.lister.pages = []discover.Page{{Names: []string{"a"}}, {Names: []string{"b"}, Done: true}}
	g.plan("a", "1.0.0")
	g.plan("b", "1.0.0")
	var off bool
	g.lister.namesErr = func(call int) error {
		off = true
		return nil
	}
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
	mustEqual(t, "pages asked", g.lister.namesCalls, 1)
	mustEqual(t, "failures", g.store.state(rigRepoID).ConsecutiveFailures, 0)
}

// Q-REPO-191: a remote whose stored document fails to load is a pause: the dispatch keeps its pass and its pending names.
func TestR_REPO_053_ADocumentThatDoesNotLoadIsAPauseNotAModeChange(t *testing.T) {
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
	broken, _ := repository.Build(rigRepoID, rigVersion.Add(1), qr("npm-mirror"), repository.Remote, "npm", []byte(`{"remote":"not an object"}`), nil)
	if _, ok := broken.Remote(); ok {
		t.Fatal("the test document loads")
	}
	g.repos.set(broken)
	if _, err := g.eng.Dispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !g.store.state(rigRepoID).InProgress() || len(g.store.names(rigRepoID)) == 0 {
		t.Fatal("a document that does not load cleared the pass")
	}
	// A job that starts for it does nothing.
	if res, err := g.run(); err != nil || fmt.Sprint(res.get("skipped")) != "invalid_document" {
		t.Fatalf("%v %+v", err, res.v)
	}
}

// R-REPO-057 (Q-OPS-171): the gauges read from PostgreSQL carry the failures in a row and when the mirror next acts for a mirror
// that is in service, and none for one that its operator paused; the pending names and the last complete pass are exported for
// both; a repository that is not a mirror has no series.
func TestR_REPO_057_TheGaugesCarryFailuresAndTheNextPassOfAMirrorInService(t *testing.T) {
	repos := &memRepos{repos: map[string]repository.Repository{}}
	mk := func(key string, id int64, doc string) repository.Repository {
		r, err := repository.Build(id, 1, qr(key), repository.Remote, "npm", []byte(doc), nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	repos.set(mk("live", 1, `{"remote":{"url":"https://a.example/","mode":"mirror","mirror":{"intervalSeconds":600}}}`))
	repos.set(mk("paused", 2, `{"remote":{"url":"https://a.example/","mode":"mirror","mirror":{"paused":true}}}`))
	repos.set(mk("pull", 3, `{"remote":{"url":"https://a.example/"}}`))
	now := time.Unix(1_800_000_000, 0).UTC()
	rows := []GaugeRow{
		{Key: "live", Pending: 4, LastComplete: now.Add(-time.Hour), ConsecutiveFailures: 3, NextPass: now},
		{Key: "paused", Pending: 1, ConsecutiveFailures: 5, NextPass: now.Add(-48 * time.Hour)},
		{Key: "pull", Pending: 9},
	}
	src := GaugeSource(func(context.Context) ([]GaugeRow, error) { return rows, nil }, repos)
	ms, err := src.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, m := range ms {
		desc := m.Desc().String()
		v := val(m)
		for _, name := range []string{MetricConsecutiveFailures, MetricNextPass, MetricPending, MetricLastComplete, MetricInterval} {
			if strings.Contains(desc, `"`+name+`"`) {
				lbl := metricRepo(t, m)
				got[name+"/"+lbl] = v
			}
		}
	}
	want := map[string]float64{
		MetricConsecutiveFailures + "/live": 3, MetricNextPass + "/live": float64(now.Unix()),
		MetricPending + "/live": 4, MetricPending + "/paused": 1,
		MetricLastComplete + "/live": float64(now.Add(-time.Hour).Unix()),
		MetricInterval + "/live":     600, MetricInterval + "/paused": 3600,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected series %s", k)
		}
	}
}

// Q-ARCH-322: the status of a rate limit comes from the error's own HTTPStatus.
func TestR_REPO_054_TheStatusOfARateLimitComesFromTheErrorsHTTPStatus(t *testing.T) {
	mustEqual(t, "no status", rateLimitStatus(&rateLimitErr{}), 0)
	mustEqual(t, "with a status", rateLimitStatus(&statusRateLimitErr{rateLimitErr{status: 429}}), 429)
}

type statusRateLimitErr struct{ rateLimitErr }

func (e *statusRateLimitErr) HTTPStatus() int { return e.status }

// metricRepo returns the value of the label repo of a metric.
func metricRepo(t *testing.T, m prometheus.Metric) string {
	t.Helper()
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		t.Fatal(err)
	}
	for _, l := range d.Label {
		if l.GetName() == "repo" {
			return l.GetValue()
		}
	}
	return ""
}
