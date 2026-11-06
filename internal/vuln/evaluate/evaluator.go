package evaluate

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

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
func (s *Service) view(ctx context.Context, name qname.Repo) (repoView, repository.Repository, bool, error) {
	repo, err := s.o.Repos.ByName(ctx, name)
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

// viewOfInputs builds the view of a repository the scanning applies to from the repository row read in the transaction: false
// for one that is gone, not active, virtual or of a format without the adapter.
func (s *Service) viewOfInputs(row store.RepoInputs, found bool) (repoView, Ecosystem, bool, error) {
	if !found || row.Lifecycle != string(repository.Active) || repository.Class(row.Class) == repository.Virtual {
		return repoView{}, Ecosystem{}, false, nil
	}
	eco, known := s.byFmt[row.Format]
	if !known {
		return repoView{}, Ecosystem{}, false, nil
	}
	v, err := viewOfRow(row, eco)
	return v, eco, err == nil, err
}

// OnVersionCommitted implements [vuln.Evaluator]: it evaluates the version of the item a commit wrote, in the commit's
// transaction, after the format's OnCommit linked the item to its version (R-VULN-011). It reads in one round trip and writes in
// one (Q-VULN-82): the repository row, the item's version, the findings of its package in the repository, the ecosystem's state
// row, the affected entries of the package and the namespace claims that can cover it are sent as one batch, the package named
// by the item inside each statement; the claimed-name matches an internal name gets are written in a second. Findings are
// written in the same transaction. A version that is committed again (a re-fill) changes nothing the second time.
func (s *Service) OnVersionCommitted(ctx context.Context, tx meta.Tx, c vuln.CommittedVersion) error {
	if !s.o.Enabled {
		return nil
	}
	eco, known := s.byFmt[c.Format]
	if !known {
		return nil // a format without the adapter: nothing to read
	}
	ev, err := store.EvalOf(tx)
	if err != nil {
		return err
	}
	// Nothing here takes a second pool connection: the commit holds one, and concurrent commits that wait for one row lock hold the
	// connections a cache load would need. The repository row, the claims and the data state are read in the transaction; the
	// replica's snapshot, claim index and state cache load through the pool on a miss, so they are not read.
	started := time.Now()
	var (
		row      store.RepoInputs
		rowOK    bool
		ver      store.StoredVersion
		verOK    bool
		existing []store.Finding
		complete bool
		affected []store.AffectedRow
		holders  = make([][]store.ClaimHolder, len(eco.Formats))
	)
	steps := []func(*store.Eval) error{
		func(e *store.Eval) (err error) { row, rowOK, err = e.RepositoryInputs(ctx, c.RepoID); return err },
		func(e *store.Eval) (err error) { ver, verOK, err = e.ItemVersion(ctx, c.ItemID); return err },
		func(e *store.Eval) (err error) {
			existing, err = e.FindingsOfRepoItem(ctx, c.RepoID, eco.Name, c.ItemID)
			return err
		},
		func(e *store.Eval) (err error) { complete, err = e.DataComplete(ctx, eco.Name); return err },
		func(e *store.Eval) (err error) {
			affected, err = e.AffectedForItem(ctx, eco.Name, c.ItemID)
			return err
		},
	}
	for i, f := range eco.Formats {
		steps = append(steps, func(e *store.Eval) (err error) { holders[i], err = e.ClaimHoldersForItem(ctx, f, c.ItemID); return err })
	}
	if err := ev.Pipeline(ctx, steps...); err != nil {
		return err
	}
	rv, rowEco, ok, err := s.viewOfInputs(row, rowOK)
	if err != nil || !ok || !rv.Settings.Enabled || !verOK {
		return err
	}
	if rowEco.Name != eco.Name {
		return errs.E(errs.Internal, errs.CodeInternal, "vuln evaluate: the commit names format "+c.Format+" and the repository row holds another")
	}
	if ver.LeadItemID != 0 && ver.LeadItemID != c.ItemID {
		return nil // a companion file of a version the lead item's commit evaluates
	}
	ver.RepoID = rv.ID
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
	claims := newTxClaims(ev)
	for i, f := range eco.Formats {
		if err := claims.prime(f, ver.Name, holders[i]); err != nil {
			return err
		}
	}
	r := &run{
		s: s, tx: tx, ev: ev, eco: eco, trigger: trigger, canResolve: complete, rule: ClaimRule{Claims: claims}, single: true,
		repos:    func(id int64) (repoView, bool) { return rv, id == rv.ID },
		affected: &affected,
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
	rv, repo, ok, err := s.view(ctx, m.Repo)
	if err != nil || !ok || !rv.Settings.Enabled || rv.Class != repository.Remote {
		return vuln.Block{}, err // only a remote repository has a miss
	}
	eco, known := s.eco[rv.Ecosystem]
	if !known {
		return vuln.Block{}, nil
	}
	started := time.Now()
	if sc, _, err := s.scopeOf(ctx, rv, m.Name, s.rule); err != nil || sc != ScopeMatched {
		return vuln.Block{}, err
	}
	pol := repo.VulnerabilityPolicy(s.o.Enabled, s.o.Enforcement)
	in, st, err := s.readMiss(ctx, eco, rv.ID, m)
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
	return missVerdict(pol, in, s.state(eco.Name), s.complete(eco.Name)), nil
}

// missInput is what the evaluation of a miss reads: the advisories that affect the version, the findings of the version in the
// repository and where the advisories of findings that are no canonical identifier any more went.
type missInput struct {
	found    map[string]*matched
	existing []store.Finding
	rekey    map[string]string
}

// readMiss reads the input of a miss in one transaction; it writes nothing.
func (s *Service) readMiss(ctx context.Context, eco Ecosystem, repoID int64, m vuln.Miss) (missInput, stats, error) {
	var in missInput
	var st stats
	err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		rows, err := ev.AffectedForNames(ctx, eco.Name, []string{m.Name})
		if err != nil {
			return err
		}
		in.found = matchVersion(eco.Adapter, groupAffected(rows)[m.Name], m.Version, &st)
		all, err := ev.FindingsOfRepoNames(ctx, repoID, eco.Name, []string{m.Name})
		if err != nil {
			return err
		}
		in.existing = in.existing[:0]
		var stray []string
		for _, f := range all {
			if f.Version == m.Version {
				in.existing = append(in.existing, f)
				if in.found[f.AdvisoryID] == nil {
					stray = append(stray, f.AdvisoryID)
				}
			}
		}
		// A finding of an advisory that joined another group since (even a resolved one, a blocked finding that was evicted)
		// stands for the group's advisory: the fill's evaluation re-keys it (Q-VULN-65).
		if len(stray) > 0 {
			in.rekey, err = ev.RekeyTargets(ctx, eco.Name, stray)
		}
		return err
	})
	return in, st, err
}

// missVerdict is the verdict of a remote miss over what was read: the finding of the strongest status of each advisory group of the
// version decides, and the data rule answers when none blocks. complete says whether the ecosystem's data is complete.
func missVerdict(pol vuln.Policy, in missInput, data advisory.DataState, complete bool) vuln.Block {
	enforced := pol.Enforcement != vuln.EnforceReport
	var best *blocking
	consider := func(mm *matched, f *store.Finding) {
		reason, blocks := pol.BlockReasonOf(mm.Kind, effectiveStatus(f, mm))
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
	have := map[string][]*store.Finding{}
	for i := range in.existing {
		f := &in.existing[i]
		id := f.AdvisoryID
		if in.found[id] == nil && in.found[in.rekey[id]] != nil {
			id = in.rekey[id]
		}
		have[id] = append(have[id], f)
	}
	for _, id := range sortedIDs(in.found) {
		// Several findings of one group stand for one: the one with the strongest status decides, as the merge keeps it.
		var strongest *store.Finding
		for _, f := range have[id] {
			if strongest == nil || strength(effectiveStatus(f, in.found[id])) > strength(effectiveStatus(strongest, in.found[id])) {
				strongest = f
			}
		}
		consider(in.found[id], strongest)
	}
	// A blocked finding that resolved with version_removed counts although no advisory matches now, while the data cannot
	// say that the advisory stopped applying: a restored installation has none (R-VULN-011, no resolution without data).
	if !complete {
		for i := range in.existing {
			f := &in.existing[i]
			if in.found[f.AdvisoryID] == nil && in.found[in.rekey[f.AdvisoryID]] == nil && f.Status == vuln.StatusResolved &&
				f.ResolvedReason == vuln.ResolvedVersionRemoved && f.BeforeResolution == vuln.StatusBlocked {
				consider(&matched{ID: f.AdvisoryID, Kind: f.Kind, Severity: f.Severity, KnownExploited: f.KnownExploited, ModifiedUs: f.AdvisoryModifiedU}, f)
			}
		}
	}
	if best != nil {
		return vuln.Block{Reason: best.reason, FindingID: best.id, AdvisoryID: best.m.ID, Enforced: enforced}
	}
	return vuln.Verdict(pol, vuln.BlockRow{}, data)
}
