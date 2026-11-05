package sync

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln/match"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// RunOSV is the body of the jobs vuln-sync-osv (full false) and vuln-sync-osv-full (full true): it syncs every ecosystem in
// turn, one failing ecosystem not stopping the others, and returns the errors of the failed ones joined (R-VULN-004). The job
// is not retried within its slot (MaxAttempts 1); the next slot tries again.
//
// A context that ends with jobs.ErrReleased is a drain: the checkpoint is saved and the cause returned, so that the job is
// released with its attempt unchanged and another worker resumes it. A context that ends otherwise (a lost lease, a cancel)
// stops the run too, and the ecosystem it was syncing records what ended it; so does a panic, which then goes on to the
// worker (R-VULN-004 "Failure", Q-VULN-80).
func (s *Service) RunOSV(ctx context.Context, run Run, p Params, full bool) error {
	cfg := s.o.Config
	if !cfg.Enabled || cfg.Sources.OSV.Mode == "off" {
		return run.SetResult(map[string]string{"skipped": "the source is off"})
	}
	ecosystems, adapters := s.ecosystems()
	var cp checkpoint
	if _, err := run.LoadCheckpoint(&cp); err != nil {
		cp = checkpoint{}
	}
	results := make([]Result, 0, len(ecosystems))
	var errList []error
	for _, eco := range ecosystems {
		if slices.Contains(cp.Done, eco) {
			continue
		}
		var at attempt
		res, err := guarded(ctx, s, store.SourceOSV, eco, kindOf(Result{}, full), &at, func() (Result, error) {
			return s.syncOne(ctx, run, p, full, eco, adapters[eco], &cp, &at)
		})
		if err != nil {
			if released(ctx) {
				return context.Cause(ctx)
			}
			if ctx.Err() != nil {
				s.recordEnd(ctx, store.SourceOSV, eco, kindOf(res, full), at.at, context.Cause(ctx))
				return context.Cause(ctx)
			}
			s.recordFailure(ctx, store.SourceOSV, eco, kindOf(res, full), at.at, err)
			errList = append(errList, err)
			continue
		}
		results = append(results, res)
		cp = checkpoint{Done: append(cp.Done, eco)}
		if err := run.SaveCheckpoint(ctx, &cp); err != nil {
			return err
		}
	}
	if err := run.SetResult(results); err != nil {
		return err
	}
	return errors.Join(errList...)
}

func kindOf(res Result, full bool) string {
	switch {
	case res.Kind != "":
		return res.Kind
	case full:
		return kindFull
	}
	return kindIncremental
}

// syncOne runs one ecosystem: the mode picks the source, the state picks full or incremental. at receives the run's mark on
// the ecosystem's row once its attempt committed.
func (s *Service) syncOne(ctx context.Context, run Run, p Params, forceFull bool, eco string, ad match.Adapter, cp *checkpoint, at *attempt) (Result, error) {
	var mark time.Time
	if err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		if err := s.st.Ensure(ctx, db, store.SourceOSV, eco); err != nil {
			return err
		}
		mark, err = s.st.Attempt(ctx, db, store.SourceOSV, eco)
		return err
	}); err != nil {
		return Result{Ecosystem: eco}, err
	}
	at.at = mark
	var st store.SyncState
	if err := s.o.DB.Read(ctx, func(db q.DBTX) (err error) {
		st, _, err = s.st.Get(ctx, db, store.SourceOSV, eco)
		return err
	}); err != nil {
		return Result{Ecosystem: eco}, err
	}
	// What an earlier run that failed after some batches left is finished first: its alias groups, the epoch, the evaluation.
	if err := s.recoverTouched(ctx, eco, st); err != nil {
		return Result{Ecosystem: eco}, err
	}
	if s.o.Config.Sources.OSV.Mode == "repository" {
		return s.fullMirror(ctx, run, p, eco, ad, st, cp)
	}
	if !forceFull && st.Complete() && !st.Watermark.IsZero() && s.now().Sub(st.Watermark) <= MaxWatermarkAge && !adapterStale(eco, st) {
		res, escalate, err := s.incremental(ctx, eco, ad, st)
		if err != nil || !escalate {
			return res, err
		}
	}
	return s.fullOnline(ctx, run, p, eco, ad, st, cp)
}

// fullOnline is a full sync from the source's archive (R-VULN-004 full sync).
func (s *Service) fullOnline(ctx context.Context, run Run, p Params, eco string, ad match.Adapter, st store.SyncState, cp *checkpoint) (Result, error) {
	started := s.now()
	dctx, cancel := context.WithTimeout(ctx, s.o.Config.Sources.OSV.Timeout)
	ifNone := ""
	if st.Complete() && !adapterStale(eco, st) {
		ifNone = st.ETag
	}
	arch, notModified, err := s.downloadArchive(dctx, eco, ifNone)
	cancel()
	if err != nil {
		return Result{Ecosystem: eco, Kind: kindFull}, err
	}
	if notModified {
		return s.finishUnchanged(ctx, eco, kindFull)
	}
	defer arch.Close()
	return s.applyArchive(ctx, run, p, cp, fullInput{eco: eco, ad: ad, state: st, arch: arch, started: started})
}

// incremental is the sync from the change file (R-VULN-004 incremental). escalate is true when more records changed than
// full_resync_threshold, and the caller then runs a full sync instead.
func (s *Service) incremental(ctx context.Context, eco string, ad match.Adapter, st store.SyncState) (res Result, escalate bool, err error) {
	res = Result{Ecosystem: eco, Kind: kindIncremental}
	sink, err := s.changeRows(ctx, eco, st.Watermark, s.o.Config.Sources.OSV.FullResyncThreshold)
	if err != nil {
		return res, false, err
	}
	if sink.over {
		return res, true, nil
	}
	res.Rejected = sink.bad
	if len(sink.rows) == 0 && sink.bad == 0 {
		res, err = s.finishUnchanged(ctx, eco, kindIncremental)
		return res, false, err
	}
	// The newest row of an id wins; the rows are newest first.
	seen := map[string]bool{}
	var ids []string
	for _, r := range sink.rows {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		if !validRecordID(r.ID) {
			res.Rejected++
			continue
		}
		ids = append(ids, r.ID)
	}
	for len(ids) > 0 {
		chunk := ids[:min(BatchSize, len(ids))]
		ids = ids[len(chunk):]
		w, k, rej, err := s.fetchApply(ctx, eco, ad, chunk)
		if err != nil {
			return res, false, err
		}
		res.Written += w
		res.Skipped += k
		res.Rejected += rej
	}
	s.count(store.SourceOSV, eco, "written", res.Written)
	s.count(store.SourceOSV, eco, "skipped", res.Skipped)
	s.count(store.SourceOSV, eco, "rejected", res.Rejected)
	if err := s.finishGroups(ctx, eco, st.Complete()); err != nil {
		return res, false, err
	}
	var newest time.Time
	if len(sink.rows) > 0 {
		newest = sink.rows[0].Modified
	}
	res, err = s.finishIncremental(ctx, eco, res, newest)
	return res, false, err
}
