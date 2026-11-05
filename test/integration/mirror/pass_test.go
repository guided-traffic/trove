//go:build integration

package mirror

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-REPO-053, R-REPO-057: the dispatch task enqueues the pass of a mirrored remote, the pass runs as a job on the queue mirror,
// stores every package once, ends with its summary and clears the pass; the audit trail holds one mirror.pass_completed.
func TestR_REPO_053_APassRunsAsAJobAndEndsWithItsSummary(t *testing.T) {
	w := newWorld(5)
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-a", "all")
	e.dispatch(rep)
	e.waitOutcome("complete", 60*time.Second)

	if got := w.storedCount(); got != 5 {
		t.Fatalf("%d artifacts stored, want 5", got)
	}
	for _, n := range w.names {
		if w.times(pathOf(n)) != 1 {
			t.Errorf("%s stored %d times", n, w.times(pathOf(n)))
		}
	}
	if left := e.pending(); len(left) != 0 {
		t.Fatalf("pending names left: %v", left)
	}
	if got := e.text(`SELECT COALESCE(last_pass->>'packages', '') FROM mirror_state WHERE repo_id = $1`); got != "5" {
		t.Errorf("packages in the summary = %q", got)
	}
	if got := e.text(`SELECT COALESCE(last_pass->>'bytesStored', '') FROM mirror_state WHERE repo_id = $1`); got != "500" {
		t.Errorf("bytesStored in the summary = %q", got)
	}
	if got := e.text(`SELECT COALESCE(feed_cursor::text, '') || '|' || COALESCE(feed_method, '') FROM mirror_state WHERE repo_id = $1`); got != `{"seq": 1}|feed` {
		t.Errorf("the cursor of the last complete pass = %q", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_started_at IS NULL AND pass_job_id IS NULL AND pass_holder IS NULL AND last_full_pass_at IS NOT NULL AND consecutive_failures = 0`, e.repoN); n != 1 {
		t.Error("the pass was not cleared at its end")
	}
	if res := e.jobResult("succeeded"); res["outcome"] != "complete" {
		t.Errorf("job result %v", res)
	}
	if n := e.waitAudit("mirror.pass_completed", 1); n != 1 {
		t.Errorf("%d mirror.pass_completed events", n)
	}
	// The incremental pass that follows asks the feed after the cursor and stores nothing again.
	e.makeDue()
	e.dispatch(rep)
	testWaitJobs(t, e, 2)
	if got := e.text(`SELECT last_pass->>'kind' FROM mirror_state WHERE repo_id = $1`); got != "incremental" {
		t.Errorf("the second pass is %s", got)
	}
}

// testWaitJobs waits until n mirror-pass jobs have succeeded and none is unfinished.
func testWaitJobs(t *testing.T, e *env, n int64) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		done := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass' AND state = 'succeeded'`)
		if done >= n && e.activePasses() == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%d mirror-pass jobs did not succeed", n)
}

// R-REPO-053, R-REPO-058: two processes that dispatch in the same minute never leave two unfinished mirror-pass jobs for one
// repository; a process that serves clients and claims no job only enqueues and builds no engine.
func TestR_REPO_058_TwoDispatchersNeverYieldTwoUnfinishedPasses(t *testing.T) {
	w := newWorld(3)
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-web", "web")
	// The area is on every role (its gauges are exported by all of them, Q-OPS-171); the engine is built at the first job only, and a
	// replica that claims no job never has one.
	hasArea := false
	for _, a := range rep.App.Areas() {
		hasArea = hasArea || a == "mirror"
	}
	if !hasArea {
		t.Fatal("a web replica has no mirror area, so it exports none of the gauges")
	}
	engine := func() *mirror.Engine {
		eng, err := mirror.New(mirror.Options{
			Share: proxy.BackgroundShare, Store: e.store, Repos: rep.App.Repositories(), Host: w, Queue: rep.App.Queue(),
			Holder: e.st.Text(`SELECT gen_random_uuid()::text`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	}
	engines := []*mirror.Engine{engine(), engine()}
	errs := make(chan error, 40)
	for i := range 40 {
		go func() {
			_, err := engines[i%2].Dispatch(context.Background())
			errs <- err
		}()
	}
	for range 40 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := e.activePasses(); n != 1 {
		t.Fatalf("%d unfinished mirror-pass jobs after 40 dispatches, want 1", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`); n != 1 {
		t.Fatalf("%d mirror-pass jobs in the table, want 1", n)
	}
	if got := e.st.Text(`SELECT dedup_key FROM job WHERE kind = 'mirror-pass'`); got != fmt.Sprintf("mirror-pass:%d", e.repoN) {
		t.Errorf("dedup key %q", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1`, e.repoN); n != 1 {
		t.Error("the dispatch did not ensure the state row")
	}
}

// R-REPO-053: a remote in mode pullThrough never has a mirror-pass job and no state row; the dispatch leaves it alone.
func TestR_REPO_050_APullThroughRemoteGetsNoPass(t *testing.T) {
	w := newWorld(3)
	e := newEnv(t, w, "pullThrough", nil, nil)
	rep := e.start("rep-a", "all")
	e.dispatch(rep)
	time.Sleep(2 * time.Second)
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`); n != 0 {
		t.Fatalf("%d mirror-pass jobs for a pull-through remote", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state`); n != 0 {
		t.Fatalf("%d state rows for a pull-through remote", n)
	}
	if w.namesCalls.Load() != 0 {
		t.Fatal("the upstream was asked for names")
	}
}

// R-REPO-053, Q-ARCH-287: the claim makes a job the owner of the pass, a second claim takes it over with the counters and the
// pending names, and every write of the first job under its old fence changes no row.
func TestR_REPO_053_AWriteUnderAnOldFenceChangesNoRow(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	ctx := context.Background()
	if err := e.store.EnsureStates(ctx, []int64{e.repoN}); err != nil {
		t.Fatal(err)
	}
	a := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: e.st.Text(`SELECT gen_random_uuid()::text`)}
	b := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: e.st.Text(`SELECT gen_random_uuid()::text`)}
	now := time.Now()
	e.st.Exec(`UPDATE mirror_state SET pass_requested_at = now(), pass_requested_full = true WHERE repo_id = $1`, e.repoN)
	if ok, err := e.store.Claim(ctx, mirror.Claim{Fence: a, KindIfNew: discover.PassIncremental, Now: now}); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	st, _, _ := e.store.State(ctx, e.repoN)
	if st.PassKind != discover.PassFull || !st.PassRequestedAt.IsZero() || st.PassRequestedFull || st.PassJobID != a.JobID || st.PassHolder != a.Holder {
		t.Fatalf("the claim of a requested pass: %+v", st)
	}
	if err := e.store.SetMethod(ctx, a, discover.MethodFeed, discover.PassFull); err != nil {
		t.Fatal(err)
	}
	added, err := e.store.AddNames(ctx, a, mirror.Names{Names: []string{"a", "b", "c"}, SetProgress: true, Cursor: []byte(`{"i":1}`)})
	if err != nil || added != 3 {
		t.Fatalf("add: %d %v", added, err)
	}
	if added, _ = e.store.AddNames(ctx, a, mirror.Names{Names: []string{"b", "c", "d"}}); added != 1 {
		t.Fatalf("a name already pending is kept once: %d new", added)
	}
	// The job of another process takes the pass over: the counters, the cursor and the pending names stay.
	if ok, err := e.store.Claim(ctx, mirror.Claim{Fence: b, KindIfNew: discover.PassIncremental, Now: now}); err != nil || !ok {
		t.Fatalf("takeover: %v %v", ok, err)
	}
	st, _, _ = e.store.State(ctx, e.repoN)
	if st.PassJobID != b.JobID || st.PassKind != discover.PassFull || st.PassMethod != discover.MethodFeed || st.PassNames != 4 || string(st.PassCursor) != `{"i": 1}` {
		t.Fatalf("takeover state: %+v", st)
	}
	before := e.snapshot()
	late := []struct {
		what string
		do   func() error
	}{
		{"PackageDone", func() error { return e.store.PackageDone(ctx, a, mirror.PackageResult{Name: "a", Items: 1, Bytes: 5}) }},
		{"PackageFailed", func() error {
			_, err := e.store.PackageFailed(ctx, a, mirror.PackageResult{Name: "b", Now: now})
			return err
		}},
		{"PackageKept", func() error { return e.store.PackageKept(ctx, a, mirror.PackageResult{Name: "c", Items: 1}) }},
		{"AddNames", func() error {
			_, err := e.store.AddNames(ctx, a, mirror.Names{Names: []string{"z"}, SetProgress: true, Done: true})
			return err
		}},
		{"SetMethod", func() error { return e.store.SetMethod(ctx, a, discover.MethodNames, discover.PassFull) }},
		{"RecordRateLimit", func() error { return e.store.RecordRateLimit(ctx, a, now, time.Minute, 429) }},
		{"Defer", func() error { return e.store.Defer(ctx, a, now.Add(time.Hour)) }},
		{"RecordFailure", func() error { _, err := e.store.RecordFailure(ctx, a, now, "x", "y", 1); return err }},
		{"EndPass", func() error {
			_, err := e.store.EndPass(ctx, a, mirror.End{Outcome: "complete", LastPass: []byte(`{}`), Now: now, NextPassAt: now})
			return err
		}},
	}
	for _, l := range late {
		if err := l.do(); !errors.Is(err, mirror.ErrFenceLost) {
			t.Errorf("%s under the old fence: %v", l.what, err)
		}
	}
	if after := e.snapshot(); after != before {
		t.Fatalf("a write under the old fence changed the rows:\n before %s\n after  %s", before, after)
	}
	// The owner's writes go through.
	if err := e.store.PackageDone(ctx, b, mirror.PackageResult{Name: "a", Items: 1, Bytes: 5}); err != nil {
		t.Fatal(err)
	}
	if got := e.pending(); len(got) != 3 {
		t.Fatalf("pending %v", got)
	}
	// A package that fails keeps its row with a retry a minute later per attempt and is left after three attempts.
	for i := 1; i <= mirror.MaxPackageAttempts; i++ {
		n, err := e.store.PackageFailed(ctx, b, mirror.PackageResult{Name: "b", Now: now, Error: "500"})
		if err != nil || n != i {
			t.Fatalf("attempt %d: %d %v", i, n, err)
		}
		wake, _ := e.store.NextWake(ctx, e.repoN, now)
		if i < mirror.MaxPackageAttempts && !wake.Equal(now.Add(time.Duration(i)*time.Minute).Truncate(time.Microsecond)) {
			t.Fatalf("attempt %d: retry at %v, now %v", i, wake, now)
		}
	}
	if st, _, _ = e.store.State(ctx, e.repoN); st.PassPackagesFailed != 1 {
		t.Fatalf("packages failed: %d", st.PassPackagesFailed)
	}
	rows, _ := e.store.NextPending(ctx, e.repoN, now.Add(time.Hour), "", 10)
	if len(rows) != 2 || rows[0].Name != "c" || rows[1].Name != "d" {
		t.Fatalf("pending names a pass takes: %+v", rows)
	}
	// The end clears the pass; the next pass gives the left package its attempts back.
	if _, err := e.store.EndPass(ctx, b, mirror.End{Outcome: "partial", LastPass: []byte(`{"outcome":"partial"}`), Now: now, NextPassAt: now}); err != nil {
		t.Fatal(err)
	}
	c := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: b.Holder}
	if _, err := e.store.Claim(ctx, mirror.Claim{Fence: c, KindIfNew: discover.PassFull, Now: now}); err != nil {
		t.Fatal(err)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_pending WHERE repo_id = $1 AND attempts = 0 AND not_before IS NULL`, e.repoN); n != 3 {
		t.Fatalf("%d pending rows with their attempts back, want 3", n)
	}
	if st, _, _ = e.store.State(ctx, e.repoN); st.PassNames != 0 || st.PassPackagesFailed != 0 || st.PassCursor != nil {
		t.Fatalf("a new pass starts with empty counters: %+v", st)
	}
}

// snapshot is the state row and the pending rows as one string, to compare before and after.
func (e *env) snapshot() string {
	return e.st.Text(`SELECT COALESCE((SELECT row_to_json(s)::text FROM (SELECT * FROM mirror_state WHERE repo_id = $1) s), '') || '|' ||
	        COALESCE((SELECT json_agg(p ORDER BY name)::text FROM mirror_pending p WHERE repo_id = $1), '')`, e.repoN)
}

// R-REPO-053: the stub fails every discovery request: the failure count grows to three and next_pass_at lies 60 s, 120 s and
// 240 s (+-20 %) after each failure; after the upstream recovers the next pass completes and the count is 0.
func TestR_REPO_053_AFailingUpstreamBacksOffAndARecoveryResetsTheCount(t *testing.T) {
	w := newWorld(3)
	fail := errors.New("the upstream answered 500 for https://registry.example.org/-/all")
	w.discoveryErr.Store(&fail)
	e := newEnv(t, w, "mirror", nil, nil)
	rep := e.start("rep-a", "all")
	for i, base := range []float64{60, 120, 240} {
		if i > 0 {
			e.makeDue()
		}
		e.dispatch(rep)
		want := int64(i + 1)
		testenv.WaitFor(t, fmt.Sprintf("failure %d", want), 60*time.Second, func() bool {
			got, err := e.st.TryNumber(`SELECT consecutive_failures FROM mirror_state WHERE repo_id = $1`, e.repoN)
			return err == nil && got == want && e.activePasses() == 0
		})
		var gap float64
		e.st.Query(`SELECT extract(epoch FROM next_pass_at - last_error_at)::float8 FROM mirror_state WHERE repo_id = $1`, func(r pgx.Rows) error { return r.Scan(&gap) }, e.repoN)
		if gap < base*0.8-0.01 || gap > base*1.2+0.01 {
			t.Errorf("failure %d: next pass %.1f s after the failure, want %.0f s +-20%%", want, gap, base)
		}
		if got := e.text(`SELECT COALESCE(last_error_code, '') || '|' || COALESCE(last_error, '') FROM mirror_state WHERE repo_id = $1`); !contains(got, "discovery_failed|") || contains(got, "/-/all") {
			t.Errorf("the recorded error %q names no code or shows a URL", got)
		}
		if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_started_at IS NOT NULL`, e.repoN); n != 1 {
			t.Error("a failure cleared the pass in progress")
		}
	}
	if n := e.waitAudit("mirror.pass_failed", 3); n != 3 {
		t.Errorf("%d mirror.pass_failed events, want 3", n)
	}
	w.discoveryErr.Store(nil)
	e.makeDue()
	e.dispatch(rep)
	e.waitOutcome("complete", 60*time.Second)
	if n := e.st.Number(`SELECT consecutive_failures FROM mirror_state WHERE repo_id = $1`, e.repoN); n != 0 {
		t.Errorf("failures after the recovery: %d", n)
	}
	if w.storedCount() != 3 {
		t.Errorf("%d artifacts stored", w.storedCount())
	}
}

// R-REPO-052: with a bound of 1,000 bytes and a counter of 900 the pass stores one artifact of 100 bytes and ends bound_reached
// keeping its pending names; mirror.bound_reached is written once; a later pass that finds the counter at the bound ends at once
// without an upstream request; after maxBytes is raised the next pass stores the rest.
func TestR_REPO_052_TheByteBoundEndsThePassOnceAndHoldsUntilItIsRaised(t *testing.T) {
	w := newWorld(5)
	e := newEnv(t, w, "mirror", nil, settings{"maxBytes": 1048576})
	e.st.Exec(`INSERT INTO repository_usage (repo_id, logical_bytes) VALUES ($1, 900)`, e.repoN)
	rep := e.start("rep-a", "all")
	e.setDocument(rep, remoteDoc("mirror", nil, settings{"maxBytes": 1000}))
	e.waitMaxBytes(rep, 1000)
	e.dispatch(rep)
	e.waitOutcome("bound_reached", 60*time.Second)
	if w.storedCount() != 1 {
		t.Fatalf("%d artifacts stored, want 1", w.storedCount())
	}
	if got := e.pending(); len(got) != 4 {
		t.Fatalf("pending names kept: %v", got)
	}
	if got := e.text(`SELECT bound_reached FROM mirror_state WHERE repo_id = $1`); got != "bytes" {
		t.Fatalf("bound_reached = %q", got)
	}
	if n := e.waitAudit("mirror.bound_reached", 1); n != 1 {
		t.Fatalf("%d mirror.bound_reached events", n)
	}
	// The fold has counted what the pass stored: the bound holds.
	e.st.Exec(`UPDATE repository_usage SET logical_bytes = 1000, updated_at = now() WHERE repo_id = $1`, e.repoN)
	calls := w.namesCalls.Load()
	plans := w.planCalls.Load()
	e.makeDue()
	e.dispatch(rep)
	testWaitJobs(t, e, 2)
	if w.namesCalls.Load() != calls || w.planCalls.Load() != plans || w.storedCount() != 1 {
		t.Fatal("a pass under a bound that holds sent a request")
	}
	if got := e.jobResult("succeeded")["outcome"]; got != "bound_reached" {
		t.Fatalf("job result outcome %v", got)
	}
	if n := e.waitAudit("mirror.bound_reached", 1); n != 1 {
		t.Fatalf("%d mirror.bound_reached events after the second pass, want one per transition", n)
	}
	// Raise the bound: the next pass continues with the pending names and clears the bound.
	e.setDocument(rep, remoteDoc("mirror", nil, settings{"maxBytes": 10485760}))
	e.waitMaxBytes(rep, 10485760)
	e.makeDue()
	e.dispatch(rep)
	e.waitOutcome("complete", 60*time.Second)
	if w.storedCount() != 5 {
		t.Fatalf("%d artifacts stored after the bound was raised", w.storedCount())
	}
	if got := e.text(`SELECT COALESCE(bound_reached, '') FROM mirror_state WHERE repo_id = $1`); got != "" {
		t.Fatalf("the bound was not cleared: %q", got)
	}
}

// R-REPO-052 with R-STOR-003: nothing writes the counter but the fold. The fake host stores real file items, the triggers append
// the deltas and the fold of the replica counts them, so a second and a third pass, each a new attempt with an empty tally of its
// own, start from the folded counter: the bound holds across passes and the raised bound lets the next pass store the rest.
func TestR_REPO_052_TheByteBoundHoldsAcrossPassesWhileTheFoldCountsWhatWasStored(t *testing.T) {
	w := newWorld(5)
	e := newEnv(t, w, "mirror", nil, settings{"maxBytes": 250})
	e.persistFiles()
	rep := e.start("rep-a", "all", "TROVE_STORAGE_USAGE_FOLD_INTERVAL=1s")
	counter := func() int64 {
		return e.st.Number(`SELECT COALESCE((SELECT logical_bytes FROM repository_usage WHERE repo_id = $1), 0)`, e.repoN)
	}
	if counter() != 0 {
		t.Fatal("the counter starts above zero")
	}
	e.dispatch(rep)
	e.waitOutcome("bound_reached", 60*time.Second)
	if w.storedCount() != 2 {
		t.Fatalf("pass 1 stored %d artifacts, want 2 (200 of 250 bytes, the third would cross the bound)", w.storedCount())
	}
	testenv.WaitFor(t, "the fold to count the 200 stored bytes", 30*time.Second, func() bool { return counter() == 200 })
	if n := e.st.Number(`SELECT count(*) FROM storage_usage_deltas WHERE repo_id = $1`, e.repoN); n != 0 {
		t.Fatalf("%d deltas of the repository stay after the fold", n)
	}

	// Pass 2 is a new attempt: without the fold it would start from zero and store two more.
	e.makeDue()
	e.dispatch(rep)
	testWaitJobs(t, e, 2)
	if w.storedCount() != 2 {
		t.Fatalf("pass 2 stored %d artifacts in total, want 2: the bound holds across passes", w.storedCount())
	}
	if got := e.jobResult("succeeded")["outcome"]; got != "bound_reached" {
		t.Fatalf("pass 2 outcome %v, want bound_reached", got)
	}

	// Raise the bound: pass 3 stores the rest, and the fold counts it.
	e.setDocument(rep, remoteDoc("mirror", nil, settings{"maxBytes": 10485760}))
	e.waitMaxBytes(rep, 10485760)
	e.makeDue()
	e.dispatch(rep)
	e.waitOutcome("complete", 60*time.Second)
	if w.storedCount() != 5 {
		t.Fatalf("%d artifacts stored after the bound was raised, want 5", w.storedCount())
	}
	testenv.WaitFor(t, "the fold to count 500 bytes", 30*time.Second, func() bool { return counter() == 500 })
}

// R-REPO-058: a restore clears the pass in progress, the request, the rate-limit wait and the bound and makes the next pass due
// at the restore time; without remote caches it also forgets the cursor, the full pass and the fingerprint.
func TestR_REPO_058_ARestoreClearsThePassAndKeepsOrForgetsTheCursor(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	seed := func() {
		e.st.Exec(`DELETE FROM mirror_state`)
		e.st.Exec(`INSERT INTO mirror_state (repo_id, next_pass_at, pass_requested_at, pass_requested_full, pass_started_at, pass_kind, pass_method,
		        pass_job_id, pass_holder, pass_cursor, pass_names, rate_limited_until, bound_reached, bound_reached_at,
		        feed_cursor, feed_method, patterns_fingerprint, last_full_pass_at, last_pass, consecutive_failures)
		    VALUES ($1, now() + interval '1 hour', now(), true, now(), 'full', 'feed', gen_random_uuid(), gen_random_uuid(), '{"i":1}', 7,
		        now() + interval '1 hour', 'bytes', now(), '{"seq":9}', 'feed', '\x01', now(), '{"outcome":"complete"}', 2)`, e.repoN)
		e.st.Exec(`INSERT INTO mirror_pending (repo_id, name) VALUES ($1, 'a')`, e.repoN)
	}
	ctx := context.Background()
	restore := time.Now()
	seed()
	if _, err := e.store.ClearAfterRestore(ctx, restore, false); err != nil {
		t.Fatal(err)
	}
	st, ok, _ := e.store.State(ctx, e.repoN)
	if !ok || st.InProgress() || st.PassJobID != "" || !st.PassRequestedAt.IsZero() || st.PassRequestedFull || !st.RateLimitedUntil.IsZero() || st.BoundReached != "" ||
		st.PassNames != 0 || st.PassCursor != nil {
		t.Fatalf("after a restore: %+v", st)
	}
	if d := st.NextPassAt.Sub(restore); d < -time.Second || d > time.Second {
		t.Errorf("next_pass_at %v, restore at %v", st.NextPassAt, restore)
	}
	if string(st.FeedCursor) != `{"seq": 9}` || st.FeedMethod != discover.MethodFeed || len(st.PatternsFingerprint) == 0 || st.LastFullPassAt.IsZero() || st.ConsecutiveFailures != 2 {
		t.Errorf("a restore keeps the state of the last complete pass: %+v", st)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_pending`); n != 0 {
		t.Errorf("%d pending rows survived the restore", n)
	}
	seed()
	if _, err := e.store.ClearAfterRestore(ctx, restore, true); err != nil {
		t.Fatal(err)
	}
	st, _, _ = e.store.State(ctx, e.repoN)
	if len(st.FeedCursor) != 0 || st.FeedMethod != "" || len(st.PatternsFingerprint) != 0 || !st.LastFullPassAt.IsZero() {
		t.Errorf("a restore without remote caches keeps the cursor: %+v", st)
	}
}

// R-REPO-057: the gauges read from PostgreSQL carry the pending names, the end of the last complete pass and the configured
// interval of the mirrored repository.
func TestR_REPO_057_TheGaugesReadFromPostgreSQL(t *testing.T) {
	w := newWorld(4)
	e := newEnv(t, w, "mirror", nil, settings{"intervalSeconds": 1800})
	rep := e.start("rep-a", "all")
	e.dispatch(rep)
	e.waitOutcome("complete", 60*time.Second)
	e.st.Exec(`INSERT INTO mirror_pending (repo_id, name) VALUES ($1, 'left-1'), ($1, 'left-2')`, e.repoN)
	e.st.Exec(`UPDATE mirror_state SET pass_started_at = now(), pass_kind = 'full' WHERE repo_id = $1`, e.repoN)
	src := mirror.GaugeSource(e.store.Gauges, rep.App.Repositories())
	ms, err := src.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var pending, last, interval float64
	for _, m := range ms {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		if len(d.Label) != 1 || d.Label[0].GetName() != "repo" || d.Label[0].GetValue() != repoKey {
			t.Errorf("labels %v", d.Label)
		}
		switch desc := m.Desc().String(); {
		case contains(desc, mirror.MetricPending):
			pending = d.Gauge.GetValue()
		case contains(desc, mirror.MetricLastComplete):
			last = d.Gauge.GetValue()
		case contains(desc, mirror.MetricInterval):
			interval = d.Gauge.GetValue()
		}
	}
	if pending != 2 || interval != 1800 {
		t.Errorf("pending %v, interval %v", pending, interval)
	}
	if d := float64(time.Now().UnixMicro())/1e6 - last; d < -1 || d > 120 {
		t.Errorf("the last complete pass ended %v s ago", d)
	}
}
