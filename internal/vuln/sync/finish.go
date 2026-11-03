package sync

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/match"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

type (
	metaTx = meta.Tx
	dbtx   = q.DBTX
)

// mirrorFacts is what a run of mode repository knows of the item it imported: its digest and who deployed it (R-VULN-005).
type mirrorFacts struct {
	sha256     [32]byte
	deployedBy string
}

// finishFull ends a full sync in one transaction (R-VULN-004 full sync step 5): the state becomes complete with the archive's
// ETag, the watermark is the time the download started minus the overlap, the data epoch is raised when records changed, the
// change area vuln_data is notified, and a vuln-evaluate job is enqueued; the first complete import and a raised matcher
// version request the full evaluation of the ecosystem in the same transaction (R-VULN-011, Q-VULN-18).
func (s *Service) finishFull(ctx context.Context, p Params, in fullInput, res Result) (Result, error) {
	cfg := s.o.Config.Sources.OSV
	now := s.now()
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		cur, err := s.st.Lock(ctx, db, store.SourceOSV, in.eco)
		if err != nil {
			return err
		}
		next := cur
		first := !cur.Complete()
		changed := res.Written > 0 || res.Deleted > 0 || first
		next.State = store.StateComplete
		next.ETag = in.arch.etag
		next.ArchiveSHA256 = nil
		if in.mirror != nil {
			next.ArchiveSHA256 = in.mirror.sha256[:]
		}
		next.Watermark = in.started.Add(-cfg.Overlap)
		next.FullSyncedAt, next.SyncedAt = now, now
		if next.Records, err = s.st.Count(ctx, db, in.eco); err != nil {
			return err
		}
		next.Rejected, next.Unevaluable = res.Rejected, res.Unevaluable
		if changed {
			next.DataEpoch++
		}
		requestEval := first || cur.MatcherVersion < match.MatcherVersion
		if requestEval {
			next.MatcherVersion, next.EvalRequestedAt = match.MatcherVersion, now
		}
		if err := s.st.Save(ctx, db, next); err != nil {
			return err
		}
		if changed {
			if err := s.o.Changes.Notify(tx, cluster.AreaVulnData, vuln.DataChangePayloadVersion, vuln.DataChange{Ecosystem: in.eco, Epoch: next.DataEpoch}); err != nil {
				return err
			}
		}
		if changed || requestEval {
			if err := s.enqueueEvaluate(ctx, tx); err != nil {
				return err
			}
		}
		if in.mirror != nil || res.AcceptedShrink {
			if err := s.recordImported(ctx, tx, p, store.SourceOSV, in.eco, in.arch.sha256[:], in.mirror, res.AcceptedShrink); err != nil {
				return err
			}
		}
		res.Records = next.Records
		res.Outcome = outcomeUnchanged
		if changed {
			res.Outcome = outcomeChanged
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	s.countRun(store.SourceOSV, in.eco, kindFull, res.Outcome)
	return res, nil
}

// finishUnchanged ends a run that found nothing to apply: the source answered 304, or the change file lists nothing newer than
// the watermark. The run still counts as a success (synced_at), and a matcher version that was raised since the last run
// requests the full evaluation.
func (s *Service) finishUnchanged(ctx context.Context, eco, kind string) (Result, error) {
	now := s.now()
	res := Result{Ecosystem: eco, Kind: kind, Outcome: outcomeUnchanged}
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		cur, err := s.st.Lock(ctx, db, store.SourceOSV, eco)
		if err != nil {
			return err
		}
		next := cur
		next.SyncedAt = now
		requestEval := cur.Complete() && cur.MatcherVersion < match.MatcherVersion
		if requestEval {
			next.MatcherVersion, next.EvalRequestedAt = match.MatcherVersion, now
		}
		if err := s.st.Save(ctx, db, next); err != nil {
			return err
		}
		if requestEval {
			if err := s.enqueueEvaluate(ctx, tx); err != nil {
				return err
			}
		}
		res.Records = next.Records
		return nil
	})
	if err != nil {
		return res, err
	}
	s.countRun(store.SourceOSV, eco, kind, outcomeUnchanged)
	return res, nil
}

// finishIncremental ends an incremental run: the watermark becomes the newest row applied, the epoch is raised when records
// were written, and the remaining steps are those of a full sync without the deletion (R-VULN-004 incremental step 3).
func (s *Service) finishIncremental(ctx context.Context, eco string, res Result, watermark time.Time) (Result, error) {
	now := s.now()
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		cur, err := s.st.Lock(ctx, db, store.SourceOSV, eco)
		if err != nil {
			return err
		}
		next := cur
		changed := res.Written > 0
		if watermark.After(next.Watermark) {
			next.Watermark = watermark
		}
		next.SyncedAt = now
		if next.Records, err = s.st.Count(ctx, db, eco); err != nil {
			return err
		}
		next.Rejected = res.Rejected
		next.Unevaluable = cur.Unevaluable + res.Unevaluable
		if changed {
			next.DataEpoch++
		}
		requestEval := cur.Complete() && cur.MatcherVersion < match.MatcherVersion
		if requestEval {
			next.MatcherVersion, next.EvalRequestedAt = match.MatcherVersion, now
		}
		if err := s.st.Save(ctx, db, next); err != nil {
			return err
		}
		if changed {
			if err := s.o.Changes.Notify(tx, cluster.AreaVulnData, vuln.DataChangePayloadVersion, vuln.DataChange{Ecosystem: eco, Epoch: next.DataEpoch}); err != nil {
				return err
			}
		}
		if changed || requestEval {
			if err := s.enqueueEvaluate(ctx, tx); err != nil {
				return err
			}
		}
		res.Records = next.Records
		res.Outcome = outcomeUnchanged
		if changed {
			res.Outcome = outcomeChanged
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	s.countRun(store.SourceOSV, eco, kindIncremental, res.Outcome)
	return res, nil
}

// enqueueEvaluate enqueues the evaluation job in the transaction that made the data change visible (R-VULN-011). The kind is
// registered by the evaluation's work package; a build without it has nothing to evaluate with, which is logged and not
// an error of the sync.
func (s *Service) enqueueEvaluate(ctx context.Context, tx metaTx) error {
	if s.o.Jobs == nil {
		return nil
	}
	_, err := s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: vuln.KindEvaluate, DedupKey: vuln.DedupEvaluate})
	if errors.Is(err, jobs.ErrUnknownKind) {
		s.log.Warn("the advisory data changed and the job " + vuln.KindEvaluate + " is not registered in this build; nothing evaluates the change")
		return nil
	}
	return err
}

// recordImported records the audit event of an import of mode repository, or of a full sync that applied the shrink override
// (R-VULN-005, R-VULN-024): it names the principal that deployed the imported item.
func (s *Service) recordImported(ctx context.Context, tx metaTx, p Params, source, eco string, sha []byte, m *mirrorFacts, acceptShrink bool) error {
	d := map[string]any{"source": source, "ecosystem": eco, "sha256": hex.EncodeToString(sha)}
	if m != nil {
		d["sha256"] = hex.EncodeToString(m.sha256[:])
		if m.deployedBy != "" {
			d["deployedBy"] = m.deployedBy
		}
	}
	if acceptShrink {
		d["acceptShrink"] = true
	}
	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	actor := audit.Actor{Kind: "system", Name: "vulnerability-sync"}
	if p.Principal != "" {
		actor = audit.Actor{Kind: "user", ID: p.Principal}
	}
	return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventVulnSourceImported, Actor: actor, Outcome: audit.Success,
		Subject: audit.Subject{Kind: "vulnerability_source", Name: source + ":" + eco}, Details: body,
	})
}
