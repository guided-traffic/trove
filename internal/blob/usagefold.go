package blob

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// UsageFoldBatch is the number of delta rows one fold iteration takes (R-STOR-003).
const UsageFoldBatch = 10000

// UsageFolderOptions configure [NewUsageFolder].
type UsageFolderOptions struct {
	// DB runs one iteration in one transaction on the main pool.
	DB DB
	// Interval is storage.usage.fold_interval.
	Interval time.Duration
	// Loop is the loop's entry in the liveness watchdog (R-ARCH-016), registered as "blob.usage_fold" by the composition
	// root (this package may not import internal/cluster, R-BAS-001); nil watches nothing. It is beaten after every
	// iteration and while the loop waits, and stopped when the loop ends.
	Loop WatchedLoop
	// WatchdogTimeout is health.watchdog_timeout; the wait beats at a third of it. Zero beats once per interval.
	WatchdogTimeout time.Duration
	// CallTimeout is the deadline of one iteration, shorter than the watchdog bound (cluster.CallTimeout); zero is 10 s.
	CallTimeout time.Duration
	Log         *slog.Logger
}

// WatchedLoop is the part of a liveness entry the fold uses; *cluster.WatchedLoop implements it.
type WatchedLoop interface {
	Beat()
	Stop()
}

// UsageFolder is the fold loop of the usage counters (R-STOR-003): every worker replica runs one, with
// no lease and no lock; the folds of several replicas take disjoint delta rows. It holds no state
// between iterations, so a replica that is drained mid-fold rolls back and loses nothing.
type UsageFolder struct {
	o   UsageFolderOptions
	log *slog.Logger
	// iterate is one iteration and the number of delta rows it took; tests replace it.
	iterate func(ctx context.Context) (int, error)

	mu       sync.Mutex
	draining bool
	cancel   context.CancelFunc // of the loop; nil when not started
	iterStop context.CancelFunc // of the iteration in flight; nil between iterations
	done     chan struct{}
}

// NewUsageFolder creates the fold loop; Start runs it.
func NewUsageFolder(o UsageFolderOptions) (*UsageFolder, error) {
	if o.DB == nil {
		return nil, errors.New("blob: the usage folder needs a database")
	}
	if o.Interval <= 0 {
		return nil, errors.New("blob: the usage folder needs a positive interval")
	}
	f := &UsageFolder{o: o, log: o.Log}
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
	}
	f.iterate = f.Fold
	return f, nil
}

// Name is the component name.
func (f *UsageFolder) Name() string { return "usage-fold" }

// Start runs the loop in the background and returns.
func (f *UsageFolder) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		return errors.New("blob: the usage folder is started")
	}
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	f.cancel, f.done = cancel, make(chan struct{})
	go f.run(lctx, f.done)
	return nil
}

// Drain stops the loop from starting another iteration (R-ARCH-024 step 1); one in flight finishes.
func (f *UsageFolder) Drain(context.Context) {
	f.mu.Lock()
	f.draining = true
	f.mu.Unlock()
}

// Settle waits for the loop's iteration in flight to commit or fail, until ctx ends. The loop itself
// runs on until Stop; once drained it starts no iteration.
func (f *UsageFolder) Settle(ctx context.Context) error {
	for {
		f.mu.Lock()
		busy := f.iterStop != nil
		f.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Quiesce cancels the iteration in flight (R-ARCH-024 step 4): its transaction rolls back and another
// replica folds its rows.
func (f *UsageFolder) Quiesce(context.Context) {
	f.mu.Lock()
	stop := f.iterStop
	f.draining = true
	f.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// Stop ends the loop and waits for it, until ctx ends.
func (f *UsageFolder) Stop(ctx context.Context) error {
	f.mu.Lock()
	cancel, done := f.cancel, f.done
	f.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// run folds every interval, and at once again while the previous iteration took a full batch, so a
// backlog cannot grow with the write rate. A failed iteration is repeated at the next interval.
func (f *UsageFolder) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	loop := f.o.Loop
	if loop == nil {
		loop = noLoop{}
	}
	defer loop.Stop()
	for ctx.Err() == nil {
		for !f.isDraining() && ctx.Err() == nil {
			n, err := f.once(ctx)
			loop.Beat()
			if err != nil {
				if ctx.Err() == nil {
					f.log.Warn("usage fold failed; it is repeated at the next interval", "error", err)
				}
				break
			}
			if n < UsageFoldBatch {
				break
			}
		}
		f.wait(ctx, loop)
	}
}

// wait sleeps one interval and beats while it does, so an interval longer than the watchdog bound
// does not read as a stuck loop.
func (f *UsageFolder) wait(ctx context.Context, loop WatchedLoop) {
	step := f.o.Interval
	if wd := f.o.WatchdogTimeout; wd > 0 && wd/3 < step {
		step = wd / 3
	}
	for left := f.o.Interval; left > 0 && ctx.Err() == nil; left -= step {
		t := time.NewTimer(min(step, left))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		loop.Beat()
	}
}

func (f *UsageFolder) isDraining() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.draining
}

// once runs one iteration under a deadline shorter than the watchdog bound.
func (f *UsageFolder) once(ctx context.Context) (int, error) {
	timeout := f.o.CallTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	f.mu.Lock()
	f.iterStop = cancel
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.iterStop = nil
		f.mu.Unlock()
		cancel()
	}()
	return f.iterate(ictx)
}

type noLoop struct{}

func (noLoop) Beat() {}
func (noLoop) Stop() {}

type usageGroup struct {
	RepoID       int64 `json:"repo_id"`
	LogicalBytes int64 `json:"logical_bytes"`
	Files        int64 `json:"files"`
	Folders      int64 `json:"folders"`
}

// Fold runs one iteration (R-STOR-003) and returns the number of delta rows it folded; a caller that
// folds on demand repeats it while the number is UsageFoldBatch. It is one transaction that deletes up
// to UsageFoldBatch delta rows that no other fold holds, adds their sums per repository to
// repository_usage in repository order (dropping those of a deleted repository) and the physical sums
// to storage_usage_totals last.
func (f *UsageFolder) Fold(ctx context.Context) (int, error) {
	taken := 0
	err := f.o.DB.InTx(ctx, func(tx meta.Tx) error {
		taken = 0
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.FoldTakeUsageDeltas(ctx, UsageFoldBatch)
		if err != nil {
			return err
		}
		var groups []usageGroup
		var phys q.FoldAddPhysicalUsageParams
		physical := false
		for _, r := range rows {
			taken += int(r.Taken)
			if r.Physical {
				physical, phys = true, q.FoldAddPhysicalUsageParams{PhysicalBytes: r.PhysicalBytes, Blobs: r.Blobs}
				continue
			}
			groups = append(groups, usageGroup{RepoID: r.Repo, LogicalBytes: r.LogicalBytes, Files: r.Files, Folders: r.Folders})
		}
		if len(groups) > 0 {
			b, err := json.Marshal(groups)
			if err != nil {
				return err
			}
			if err := qs.FoldUpsertRepositoryUsage(ctx, string(b)); err != nil {
				return err
			}
		}
		if physical {
			return qs.FoldAddPhysicalUsage(ctx, phys)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return taken, nil
}
