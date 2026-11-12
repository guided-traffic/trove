package report

import (
	"context"
	"slices"
	"sort"
	"strings"
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
		if r.Source != store.SourceOSV && r.Source != store.SourceTrivy {
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
	// Images are the manifests an image version stands for, one per platform, each with how far it has been scanned; nil for a package.
	Images []ImageState
	// Grade is the version's grade in the repository (R-VULN-038).
	Grade VersionGrade
}

// imageFormat is the format whose versions are tags that stand for manifests (R-VULN-012).
const imageFormat = "oci"

// ImageState is one manifest of an image version in the report.
type ImageState struct {
	Digest, Platform string
	// State is pending, scanned or failed; ErrorCode says why a scan failed.
	State, ErrorCode string
	ScannedAt        *time.Time
	OSFamily, OSName string
	// EOSL says that the distribution no longer provides security updates, so that an absence of findings says little.
	EOSL     bool
	Packages int
}

func imagesOf(all []store.ImageManifest, repoID int64) []ImageState {
	out := []ImageState{}
	for _, m := range all {
		if m.RepoID != repoID {
			continue
		}
		state := m.State
		if state == "" {
			state = store.ImageScanPending
		}
		out = append(out, ImageState{Digest: m.Digest, Platform: m.Platform, State: state, ErrorCode: m.ErrorCode, ScannedAt: m.ScannedAt, OSFamily: m.OSFamily, OSName: m.OSName, EOSL: m.EOSL, Packages: m.Packages})
	}
	return out
}

// manifestDigests are the versions of the findings of what a reference stands for: the digest itself when the caller asked by digest.
func manifestDigests(all []store.ImageManifest, version string) []string {
	seen := map[string]bool{}
	out := []string{version}
	for _, m := range all {
		if !seen[m.Digest] {
			seen[m.Digest] = true
			out = append(out, m.Digest)
		}
	}
	return out
}

// imageEvaluation refines the state of an evaluated image version by how far its manifests have been scanned: an image whose scan has not
// finished is never reported as evaluated, so that nobody reads the absence of findings as clean (R-VULN-001).
func imageEvaluation(base vuln.Evaluation, images []ImageState) vuln.Evaluation {
	if base != vuln.EvaluationEvaluated {
		return base
	}
	if len(images) == 0 {
		return vuln.EvaluationPendingScan
	}
	state := vuln.EvaluationEvaluated
	for _, m := range images {
		switch m.State {
		case store.ImageScanFailed:
			return vuln.EvaluationScanFailed
		case store.ImageScanPending:
			state = vuln.EvaluationPendingScan
		}
	}
	return state
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
	versions := []string{version}
	var images []store.ImageManifest
	if format == imageFormat && s.images != nil {
		if images, err = s.images.Manifests(ctx, repos, name, version); err != nil {
			return nil, false, err
		}
		versions = manifestDigests(images, version)
	}
	rows, err := s.st.List(ctx, sc.Scope, s.withPolicy(sc.snap, store.Filter{
		Formats: []string{format}, Packages: []string{name}, Versions: versions, Repos: repos, HasRepos: true,
	}), store.Order{Sort: "severity", Desc: true}, nil, 1000*len(repos))
	if err != nil {
		return nil, false, err
	}
	if format == imageFormat {
		if rows, err = s.st.WithComponents(ctx, rows); err != nil {
			return nil, false, err
		}
	}
	fs := s.findings(sc.snap, rows)
	open, err := s.versionCounts(ctx, sc, format, name, version, repos)
	if err != nil {
		return nil, false, err
	}
	out := make([]RepositoryVersion, 0, len(repos))
	for _, id := range repos {
		r, _ := sc.snap.Repository(id)
		rv := RepositoryVersion{Repository: r.Name.String(), Evaluation: s.evaluation(ctx, r, name, data), Findings: []Finding{}}
		if format == imageFormat {
			rv.Images = imagesOf(images, id)
			rv.Evaluation = imageEvaluation(rv.Evaluation, rv.Images)
		}
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
		rv.Grade = gradeIn(rv.Repository, rv.Evaluation, open[id], rv.Images, info.SyncedAt)
		out = append(out, rv)
	}
	return out, true, nil
}

// versionCounts counts the open findings of the version per repository for its grade, from the counts and not from the listed
// findings, which stop at 1,000 per repository: a tag of an image counts the findings of every manifest it stands for, a digest its own.
func (s *Service) versionCounts(ctx context.Context, sc Scope, format, name, version string, repos []int64) (map[int64]Counts, error) {
	pair := []store.NameVersion{{Name: name, Version: version}}
	f := store.Filter{Repos: repos, HasRepos: true}
	var counts []store.GradeCount
	var err error
	if format == imageFormat && !strings.HasPrefix(version, "sha256:") {
		counts, err = s.st.ImageGradeCounts(ctx, sc.Scope, pair, f)
	} else {
		counts, err = s.st.GradeCounts(ctx, sc.Scope, format, pair, f)
	}
	if err != nil {
		return nil, err
	}
	out := map[int64]Counts{}
	for _, c := range counts {
		o := out[c.RepoID]
		o.Add(c.Severity, c.N)
		out[c.RepoID] = o
	}
	return out, nil
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

// VersionGrades is the grade of one version, the worst of the repositories that hold it.
type VersionGrades struct {
	Version string
	Grade   VersionGrade
}

// Package returns the versions of a package the caller can read, each with its grade over the repositories of f that hold it
// (R-VULN-039); every readable version is listed. found is false when the caller can read no version of it.
func (s *Service) Package(ctx context.Context, sc Scope, format, name string, f store.Filter) ([]VersionGrades, bool, error) {
	if len(sc.Holders) == 0 {
		return nil, false, nil
	}
	var holders []store.Candidate
	var order []string
	seen := map[string]bool{}
	for _, h := range sc.Holders {
		if f.HasRepos && !slices.Contains(f.Repos, h.RepoID) {
			continue
		}
		holders = append(holders, h)
		if !seen[h.Version] {
			seen[h.Version] = true
			order = append(order, h.Version)
		}
	}
	gs, err := s.grades(ctx, sc, format, holders, f)
	if err != nil {
		return nil, false, err
	}
	sort.Strings(order)
	out := make([]VersionGrades, 0, len(order))
	for _, v := range order {
		out = append(out, VersionGrades{Version: v, Grade: gs[gradeKey{Name: name, Version: v}]})
	}
	return out, true, nil
}

// PackageListKey identifies one package of a page of the packages list.
func PackageListKey(format, name string) string { return format + "\x1f" + name }
