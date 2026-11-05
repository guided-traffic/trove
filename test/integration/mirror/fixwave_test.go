//go:build integration

package mirror

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// claim starts a pass for a fresh fence on the row of the repository.
func (e *env) claim(ctx context.Context, kind discover.PassKind) mirror.Fence {
	e.t.Helper()
	if err := e.store.EnsureStates(ctx, []int64{e.repoN}); err != nil {
		e.t.Fatal(err)
	}
	f := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: e.st.Text(`SELECT gen_random_uuid()::text`)}
	if ok, err := e.store.Claim(ctx, mirror.Claim{Fence: f, KindIfNew: kind, Now: time.Now()}); err != nil || !ok {
		e.t.Fatalf("claim: %v %v", ok, err)
	}
	return f
}

// R-REPO-053 (the data model of migration 00040): the state keeps the end of the last complete pass, and no index serves a column
// that nothing filters by.
func TestR_REPO_057_TheStateKeepsTheLastCompletePassAndHasNoUnusedIndex(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	if n := e.st.Number(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'mirror_state' AND column_name = 'last_complete_pass_at'`); n != 1 {
		t.Fatal("mirror_state has no last_complete_pass_at")
	}
	if n := e.st.Number(`SELECT count(*) FROM pg_indexes WHERE tablename = 'mirror_state' AND indexname = 'mirror_state_due'`); n != 0 {
		t.Fatal("the unused index mirror_state_due exists")
	}
}

// R-REPO-053: a cursor is measured as the column holds it, the text PostgreSQL prints for the jsonb value (a space after every
// comma): one that is compact below 4 KiB and longer when printed is cursor_too_large, not an internal error.
func TestR_REPO_053_ACursorIsMeasuredAsTheColumnHoldsIt(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	ctx := context.Background()
	f := e.claim(ctx, discover.PassFull)
	compact := "[" + strings.Repeat("1,", 1499) + "1]" // 3,000 bytes compact, about 4,500 as jsonb text
	if len(compact) > discover.MaxCursorBytes {
		t.Fatal("the test cursor is not compact-small")
	}
	_, err := e.store.AddNames(ctx, f, mirror.Names{Names: []string{"a"}, SetProgress: true, Cursor: []byte(compact)})
	if !errors.Is(err, mirror.ErrCursorTooLarge) {
		t.Fatalf("err = %v, want ErrCursorTooLarge", err)
	}
	if got := e.pending(); len(got) != 0 {
		t.Fatalf("a refused page inserted names: %v", got)
	}
	small := "[" + strings.Repeat("1,", 500) + "1]"
	if _, err := e.store.AddNames(ctx, f, mirror.Names{Names: []string{"a"}, SetProgress: true, Cursor: []byte(small)}); err != nil {
		t.Fatalf("a cursor that fits: %v", err)
	}
}

// R-REPO-056, R-REPO-054: a pass that is taken over leaves a request for a full pass and the upstream's wait alone, a new pass
// consumes the request, and the end clears the wait.
func TestR_REPO_056_ATakeoverKeepsTheRequestAndTheWaitAndANewPassConsumesTheRequest(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	ctx := context.Background()
	f := e.claim(ctx, discover.PassIncremental)
	// A request arrives while the pass is in progress, and the upstream's wait is stored.
	e.st.Exec(`UPDATE mirror_state SET pass_requested_at = now(), pass_requested_full = true WHERE repo_id = $1`, e.repoN)
	if err := e.store.RecordRateLimit(ctx, f, time.Now(), 90*time.Second, 429); err != nil {
		t.Fatal(err)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND rate_limited_until > now() + interval '60 seconds' AND rate_limited_until < now() + interval '120 seconds'`, e.repoN); n != 1 {
		t.Fatal("a wait of 90 s is not stored while it lasts")
	}
	g := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: f.Holder}
	if _, err := e.store.Claim(ctx, mirror.Claim{Fence: g, KindIfNew: discover.PassIncremental, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_requested_at IS NOT NULL AND pass_requested_full AND rate_limited_until IS NOT NULL`, e.repoN); n != 1 {
		t.Fatal("the takeover consumed the request or cleared the wait")
	}
	if _, err := e.store.EndPass(ctx, g, mirror.End{Outcome: "complete", LastPass: []byte(`{"outcome":"complete"}`), Now: time.Now(), NextPassAt: time.Now(), Completed: true}); err != nil {
		t.Fatal(err)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND rate_limited_until IS NULL AND last_complete_pass_at IS NOT NULL AND pass_requested_full`, e.repoN); n != 1 {
		t.Fatal("the end did not clear the wait, set the last complete pass or keep the request")
	}
	h := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: f.Holder}
	if _, err := e.store.Claim(ctx, mirror.Claim{Fence: h, KindIfNew: discover.PassIncremental, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	st, _, _ := e.store.State(ctx, e.repoN)
	if st.PassKind != discover.PassFull || !st.PassRequestedAt.IsZero() || st.PassRequestedFull {
		t.Fatalf("the new pass did not take the request: %+v", st)
	}
	// An end that is no complete pass does not move the last complete pass.
	before := e.text(`SELECT last_complete_pass_at::text FROM mirror_state WHERE repo_id = $1`)
	if _, err := e.store.EndPass(ctx, h, mirror.End{Outcome: "stopped", LastPass: []byte(`{"outcome":"stopped"}`), Now: time.Now().Add(time.Hour), NextPassAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if after := e.text(`SELECT last_complete_pass_at::text FROM mirror_state WHERE repo_id = $1`); after != before {
		t.Fatalf("a stopped end moved the last complete pass: %s to %s", before, after)
	}
}

// R-REPO-053 step 2, R-REPO-051: the bound of 10,000 counts every row, the names that failed three times included; the held names
// come in name order; refused names are dropped by name under the fence.
func TestR_REPO_053_ThePendingCountTakesEveryRowAndDropsRefusedNamesUnderTheFence(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	ctx := context.Background()
	f := e.claim(ctx, discover.PassFull)
	if _, err := e.store.AddNames(ctx, f, mirror.Names{Names: []string{"a", "b", "c", "d"}}); err != nil {
		t.Fatal(err)
	}
	for range mirror.MaxPackageAttempts {
		if _, err := e.store.PackageFailed(ctx, f, mirror.PackageResult{Name: "b", Now: time.Now(), Error: "500"}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := e.store.PendingCount(ctx, e.repoN); n != 4 {
		t.Fatalf("PendingCount = %d, want every row (4)", n)
	}
	if err := e.store.DropPending(ctx, f, []string{"a", "d", "nothing"}); err != nil {
		t.Fatal(err)
	}
	if got := e.pending(); strings.Join(got, ",") != "b,c" {
		t.Fatalf("pending after the drop: %v", got)
	}
	old := mirror.Fence{RepoID: e.repoN, JobID: e.st.Text(`SELECT gen_random_uuid()::text`), Holder: f.Holder}
	if err := e.store.DropPending(ctx, old, []string{"c"}); !errors.Is(err, mirror.ErrFenceLost) {
		t.Fatalf("a drop under another fence: %v", err)
	}
	if got := e.pending(); strings.Join(got, ",") != "b,c" {
		t.Fatalf("a drop under another fence changed rows: %v", got)
	}
	// The held names, in name order, as the index of packages gives them.
	e.st.Exec(`INSERT INTO packages (repo_id, name, display_name) VALUES ($1, 'Zed', 'Zed'), ($1, 'alpha', 'alpha'), ($1, 'beta', 'beta')`, e.repoN)
	var all []string
	after := ""
	for {
		names, err := e.store.HeldNames(ctx, e.repoN, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) == 0 {
			break
		}
		all = append(all, names...)
		after = names[len(names)-1]
	}
	if len(all) != 3 {
		t.Fatalf("held names %v, want all three exactly once", all)
	}
}

// R-REPO-057 (Q-OPS-171): the gauge rows carry the pending names of the pass in progress only (the names a pass still takes), the
// end of the last complete pass, the failures in a row and the time of the next pass; for a pass in progress that is the later of
// the due time and its last write.
func TestR_REPO_057_TheGaugeRowsCarryWhatTheAlertsRead(t *testing.T) {
	e := newEnv(t, newWorld(1), "mirror", nil, nil)
	ctx := context.Background()
	if err := e.store.EnsureStates(ctx, []int64{e.repoN}); err != nil {
		t.Fatal(err)
	}
	e.st.Exec(`UPDATE mirror_state SET next_pass_at = now() - interval '5 hours', consecutive_failures = 4,
	       last_complete_pass_at = now() - interval '2 hours' WHERE repo_id = $1`, e.repoN)
	e.st.Exec(`INSERT INTO mirror_pending (repo_id, name, attempts) VALUES ($1, 'a', 0), ($1, 'b', 3)`, e.repoN)
	rows, err := e.store.Gauges(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v, %v", rows, err)
	}
	g := rows[0]
	if g.Key != repoKey || g.Pending != 0 || g.ConsecutiveFailures != 4 {
		t.Fatalf("a mirror without a pass in progress: %+v", g)
	}
	if d := time.Since(g.NextPass); d < 4*time.Hour+50*time.Minute || d > 5*time.Hour+10*time.Minute {
		t.Fatalf("the next pass of an idle mirror is its due time (5 h ago): %v ago", d)
	}
	if d := time.Since(g.LastComplete); d < time.Hour+50*time.Minute || d > 2*time.Hour+10*time.Minute {
		t.Fatalf("last complete %v ago", d)
	}
	// A pass in progress: the pending names it takes (attempts below the limit), and the last write moves the time forward.
	e.claim(ctx, discover.PassFull)
	rows, _ = e.store.Gauges(ctx)
	g = rows[0]
	if g.Pending != 2 {
		t.Fatalf("pending of a pass in progress: %d (the claim gave the attempts back, so both rows count)", g.Pending)
	}
	if time.Since(g.NextPass) > time.Minute {
		t.Fatalf("a pass that was just written is not behind its schedule: %v ago", time.Since(g.NextPass))
	}
}
