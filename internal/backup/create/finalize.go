package create

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// finalize is the phase 'finalizing' (R-BAK-009): the fenced lease check, the spot check of the pool, the marker check and the
// manifest, the last object of a backup and its commit point (R-BAK-002).
func (r *runState) finalize(ctx context.Context) error {
	// copying -> finalizing only while the lease has room for the manifest's deadline (R-BAK-010).
	if err := r.h.Fence(ctx, backup.StateFinalizing); err != nil {
		return err
	}
	if err := r.fault(ctx, PointFinalizing); err != nil {
		return err
	}
	if err := r.spotCheck(ctx); err != nil {
		return err
	}
	m, err := r.buildManifest()
	if err != nil {
		return err
	}
	body, err := m.Encode()
	if err != nil {
		return err
	}
	// The marker is read again before a manifest is written (R-BAK-002), and the lease is fenced once more, so that a
	// run that stalled in the spot check cannot write after it was abandoned.
	if mk, found, err := r.readMarker(ctx); err != nil {
		return err
	} else if !found {
		return backup.Failf(backup.ExitForeignTarget, backup.CodeForeignTarget, "the target marker disappeared during the run")
	} else if err := mk.Check(r.facts.InstallationID, r.facts.Incarnation); err != nil {
		return backup.Fail(backup.ExitForeignTarget, backup.CodeForeignTarget, err)
	}
	if err := r.h.Fence(ctx, backup.StateFinalizing); err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, r.d.Cfg.FinalizeTimeout)
	defer cancel()
	if _, err := r.d.Target.Put(pctx, r.layout.Manifest(r.id), body, target.PutOptions{ContentType: "application/json"}); err != nil {
		return classify(fmt.Errorf("write the manifest: %w", err))
	}
	r.res.DatabaseBytes = listing.DatabaseBytes(m)
	r.res.BlobsCopied, r.res.BytesCopied, r.res.DamagedBlobs = m.Blobs.Copied, m.Blobs.CopiedBytes, int64(len(m.Blobs.Damaged))
	for _, t := range r.snapInfo.Tables {
		if !t.Registered {
			r.res.UnregisteredTables = append(r.res.UnregisteredTables, t.Name)
		}
	}
	sort.Strings(r.res.UnregisteredTables)
	if len(r.res.UnregisteredTables) > 0 {
		r.d.Log.Warn("tables without an export class were exported as data", "tables", r.res.UnregisteredTables)
	}
	return nil
}

// spotCheck sends HeadObject for backup.verify_sample random digests that earlier backups listed (R-BAK-009): a missing object
// fails the run with pool_damaged and the advice to run `trove backup verify`.
func (r *runState) spotCheck(ctx context.Context) error {
	if len(r.list.sample) == 0 {
		return nil
	}
	g, _ := newGroup(ctx)
	work := make(chan format.Digest)
	g.Go(func(ctx context.Context) error {
		defer close(work)
		for _, d := range r.list.sample {
			select {
			case work <- d:
			case <-ctx.Done():
				return nil
			}
		}
		return nil
	})
	for range max(r.d.Cfg.Copy.Concurrency, 1) {
		g.Go(func(ctx context.Context) error {
			for d := range work {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				_, err := r.d.Target.Head(ctx, r.layout.Pool(d))
				if errors.Is(err, target.ErrNotFound) {
					return fmt.Errorf("%w: %s is listed by an earlier backup and missing from the pool", backup.ErrPoolDamaged, d.Hex())
				}
				if err != nil {
					return classify(fmt.Errorf("spot check of %s: %w", d.Hex(), err))
				}
				r.h.Tick()
			}
			return nil
		})
	}
	return g.Wait()
}

func (r *runState) buildManifest() (*format.Manifest, error) {
	d := r.d
	damaged := append(append([]format.DamagedBlob{}, r.copied.damaged...), r.list.damaged...)
	sort.Slice(damaged, func(i, j int) bool { return damaged[i].SHA256 < damaged[j].SHA256 })
	files := r.tableFiles
	if files == nil {
		files = []format.TableFile{}
	}
	seqs := r.sequences
	if seqs == nil {
		seqs = []format.Sequence{}
	}
	blobs := r.list.file
	blobs.Copied, blobs.CopiedBytes, blobs.Damaged = r.copied.copied, r.copied.bytes, damaged
	m := &format.Manifest{
		Format: format.ManifestFormat, BackupID: r.id, InstallationID: r.facts.InstallationID, Incarnation: r.facts.Incarnation,
		Trove:     format.TroveInfo{Version: d.Version, Revision: d.Revision},
		Schema:    r.snapInfo.Schema,
		Postgres:  format.PostgresRef{ServerVersion: r.snapInfo.ServerVersion, Isolation: "repeatable read"},
		StartedAt: r.started.UTC(), SnapshotAt: r.snapInfo.At.UTC(), CompletedAt: d.Now().UTC(),
		Options: format.Options{ExcludeRemoteCaches: r.opts.ExcludeRemoteCaches || d.Cfg.Exclude.RemoteCaches},
		Source:  format.Source{Storage: d.SourceRef, RevocationJournal: d.Cfg.RevocationJournal.Enabled},
		Tables:  files, Sequences: seqs, Blobs: blobs,
		Secrets:    format.SecretsInfo{KeyIDs: r.snapInfo.KeyIDs},
		Encryption: format.Encryption{SSE: r.sse},
	}
	if r.prev != nil {
		id := r.prev.id
		m.PreviousBackupID = &id
	}
	return m, nil
}

// prune is the phase 'pruning' (R-BAK-009, R-BAK-016): retention and the pool sweep through the retention package, inside this
// run. The backup is complete by now, so a failure of the sweep is a warning of the run, not a failed backup; only a run that was
// taken over (superseded) must stop writing.
func (r *runState) prune(ctx context.Context) error {
	if r.opts.NoPrune || r.d.Prune == nil {
		return nil
	}
	if err := r.h.Fence(ctx, backup.StatePruning); err != nil {
		return err
	}
	err := r.d.Prune.Prune(ctx, r.h, r.res)
	if err == nil {
		return nil
	}
	if errors.Is(err, backup.ErrSuperseded) {
		return err
	}
	msg := fmt.Sprintf("retention and the pool sweep did not finish: %v; the next run prunes", err)
	r.d.Log.Error("pruning failed after the backup was written", "backup", r.id, "err", err)
	r.res.Warnings = append(r.res.Warnings, msg)
	r.pruneWarned = true
	return nil
}
