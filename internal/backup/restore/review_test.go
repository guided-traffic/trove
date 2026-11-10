package restore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// watched runs fn under the stall watchdog the way Restore does and returns the cause that ended the context, nil if it
// stayed alive.
func watched(r *Restorer, fn func(ctx context.Context) error) (error, error) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	r.progress()
	go r.watch(ctx, cancel)
	err := fn(ctx)
	return err, context.Cause(ctx)
}

func stallRestorer(e *env, limit time.Duration) *Restorer {
	r := e.restorer()
	r.d.Config.StallTimeout = limit
	return r
}

func TestR_BAK_013_LongStatementsOfTheRestoreAreProgress(t *testing.T) {
	const limit = 100 * time.Millisecond
	// Each case is one statement or transaction that reads no file. It runs three times the stall timeout, which is inside
	// the bound of four times; the same statement that does not return is a hang and ends the restore as stalled.
	cases := []struct {
		name string
		set  func(e *env, d time.Duration)
		run  func(r *Restorer, e *env, ctx context.Context) error
	}{
		{"the fix-up transaction (step 6)", func(e *env, d time.Duration) { e.sess.fixDelay = d }, func(r *Restorer, e *env, ctx context.Context) error {
			return r.stepFixups(ctx, &state{id: e.id, m: e.m}, &postgres.RestoreMarker{Phase: postgres.PhaseLoaded})
		}},
		{"a backfilling forward migration (step 5)", func(e *env, d time.Duration) { e.sess.migrateDelay = d }, func(r *Restorer, _ *env, ctx context.Context) error {
			return r.stepForward(ctx)
		}},
		{"the insert from a stage table of the load (step 4)", func(e *env, d time.Duration) { e.sess.loadStatement = d }, func(r *Restorer, e *env, ctx context.Context) error {
			return r.stepLoad(ctx, &state{id: e.id, m: e.m}, &postgres.RestoreMarker{Phase: postgres.PhaseBlobs}, &backup.RestoreReport{})
		}},
	}
	for _, c := range cases {
		t.Run("R-BAK-013 "+c.name+" within the bound is not a stall", func(t *testing.T) {
			e := newEnv(t)
			c.set(e, 3*limit)
			r := stallRestorer(e, limit)
			err, cause := watched(r, func(ctx context.Context) error { return c.run(r, e, ctx) })
			if err != nil || cause != nil {
				t.Fatalf("err %v, cause %v: the statement was counted as a stall", err, cause)
			}
		})
		t.Run("R-BAK-013 "+c.name+" beyond the bound is still a stall", func(t *testing.T) {
			e := newEnv(t)
			c.set(e, time.Minute) // a real hang: the watchdog ends it, however late a loaded machine runs the watchdog
			r := stallRestorer(e, limit)
			err, cause := watched(r, func(ctx context.Context) error { return c.run(r, e, ctx) })
			if !errors.Is(cause, backup.ErrStalled) || err == nil {
				t.Fatalf("err %v, cause %v: a hung statement must end the restore", err, cause)
			}
		})
	}
}

func TestR_BAK_013_ObjectStoreErrorsMapToExitCodes(t *testing.T) {
	e := newEnv(t)
	r := e.restorer()
	ctx := context.Background()
	transient := &blob.StoreError{Op: "GetObject", Kind: blob.KindTransient, Err: errors.New("connection reset")}
	config := &blob.StoreError{Op: "PutObject", Code: "AccessDenied", Status: 403, Kind: blob.KindConfig}
	other := &blob.StoreError{Op: "PutObject", Status: 400, Kind: blob.KindOther}
	for name, c := range map[string]struct {
		err  error
		code int
	}{
		"an unreachable store":           {transient, backup.ExitTemporary},
		"an open circuit":                {blob.ErrCircuitOpen, backup.ExitTemporary},
		"a wrapped transient error":      {errors.Join(errors.New("copy x"), transient), backup.ExitTemporary},
		"a refused credential or bucket": {config, backup.ExitUsage},
		"another store error":            {other, backup.ExitFailed},
		"an error that is none of those": {errors.New("boom"), backup.ExitFailed},
	} {
		if got := backup.ExitCode(r.classify(ctx, c.err)); got != c.code {
			t.Errorf("%s: exit %d, want %d", name, got, c.code)
		}
	}
}

func TestR_BAK_021_AFinishedRestoreOfThisBackupIsNotRestoredAgain(t *testing.T) {
	finished := func(e *env) {
		e.sess.inspect.RelationCount = 60
		e.sess.inspect.Relations = []string{"items"}
		e.sess.inspect.RestoredFromBackup = string(e.id)
	}
	t.Run("R-BAK-021 killed after the commit of step 9: the next pod exits 0 and writes nothing", func(t *testing.T) {
		e := newEnv(t)
		finished(e)
		e.blobs.foreign = "trove/sha256/ab/cd/x" // the restored blobs: check 5 would refuse them
		rep, err := e.restorer().Restore(context.Background(), backup.RestoreOptions{ID: e.id})
		if err != nil || rep == nil || !rep.AlreadyRestored {
			t.Fatalf("report %+v, err %v", rep, err)
		}
		for _, c := range []string{"anchor", "migrate", "load", "fix", "finish"} {
			if e.sess.called(c) {
				t.Errorf("called %s", c)
			}
		}
	})
	t.Run("R-BAK-021 another backup's restore, a restore in progress and a plain database still refuse at check 4", func(t *testing.T) {
		for name, mod := range map[string]func(e *env){
			"another backup": func(e *env) { e.sess.inspect.RestoredFromBackup = "20250101T000000Z-aaaa" },
			"a marker": func(e *env) {
				e.sess.inspect.Marker = &postgres.RestoreMarker{BackupID: string(e.id), Phase: postgres.PhaseLoaded}
			},
			"an anchor of another id": func(e *env) { e.sess.inspect.Anchor = "20250101T000000Z-aaaa" },
			"no restored_from":        func(e *env) { e.sess.inspect.RestoredFromBackup = "" },
		} {
			e := newEnv(t)
			finished(e)
			mod(e)
			_, err := e.restorer().Restore(context.Background(), backup.RestoreOptions{ID: e.id})
			if backup.ExitCode(err) != backup.ExitPrecondition {
				t.Errorf("%s: err %v", name, err)
			}
		}
	})
}
