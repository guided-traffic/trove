package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
	// maxRatio is above the most deflate can compress (about 1032:1): for the data of a real archive the bound is inert, and it
	// stops an entry whose header declares far more than its stream can inflate to, before the entry is read (Q-VULN-116).
	maxRatio = 2048
)

func (s *Service) zipLimits() untrusted.Limits {
	return untrusted.Limits{
		Entries: maxArchiveEntries, UncompressedBytes: maxInflated, StreamBytes: maxInflated, Ratio: maxRatio,
		ManifestEntryBytes: int64(s.o.Config.Sources.OSV.MaxRecordSize), JSONDepth: 64,
		// An entry above the record size is rejected unread, so it adds nothing to the declared total; the entry count bounds the
		// archive then.
		DeclaredEntryCap: int64(s.o.Config.Sources.OSV.MaxRecordSize),
	}
}

// archiveError maps what the zip reader refused to the run's problem: a bound of the archive as a whole that is crossed, or an
// archive that is damaged or ambiguous. The detail carries the reader's reason (Q-VULN-121); the bounds of one entry never reach
// it, they reject the entry (Q-VULN-116).
func archiveError(err error) error {
	reason := err.Error()
	if e, ok := errs.As(err); ok {
		reason = e.Detail
	}
	switch {
	case untrusted.IsTooLarge(err):
		return problemf(errs.CodeArchiveLimit, "the archive crosses a fixed bound of the import: "+reason, err)
	case untrusted.IsRefused(err):
		return problemf(errs.CodeArchiveLimit, "the archive is not a valid zip archive: "+reason, err)
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
	metas []entryMeta
	// records is every ".json" entry of the archive (Q-VULN-118). rejected counts those that crossed a bound of the entry or
	// failed validation, invalid those that failed validation: the guard of 1 % counts invalid only (Q-VULN-118).
	records     int
	rejected    int
	invalid     int
	unevaluable int
	// kept holds the identifiers that rejected entries are named after: their stored rows are neither updated nor deleted
	// (Q-VULN-117).
	kept []string
	// firstEntry and firstReason describe the first record that failed validation, for the detail of the guard.
	firstEntry, firstReason string
}

// reject counts a rejected entry, remembers the identifier its name gives and logs it; the rejection says whether the entry failed
// a rule of R-VULN-006 rather than a bound of its own.
func (r *scanResult) reject(rl *rejectLog, name string, why *rejection) {
	if why.validation {
		if r.invalid == 0 {
			r.firstEntry, r.firstReason = name, why.reason
		}
		r.invalid++
	}
	r.rejected++
	id, _ := entryID(name)
	if id != "" {
		r.kept = append(r.kept, id)
	}
	rl.add(name, id, why)
}

// tooManyInvalid reports the guard: more than invalidPercent of the archive's records failed validation.
func (r *scanResult) tooManyInvalid() bool { return r.invalid*100 > r.records*invalidPercent }

// jsonEntries counts the ".json" entries of the central directory.
func jsonEntries(entries []untrusted.ZipEntry) int {
	n := 0
	for _, e := range entries {
		if !e.Dir && strings.HasSuffix(e.Name, ".json") {
			n++
		}
	}
	return n
}

// scanArchive is the first pass: every entry is checked against the bounds of one entry, read, decoded and validated, and only
// its identity is kept (R-VULN-004 step 4). An entry above a bound of its own, or a record that fails validation, is rejected:
// it is counted and logged and the scan goes on. A bound of the archive as a whole that is crossed, or a damaged archive, ends
// the run, and so does the guard: once the records that failed validation exceed 1 % of the entries the central directory
// lists, the rest of the archive is not read. Of two entries with one identifier the one with the later modified time is kept.
func (s *Service) scanArchive(ctx context.Context, z *untrusted.ZipReader, eco string, ad match.Adapter) (*scanResult, error) {
	max := int64(s.o.Config.Sources.OSV.MaxRecordSize)
	entries := z.Entries()
	res := &scanResult{records: jsonEntries(entries)}
	rl := s.newRejectLog(eco)
	defer rl.finish()
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
		data, why, err := readRecordEntry(z, e, max)
		if err != nil {
			return nil, err
		}
		if why != nil {
			res.reject(rl, e.Name, why)
			continue
		}
		rec, err := osv.Decode(data, eco, ad)
		if err != nil {
			if !isRejected(err) {
				return nil, err
			}
			res.reject(rl, e.Name, ruleRejection(err))
			if res.tooManyInvalid() {
				return res, nil
			}
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

// readRecordEntry reads one entry of the archive. An entry that crosses a bound of its own (size or ratio) is not read and comes
// back as a rejection; any other refusal of the reader ends the run.
func readRecordEntry(z *untrusted.ZipReader, e untrusted.ZipEntry, max int64) (data []byte, why *rejection, err error) {
	if err := z.CheckEntry(e, max); err != nil {
		if !untrusted.IsTooLarge(err) {
			return nil, nil, archiveError(err)
		}
		if e.Size > max {
			return nil, sizeRejection(e.Size, max), nil
		}
		return nil, ratioRejection(e.Size), nil
	}
	data, err = z.ReadEntry(e, max)
	if err != nil {
		return nil, nil, archiveError(err)
	}
	return data, nil, nil
}

// checkpoint is the resumable state of a job (R-VULN-004 "Resumption"): the ecosystems whose run ended, and for the one in
// progress the identity of the archive and the number of batches committed.
type checkpoint struct {
	Done      []string `json:"done,omitempty"`
	Ecosystem string   `json:"ecosystem,omitempty"`
	// Ident is the ETag of the archive (the SHA-256 of a mirrored item): a resumed run continues only when the archive it
	// downloads again has the same one.
	Ident string `json:"ident,omitempty"`
	// Cut is the digest of the batch cut ([cutDigest]), AdapterVersion the adapter that wrote the batches: the first pass leaves
	// out the entries that a bound rejects, so a batch index means another batch when the records that pass are other ones, and a
	// batch an older adapter wrote is not one a newer adapter may skip (Q-VULN-116).
	Cut            string `json:"cut,omitempty"`
	AdapterVersion int    `json:"adapterVersion,omitempty"`
	Batches        int    `json:"batches,omitempty"`
	Written        int    `json:"written,omitempty"`
}

// resumes reports whether the checkpoint was taken on the run that is starting: the same ecosystem, the same archive, the same
// batch cut and the same adapter version, with at least one batch committed.
func (cp *checkpoint) resumes(eco, ident, cut string, adapterVersion int) bool {
	return cp.Ecosystem == eco && cp.Ident == ident && cp.Cut == cut && cp.AdapterVersion == adapterVersion && cp.Batches > 0
}

// cutDigest is the SHA-256, in hex, of the identifiers of the records that passed the first pass, in the order of the batches,
// and of the end offset of each batch: two runs that read the same archive under the same bounds get the same digest, and
// any other limit, adapter or archive that cuts the batches elsewhere gets another.
func cutDigest(metas []entryMeta, ends []int) string {
	h := sha256.New()
	var n [8]byte
	for _, m := range metas {
		binary.BigEndian.PutUint64(n[:], uint64(len(m.id)))
		h.Write(n[:])
		h.Write([]byte(m.id))
	}
	for _, e := range ends {
		binary.BigEndian.PutUint64(n[:], uint64(e)) //nolint:gosec // an offset into a slice
		h.Write(n[:])
	}
	return hex.EncodeToString(h.Sum(nil))
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

// reportOlderAdapter logs the stored rows that an older adapter wrote and that this full sync did not write again, which are the
// ones a rejected record kept (Q-VULN-122): the first maxRejectLines one by one at WARN, then one line that counts the rest. Such a
// row is written when its record next passes. It is best effort: the report is not worth a failed run.
func (s *Service) reportOlderAdapter(ctx context.Context, eco string) {
	version := match.AdapterVersion(eco)
	var ids []string
	var total int
	err := s.o.DB.Read(ctx, func(db dbtx) (err error) {
		ids, total, err = store.Sync{}.OlderAdapter(ctx, db, eco, version, maxRejectLines)
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("the stored records of an older adapter were not counted", "source", store.SourceOSV, "ecosystem", eco, "error", err)
		}
		return
	}
	for _, id := range ids {
		s.log.Warn("a stored advisory record was written by an older adapter and is kept until it passes again",
			"source", store.SourceOSV, "ecosystem", eco, "id", escaped(id), "adapterVersion", version)
	}
	if total > len(ids) {
		s.log.Warn("further stored advisory records were written by an older adapter and are not logged one by one",
			"source", store.SourceOSV, "ecosystem", eco, "count", total-len(ids), "adapterVersion", version)
	}
}

// limitChanged reports that the configured max_record_size is not the one the last full sync completed with, and that sync
// rejected records: such a record may pass now, so the source's "unchanged" answers (a 304, an unchanged digest) and the
// incremental run, which fetches only what changed, do not apply and the archive is read again (Q-VULN-122).
func (s *Service) limitChanged(st store.SyncState) bool {
	return st.FullRejected > 0 && st.RecordLimit != int64(s.o.Config.Sources.OSV.MaxRecordSize)
}

// adapterStale reports whether the records stored for an ecosystem were imported with an older adapter than this release's, so
// that the next run must import them again, in full and unconditionally (Q-VULN-63).
func adapterStale(eco string, st store.SyncState) bool {
	return st.AdapterVersion < match.AdapterVersion(eco)
}

// gone lists the stored identifiers the archive no longer has. A rejected entry named after an identifier still has it: the
// stored row stays (Q-VULN-117).
func (r *scanResult) gone(stored []string) []string {
	seen := make(map[string]struct{}, len(r.metas)+len(r.kept))
	for _, m := range r.metas {
		seen[m.id] = struct{}{}
	}
	for _, id := range r.kept {
		seen[id] = struct{}{}
	}
	var gone []string
	for _, id := range stored {
		if _, ok := seen[id]; !ok {
			gone = append(gone, id)
		}
	}
	return gone
}

// invalidDetail is the detail of too_many_invalid_records: the measured share, the fixed limit and the first record that failed
// validation (Q-VULN-121). The reason is cleaned here, at the sink, whatever made it.
func (r *scanResult) invalidDetail() string {
	return fmt.Sprintf("%d of %d records were rejected by validation (%.2f %%), more than the fixed %d %%; the first: %s: %s",
		r.invalid, r.records, percent(r.invalid, r.records), invalidPercent, quoted(r.firstEntry), clean(r.firstReason, 200))
}

// shrinkDetail is the detail of shrink_refused: the measured share and the key that sets the limit with its value.
func shrinkDetail(gone, stored int, limit float64) string {
	return fmt.Sprintf("the archive would delete %d of %d stored records (%.2f %%), more than vulnerabilities.sources.osv.max_shrink (%g)",
		gone, stored, percent(gone, stored), limit)
}

// percent is part of whole in percent; 0 for an empty whole.
func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
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

	// The integrity guards, before anything is written: a truncated or tampered source changes nothing. Only the records that
	// failed validation count; the ones a bound of their own rejected are reported by the metric and its alert (Q-VULN-118).
	if scan.tooManyInvalid() {
		return res, problemf(errs.CodeTooManyInvalidRecords, scan.invalidDetail(), nil)
	}
	stored, err := s.storedIDs(ctx, in.eco)
	if err != nil {
		return res, err
	}
	gone := scan.gone(stored)
	if len(stored) > 0 && float64(len(gone)) > cfg.MaxShrink*float64(len(stored)) {
		if !p.AcceptShrink {
			return res, problemf(errs.CodeShrinkRefused, shrinkDetail(len(gone), len(stored), cfg.MaxShrink), nil)
		}
		res.AcceptedShrink = true
	}

	// Pass 2: the batches, from the checkpoint when the archive is the one it was taken on.
	in.force = adapterStale(in.eco, in.state)
	ends := batchEnds(scan.metas)
	start := 0
	cut, version := cutDigest(scan.metas, ends), match.AdapterVersion(in.eco)
	if cp.resumes(in.eco, in.arch.ident, cut, version) {
		start, res.Written = cp.Batches, cp.Written
	} else {
		*cp = checkpoint{Done: cp.Done, Ecosystem: in.eco, Ident: in.arch.ident, Cut: cut, AdapterVersion: version}
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
	s.analyzeAfterImport(ctx, in.eco, res.Written)

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
	s.reportOlderAdapter(ctx, in.eco)
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
	version := match.AdapterVersion(eco)
	var batch []store.Written
	entries := z.Entries()
	for _, m := range chunk {
		if st, ok := stored[m.id]; ok && !force && st.AdapterVersion == version && bytes.Equal(st.SHA256, m.sha[:]) && st.Modified.Equal(m.modified) {
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
		if err := (store.Sync{}).Upsert(ctx, db, eco, match.AdapterVersion(eco), batch); err != nil {
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

// analyzeMinWritten is the number of records a run writes before it refreshes the planner statistics of the advisory tables: the
// first import of an ecosystem and a resync after a long absence, not the few hundred records of a change file.
const analyzeMinWritten = 10000

// analyzeAfterImport refreshes the statistics of the tables the run filled, so that the rest of the run (the group recomputation, the
// known-exploited flags) and the evaluation it requests are planned on what the tables hold and not on the estimates of a table
// the autovacuum has not reached yet (Q-VULN-96). It is best effort: a failure is logged and the run goes on, because no statement of
// the sync depends on statistics (Q-VULN-94, Q-VULN-95); a drain or a lost lease ends the run at its next step.
func (s *Service) analyzeAfterImport(ctx context.Context, eco string, written int) {
	if written < analyzeMinWritten {
		return
	}
	start := s.now()
	if err := s.o.DB.Read(ctx, func(db dbtx) error { return s.st.Analyze(ctx, db) }); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("the statistics of the advisory tables were not refreshed after the import", "ecosystem", eco, "error", err)
		}
		return
	}
	s.log.Info("the statistics of the advisory tables were refreshed after the import", "ecosystem", eco, "records", written, "took", s.now().Sub(start).Round(time.Millisecond).String())
}
