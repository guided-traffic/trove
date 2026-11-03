package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/match"
	"github.com/guided-traffic/trove/internal/vuln/osv"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The archive limits of R-VULN-004 step 3: the archive is far larger than a package, so this page declares its own finite
// limits for the pass outside Inspect (R-ARCH-028).
const (
	maxArchiveEntries = 1_000_000
	maxInflated       = int64(4) << 30
	maxRatio          = 100
)

func (s *Service) zipLimits() untrusted.Limits {
	return untrusted.Limits{
		Entries: maxArchiveEntries, UncompressedBytes: maxInflated, StreamBytes: maxInflated, Ratio: maxRatio,
		ManifestEntryBytes: int64(s.o.Config.Sources.OSV.MaxRecordSize), JSONDepth: 64,
	}
}

// archiveError maps what the zip reader refused to the run's problem: a crossed limit, a damaged or ambiguous archive.
func archiveError(err error) error {
	if untrusted.IsTooLarge(err) || untrusted.IsRefused(err) {
		return problemf(errs.CodeArchiveLimit, "the archive crossed a limit or is not a valid zip archive", err)
	}
	return err
}

// entryMeta is what the first pass keeps of a valid record: enough to decide, in the second pass, whether the record is
// written, without holding the records.
type entryMeta struct {
	idx      int
	id       string
	modified time.Time
	sha      [sha256.Size]byte
	// size is the length of the record as received, the measure that cuts the batches by bytes.
	size int
}

// scanResult is the first pass over an archive.
type scanResult struct {
	metas       []entryMeta
	records     int
	rejected    int
	unevaluable int
}

// scanArchive is the first pass: every entry is read, decoded and validated, and only its identity is kept (R-VULN-004 step
// 4). A record that fails validation is counted; a limit that is crossed ends the run. Of two entries with one identifier the
// one with the later modified time is kept.
func (s *Service) scanArchive(ctx context.Context, z *untrusted.ZipReader, eco string, ad match.Adapter) (*scanResult, error) {
	max := int64(s.o.Config.Sources.OSV.MaxRecordSize)
	res := &scanResult{}
	entries := z.Entries()
	byID := map[string]int{}
	for i, e := range entries {
		if e.Dir || !strings.HasSuffix(e.Name, ".json") {
			continue
		}
		if i%BatchSize == 0 {
			if err := ctx.Err(); err != nil {
				return nil, context.Cause(ctx)
			}
		}
		data, err := z.ReadEntry(e, max)
		if err != nil {
			return nil, archiveError(err)
		}
		res.records++
		rec, err := osv.Decode(data, eco, ad)
		if err != nil {
			if !errors.Is(err, osv.ErrRejected) {
				return nil, err
			}
			res.rejected++
			continue
		}
		res.unevaluable += rec.Unevaluable
		m := entryMeta{idx: i, id: rec.Advisory.ID, modified: rec.Advisory.Modified, sha: rec.SHA256, size: len(data)}
		if j, dup := byID[m.id]; dup {
			if m.modified.Before(res.metas[j].modified) {
				continue
			}
			res.metas[j] = m
			continue
		}
		byID[m.id] = len(res.metas)
		res.metas = append(res.metas, m)
	}
	return res, nil
}

// checkpoint is the resumable state of a job (R-VULN-004 "Resumption"): the ecosystems whose run ended, and for the one in
// progress the identity of the archive and the number of batches committed.
type checkpoint struct {
	Done      []string `json:"done,omitempty"`
	Ecosystem string   `json:"ecosystem,omitempty"`
	// Ident is the ETag of the archive (the SHA-256 of a mirrored item): a resumed run continues only when the archive it
	// downloads again has the same one.
	Ident   string `json:"ident,omitempty"`
	Batches int    `json:"batches,omitempty"`
	Written int    `json:"written,omitempty"`
}

// batchEnds cuts the records of an archive into the batches of the second pass: at most BatchSize records and, once a batch holds
// BatchBytes of record bytes, no more. A record larger than BatchBytes is a batch of its own. The cut depends on the archive only,
// so a resumed run, which reads the same archive, finds the batch its checkpoint counted. It returns the end offset of each batch.
func batchEnds(metas []entryMeta) []int {
	var ends []int
	start, bytes := 0, 0
	for i, m := range metas {
		if i > start && (i-start >= BatchSize || bytes+m.size > BatchBytes) {
			ends = append(ends, i)
			start, bytes = i, 0
		}
		bytes += m.size
	}
	if len(metas) > start {
		ends = append(ends, len(metas))
	}
	return ends
}

// full is what a full sync passes to the final step.
type fullInput struct {
	eco     string
	ad      match.Adapter
	state   store.SyncState
	arch    *archive
	started time.Time
	mirror  *mirrorFacts
	// force is true when the stored records were imported with an older adapter version: every record is written again, whatever
	// the stored digest says (Q-VULN-63).
	force bool
}

// adapterStale reports whether the records stored for an ecosystem were imported with an older adapter than this release's, so
// that the next run must import them again, in full and unconditionally (Q-VULN-63).
func adapterStale(eco string, st store.SyncState) bool {
	return st.AdapterVersion < match.AdapterVersion(eco)
}

// applyArchive runs a full sync over a downloaded or mirrored archive: pass 1, the integrity guards, pass 2 in batches with a
// checkpoint after each, the deletion of what the archive no longer has, the alias groups, and the final transaction.
func (s *Service) applyArchive(ctx context.Context, run Run, p Params, cp *checkpoint, in fullInput) (Result, error) {
	cfg := s.o.Config.Sources.OSV
	lim := s.zipLimits()
	z, err := untrusted.NewZipDeclared(ctx, in.arch.ra, in.arch.size, lim)
	if err != nil {
		return Result{}, archiveError(err)
	}
	scan, err := s.scanArchive(ctx, z, in.eco, in.ad)
	if err != nil {
		return Result{}, err
	}
	res := Result{Ecosystem: in.eco, Kind: kindFull, Rejected: scan.rejected, Unevaluable: scan.unevaluable}
	s.count(store.SourceOSV, in.eco, "rejected", scan.rejected)

	// The integrity guards, before anything is written: a truncated or tampered source changes nothing.
	if scan.rejected*100 > scan.records*invalidPercent {
		return res, problemf(errs.CodeTooManyInvalidRecords, "more than 1 % of the archive's records failed validation", nil)
	}
	stored, err := s.storedIDs(ctx, in.eco)
	if err != nil {
		return res, err
	}
	seen := make(map[string]struct{}, len(scan.metas))
	for _, m := range scan.metas {
		seen[m.id] = struct{}{}
	}
	var gone []string
	for _, id := range stored {
		if _, ok := seen[id]; !ok {
			gone = append(gone, id)
		}
	}
	if len(stored) > 0 && float64(len(gone)) > cfg.MaxShrink*float64(len(stored)) {
		if !p.AcceptShrink {
			return res, problemf(errs.CodeShrinkRefused, "the archive would delete more than vulnerabilities.sources.osv.max_shrink of the stored records", nil)
		}
		res.AcceptedShrink = true
	}

	// Pass 2: the batches, from the checkpoint when the archive is the one it was taken on.
	in.force = adapterStale(in.eco, in.state)
	ends := batchEnds(scan.metas)
	start := 0
	if cp.Ecosystem == in.eco && cp.Ident == in.arch.ident && cp.Batches > 0 {
		start, res.Written = cp.Batches, cp.Written
	} else {
		*cp = checkpoint{Done: cp.Done, Ecosystem: in.eco, Ident: in.arch.ident}
	}
	z2, err := untrusted.NewZipDeclared(ctx, in.arch.ra, in.arch.size, lim)
	if err != nil {
		return res, archiveError(err)
	}
	total := int64(len(scan.metas))
	for b := start; b < len(ends); b++ {
		if err := ctx.Err(); err != nil {
			return res, context.Cause(ctx)
		}
		lo := 0
		if b > 0 {
			lo = ends[b-1]
		}
		chunk := scan.metas[lo:ends[b]]
		w, k, err := s.writeChunk(ctx, z2, in.eco, in.ad, chunk, in.force)
		if err != nil {
			return res, err
		}
		res.Written += w
		res.Skipped += k
		cp.Batches, cp.Written = b+1, res.Written
		if err := run.SaveCheckpoint(ctx, cp); err != nil {
			return res, err
		}
		_ = run.SaveProgress(ctx, int64(ends[b]), total, "records")
		if hook := s.batchHook(); hook != nil {
			hook(in.eco, b+1)
		}
	}
	s.count(store.SourceOSV, in.eco, "written", res.Written)
	s.count(store.SourceOSV, in.eco, "skipped", res.Skipped)

	// What the archive no longer has is deleted and queued.
	deleted, err := s.deleteIDs(ctx, in.eco, gone)
	if err != nil {
		return res, err
	}
	res.Deleted = deleted
	s.count(store.SourceOSV, in.eco, "deleted", deleted)

	// The names the run wrote, those of an earlier run that failed after some batches and those of the records deleted are in the
	// touched table; the records whose canonical record is gone join them.
	orphans, err := s.orphanIDs(ctx, in.eco)
	if err != nil {
		return res, err
	}
	if err := s.touch(ctx, in.eco, orphans); err != nil {
		return res, err
	}
	if err := s.finishGroups(ctx, in.eco, in.state.Complete()); err != nil {
		return res, err
	}
	res.Records = len(scan.metas)
	return s.finishFull(ctx, p, in, res)
}

// writeChunk writes the records of one batch that differ from the stored ones: it compares the first pass's identity with the
// stored modified time and digest, reads and decodes the others again, and upserts them in one transaction.
func (s *Service) writeChunk(ctx context.Context, z *untrusted.ZipReader, eco string, ad match.Adapter, chunk []entryMeta, force bool) (written, skipped int, err error) {
	ids := make([]string, len(chunk))
	for i, m := range chunk {
		ids[i] = m.id
	}
	var stored map[string]store.Stored
	err = s.o.DB.Read(ctx, func(db dbtx) (err error) {
		stored, err = store.Sync{}.Stored(ctx, db, eco, ids)
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	max := int64(s.o.Config.Sources.OSV.MaxRecordSize)
	var batch []store.Written
	entries := z.Entries()
	for _, m := range chunk {
		if st, ok := stored[m.id]; ok && !force && bytes.Equal(st.SHA256, m.sha[:]) && st.Modified.Equal(m.modified) {
			skipped++
			continue
		}
		data, err := z.ReadEntry(entries[m.idx], max)
		if err != nil {
			return 0, 0, archiveError(err)
		}
		rec, err := osv.Decode(data, eco, ad)
		if err != nil {
			return 0, 0, err // it decoded in the first pass
		}
		batch = append(batch, store.Written{Advisory: rec.Advisory, SHA256: rec.SHA256[:]})
	}
	if err := s.commitBatch(ctx, eco, batch); err != nil {
		return 0, 0, err
	}
	return len(batch), skipped, nil
}

// commitBatch upserts a batch, queues its records for the evaluation and records their names as touched in one transaction
// (R-VULN-004 step 4, Q-VULN-62).
func (s *Service) commitBatch(ctx context.Context, eco string, batch []store.Written) error {
	if len(batch) == 0 {
		return nil
	}
	ids := make([]string, 0, len(batch))
	var names []string
	for _, w := range batch {
		ids = append(ids, w.Advisory.ID)
		names = append(names, touchedNames(w.Advisory)...)
	}
	return s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		if err := (store.Sync{}).Upsert(ctx, db, eco, batch); err != nil {
			return err
		}
		if err := (store.Sync{}).TouchedAdd(ctx, db, eco, names); err != nil {
			return err
		}
		return (store.Sync{}).AddPending(ctx, db, eco, ids)
	})
}

// touch adds names to the run's touched set.
func (s *Service) touch(ctx context.Context, eco string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	return s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		return (store.Sync{}).TouchedAdd(ctx, db, eco, names)
	})
}

func (s *Service) storedIDs(ctx context.Context, eco string) (ids []string, err error) {
	err = s.o.DB.Read(ctx, func(db dbtx) (err error) {
		ids, err = store.Sync{}.IDs(ctx, db, eco)
		return err
	})
	return ids, err
}

func (s *Service) orphanIDs(ctx context.Context, eco string) (ids []string, err error) {
	err = s.o.DB.Read(ctx, func(db dbtx) (err error) {
		ids, err = store.Sync{}.Orphans(ctx, db, eco)
		return err
	})
	return ids, err
}

// deleteIDs deletes the records the archive no longer has, in batches, and queues them for the evaluation (R-VULN-004 step 5).
func (s *Service) deleteIDs(ctx context.Context, eco string, ids []string) (int, error) {
	slices.Sort(ids)
	total := 0
	for len(ids) > 0 {
		chunk := ids[:min(BatchSize, len(ids))]
		ids = ids[len(chunk):]
		err := s.o.DB.InTx(ctx, func(tx metaTx) error {
			db, err := store.DBOf(tx)
			if err != nil {
				return err
			}
			if err := (store.Sync{}).AddPending(ctx, db, eco, chunk); err != nil {
				return err
			}
			if err := (store.Sync{}).TouchedAddRecords(ctx, db, eco, chunk); err != nil {
				return err
			}
			n, err := (store.Sync{}).Delete(ctx, db, eco, chunk)
			total += n
			return err
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// finishGroups recomputes the alias groups of the touched names, applies the known-exploited flags to their canonical records
// and queues what the recomputation changed (R-VULN-006, R-VULN-008). The names come from the touched table, page by page; the
// table is cleared by the transaction that ends the run, so that a run that fails here leaves them for the next one (Q-VULN-62).
// announce says whether a change of a group's kind is an audit event: not in the first import of an ecosystem, where every
// group is new (Q-VULN-61).
func (s *Service) finishGroups(ctx context.Context, eco string, announce bool) error {
	var canonical []string
	after := ""
	for {
		var page []string
		err := s.o.DB.Read(ctx, func(db dbtx) (err error) {
			page, err = store.Sync{}.TouchedPage(ctx, db, eco, after, touchedPage)
			return err
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1]
		stats, err := s.recomputeGroups(ctx, eco, page, announce)
		if err != nil {
			return err
		}
		canonical = append(canonical, stats.canonical...)
	}
	_, err := s.applyKEV(ctx, eco, canonical)
	return err
}

// recoverTouched finishes what a run that failed after some batches left: the names it touched are in the table, the records are
// stored and queued, and nothing has recomputed their groups or raised the epoch. It runs at the start of every run of the
// ecosystem, before the source is asked, so that an unchanged source (304, no new rows) does not leave the groups as they were.
func (s *Service) recoverTouched(ctx context.Context, eco string, st store.SyncState) error {
	var left bool
	if err := s.o.DB.Read(ctx, func(db dbtx) (err error) {
		left, err = store.Sync{}.TouchedExists(ctx, db, eco)
		return err
	}); err != nil {
		return err
	}
	if !left {
		return nil
	}
	s.log.Info("recomputing the alias groups of a run that did not finish", "ecosystem", eco)
	if err := s.finishGroups(ctx, eco, st.Complete()); err != nil {
		return err
	}
	return s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		if err := (store.Sync{}).TouchedClear(ctx, db, eco); err != nil {
			return err
		}
		if !st.Complete() {
			return nil // the first full import raises the epoch and requests the evaluation when it ends
		}
		epoch, err := (store.Sync{}).BumpEpoch(ctx, db, eco)
		if err != nil {
			return err
		}
		if err := s.o.Changes.Notify(tx, cluster.AreaVulnData, vuln.DataChangePayloadVersion, vuln.DataChange{Ecosystem: eco, Epoch: epoch}); err != nil {
			return err
		}
		return s.enqueueEvaluate(ctx, tx)
	})
}
