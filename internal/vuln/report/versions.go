package report

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// DataInfo is the state of one ecosystem's advisory data as the report reads it, from the source state row.
type DataInfo struct {
	Complete bool
	Stale    bool
	SyncedAt *time.Time
}

// dataStates reads the state of the advisory data per OSV ecosystem.
func (s *Service) dataStates(ctx context.Context) (map[string]DataInfo, []store.SourceRow, error) {
	rows, err := s.st.Sources(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]DataInfo{}
	now := s.now()
	for _, r := range rows {
		if r.Source != "osv" {
			continue
		}
		out[r.Ecosystem] = DataInfo{Complete: r.State == "complete", Stale: s.stale(r.SyncedAt, now), SyncedAt: r.SyncedAt}
	}
	return out, rows, nil
}

func (s *Service) stale(syncedAt *time.Time, now time.Time) bool {
	return syncedAt != nil && s.cfg.StaleAfter > 0 && now.Sub(*syncedAt) > s.cfg.StaleAfter
}

// evaluation derives the evaluation state of a repository's versions (R-VULN-001) with the scope function of the evaluation
// (R-VULN-010, [evaluate.ScopeOf]): name is the package of a version, "" for the repository as a whole, which a local
// repository's names count as public for.
func (s *Service) evaluation(ctx context.Context, r repository.Repository, name string, data map[string]DataInfo) vuln.Evaluation {
	p := r.VulnerabilityPolicy(s.cfg.Enabled, s.cfg.Enforcement)
	if !p.Enabled {
		return vuln.EvaluationNotCovered
	}
	// An error of the rule leaves the name matched, as the evaluation does: a rule that cannot read a claim withholds less.
	sc, _, _ := evaluate.ScopeOf(ctx, r, name, s.rule, s.cfg.PublicUpstreams)
	switch sc {
	case evaluate.ScopeInternal:
		return vuln.EvaluationInternal
	case evaluate.ScopeNotCovered:
		return vuln.EvaluationNotCovered
	}
	if data[p.Ecosystem].Complete {
		return vuln.EvaluationEvaluated
	}
	return vuln.EvaluationPendingData
}

// RepositoryVersion is what the scanning says of one version in one repository.
type RepositoryVersion struct {
	Repository  string
	Evaluation  vuln.Evaluation
	SyncedAt    *time.Time
	Blocked     bool
	BlockReason vuln.BlockReason
	Findings    []Finding
}

// Version returns, per readable repository that holds the version, the evaluation state, the block and the findings
// (R-VULN-020): the download status without downloading. found is false when the caller can read no repository that holds it.
func (s *Service) Version(ctx context.Context, sc Scope, format, name, version string) ([]RepositoryVersion, bool, error) {
	var repos []int64
	for _, h := range sc.Holders {
		if h.Version == version && !slices.Contains(repos, h.RepoID) {
			repos = append(repos, h.RepoID)
		}
	}
	if len(repos) == 0 {
		return nil, false, nil
	}
	slices.SortFunc(repos, func(a, b int64) int {
		switch {
		case sc.snap.Topo.Less(a, b):
			return -1
		case sc.snap.Topo.Less(b, a):
			return 1
		}
		return 0
	})
	data, _, err := s.dataStates(ctx)
	if err != nil {
		return nil, false, err
	}
	rows, err := s.st.List(ctx, sc.Scope, s.withPolicy(sc.snap, store.Filter{
		Formats: []string{format}, Packages: []string{name}, Versions: []string{version}, Repos: repos, HasRepos: true,
	}), store.Order{Sort: "severity", Desc: true}, nil, 1000*len(repos))
	if err != nil {
		return nil, false, err
	}
	fs := s.findings(sc.snap, rows)
	out := make([]RepositoryVersion, 0, len(repos))
	for _, id := range repos {
		r, _ := sc.snap.Repository(id)
		rv := RepositoryVersion{Repository: r.Name.String(), Evaluation: s.evaluation(ctx, r, name, data), Findings: []Finding{}}
		p := s.policy(sc.snap, id)
		for _, f := range fs {
			if f.RepoID == id {
				rv.Findings = append(rv.Findings, f)
			}
		}
		info := data[p.Ecosystem]
		covered := rv.Evaluation == vuln.EvaluationEvaluated || rv.Evaluation == vuln.EvaluationPendingData
		if rv.Evaluation == vuln.EvaluationEvaluated {
			rv.SyncedAt = info.SyncedAt
		}
		// The verdict of the download: the finding that blocks, else the missing data. An ecosystem whose data is complete is
		// asked as complete; a version that is not evaluated has no missing data to block on.
		state := advisory.DataState{Complete: info.Complete || !covered}
		b := vuln.Verdict(p, s.blockingRow(p, rv.Findings), state)
		rv.Blocked, rv.BlockReason = b.Active(), b.Reason
		out = append(out, rv)
	}
	return out, true, nil
}

// blockingRow picks the finding the download's verdict names: a malicious block before a block set in triage, as the
// lateral join of R-VULN-015 orders them.
func (s *Service) blockingRow(p vuln.Policy, fs []Finding) vuln.BlockRow {
	var best vuln.BlockRow
	for _, f := range fs {
		reason, ok := p.BlockReasonOf(f.Kind, f.Status)
		if !ok {
			continue
		}
		if best.FindingID == "" || reason == vuln.BlockMalicious {
			best = vuln.BlockRow{FindingID: f.ID, AdvisoryID: f.Advisory, Kind: f.Kind, Status: f.Status}
			if reason == vuln.BlockMalicious {
				break
			}
		}
	}
	return best
}

// Counts are open findings per severity.
type Counts struct{ Critical, High, Medium, Low, None, Unknown int64 }

// Add counts n findings of the severity.
func (c *Counts) Add(sev advisory.Severity, n int64) {
	switch sev {
	case advisory.SeverityCritical:
		c.Critical += n
	case advisory.SeverityHigh:
		c.High += n
	case advisory.SeverityMedium:
		c.Medium += n
	case advisory.SeverityLow:
		c.Low += n
	case advisory.SeverityNone:
		c.None += n
	default:
		c.Unknown += n
	}
}

// Total is the sum of the severities.
func (c Counts) Total() int64 { return c.Critical + c.High + c.Medium + c.Low + c.None + c.Unknown }

// VersionCounts is the open findings of one version.
type VersionCounts struct {
	Version string
	Open    Counts
}

// Package returns the versions of a package the caller can read with their open findings counted by severity; every readable
// version is listed, one without a finding with zero counts. found is false when the caller can read no version of it.
func (s *Service) Package(ctx context.Context, sc Scope, format, name string) ([]VersionCounts, bool, error) {
	if len(sc.Holders) == 0 {
		return nil, false, nil
	}
	counts, err := s.st.PackageCounts(ctx, sc.Scope, format, name)
	if err != nil {
		return nil, false, err
	}
	byVersion := map[string]*VersionCounts{}
	var order []string
	add := func(v string) *VersionCounts {
		if c, ok := byVersion[v]; ok {
			return c
		}
		c := &VersionCounts{Version: v}
		byVersion[v] = c
		order = append(order, v)
		return c
	}
	for _, h := range sc.Holders {
		add(h.Version)
	}
	for _, c := range counts {
		add(c.Version).Open.Add(c.Severity, c.N)
	}
	sort.Strings(order)
	out := make([]VersionCounts, 0, len(order))
	for _, v := range order {
		out = append(out, *byVersion[v])
	}
	return out, true, nil
}
