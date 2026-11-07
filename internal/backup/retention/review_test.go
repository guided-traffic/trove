package retention

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
)

// leaseFence models a lease that expires when the run lists more than budget keys without a tick: the fence before a
// delete request then fails as superseded, as the real fence does with a lease that no heartbeat renewed (R-BAK-010).
type leaseFence struct {
	clock, lastTick, budget int
}

func (f *leaseFence) Tick() { f.lastTick = f.clock }

func (f *leaseFence) Fence(context.Context, backup.State) error {
	if f.clock-f.lastTick > f.budget {
		return fmt.Errorf("%w: the lease ran out %d keys after the last progress", backup.ErrSuperseded, f.clock-f.lastTick)
	}
	f.lastTick = f.clock
	return nil
}

// R-BAK-013 names a listing page as progress: a sweep that lists thousands of kept keys before it meets the one orphan must
// renew the lease while it lists.
func TestR_BAK_013_SweepTicksWhileListingKeptKeys(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	var blobs []string
	for i := range 5000 {
		blobs = append(blobs, fmt.Sprintf("kept-%d", i))
	}
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: blobs})
	// The only deletable key sorts last, after every kept key.
	last := format.Digest{}
	for i := range last {
		last[i] = 0xff
	}
	e.mem.Set(e.l.Pool(last), []byte("orphan"))
	f := &leaseFence{budget: 1500}
	e.rec.onKey = func() { f.clock++ }
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), f, res, false); err != nil {
		t.Fatal(err)
	}
	if res.PoolObjectsDeleted != 1 || res.PoolSweptAt == nil {
		t.Fatalf("deleted %d, poolSweptAt %v", res.PoolObjectsDeleted, res.PoolSweptAt)
	}
}

// The scan of backups/ ticks too: a target with many backup directories is listed page by page.
func TestR_BAK_013_ScanTicksWhileListingBackupKeys(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	tables := map[string]string{}
	for i := range 2600 {
		tables[fmt.Sprintf("t%04d", i)] = "x"
	}
	e.put(t, fixture.Spec{ID: fixture.ID(1), Tables: tables})
	f := &leaseFence{budget: 1500}
	e.rec.onKey = func() { f.clock++ }
	ticks := 0
	counting := &countingFence{leaseFence: f, ticks: &ticks}
	if err := e.stage.prune(context.Background(), counting, &backup.Result{}, false); err != nil {
		t.Fatal(err)
	}
	if ticks < 2 {
		t.Fatalf("ticks = %d while listing 2,600 keys", ticks)
	}
}

type countingFence struct {
	*leaseFence
	ticks *int
}

func (c *countingFence) Tick() { *c.ticks++; c.leaseFence.Tick() }

// R-BAK-010: the fence margin assumes every deletion ends within finalize_timeout.
func TestR_BAK_010_EveryPruningDeleteHasTheFinalizeDeadline(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 1})
	e.stage.DeleteTimeout = 30 * time.Second
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}, Tables: map[string]string{"x": "1", "y": "2"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"b"}})
	e.mem.Set(e.l.Pool(fixture.Digest("orphan")), []byte("orphan"))
	e.put(t, fixture.Spec{ID: fixture.ID(3), Incomplete: true})
	e.put(t, fixture.Spec{ID: fixture.ID(4), Blobs: []string{"c"}})
	if err := e.stage.prune(context.Background(), &fakeFence{}, &backup.Result{}, false); err != nil {
		t.Fatal(err)
	}
	if len(e.rec.deadlines) < 4 {
		t.Fatalf("only %d delete requests: manifest, files, incomplete directory and pool expected", len(e.rec.deadlines))
	}
	for i, d := range e.rec.deadlines {
		if !d.set || d.left <= 0 || d.left > 30*time.Second {
			t.Fatalf("delete request %d: deadline set %v, %s left", i, d.set, d.left)
		}
	}
}

// R-BAK-013: a database or target that cannot be reached is temporary (75) for delete and prune too.
func TestR_BAK_013_PruneMapsTransientErrorsToExit75(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"connection refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, backup.ExitTemporary},
		{"request deadline", fmt.Errorf("deleting: %w", context.DeadlineExceeded), backup.ExitTemporary},
		{"plain failure", errors.New("boom"), backup.ExitFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRunEnv(t, Policy{KeepLast: 1})
			r.put(t, fixture.Spec{ID: fixture.ID(1)})
			r.put(t, fixture.Spec{ID: fixture.ID(2)})
			r.rec.onList = func() error { return c.err }
			_, err := r.runner.Pruner().Prune(context.Background())
			if got := backup.ExitCode(err); got != c.want {
				t.Fatalf("exit %d, want %d (%v)", got, c.want, err)
			}
			rows := r.runs.Runs()
			if len(rows) != 1 || rows[0].State != backup.StateFailed {
				t.Fatalf("rows = %+v", rows)
			}
		})
	}
}
