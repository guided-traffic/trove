package blob

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// GCMode selects the candidates of a run (R-STOR-025).
type GCMode string

// The two collection tasks.
const (
	// GCIncremental takes the digests in blob_gc_queue older than the grace period.
	GCIncremental GCMode = "gc"
	// GCFull sweeps every live blob older than the grace period in keyset order.
	GCFull GCMode = "gc-full"
)

// Cursor is the keyset position of a full sweep: the last digest it finished.
// It is the job's checkpoint, so a run a lost replica returns to the queue
// continues where it stopped. The zero Cursor starts at the beginning.
type Cursor struct {
	After string `json:"after,omitempty"`
}

// GCResult is the result of a run (R-STOR-025).
type GCResult struct {
	DryRun         bool  `json:"dryRun"`
	TrashPurged    int   `json:"trashPurged"`
	Candidates     int   `json:"candidates"`
	Kept           int   `json:"kept"`
	Held           int   `json:"held"`
	Deleted        int   `json:"deleted"`
	BytesReclaimed int64 `json:"bytesReclaimed"`
	Errors         int   `json:"errors"`
	Paused         bool  `json:"paused,omitempty"`
}

// RunOptions are the parameters of one run.
type RunOptions struct {
	Mode GCMode
	// DryRun reports what would be deleted and changes nothing.
	DryRun bool
	// Cursor is where a full sweep continues.
	Cursor Cursor
	// Checkpoint receives the cursor after every batch of a full sweep.
	Checkpoint func(Cursor)
}

// Collector deletes blobs that no reference uses (R-STOR-024). It is safe to run
// on any number of workers at once: its correctness rests on the row locks of
// the per-blob protocol, and the exclusive job key only avoids duplicate work.
type Collector struct {
	db      DB
	objects ObjectStore
	prefix  string
	cfg     config.StorageGC
	log     *slog.Logger
	metrics *Metrics
	now     func() time.Time
	poll    time.Duration

	// Paused reports the deletion pause of R-HA-025. The default reads the
	// system_state key deletions_paused_at.
	Paused func(ctx context.Context) (bool, error)
	// Hold reports whether a running backup holds the digest (R-STOR-024 step
	// 2b). It runs inside the claim transaction, after the reference check. A
	// nil Hold holds nothing.
	Hold func(ctx context.Context, tx meta.Tx, d Digest) (bool, error)
	// Fault is a test hook called at named points of a run. A non-nil error ends
	// the run at once as if the process had been killed there: no hand-over, no
	// bookkeeping. It returns ErrFault wrapped.
	Fault func(point string) error
}

// CollectorOption configures a Collector.
type CollectorOption func(*Collector)

// WithCollectorLogger sets the logger.
func WithCollectorLogger(l *slog.Logger) CollectorOption { return func(c *Collector) { c.log = l } }

// WithCollectorMetrics sets the metrics.
func WithCollectorMetrics(m *Metrics) CollectorOption { return func(c *Collector) { c.metrics = m } }

// WithCollectorClock sets the clock whose monotonic reading bounds the delete
// of a claimed row; tests use it.
func WithCollectorClock(now func() time.Time) CollectorOption {
	return func(c *Collector) { c.now = now }
}

// WithCollectorPoll sets how often the end of a run looks for rows to settle.
func WithCollectorPoll(d time.Duration) CollectorOption { return func(c *Collector) { c.poll = d } }

// NewCollector builds a collector. prefix is storage.s3.prefix.
func NewCollector(cfg config.StorageGC, prefix string, objects ObjectStore, db DB, opts ...CollectorOption) *Collector {
	c := &Collector{db: db, objects: objects, prefix: prefix, cfg: cfg, log: slog.Default(), now: time.Now}
	for _, o := range opts {
		o(c)
	}
	if c.poll <= 0 {
		c.poll = max(10*time.Millisecond, min(250*time.Millisecond, cfg.DeleteSettle/10))
	}
	c.Paused = c.defaultPaused
	return c
}

func (c *Collector) defaultPaused(ctx context.Context) (bool, error) {
	var paused bool
	err := c.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.DeletionsPaused(ctx)
		paused = len(rows) > 0
		return err
	})
	return paused, err
}

type claimResult uint8

const (
	outKept claimResult = iota
	outGone
	outHeld
	outClaimed
)

type claim struct {
	start time.Time // clock reading before the claim transaction began
	size  int64
}

type candidate struct {
	digest Digest
	seen   *time.Time
}

// run is the state of one Run.
type run struct {
	c       *Collector
	opts    RunOptions
	res     GCResult
	claimed map[Digest]claim // claimed by this run, object not yet deleted
	pending []Digest         // object deleted, object_deleted_at not yet recorded
}

// Run performs one collection run (R-STOR-024, R-STOR-025): it finishes
// deleting rows, then processes candidates in batches, then waits up to
// storage.gc.delete_settle for the rows it deleted to become removable.
//
// Cancelling ctx is a graceful stop: the S3 calls are cancelled and awaited,
// the deletes that succeeded are recorded and the other claims are handed over
// so the next run finishes them after delete_settle instead of handover_delay.
// A returned error does not mean nothing happened; the result says what did.
func (c *Collector) Run(ctx context.Context, o RunOptions) (res GCResult, err error) {
	if o.Mode == "" {
		o.Mode = GCIncremental
	}
	r := &run{c: c, opts: o, claimed: map[Digest]claim{}}
	r.res.DryRun = o.DryRun
	defer func() {
		if !errors.Is(err, ErrFault) {
			r.finalize(ctx)
		}
		c.metrics.gc(r.res.Deleted, r.res.Kept, r.res.Held, r.res.BytesReclaimed)
		res = r.res
	}()

	if r.res.Paused, err = r.paused(ctx); err != nil || r.res.Paused {
		return r.res, err
	}
	if !o.DryRun {
		if err = r.finishRows(ctx); err != nil || r.res.Paused {
			return r.res, err
		}
	}
	if err = r.candidates(ctx); err != nil || r.res.Paused {
		return r.res, err
	}
	if !o.DryRun {
		err = r.settle(ctx)
	}
	return r.res, err
}

func (r *run) fault(point string) error {
	if r.c.Fault == nil {
		return nil
	}
	if err := r.c.Fault(point); err != nil {
		return fmt.Errorf("%w at %s: %w", ErrFault, point, err)
	}
	return nil
}

// paused reads the deletion pause before a batch of state changes.
func (r *run) paused(ctx context.Context) (bool, error) {
	p, err := r.c.Paused(ctx)
	if err != nil {
		return false, fmt.Errorf("read deletion pause: %w", err)
	}
	if p {
		r.c.log.Info("garbage collection paused: deletions are paused")
	}
	return p, nil
}

// candidates processes the candidate batches of the mode.
func (r *run) candidates(ctx context.Context) error {
	after := []byte{}
	if r.opts.Mode == GCFull && r.opts.Cursor.After != "" {
		b, err := hex.DecodeString(r.opts.Cursor.After)
		if err != nil {
			return fmt.Errorf("garbage collection cursor: %w", err)
		}
		after = b
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := r.nextBatch(ctx, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if !r.opts.DryRun {
			if p, err := r.paused(ctx); err != nil || p {
				r.res.Paused = p
				return err
			}
		}
		if err := r.process(ctx, batch); err != nil {
			return err
		}
		after = batch[len(batch)-1].digest.Bytes()
		if r.opts.Mode == GCFull && r.opts.Checkpoint != nil {
			r.opts.Checkpoint(Cursor{After: hex.EncodeToString(after)})
		}
	}
}

// nextBatch reads the next candidates after a digest.
func (r *run) nextBatch(ctx context.Context, after []byte) ([]candidate, error) {
	grace := r.c.cfg.GracePeriod.Seconds()
	limit := int32(min(r.c.cfg.BatchSize, math.MaxInt32)) //nolint:gosec // bounded by the min
	var out []candidate
	err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
		out = out[:0]
		if r.opts.Mode == GCFull {
			refs, err := postgres.LoadBlobRefs(ctx, tx)
			if err != nil {
				return err
			}
			cs, err := refs.FullSweep(ctx, tx, grace, after, limit)
			if err != nil {
				return err
			}
			for _, c := range cs {
				var d Digest
				copy(d[:], c.SHA256)
				out = append(out, candidate{digest: d, seen: c.EnqueuedAt})
			}
			return nil
		}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.BlobQueueCandidates(ctx, q.BlobQueueCandidatesParams{GraceSecs: grace, After: after, Batch: limit})
		if err != nil {
			return err
		}
		for _, row := range rows {
			var d Digest
			copy(d[:], row.Sha256)
			seen := row.EnqueuedAt
			out = append(out, candidate{digest: d, seen: &seen})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read gc candidates: %w", err)
	}
	return out, nil
}

// loadRefs reads the referencing columns of blobs from the catalog. A batch
// reads them once and uses them for all its candidates (R-STOR-024 rule 3).
func (r *run) loadRefs(ctx context.Context) (*postgres.BlobRefs, error) {
	var refs *postgres.BlobRefs
	err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
		var err error
		refs, err = postgres.LoadBlobRefs(ctx, tx)
		return err
	})
	return refs, err
}

// process handles one batch: dry-run accounting, or claim each candidate and
// delete the objects of the claimed ones.
func (r *run) process(ctx context.Context, batch []candidate) error {
	r.res.Candidates += len(batch)
	refs, err := r.loadRefs(ctx)
	if err != nil {
		return fmt.Errorf("read the tables that reference blobs: %w", err)
	}
	if r.opts.DryRun {
		return r.dryRun(ctx, batch, refs)
	}
	var claimed []Digest
	for _, cand := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, size, start, err := r.claim(ctx, cand, refs)
		if err != nil {
			return err
		}
		switch out {
		case outClaimed:
			r.claimed[cand.digest] = claim{start: start, size: size}
			claimed = append(claimed, cand.digest)
		case outHeld:
			r.res.Held++
		default:
			r.res.Kept++
		}
	}
	return r.deleteObjects(ctx, claimed)
}

func (r *run) dryRun(ctx context.Context, batch []candidate, refs *postgres.BlobRefs) error {
	for _, cand := range batch {
		var ref, live bool
		var size int64
		err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			rows, err := qs.BlobStateSize(ctx, cand.digest.Bytes())
			if err != nil {
				return err
			}
			live = len(rows) > 0 && rows[0].State == "live"
			if !live {
				return nil
			}
			size = rows[0].Size
			ref, err = refs.Referenced(ctx, tx, cand.digest.Bytes())
			return err
		})
		if err != nil {
			return err
		}
		if live && !ref {
			r.res.Deleted++
			r.res.BytesReclaimed += size
		} else {
			r.res.Kept++
		}
	}
	return nil
}

// claim is steps (1) to (3) for one candidate in one transaction.
func (r *run) claim(ctx context.Context, cand candidate, refs *postgres.BlobRefs) (out claimResult, size int64, start time.Time, err error) {
	if err := r.fault("claim:before"); err != nil {
		return 0, 0, time.Time{}, err
	}
	// The reading is no later than the transaction's start, which step (3)
	// stores as state_changed_at.
	start = r.c.now()
	err = r.c.db.InTx(ctx, func(tx meta.Tx) error {
		out = outKept
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.LockBlobUpdate(ctx, cand.digest.Bytes())
		if err != nil {
			return err
		}
		if err := r.fault("claim:locked"); err != nil {
			return err
		}
		dequeue := func() error {
			if cand.seen == nil {
				return nil // a full sweep found the digest in no queue
			}
			return qs.DequeueBlob(ctx, q.DequeueBlobParams{Sha256: cand.digest.Bytes(), EnqueuedAt: *cand.seen})
		}
		if len(rows) == 0 || rows[0].State != "live" {
			out = outGone
			return dequeue()
		}
		size = rows[0].Size
		ref, err := refs.Referenced(ctx, tx, cand.digest.Bytes())
		if err != nil {
			return err
		}
		if err := r.fault("claim:checked"); err != nil {
			return err
		}
		if ref {
			return dequeue()
		}
		if r.c.Hold != nil {
			held, err := r.c.Hold(ctx, tx, cand.digest)
			if err != nil {
				return err
			}
			if held {
				out = outHeld
				return nil
			}
		}
		if err := qs.ClaimBlob(ctx, cand.digest.Bytes()); err != nil {
			return err
		}
		out = outClaimed
		return dequeue()
	})
	if err != nil {
		return 0, 0, start, err
	}
	if err := r.fault("claim:committed"); err != nil {
		return 0, 0, start, err
	}
	return out, size, start, nil
}

// deleteObjects is steps (4) and (5) for the digests claimed in this batch.
// The hand-over rule: a row this run claimed is deleted only while less than
// handover_delay - delete_settle has passed on the run's own clock since the
// reading before its claim, and the call gets that instant as its deadline.
func (r *run) deleteObjects(ctx context.Context, digests []Digest) error {
	if len(digests) == 0 {
		return nil
	}
	earliest := r.claimed[digests[0]].start
	for _, d := range digests {
		if s := r.claimed[d].start; s.Before(earliest) {
			earliest = s
		}
	}
	deadline := earliest.Add(r.c.cfg.HandoverDelay - r.c.cfg.DeleteSettle)
	if !r.c.now().Before(deadline) {
		r.c.log.Warn("garbage collection claimed rows too long ago to delete them; a later run finishes them", "rows", len(digests))
		return nil
	}
	if p, err := r.paused(ctx); err != nil || p {
		r.res.Paused = p
		return err
	}
	if err := r.fault("delete:before"); err != nil {
		return err
	}
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	done, err := r.c.removeObjects(dctx, digests, &r.res)
	r.pending = append(r.pending, done...)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil // the hand-over deadline: the unfinished rows are handed over
		}
		return err
	}
	if err := r.fault("delete:after"); err != nil {
		return err
	}
	return r.record(ctx)
}

// record is step (5) for the deletes that succeeded.
func (r *run) record(ctx context.Context) error {
	if len(r.pending) == 0 {
		return nil
	}
	bytes, err := r.c.recordDeleted(ctx, r.pending)
	if err != nil {
		return err
	}
	r.res.Deleted += len(r.pending)
	r.res.BytesReclaimed += bytes
	for _, d := range r.pending {
		delete(r.claimed, d)
	}
	r.pending = r.pending[:0]
	return r.fault("record:after")
}

func (c *Collector) recordDeleted(ctx context.Context, digests []Digest) (int64, error) {
	var total int64
	err := c.db.InTx(ctx, func(tx meta.Tx) error {
		total = 0
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		sizes, err := qs.RecordBlobsDeleted(ctx, digestBytes(digests))
		if err != nil {
			return err
		}
		for _, size := range sizes {
			total += size
		}
		return nil
	})
	return total, err
}

func digestBytes(ds []Digest) [][]byte {
	out := make([][]byte, len(ds))
	for i := range ds {
		out[i] = ds[i].Bytes()
	}
	return out
}

// removeObjects sends the deletes of step (4) and returns the digests whose
// delete succeeded. A key that is already gone counts as success. Errors of
// single objects are counted in res.Errors; an error that stops the run (the
// context, an open circuit) is returned.
func (c *Collector) removeObjects(ctx context.Context, digests []Digest, res *GCResult) (done []Digest, err error) {
	if c.cfg.BatchDelete {
		for len(digests) > 0 {
			n := min(len(digests), 1000)
			chunk := digests[:n]
			digests = digests[n:]
			keys := make([]string, n)
			for i, d := range chunk {
				keys[i] = Key(c.prefix, d)
			}
			failed, derr := c.objects.DeleteMany(ctx, keys)
			if derr != nil {
				if stopsRun(ctx, derr) {
					return done, derr
				}
				res.Errors += n
				c.log.Warn("batch delete failed", "keys", n, "error", derr)
				continue
			}
			bad := map[string]bool{}
			for _, f := range failed {
				bad[f.Key] = true
			}
			res.Errors += len(failed)
			for i, d := range chunk {
				if !bad[keys[i]] {
					done = append(done, d)
				}
			}
		}
		return done, nil
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	sem := make(chan struct{}, max(1, c.cfg.DeleteConcurrency))
	for _, d := range digests {
		sem <- struct{}{}
		wg.Add(1)
		go func(d Digest) {
			defer func() { <-sem; wg.Done() }()
			derr := c.objects.Delete(ctx, Key(c.prefix, d))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case derr == nil || errors.Is(derr, ErrNotFound):
				done = append(done, d)
			case stopsRun(ctx, derr):
				if firstErr == nil {
					firstErr = derr
				}
			default:
				res.Errors++
				c.log.Warn("delete failed", "sha256", d.Hex(), "error", derr)
			}
		}(d)
	}
	wg.Wait()
	return done, firstErr
}

// stopsRun reports an error that ends the run instead of one candidate.
func stopsRun(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, ErrCircuitOpen) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// finishRows finishes the rows earlier runs left: step (6) for rows whose delete
// settled, steps (4) to (6) for rows nobody finished within handover_delay.
func (r *run) finishRows(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.finishEligible(ctx)
		if err != nil || n == 0 {
			return err
		}
	}
}

// finishEligible does one pass over eligible rows and returns how many it
// changed. A row whose delete settled is dropped (step 6). A row nobody finished
// within handover_delay is taken over with a compare-and-set claim, so that two
// runs that read the same row cannot both delete its object, and is then
// finished under the same hand-over deadline as a row this run claimed itself.
func (r *run) finishEligible(ctx context.Context) (int, error) {
	if p, err := r.paused(ctx); err != nil || p {
		r.res.Paused = p
		return 0, err
	}
	type row struct {
		d       Digest
		size    int64
		deleted bool
	}
	var rows []row
	err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
		rows = rows[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.EligibleBlobs(ctx, q.EligibleBlobsParams{
			HandoverSecs: r.c.cfg.HandoverDelay.Seconds(), SettleSecs: r.c.cfg.DeleteSettle.Seconds(),
			Batch: int32(min(r.c.cfg.BatchSize, math.MaxInt32)), //nolint:gosec // bounded by the min
		})
		if err != nil {
			return err
		}
		for _, x := range rs {
			e := row{size: x.Size, deleted: x.ObjectDeleted}
			copy(e.d[:], x.Sha256)
			rows = append(rows, e)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var toDelete, toDrop []Digest
	for _, x := range rows {
		if _, mine := r.claimed[x.d]; mine {
			continue
		}
		if x.deleted {
			toDrop = append(toDrop, x.d)
		} else {
			toDelete = append(toDelete, x.d)
		}
	}
	changed := 0
	if len(toDelete) > 0 {
		if err := r.fault("finish:delete"); err != nil {
			return changed, err
		}
		taken, err := r.takeOver(ctx, toDelete)
		if err != nil {
			return changed, err
		}
		if err := r.deleteObjects(ctx, taken); err != nil {
			return changed, err
		}
		changed += len(taken)
	}
	if len(toDrop) > 0 {
		if err := r.drop(ctx, toDrop); err != nil {
			return changed, err
		}
		changed += len(toDrop)
	}
	return changed, nil
}

// takeOver claims rows another run left, with a compare-and-set that renews
// their state_changed_at, and registers the ones this run won as its own claims.
// The clock reading before the transaction is the start of the hand-over rule,
// as for a claim.
func (r *run) takeOver(ctx context.Context, digests []Digest) ([]Digest, error) {
	start := r.c.now()
	var taken []Digest
	err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
		taken = taken[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.TakeOverBlobs(ctx, q.TakeOverBlobsParams{
			Digests: digestBytes(digests), HandoverSecs: r.c.cfg.HandoverDelay.Seconds(),
		})
		if err != nil {
			return err
		}
		for _, x := range rows {
			var d Digest
			copy(d[:], x.Sha256)
			taken = append(taken, d)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Sizes are only needed for the result, which record() takes from the
	// database; a taken row is registered with the reading before the claim.
	for _, d := range taken {
		r.claimed[d] = claim{start: start}
	}
	return taken, nil
}

// drop is step (6).
func (r *run) drop(ctx context.Context, digests []Digest) error {
	if err := r.fault("drop:before"); err != nil {
		return err
	}
	return r.c.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return qs.DropBlobRows(ctx, q.DropBlobRowsParams{Digests: digestBytes(digests), SettleSecs: r.c.cfg.DeleteSettle.Seconds()})
	})
}

// settle waits up to delete_settle so that the rows this run deleted (and the
// rows handed over to it) are removed before it ends: an upload waiting for such
// a row is not left until the next scheduled run.
func (r *run) settle(ctx context.Context) error {
	// A row this run deleted is removable delete_settle after its delete, which
	// lies at most that far before now; two polls of slack cover the last one.
	deadline := r.c.now().Add(r.c.cfg.DeleteSettle + 2*r.c.poll)
	for {
		if _, err := r.finishEligible(ctx); err != nil || r.res.Paused {
			return err
		}
		var soon int64
		err := r.c.db.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			soon, err = qs.CountDeletingSoon(ctx, (r.c.cfg.HandoverDelay - r.c.cfg.DeleteSettle).Seconds())
			return err
		})
		if err != nil {
			return err
		}
		if soon == 0 || !r.c.now().Before(deadline) {
			return nil
		}
		t := time.NewTimer(r.c.poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// finalize records the deletes that succeeded and hands the other claims over.
// It uses a context of its own: the run's may be the thing that ended.
func (r *run) finalize(ctx context.Context) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.record(fctx); err != nil {
		r.c.log.Warn("could not record deleted objects; the next run repeats the delete", "error", err)
	}
	if len(r.claimed) > 0 {
		left := make([]Digest, 0, len(r.claimed))
		for d := range r.claimed {
			left = append(left, d)
		}
		back := (r.c.cfg.HandoverDelay - r.c.cfg.DeleteSettle).Seconds()
		err := r.c.db.InTx(fctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			return qs.HandOverBlobs(fctx, q.HandOverBlobsParams{Digests: digestBytes(left), BackSecs: back})
		})
		if err != nil {
			r.c.log.Warn("could not hand claimed rows over; they wait for handover_delay", "rows", len(left), "error", err)
		}
	}
	var deleting int64
	_ = r.c.db.InTx(fctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		deleting, err = qs.CountDeletingBlobs(fctx)
		return err
	})
	r.c.metrics.setDeleting(int(deleting))
}
