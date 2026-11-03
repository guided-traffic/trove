package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// JobRun is what the jobs need of the job framework's handle (*jobs.Run implements it): the checkpoint of an earlier attempt,
// the progress of the job API and the result.
type JobRun interface {
	LoadCheckpoint(v any) (bool, error)
	SaveCheckpoint(ctx context.Context, v any) error
	SaveProgress(ctx context.Context, done, total int64, unit string) error
	SetResult(v any) error
}

var _ JobRun = (*jobs.Run)(nil)

// ProgressUnit is the unit of the progress of the evaluation jobs (R-VULN-011).
const ProgressUnit = "versions"

// maxRescanRounds bounds how often a rescan runs again because its inputs changed while it ran.
const maxRescanRounds = 5

// evalCheckpoint is the resumable state of vuln-evaluate: the ecosystem of the full evaluation in progress, the value of its
// request read at the run's start and the cursor over the package names.
type evalCheckpoint struct {
	Ecosystem   string `json:"ecosystem,omitempty"`
	RequestedUs int64  `json:"requestedUs,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
	Done        int64  `json:"done,omitempty"`
}

// RunEvaluate is the handler of vuln-evaluate (R-VULN-011, Q-VULN-18): first the full evaluation of each ecosystem whose
// request is set, then the queued advisories in batches. The work lives in tables, so a coalesced enqueue loses nothing.
func (s *Service) RunEvaluate(ctx context.Context, run JobRun) error {
	if !s.o.Enabled {
		return run.SetResult(map[string]string{"skipped": "vulnerabilities.enabled is false"})
	}
	var cp evalCheckpoint
	if _, err := run.LoadCheckpoint(&cp); err != nil {
		return err
	}
	var requests []store.FullRequest
	if err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		requests, err = ev.FullEvaluations(ctx)
		return err
	}); err != nil {
		return err
	}
	if cp.Ecosystem != "" && !hasRequest(requests, cp.Ecosystem) {
		cp = evalCheckpoint{} // the request was served by another run
	}
	fullDone := 0
	for _, req := range requests {
		if cp.Ecosystem != "" && req.Ecosystem != cp.Ecosystem {
			continue
		}
		if cp.Ecosystem == "" {
			cp = evalCheckpoint{Ecosystem: req.Ecosystem, RequestedUs: req.RequestedUs}
		}
		ran, err := s.fullEvaluation(ctx, run, &cp)
		if err != nil {
			return err
		}
		if ran {
			fullDone++
		}
		cp = evalCheckpoint{}
	}
	advisories, err := s.evaluatePending(ctx, run)
	if err != nil {
		return err
	}
	return run.SetResult(map[string]int{"fullEvaluations": fullDone, "advisories": advisories})
}

func hasRequest(list []store.FullRequest, eco string) bool {
	for _, r := range list {
		if r.Ecosystem == eco {
			return true
		}
	}
	return false
}

// fullEvaluation evaluates every stored version of the ecosystem in batches of package names, with a checkpoint after each, and
// clears the request in the transaction of the last batch when it still holds the value read at the start. It runs only for an
// ecosystem whose data is complete: the request is set with that state, and an evaluation without data resolves nothing.
func (s *Service) fullEvaluation(ctx context.Context, run JobRun, cp *evalCheckpoint) (bool, error) {
	eco, ok := s.eco[cp.Ecosystem]
	if !ok {
		return false, nil // an ecosystem this build has no adapter for: the request stays for a build that has
	}
	var total int64
	var complete bool
	if err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		if complete, err = ev.DataComplete(ctx, eco.Name); err != nil {
			return err
		}
		total, err = ev.CountStoredVersions(ctx, eco.Formats, 0)
		return err
	}); err != nil {
		return false, err
	}
	if !complete {
		return false, nil
	}
	req := store.FullRequest{Ecosystem: cp.Ecosystem, RequestedUs: cp.RequestedUs}
	for {
		if err := ctx.Err(); err != nil {
			return false, context.Cause(ctx)
		}
		var names []string
		last := false
		started := time.Now()
		var versions int
		err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			ev, err := store.EvalOf(tx)
			if err != nil {
				return err
			}
			names, err = ev.PackageNamesPage(ctx, eco.Formats, 0, cp.Cursor, s.o.BatchNames)
			if err != nil {
				return err
			}
			last = len(names) < s.o.BatchNames
			if len(names) > 0 {
				versions, err = s.evaluateBatch(ctx, tx, ev, eco, names, 0, vuln.TriggerFull, true, nil, started)
				if err != nil {
					return err
				}
			}
			if last {
				_, err = ev.ClearFullEvaluation(ctx, req)
			}
			return err
		})
		if err != nil {
			return false, err
		}
		cp.Done += int64(versions)
		if len(names) > 0 {
			cp.Cursor = names[len(names)-1]
		}
		if last {
			return true, run.SaveProgress(ctx, total, total, ProgressUnit)
		}
		if err := run.SaveCheckpoint(ctx, cp); err != nil {
			return false, err
		}
		if err := run.SaveProgress(ctx, cp.Done, total, ProgressUnit); err != nil {
			return false, err
		}
	}
}

// evaluateBatch evaluates every stored version of the names in the repositories of the ecosystem (one repository when repoID is
// not 0) in the transaction. repos, when nil, is the snapshot. It returns the number of versions evaluated.
func (s *Service) evaluateBatch(ctx context.Context, tx meta.Tx, ev *store.Eval, eco Ecosystem, names []string, repoID int64, trigger vuln.Trigger,
	canResolve bool, repos repoLookup, started time.Time,
) (int, error) {
	if repos == nil {
		var err error
		if repos, err = s.snapshotLookup(ctx); err != nil {
			return 0, err
		}
	}
	versions, err := ev.StoredVersions(ctx, eco.Formats, names, repoID)
	if err != nil {
		return 0, err
	}
	var existing []store.Finding
	if repoID != 0 {
		existing, err = ev.FindingsOfRepoNames(ctx, repoID, eco.Name, names)
	} else {
		existing, err = ev.FindingsForNames(ctx, eco.Name, names)
	}
	if err != nil {
		return 0, err
	}
	r := &run{s: s, tx: tx, ev: ev, eco: eco, repos: repos, trigger: trigger, canResolve: canResolve}
	if err := r.evaluateNames(ctx, names, versions, existing); err != nil {
		return 0, err
	}
	r.finish(started)
	return r.st.versions, nil
}

// evaluatePending works off vuln_pending_evaluation in batches of advisories: every stored version of every name the queued
// advisories name or named, each batch one transaction that also deletes its queue rows (R-VULN-011). It returns the number
// of advisories worked off.
func (s *Service) evaluatePending(ctx context.Context, run JobRun) (int, error) {
	var total int64
	if err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		total, err = ev.PendingCount(ctx)
		return err
	}); err != nil {
		return 0, err
	}
	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return done, context.Cause(ctx)
		}
		n := 0
		started := time.Now()
		err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			ev, err := store.EvalOf(tx)
			if err != nil {
				return err
			}
			batch, err := ev.PendingBatch(ctx, s.o.BatchNames)
			if err != nil || len(batch) == 0 {
				return err
			}
			n = len(batch)
			byEco := map[string][]string{}
			for _, p := range batch {
				byEco[p.Ecosystem] = append(byEco[p.Ecosystem], p.AdvisoryID)
			}
			ecosystems := make([]string, 0, len(byEco))
			for e := range byEco {
				ecosystems = append(ecosystems, e)
			}
			sort.Strings(ecosystems)
			repos, err := s.snapshotLookup(ctx)
			if err != nil {
				return err
			}
			for _, name := range ecosystems {
				eco, ok := s.eco[name]
				if !ok {
					s.log.Warn("vulnerability evaluation: queued advisories of an ecosystem this build cannot evaluate are dropped", "ecosystem", name, "advisories", len(byEco[name]))
					continue
				}
				names, err := s.namesOf(ctx, ev, name, byEco[name])
				if err != nil {
					return err
				}
				if len(names) == 0 {
					continue
				}
				complete, err := ev.DataComplete(ctx, name)
				if err != nil {
					return err
				}
				if _, err := s.evaluateBatch(ctx, tx, ev, eco, names, 0, vuln.TriggerAdvisory, complete, repos, started); err != nil {
					return err
				}
			}
			return ev.DeletePending(ctx, batch)
		})
		if err != nil {
			return done, err
		}
		if n == 0 {
			return done, nil
		}
		done += n
		if err := run.SaveProgress(ctx, int64(done), max(total, int64(done)), ProgressUnit); err != nil {
			return done, err
		}
	}
}

// namesOf is the set of names a batch of queued advisories (record identifiers) concerns: the names the alias groups of the
// records name now, and the names that findings of the records' canonical identifiers (and of the identifiers themselves)
// exist for.
func (s *Service) namesOf(ctx context.Context, ev *store.Eval, ecosystem string, ids []string) ([]string, error) {
	set := map[string]bool{}
	groups, err := ev.GroupNames(ctx, ecosystem, ids)
	if err != nil {
		return nil, err
	}
	for _, n := range groups {
		set[n] = true
	}
	canon, err := ev.CanonicalOf(ctx, ecosystem, ids)
	if err != nil {
		return nil, err
	}
	findingIDs := append([]string(nil), ids...)
	for _, c := range canon {
		findingIDs = append(findingIDs, c)
	}
	named, err := ev.FindingNamesForAdvisories(ctx, ecosystem, findingIDs)
	if err != nil {
		return nil, err
	}
	for _, n := range named {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// rescanInputs are the scanning inputs of a repository, read from its row (R-VULN-011): the job compares them at its start and
// at its end.
type rescanInputs struct {
	view    repoView
	deleted bool
}

type rowConfig struct {
	Vulnerabilities *vuln.Settings `json:"vulnerabilities"`
	Remote          *struct {
		URL string `json:"url"`
	} `json:"remote"`
}

func (s *Service) readInputs(ctx context.Context, repoID int64) (rescanInputs, error) {
	var in rescanInputs
	err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		row, ok, err := ev.RepositoryInputs(ctx, repoID)
		if err != nil {
			return err
		}
		eco, known := s.byFmt[row.Format]
		if !ok || row.Lifecycle != string(repository.Active) || !known || repository.Class(row.Class) == repository.Virtual {
			in = rescanInputs{deleted: true}
			return nil
		}
		var cfg rowConfig
		if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
			return fmt.Errorf("the document of repository %d: %w", repoID, err)
		}
		v := repoView{ID: row.ID, Key: row.Key, Format: row.Format, Class: repository.Class(row.Class), Ecosystem: eco.Name, Settings: vuln.DefaultSettings()}
		if cfg.Vulnerabilities != nil {
			v.Settings = *cfg.Vulnerabilities
		}
		if cfg.Remote != nil {
			v.UpstreamHost = hostOf(cfg.Remote.URL)
		}
		in = rescanInputs{view: v}
		return nil
	})
	return in, err
}

// RunRescan is the handler of vuln-rescan (R-VULN-011): every stored version of one repository is evaluated in batches of
// package names; with `enabled: false` the repository's findings resolve with scanning_disabled. The inputs are read at the
// start and again after the last batch, and the job runs once more from the start when they differ, so a setting changed while
// it ran, whose enqueue returned the running job, is applied (Q-VULN-18).
func (s *Service) RunRescan(ctx context.Context, run JobRun, args json.RawMessage) error {
	var a vuln.RescanArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return fmt.Errorf("vuln-rescan: the arguments: %w", err)
	}
	if a.RepositoryID == 0 {
		return errors.New("vuln-rescan: the arguments name no repository")
	}
	if !s.o.Enabled {
		return run.SetResult(map[string]string{"skipped": "vulnerabilities.enabled is false"})
	}
	rounds, versions := 0, 0
	for ; rounds < maxRescanRounds; rounds++ {
		first, err := s.readInputs(ctx, a.RepositoryID)
		if err != nil {
			return err
		}
		if first.deleted {
			return run.SetResult(map[string]string{"skipped": "the repository is gone or not scanned"})
		}
		n, err := s.rescanOnce(ctx, run, first.view)
		if err != nil {
			return err
		}
		versions += n
		last, err := s.readInputs(ctx, a.RepositoryID)
		if err != nil {
			return err
		}
		if last.deleted || last.view == first.view {
			return run.SetResult(map[string]int{"versions": versions, "rounds": rounds + 1})
		}
	}
	return fmt.Errorf("vuln-rescan: the scanning inputs of repository %d kept changing for %d rounds", a.RepositoryID, maxRescanRounds)
}

// rescanOnce is one pass over a repository with the inputs it was given.
func (s *Service) rescanOnce(ctx context.Context, run JobRun, rv repoView) (int, error) {
	eco, ok := s.byFmt[rv.Format]
	if !ok {
		return 0, nil
	}
	if !rv.Settings.Enabled {
		err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			ev, err := store.EvalOf(tx)
			if err != nil {
				return err
			}
			done, err := ev.ResolveRepo(ctx, rv.ID)
			if err != nil {
				return err
			}
			return s.resolvedEvents(ctx, ev, done, vuln.ResolvedScanningDisabled)
		})
		return 0, err
	}
	var total int64
	var complete bool
	if err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		ev, err := store.EvalOf(tx)
		if err != nil {
			return err
		}
		if complete, err = ev.DataComplete(ctx, eco.Name); err != nil {
			return err
		}
		total, err = ev.CountStoredVersions(ctx, eco.Formats, rv.ID)
		return err
	}); err != nil {
		return 0, err
	}
	lookup := func(id int64) (repoView, bool) { return rv, id == rv.ID }
	cursor, done := "", 0
	for {
		if err := ctx.Err(); err != nil {
			return done, context.Cause(ctx)
		}
		var names []string
		var n int
		started := time.Now()
		err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			ev, err := store.EvalOf(tx)
			if err != nil {
				return err
			}
			names, err = ev.PackageNamesPage(ctx, eco.Formats, rv.ID, cursor, s.o.BatchNames)
			if err != nil || len(names) == 0 {
				return err
			}
			n, err = s.evaluateBatch(ctx, tx, ev, eco, names, rv.ID, vuln.TriggerRescan, complete, lookup, started)
			return err
		})
		if err != nil {
			return done, err
		}
		if len(names) == 0 {
			return done, nil
		}
		cursor, done = names[len(names)-1], done+n
		if err := run.SaveCheckpoint(ctx, evalCheckpoint{Ecosystem: eco.Name, Cursor: cursor, Done: int64(done)}); err != nil {
			return done, err
		}
		if err := run.SaveProgress(ctx, int64(done), max(total, int64(done)), ProgressUnit); err != nil {
			return done, err
		}
		if len(names) < s.o.BatchNames {
			return done, nil
		}
	}
}

// RunRetention is the handler of the task vuln-findings-retention (R-VULN-014): resolved findings older than
// vulnerabilities.findings.resolved_retention are deleted with their events, in batches. A finding re-detected before then is
// not resolved and stays.
func (s *Service) RunRetention(ctx context.Context, run JobRun) error {
	if s.o.Retention <= 0 {
		return run.SetResult(map[string]string{"skipped": "no retention configured"})
	}
	var deleted int64
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		var n int64
		if err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			ev, err := store.EvalOf(tx)
			if err != nil {
				return err
			}
			n, err = ev.DeleteResolved(ctx, s.o.Retention, s.o.BatchNames)
			return err
		}); err != nil {
			return err
		}
		deleted += n
		if n < int64(s.o.BatchNames) {
			return run.SetResult(map[string]int64{"deleted": deleted})
		}
	}
}
