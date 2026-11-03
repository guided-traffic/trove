package evaluate

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// complete reports whether the ecosystem's data is complete as the replica's cache says. It is for a caller that holds no
// transaction: a miss of the cache loads through the pool (the commit hook reads the state row on its own connection instead). A
// replica without the state cache reads as not complete, so the evaluation only adds (R-VULN-011).
func (s *Service) complete(ecosystem string) bool {
	return s.o.States != nil && s.o.States.State(ecosystem).Complete
}

func (s *Service) state(ecosystem string) advisory.DataState {
	if s.o.States == nil {
		return advisory.DataState{}
	}
	return s.o.States.State(ecosystem)
}

// view returns the view of a repository by key; false for a repository that does not exist or that the scanning does not
// apply to.
func (s *Service) view(ctx context.Context, key string) (repoView, repository.Repository, bool, error) {
	repo, err := s.o.Repos.ByKey(ctx, key)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
			return repoView{}, repository.Repository{}, false, nil
		}
		return repoView{}, repository.Repository{}, false, err
	}
	if repo.Lifecycle != repository.Active {
		return repoView{}, repo, false, nil
	}
	v, ok := viewOf(repo)
	return v, repo, ok, nil
}

// OnVersionCommitted implements [vuln.Evaluator]: it evaluates the version of the item a commit wrote, in the commit's
// transaction, after the format's OnCommit linked the item to its version (R-VULN-011). The cost is the lookup of the item's
// version, one indexed query on vuln_affected, the findings of the name in the repository and the state row of the ecosystem; findings are written in the
// same transaction. A version that is committed again (a re-fill) changes nothing the second time.
func (s *Service) OnVersionCommitted(ctx context.Context, tx meta.Tx, c vuln.CommittedVersion) error {
	if !s.o.Enabled {
		return nil
	}
	rv, _, ok, err := s.view(ctx, c.RepoKey)
	if err != nil || !ok || !rv.Settings.Enabled {
		return err
	}
	eco, known := s.eco[rv.Ecosystem]
	if !known {
		return nil
	}
	started := time.Now()
	ev, err := store.EvalOf(tx)
	if err != nil {
		return err
	}
	ver, ok, err := ev.ItemVersion(ctx, c.ItemID)
	if err != nil || !ok {
		return err
	}
	if ver.LeadItemID != 0 && ver.LeadItemID != c.ItemID {
		return nil // a companion file of a version the lead item's commit evaluates
	}
	ver.RepoID = rv.ID
	existing, err := ev.FindingsOfRepoNames(ctx, rv.ID, eco.Name, []string{ver.Name})
	if err != nil {
		return err
	}
	var ofVersion []store.Finding
	for _, f := range existing {
		if f.Version == ver.Version {
			ofVersion = append(ofVersion, f)
		}
	}
	trigger := c.Trigger
	if trigger == "" {
		trigger = vuln.TriggerPublish
	}
	// The data state is read on the commit's own connection: the replica's cache loads through the pool, and a commit that holds a
	// connection while it waits for another one starves the pool when commits run concurrently (the waiters of one row lock hold
	// the connections the load needs).
	complete, err := ev.DataComplete(ctx, eco.Name)
	if err != nil {
		return err
	}
	r := &run{
		s: s, tx: tx, ev: ev, eco: eco, trigger: trigger, canResolve: complete, inTx: ev,
		repos: func(id int64) (repoView, bool) { return rv, id == rv.ID },
	}
	if err := r.evaluateNames(ctx, []string{ver.Name}, []store.StoredVersion{ver}, ofVersion); err != nil {
		return err
	}
	r.finish(started)
	return nil
}

// OnVersionsRemoved implements [vuln.Evaluator]: the findings of the versions that went (delete, unpublish, cache eviction)
// resolve with version_removed, in the removing transaction (R-VULN-011). It runs before the format removes the version rows,
// so that the findings still point to them; a version whose lead item is still stored keeps its findings. A resolved finding
// keeps its identity: the version fetched again re-detects it with the earlier status.
func (s *Service) OnVersionsRemoved(ctx context.Context, tx meta.Tx, versionIDs []int64) error {
	if len(versionIDs) == 0 {
		return nil
	}
	ev, err := store.EvalOf(tx)
	if err != nil {
		return err
	}
	done, err := ev.ResolveRemovedVersions(ctx, versionIDs)
	if err != nil {
		return err
	}
	return s.resolvedEvents(ctx, ev, done, vuln.ResolvedVersionRemoved)
}

// EvaluateMiss implements [vuln.Evaluator]: the block a remote miss would carry once it was fetched, with nothing written
// (R-VULN-015 "Remote miss"). It evaluates the version the path maps to against the advisory data and applies the rule of the
// download: a finding of the version in the repository that resolved while blocked counts as blocked, since the fill would
// re-detect it so (R-VULN-012). The answer is the whole verdict of the download, `missing_data` included, so a caller does not
// add the data rule again. A repository that is not scanned, or whose upstream is not recognised, is no block.
func (s *Service) EvaluateMiss(ctx context.Context, m vuln.Miss) (vuln.Block, error) {
	if !s.o.Enabled {
		return vuln.Block{}, nil
	}
	rv, repo, ok, err := s.view(ctx, m.RepoKey)
	if err != nil || !ok || !rv.Settings.Enabled || rv.Class != repository.Remote {
		return vuln.Block{}, err // only a remote repository has a miss
	}
	eco, known := s.eco[rv.Ecosystem]
	if !known {
		return vuln.Block{}, nil
	}
	started := time.Now()
	if sc, _, err := s.scopeOf(ctx, rv, m.Name, nil); err != nil || sc != ScopeMatched {
		return vuln.Block{}, err
	}
	pol := repo.VulnerabilityPolicy(s.o.Enabled, s.o.Enforcement)
	data := s.state(eco.Name)
	var found map[string]*matched
	var existing []store.Finding
	var st stats
	err = s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		rows, err := ev.AffectedForNames(ctx, eco.Name, []string{m.Name})
		if err != nil {
			return err
		}
		found = matchVersion(eco.Adapter, groupAffected(rows)[m.Name], m.Version, &st)
		all, err := ev.FindingsOfRepoNames(ctx, rv.ID, eco.Name, []string{m.Name})
		if err != nil {
			return err
		}
		existing = existing[:0]
		for _, f := range all {
			if f.Version == m.Version {
				existing = append(existing, f)
			}
		}
		return nil
	})
	if err != nil {
		return vuln.Block{}, err
	}
	if s.o.Metrics != nil {
		s.o.Metrics.Evaluations.WithLabelValues(string(vuln.TriggerMiss)).Inc()
		s.o.Metrics.EvaluationDuration.WithLabelValues(string(vuln.TriggerMiss)).Observe(time.Since(started).Seconds())
		if st.unevaluable > 0 {
			s.o.Metrics.UnevaluableRanges.WithLabelValues(eco.Name).Add(float64(st.unevaluable))
		}
	}
	enforced := pol.Enforcement != vuln.EnforceReport
	var best *blocking
	consider := func(mm *matched, f *store.Finding) {
		status := effectiveStatus(f, mm)
		reason, blocks := pol.BlockReasonOf(mm.Kind, status)
		if !blocks {
			return
		}
		b := blocking{m: mm, reason: reason}
		if f != nil {
			b.id = f.ID
		}
		if best == nil || betterBlock(b, *best) {
			best = &b
		}
	}
	have := map[string]*store.Finding{}
	for i := range existing {
		have[existing[i].AdvisoryID] = &existing[i]
	}
	for _, id := range sortedIDs(found) {
		consider(found[id], have[id])
	}
	// A blocked finding that resolved with version_removed counts although no advisory matches now, while the data cannot
	// say that the advisory stopped applying: a restored installation has none (R-VULN-011, no resolution without data).
	if !s.complete(eco.Name) {
		for i := range existing {
			f := &existing[i]
			if found[f.AdvisoryID] == nil && f.Status == vuln.StatusResolved && f.ResolvedReason == vuln.ResolvedVersionRemoved &&
				f.BeforeResolution == vuln.StatusBlocked {
				consider(&matched{ID: f.AdvisoryID, Kind: f.Kind, Severity: f.Severity, KnownExploited: f.KnownExploited, ModifiedUs: f.AdvisoryModifiedU}, f)
			}
		}
	}
	if best != nil {
		return vuln.Block{Reason: best.reason, FindingID: best.id, AdvisoryID: best.m.ID, Enforced: enforced}, nil
	}
	return vuln.Verdict(pol, vuln.BlockRow{}, data), nil
}
