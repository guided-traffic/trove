//go:build integration

package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/internal/backup/postrestore"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// observingReplayer reports what the schema check of a replica says while the replay runs.
type observingReplayer struct {
	journal.Replayer
	e       *jenv
	during  error
	failing bool
}

func (o *observingReplayer) Replay(ctx context.Context, opts journal.ReplayOptions) (*journal.Summary, error) {
	o.during = o.e.db.CheckSchema(ctx, "0.1.0")
	if o.failing {
		return nil, errors.New("the object store is gone")
	}
	return o.Replayer.Replay(ctx, opts)
}

func (e *jenv) postRestorer(fixups []fixup.Fixup, rp *observingReplayer) *postrestore.PostRestorer {
	return postrestore.New(postrestore.Options{
		DB: e.db, Fixups: fixups, Recorder: e.rec, Margin: time.Minute, Log: quietLog,
		OpenReplayer: func(context.Context, string) (journal.Replayer, error) { return rp, nil },
	})
}

func countingFixups(n *int) []fixup.Fixup {
	mk := func(name string, order int) fixup.Fixup {
		return fixup.Fixup{Name: name, Order: order, Apply: func(context.Context, meta.Tx, fixup.Env) error { *n++; return nil }}
	}
	return []fixup.Fixup{mk("jobs", fixup.OrderJobs), mk("system_state", fixup.OrderSystemState), mk("revisions", fixup.OrderRevisions)}
}

// TestR_HA_025_ReplicasAreHeldUntilThePostRestoreHasReplayed: the schema check of a replica fails with ErrRestoreInProgress
// from the first transaction of post-restore to the last, so no pod serves the restored database's old access in between
// (audit finding H1); a failed replay leaves the marker, and the run is resumed without repeating the clean-up (L3).
func TestR_HA_025_ReplicasAreHeldUntilThePostRestoreHasReplayed(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	restorePoint := time.Now().Add(-time.Second)
	opts := backup.PostRestoreOptions{RestoredTo: restorePoint}
	var cleanups int
	fx := countingFixups(&cleanups)
	if err := e.db.CheckSchema(ctx, "0.1.0"); err != nil {
		t.Fatalf("setup: the schema check must pass before post-restore: %v", err)
	}

	failing := &observingReplayer{Replayer: e.replayer(e.store, e.rec), e: e, failing: true}
	if _, err := e.postRestorer(fx, failing).PostRestore(ctx, opts); err == nil {
		t.Fatal("a failing replay must fail the command")
	}
	if !errors.Is(failing.during, postgres.ErrRestoreInProgress) {
		t.Errorf("during the replay the schema check = %v, want ErrRestoreInProgress", failing.during)
	}
	if err := e.db.CheckSchema(ctx, "0.1.0"); !errors.Is(err, postgres.ErrRestoreInProgress) {
		t.Fatalf("after a failed run the marker must stay: %v", err)
	}
	if cleanups != 3 {
		t.Fatalf("the clean-up ran %d fix-ups, want 3", cleanups)
	}

	// Another restore point while this one is unfinished is refused.
	if _, err := e.postRestorer(fx, failing).PostRestore(ctx, backup.PostRestoreOptions{RestoredTo: restorePoint.Add(-time.Hour)}); err == nil ||
		backup.ExitCode(err) != backup.ExitPrecondition {
		t.Fatalf("another restore point = %v, want a precondition failure", err)
	}

	// The same restore point resumes at the replay: no second clean-up, then the marker goes.
	ok := &observingReplayer{Replayer: e.replayer(e.store, e.rec), e: e}
	rep, err := e.postRestorer(fx, ok).PostRestore(ctx, opts)
	if err != nil {
		t.Fatalf("resumed PostRestore: %v", err)
	}
	if cleanups != 3 || !strings.Contains(strings.Join(rep.Steps, "\n"), "resuming") {
		t.Errorf("the clean-up was repeated (%d fix-ups) or the resume not reported:\n%s", cleanups, strings.Join(rep.Steps, "\n"))
	}
	if !errors.Is(ok.during, postgres.ErrRestoreInProgress) {
		t.Errorf("during the resumed replay the schema check = %v", ok.during)
	}
	if err := e.db.CheckSchema(ctx, "0.1.0"); err != nil {
		t.Fatalf("after the replay and system.restored the replicas must be released: %v", err)
	}
	if n := e.count("SELECT count(*)::text FROM audit_event WHERE action = 'system.restored'"); n != "1" {
		t.Errorf("system.restored written %s times, want 1", n)
	}
}
