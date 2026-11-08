package oci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// SweepJobKind is the periodic job that removes the blob items no manifest of their image name references (R-OCI-019, Q-OCI-71):
// queue maintenance, exclusive, every hour. The blob object itself is collected by the garbage collection (R-STOR-024) once no
// item references it.
const SweepJobKind = "oci-sweep"

// SweepSchedule is the cron expression of the task, every hour at minute 0; maintenance.tasks.oci-sweep overrides it (R-STOR-025).
const SweepSchedule = "0 * * * *"

// SweepJobSpec is the registration of the kind. The composition root registers it with the handler of its sweeper, because the
// handler needs the item store and this package holds no package-level state (R-ARCH-032).
func SweepJobSpec(h jobs.Handler) jobs.KindSpec {
	return jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Handler: h}
}

// sweepBatch is the number of candidates read per statement; each is removed in a transaction of its own.
const sweepBatch = 200

// systemSweep is the actor of the removals the sweep makes: a removal that no request asked for (Q-OCI-24).
var systemSweep = audit.Actor{Kind: "system", Name: "removal"}

// Remover removes files in the transaction of the caller; *items.Store implements it. The removal runs the storage hook of the
// format, so a blob the sweep removes goes through the same refusals as one removed by hand (R-OCI-018).
type Remover interface {
	RemoveIn(ctx context.Context, tx meta.Tx, spec items.RemoveSpec) error
}

// SweepOptions configure [NewSweeper].
type SweepOptions struct {
	Store *Store
	// DB opens the transaction of each removal.
	DB meta.Runner
	// Items gives the item store; the function is called when a run needs it, because the store is built after the sweeper.
	// A nil answer fails the run.
	Items func() Remover
	// Grace is oci.unreferenced_blob_grace: the age a blob item needs before it is swept.
	Grace time.Duration
	// Now is the clock; nil is time.Now. A test sets it to age a blob without waiting.
	Now func() time.Time
	// Batch is the number of candidates read per statement; 0 is 200.
	Batch int
	// Removed is told how many blob items a run removed in one repository, for the counter
	// trove_oci_sweep_blobs_removed_total (R-OCI-034); nil is ignored.
	Removed func(n int)
	Log     *slog.Logger
}

// Sweeper is the job oci-sweep.
type Sweeper struct{ o SweepOptions }

// NewSweeper creates the sweeper.
func NewSweeper(o SweepOptions) (*Sweeper, error) {
	switch {
	case o.Store == nil || o.DB == nil || o.Items == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "oci-sweep: the sweeper needs the data port, a database and the item store")
	case o.Grace <= 0:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "oci-sweep: the grace period must be positive")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Batch <= 0 {
		o.Batch = sweepBatch
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Sweeper{o: o}, nil
}

// SweepResult is what a run records for the job API.
type SweepResult struct {
	// Scanned is the number of candidates the run read, Removed the number of blob items it removed.
	Scanned int `json:"scanned"`
	Removed int `json:"removed"`
	// Kept is the number of candidates a manifest, an upload session or a concurrent change took out of the sweep between the scan
	// and the removal.
	Kept int `json:"kept"`
}

// Run is the handler of the kind. It is idempotent, as every handler is: a run that stops halfway leaves the rest to the next one.
func (s *Sweeper) Run(ctx context.Context, run *jobs.Run) error {
	res, err := s.Sweep(ctx)
	if err != nil {
		_ = run.SetResult(res) // what a run that failed did remove is still recorded
		return err
	}
	return run.SetResult(res)
}

// Sweep removes, in every local OCI repository, the blob items older than the grace that no manifest of their name references
// (R-OCI-019). Each blob item is removed in a transaction of its own that first locks the package row of the image name, as a
// manifest push does, and reads again whether a manifest references the blob and an upload session holds it: a push that
// committed a manifest between the scan and the removal keeps its blob. The first error ends the run; what it removed stays removed.
func (s *Sweeper) Sweep(ctx context.Context) (SweepResult, error) {
	rm := s.o.Items()
	if rm == nil {
		return SweepResult{}, errs.E(errs.Internal, errs.CodeInternal, "oci-sweep: this replica has not wired the item store")
	}
	before := s.o.Now().Add(-s.o.Grace)
	var repos []q.OCISweepRepositoriesRow
	if err := s.o.Store.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		repos, err = qs.OCISweepRepositories(ctx)
		return err
	}); err != nil {
		return SweepResult{}, err
	}
	var total SweepResult
	var failed []error
	for _, r := range repos {
		ref := items.RepoRef{ID: r.ID, Name: qname.Repo{Project: r.ProjectKey, Key: r.RepoKey}, Class: "local", Format: "oci"}
		got, err := s.sweepRepo(ctx, rm, ref, before)
		total.Scanned += got.Scanned
		total.Removed += got.Removed
		total.Kept += got.Kept
		if got.Removed > 0 && s.o.Removed != nil {
			s.o.Removed(got.Removed)
		}
		if err != nil {
			if ctx.Err() != nil {
				return total, err
			}
			// One repository that fails (a refusal of the hook, a lock timeout) does not keep the others from being swept; the run fails
			// at its end with every error, and the next run tries again.
			failed = append(failed, fmt.Errorf("repository %s: %w", ref.Name, err))
		}
	}
	if total.Removed > 0 {
		s.o.Log.Info("oci-sweep removed unreferenced blobs", "removed", total.Removed, "scanned", total.Scanned, "kept", total.Kept)
	}
	return total, errors.Join(failed...)
}

func (s *Sweeper) sweepRepo(ctx context.Context, rm Remover, ref items.RepoRef, before time.Time) (SweepResult, error) {
	var res SweepResult
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		batch, err := s.o.Store.UnreferencedBlobsAfter(ctx, ref.ID, before, after, s.o.Batch)
		if err != nil {
			return res, err
		}
		for _, b := range batch {
			res.Scanned++
			removed, err := s.removeOne(ctx, rm, ref, b, before)
			if err != nil {
				return res, err
			}
			if removed {
				res.Removed++
			} else {
				res.Kept++
			}
			after = b.Path
		}
		if len(batch) < s.o.Batch {
			return res, nil
		}
	}
}

// removeOne removes one blob item in its own transaction, under the lock of the image name's package row.
func (s *Sweeper) removeOne(ctx context.Context, rm Remover, ref items.RepoRef, b rows.Blob, before time.Time) (bool, error) {
	removed := false
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		removed = false
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		// The lock every writer of the name takes first (R-OCI-007): a manifest push that references this blob either committed its
		// references before this point or waits for the removal and finds the blob gone, which it answers with MANIFEST_BLOB_UNKNOWN.
		// A name that holds only blobs has no package row, and none is made for it (a row without a version is a row nobody reads): the blob
		// rows arbitrate there. A push holds the rows of its layers FOR SHARE until its own rows are written, so the removal below waits
		// for it, and the hook then reads the references it committed and refuses (R-OCI-007, Q-OCI-61).
		if _, err := qs.LockPackage(ctx, q.LockPackageParams{RepoID: ref.ID, Name: b.Name}); err != nil {
			return err
		}
		still, err := s.o.Store.StillUnreferenced(ctx, tx, b, before)
		if err != nil || !still {
			return err
		}
		if err := rm.RemoveIn(ctx, tx, items.RemoveSpec{Repo: ref, Paths: []string{b.Path}, Actor: systemSweep}); err != nil {
			return err
		}
		removed = true
		return nil
	})
	var refused *errs.Error
	if errors.As(err, &refused) && refused.Code == errs.CodeOCIReferenced {
		// The hook of the removal found a reference a push committed while the removal waited for its blob row: the blob stays.
		return false, nil
	}
	return removed, err
}
