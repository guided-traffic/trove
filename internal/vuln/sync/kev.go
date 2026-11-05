package sync

import (
	"bytes"
	"context"
	"io"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/osv"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// kevPath is the file of the mirror repository (R-VULN-005).
const kevPath = "kev/known_exploited_vulnerabilities.json"

// RunKEV is the body of the job vuln-sync-kev (R-VULN-008): it fetches the catalog with If-None-Match (or reads it from the
// mirror repository in mode repository), validates it, replaces vuln_kev and recomputes the known-exploited flag of the
// advisories in one transaction, and queues the advisories whose flag changed for the evaluation.
func (s *Service) RunKEV(ctx context.Context, run Run, p Params) error {
	cfg := s.o.Config
	if !cfg.Enabled || cfg.Sources.KEV.Mode == "off" {
		return run.SetResult(map[string]string{"skipped": "the source is off"})
	}
	var at attempt
	res, err := guarded(ctx, s, store.SourceKEV, "", kindFull, &at, func() (KEVResult, error) { return s.syncKEV(ctx, p, &at) })
	if err != nil {
		if released(ctx) {
			return context.Cause(ctx)
		}
		if ctx.Err() != nil {
			s.recordEnd(ctx, store.SourceKEV, "", kindFull, at.at, context.Cause(ctx))
			return context.Cause(ctx)
		}
		s.recordFailure(ctx, store.SourceKEV, "", kindFull, at.at, err)
		return err
	}
	return run.SetResult(res)
}

// KEVResult is what a run of vuln-sync-kev did.
type KEVResult struct {
	Outcome string `json:"outcome"`
	Entries int    `json:"entries"`
	// Flagged is the number of advisories whose known-exploited flag or date changed.
	Flagged int    `json:"flagged"`
	Note    string `json:"note,omitempty"`
}

func (s *Service) syncKEV(ctx context.Context, p Params, at *attempt) (KEVResult, error) {
	cfg := s.o.Config.Sources.KEV
	var mark time.Time
	if err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		if err := s.st.Ensure(ctx, db, store.SourceKEV, ""); err != nil {
			return err
		}
		mark, err = s.st.Attempt(ctx, db, store.SourceKEV, "")
		return err
	}); err != nil {
		return KEVResult{}, err
	}
	at.at = mark
	var st store.SyncState
	if err := s.o.DB.Read(ctx, func(db q.DBTX) (err error) {
		st, _, err = s.st.Get(ctx, db, store.SourceKEV, "")
		return err
	}); err != nil {
		return KEVResult{}, err
	}
	var body []byte
	var etag string
	var mirror *mirrorFacts
	switch cfg.Mode {
	case "repository":
		item, err := s.stat(ctx, cfg.Repository, kevPath)
		if err != nil {
			return KEVResult{}, err
		}
		if item == nil {
			s.countRun(store.SourceKEV, "", kindFull, outcomeUnchanged)
			return KEVResult{Outcome: outcomeUnchanged, Note: kevPath + " is not in the mirror repository"}, nil
		}
		if st.Complete() && bytes.Equal(st.ArchiveSHA256, item.SHA256[:]) {
			return s.kevUnchanged(ctx)
		}
		if item.Size > osv.MaxKEVBytes {
			return KEVResult{}, problemf(errs.CodeArchiveTooLarge, "the catalog is larger than 16 MiB", nil)
		}
		rc, err := item.Open(ctx)
		if err != nil {
			return KEVResult{}, err
		}
		defer rc.Close()
		if body, err = io.ReadAll(io.LimitReader(rc, osv.MaxKEVBytes+1)); err != nil {
			return KEVResult{}, err
		}
		mirror = &mirrorFacts{sha256: item.SHA256, deployedBy: item.DeployedBy}
	default:
		ifNone := ""
		if st.Complete() {
			ifNone = st.ETag
		}
		dctx, cancel := context.WithTimeout(ctx, s.o.Config.Sources.OSV.Timeout)
		b, tag, notModified, err := s.fetchDocument(dctx, cfg.URL, ifNone, osv.MaxKEVBytes)
		cancel()
		if err != nil {
			return KEVResult{}, err
		}
		if notModified {
			return s.kevUnchanged(ctx)
		}
		body, etag = b, tag
	}
	entries, err := osv.DecodeKEV(body)
	if err != nil {
		return KEVResult{}, problemf(errs.CodeUpstreamError, "the catalog is not valid: "+err.Error(), err)
	}
	return s.applyKEV2(ctx, p, st, entries, etag, mirror)
}

// kevUnchanged ends a run that found the catalog as it was.
func (s *Service) kevUnchanged(ctx context.Context) (KEVResult, error) {
	now := s.now()
	var res KEVResult
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		cur, err := s.st.Lock(ctx, db, store.SourceKEV, "")
		if err != nil {
			return err
		}
		next := cur
		next.SyncedAt = now
		res = KEVResult{Outcome: outcomeUnchanged, Entries: cur.Records}
		return s.st.Save(ctx, db, next)
	})
	if err == nil {
		s.countRun(store.SourceKEV, "", kindFull, outcomeUnchanged)
	}
	return res, err
}

// applyKEV2 replaces the catalog and recomputes the flags of the advisories in one transaction (R-VULN-008).
func (s *Service) applyKEV2(ctx context.Context, p Params, st store.SyncState, entries []osv.KEVEntry, etag string, mirror *mirrorFacts) (KEVResult, error) {
	now := s.now()
	ecosystems, _ := s.ecosystems()
	rows := make([]store.KEV, 0, len(entries))
	cves := make([]string, 0, len(entries))
	for _, e := range entries {
		k := store.KEV{CVE: e.CVE, DateAdded: e.DateAdded.Format("2006-01-02"), Ransomware: e.Ransomware}
		if !e.DueDate.IsZero() {
			k.DueDate = e.DueDate.Format("2006-01-02")
		}
		rows = append(rows, k)
		cves = append(cves, e.CVE)
	}
	slices.Sort(cves)
	res := KEVResult{Entries: len(entries)}
	changedEco := map[string]int64{}
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		cur, err := s.st.Lock(ctx, db, store.SourceKEV, "")
		if err != nil {
			return err
		}
		if err := s.st.ReplaceKEV(ctx, db, rows); err != nil {
			return err
		}
		flagged := 0
		for _, eco := range ecosystems {
			if err := s.st.Ensure(ctx, db, store.SourceOSV, eco); err != nil {
				return err
			}
			cands, err := s.st.KEVCandidates(ctx, db, eco, cves)
			if err != nil {
				return err
			}
			var changed []string
			for len(cands) > 0 {
				chunk := cands[:min(kevChunk, len(cands))]
				cands = cands[len(chunk):]
				ids, err := s.st.ApplyKEV(ctx, db, eco, chunk)
				if err != nil {
					return err
				}
				changed = append(changed, ids...)
			}
			if len(changed) == 0 {
				continue
			}
			flagged += len(changed)
			if err := s.st.AddPending(ctx, db, eco, changed); err != nil {
				return err
			}
			epoch, err := s.st.BumpEpoch(ctx, db, eco)
			if err != nil {
				return err
			}
			changedEco[eco] = epoch
			if err := s.o.Changes.Notify(tx, cluster.AreaVulnData, vuln.DataChangePayloadVersion, vuln.DataChange{Ecosystem: eco, Epoch: epoch}); err != nil {
				return err
			}
		}
		next := cur
		next.State, next.ETag, next.SyncedAt, next.Records = store.StateComplete, etag, now, len(entries)
		next.ArchiveSHA256 = nil
		if mirror != nil {
			next.ArchiveSHA256 = mirror.sha256[:]
		}
		if err := s.st.Save(ctx, db, next); err != nil {
			return err
		}
		if flagged > 0 {
			if err := s.enqueueEvaluate(ctx, tx); err != nil {
				return err
			}
		}
		if mirror != nil {
			if err := s.recordImported(ctx, tx, p, store.SourceKEV, "", mirror.sha256[:], mirror, false); err != nil {
				return err
			}
		}
		res.Flagged = flagged
		return nil
	})
	if err != nil {
		return KEVResult{}, err
	}
	res.Outcome = outcomeUnchanged
	if res.Flagged > 0 || st.Records != len(entries) || !st.Complete() {
		res.Outcome = outcomeChanged
	}
	s.countRun(store.SourceKEV, "", kindFull, res.Outcome)
	s.count(store.SourceKEV, "", "written", len(entries))
	return res, nil
}
