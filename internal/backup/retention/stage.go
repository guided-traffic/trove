package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// The reasons of system.backup.deleted.
const (
	ReasonRetention = "retention"
	ReasonManual    = "manual"
)

// Audit writes system.backup.deleted for a backup that was deleted (R-BAK-016). It is called after the backup's objects are
// gone; an error fails the run, because a deletion without its record must not go unnoticed (R-OPS-015).
type Audit interface {
	BackupDeleted(ctx context.Context, id format.ID, reason string) error
}

// SweepClock tells when the pool was last swept, so that the sweep runs at least every backup.retention.sweep_interval
// (R-BAK-017) even when nothing was deleted.
type SweepClock interface {
	LastSweep(ctx context.Context) (time.Time, bool, error)
}

// Fencer is the run's side of the lease: the check before every deletion (R-BAK-010) and the progress tick. *backup.Handle
// implements it.
type Fencer interface {
	Fence(ctx context.Context, to backup.State) error
	Tick()
}

// Stage applies the retention policy and sweeps the pool. It implements [backup.PruneStage] for the pruning phase of a
// create run and is the body of the runs of [Runner].
type Stage struct {
	Store  target.Store
	Layout format.Layout
	Policy Policy
	// SweepInterval is backup.retention.sweep_interval.
	SweepInterval time.Duration
	// Facts supplies the installation ID and incarnation the marker is checked against.
	Facts backup.Facts
	Audit Audit
	Clock SweepClock
	Now   func() time.Time
	Log   *slog.Logger
	// DeleteTimeout is the deadline of every delete request of the pruning phase: backup.finalize_timeout, the figure the
	// lease fence leaves room for (R-BAK-010). Zero means no deadline.
	DeleteTimeout time.Duration
}

// del sends one delete request under the stage's deadline. A request that outlives it must not go on in a run whose slot a
// later run may already hold.
func (s *Stage) del(ctx context.Context, keys []string) error {
	return deleteWithin(ctx, s.Store, s.DeleteTimeout, keys)
}

var _ backup.PruneStage = (*Stage)(nil)

func (s *Stage) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Stage) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Prune implements [backup.PruneStage]: the pruning phase of the create run that h belongs to. into carries the run's
// result; the stage fills Retention, Retained, BackupsDeleted, IncompleteDeleted, PoolObjectsDeleted, PoolBytesReclaimed and
// PoolSweptAt, appends warnings and sets Outcome to completed_with_warnings when there are any. into.BackupID names the
// run's own backup, whose directory is never treated as incomplete, and whose key IDs the escrow reminder of R-BAK-028
// compares with those of the retained backups.
//
// An error means nothing further was deleted. A sweep that is skipped because a blob list does not match its manifest or
// that a foreign pool key stopped is not an error but a warning: the backup the run wrote is complete and valid.
func (s *Stage) Prune(ctx context.Context, h *backup.Handle, into *backup.Result) error {
	return s.prune(ctx, h, into, true)
}

func (s *Stage) prune(ctx context.Context, f Fencer, into *backup.Result, create bool) error {
	if err := CheckMarker(ctx, s.Store, s.Layout, s.Facts); err != nil {
		return err
	}
	scan, err := listing.ScanTicking(ctx, s.Store, s.Layout, f.Tick)
	if err != nil {
		return err
	}
	into.Retention = &backup.Retention{KeepLast: s.Policy.KeepLast, KeepWithinSeconds: int64(s.Policy.KeepWithin / time.Second)}
	changed := false

	complete := listing.Complete(scan)
	for _, b := range s.Policy.Expired(complete, s.now()) {
		if err := s.deleteBackup(ctx, f, b.ID, true, ReasonRetention); err != nil {
			return err
		}
		into.BackupsDeleted = append(into.BackupsDeleted, string(b.ID))
		changed = true
	}
	if len(complete) > 0 {
		newest := complete[0].ID
		for _, b := range scan {
			if b.Complete() || !b.ID.Less(newest) || string(b.ID) == into.BackupID {
				continue
			}
			if err := s.deleteBackup(ctx, f, b.ID, false, ""); err != nil {
				return err
			}
			into.IncompleteDeleted = append(into.IncompleteDeleted, string(b.ID))
			changed = true
		}
	}
	return s.finish(ctx, f, into, scan, changed, create)
}

// finish is the common end of a stage: the pool sweep when it is due, the retained list and the warnings.
// scan is the listing taken before the run's deletions; when changed is set it is read again, so that a deleted backup
// protects nothing (R-BAK-017 step 1).
func (s *Stage) finish(ctx context.Context, f Fencer, into *backup.Result, scan []listing.Backup, changed, create bool) error {
	var err error
	due := changed
	if changed {
		if scan, err = listing.ScanTicking(ctx, s.Store, s.Layout, f.Tick); err != nil {
			return err
		}
	} else if due, err = s.sweepDue(ctx); err != nil {
		return err
	}
	complete := listing.Complete(scan)
	if due {
		sw := &Sweeper{
			Store: s.Store, Layout: s.Layout, Tick: f.Tick, DeleteTimeout: s.DeleteTimeout,
			Fence: func(ctx context.Context) error { return f.Fence(ctx, backup.StatePruning) },
		}
		rep, err := sw.Sweep(ctx, complete)
		into.PoolObjectsDeleted += rep.Deleted
		into.PoolBytesReclaimed += rep.Bytes
		switch {
		case errors.Is(err, listing.ErrBlobList):
			into.Warnings = append(into.Warnings, err.Error())
		case err != nil:
			return err
		case rep.Stopped != "":
			// Not a sweep: poolSweptAt stays unset so that the next run tries again.
			into.Warnings = append(into.Warnings, "the pool sweep stopped at a key it does not trust: "+rep.Stopped)
		default:
			at := s.now().UTC()
			into.PoolSweptAt = &at
		}
	}
	into.Retained = into.Retained[:0]
	for _, b := range complete {
		m := b.Manifest
		into.Retained = append(into.Retained, backup.Retained{
			BackupID: string(m.BackupID), SnapshotAt: m.SnapshotAt, TroveVersion: m.Trove.Version, SchemaStep: m.Schema.Step,
			DatabaseBytes: listing.DatabaseBytes(m), BlobCount: m.Blobs.Referenced, BlobBytes: m.Blobs.ReferencedBytes,
			Damaged: len(m.Blobs.Damaged) > 0,
		})
	}
	if create {
		into.Warnings = append(into.Warnings, EscrowWarnings(complete, format.ID(into.BackupID))...)
	}
	switch {
	case len(into.Warnings) > 0:
		into.Outcome = string(backup.StateCompletedWithWarning)
	case into.Outcome == "":
		into.Outcome = string(backup.StateCompleted)
	}
	return nil
}

func (s *Stage) sweepDue(ctx context.Context) (bool, error) {
	if s.Clock == nil {
		return true, nil
	}
	at, ok, err := s.Clock.LastSweep(ctx)
	if err != nil {
		return false, fmt.Errorf("reading when the pool was last swept: %w", err)
	}
	return !ok || s.now().Sub(at) >= s.SweepInterval, nil
}

// deleteBackup deletes one backup directory: the manifest first, so the backup stops being a backup at once, then every
// other object of the directory (R-BAK-016). Each request is preceded by the lease fence.
func (s *Stage) deleteBackup(ctx context.Context, f Fencer, id format.ID, complete bool, reason string) error {
	var keys []string
	n := 0
	err := s.Store.List(ctx, s.Layout.Dir(id), func(o target.Object) error {
		if n++; n%listing.PageSize == 0 {
			f.Tick()
		}
		if o.Key != s.Layout.Manifest(id) {
			keys = append(keys, o.Key)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("listing backup %s: %w", id, err)
	}
	if complete {
		if err := f.Fence(ctx, backup.StatePruning); err != nil {
			return err
		}
		if err := s.del(ctx, []string{s.Layout.Manifest(id)}); err != nil {
			return fmt.Errorf("deleting the manifest of backup %s: %w", id, err)
		}
		f.Tick()
	}
	for batch := range slices.Chunk(keys, deleteBatch) {
		if err := f.Fence(ctx, backup.StatePruning); err != nil {
			return err
		}
		if err := s.del(ctx, batch); err != nil {
			return fmt.Errorf("deleting backup %s: %w", id, err)
		}
		f.Tick()
	}
	if complete && s.Audit != nil {
		if err := s.Audit.BackupDeleted(ctx, id, reason); err != nil {
			return fmt.Errorf("recording the deletion of backup %s: %w", id, err)
		}
	}
	s.log().Info("backup deleted", "backup", id, "complete", complete, "reason", reason)
	return nil
}

// EscrowWarnings is the reminder of R-BAK-028: for every retained backup that lists a key ID which the run's own backup no
// longer uses, a retired key must still be in escrow. It compares IDs only. own is the run's backup; without it, or when
// it is not among the complete backups, there is nothing to compare.
func EscrowWarnings(complete []listing.Backup, own format.ID) []string {
	var current []string
	for _, b := range complete {
		if b.ID == own {
			current = b.Manifest.Secrets.KeyIDs
		}
	}
	if current == nil {
		return nil
	}
	retired := map[string][]string{}
	for _, b := range complete {
		if b.ID == own {
			continue
		}
		for _, k := range b.Manifest.Secrets.KeyIDs {
			if !slices.Contains(current, k) {
				retired[k] = append(retired[k], string(b.ID))
			}
		}
	}
	if len(retired) == 0 {
		return nil
	}
	keys := make([]string, 0, len(retired))
	for k := range retired {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		ids := retired[k]
		slices.Sort(ids)
		parts[i] = fmt.Sprintf("%s (needed by %s)", k, strings.Join(ids, ", "))
	}
	return []string{"retained backups need key IDs that the newest backup no longer uses; keep these keys in escrow until those backups are gone: " +
		strings.Join(parts, "; ") + " (R-BAK-028)"}
}
