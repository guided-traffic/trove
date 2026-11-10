// Package report is the read side of the vulnerability scanning (R-VULN-019 to R-VULN-021): the findings list with its
// filters, sorts and cursor, the detail with its history, the advisory view, the per-version and per-package views, the CSV
// export, the summary, the trend, the sources and the claimed-name matches.
//
// Every answer is narrowed to the findings the caller can read, in the statement that selects or counts them (R-VULN-019):
// the decision point classifies the repositories (authz.ReadScope), a repository the caller may read whole is passed to the
// statements by key, and the versions the caller may read in the others one by one, as internal/packages/view does for the
// package list. A cursor grants nothing: every page builds the scope again. The block a finding causes (`blocked`,
// `blockReason`) is computed with vuln.Verdict over the repository's current policy, the rule the download applies.
package report

import (
	"context"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// DefaultMaxScopeRows bounds the versions whose read the decision point decides one by one for a request; a request above it
// is refused rather than answered with a partial set, because a partial set would make a count wrong without saying so.
const DefaultMaxScopeRows = 200_000

// Config is the part of the `vulnerabilities` section the report reads.
type Config struct {
	// Enabled and Enforcement are vulnerabilities.enabled and vulnerabilities.enforcement.
	Enabled     bool
	Enforcement vuln.Enforcement
	// StaleAfter is vulnerabilities.stale_after.
	StaleAfter time.Duration
	// ExportMaxRows is vulnerabilities.export.max_rows.
	ExportMaxRows int
	// PublicUpstreams is vulnerabilities.matching.public_upstreams: per format ID the hosts of the public registries.
	PublicUpstreams map[string][]string
	// OSVMode and KEVMode are the modes of the sources (online, repository, off).
	OSVMode, KEVMode string
	// OSVRepository and KEVRepository are the mirror repositories of the mode repository.
	OSVRepository, KEVRepository string
	// Ecosystems are the ecosystems the OSV source imports.
	Ecosystems []string
	// MaxScopeRows overrides DefaultMaxScopeRows; zero keeps it.
	MaxScopeRows int
}

// Options configure [New].
type Options struct {
	Store        *store.API
	Repositories repository.Reader
	Config       Config
	// Advisories answers which names are internal (R-VULN-010) for the evaluation state of a version in a local repository;
	// nil treats every name as public.
	Advisories vuln.Advisories
	// Rule is the internal-names rule of R-VULN-010 that the evaluation and the client audits apply; the views of a version
	// and the claimed-name matches use it, so that they say what a download does (Q-VULN-70). A zero rule has no claims.
	Rule evaluate.ClaimRule
	// Images reads how far the manifests of an image version have been scanned; nil reports none.
	Images ImageReader
	// Now returns the current time; nil is time.Now.
	Now func() time.Time
}

// ImageReader is what the version view reads of the image scanning; *store.Images implements it.
type ImageReader interface {
	Manifests(ctx context.Context, repos []int64, name, tag string) ([]store.ImageManifest, error)
}

// Service is the report.
type Service struct {
	st   *store.API
	repo repository.Reader
	cfg  Config
	adv  vuln.Advisories
	rule evaluate.ClaimRule
	// images is nil while the image scanning is not wired.
	images ImageReader
	now    func() time.Time
}

// New returns the report.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Repositories == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "vuln report: the store and the repository snapshot are required")
	}
	s := &Service{st: o.Store, repo: o.Repositories, cfg: o.Config, adv: o.Advisories, rule: o.Rule, images: o.Images, now: o.Now}
	if s.now == nil {
		s.now = time.Now
	}
	if s.cfg.MaxScopeRows <= 0 {
		s.cfg.MaxScopeRows = DefaultMaxScopeRows
	}
	if s.cfg.ExportMaxRows <= 0 {
		s.cfg.ExportMaxRows = 100_000
	}
	return s, nil
}

// Snapshot is the repositories of the replica's snapshot with their topology, read once per request. The topology is keyed by
// the repositories' database IDs, the key of the statements and of the rows they return (R-VULN-028).
type Snapshot struct {
	Repos []repository.Repository
	Topo  view.Topology
}

// Snapshot reads the repositories.
func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	repos, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	return newSnapshot(repos), nil
}

func newSnapshot(repos []repository.Repository) *Snapshot {
	return &Snapshot{Repos: repos, Topo: view.NewTopology(repos)}
}

// Repository returns a repository of the snapshot by its database ID.
func (sn *Snapshot) Repository(id int64) (repository.Repository, bool) { return sn.Topo.Repository(id) }

// IDs returns the IDs of every repository of the snapshot, the physical ones first, for the request's authorization index.
func (sn *Snapshot) IDs() []int64 { return append(sn.Topo.Physical(), sn.Topo.Virtual()...) }

// Caller is what the decision point says about the caller: the classification of the physical repositories, and the virtual
// repositories the caller may read.
type Caller struct {
	Reads           authz.ReadScope
	VirtualReadable []int64
}

// Scope is what a caller may read, with the snapshot it was built on.
type Scope struct {
	store.Scope
	snap *Snapshot
	// Readable are the physical repositories the caller may read at least in part, sorted.
	Readable []int64
	// Holders are, for a package view, the (repository, version) pairs of the package the caller may read.
	Holders []store.Candidate
	virtual map[int64]bool
}

func tooBroad() error {
	return errs.E(errs.Invalid, errs.CodeInvalid, "The repositories you may read only in part cover more versions than one request decides; narrow it by format, repository or name.")
}

// build classifies the physical repositories and decides the candidates of the ones read in part. load returns the candidate
// triples of the given repository keys; withWhole asks for the candidates of the repositories read whole too, which a package
// view needs to know who holds the version.
//
// A caller whose decisions do not look at the version (authz.VersionIndependent) gets the packages of the others decided once
// each, from loadPackages (Q-VULN-81): the statement reads one pair per package and not one triple per version, the decision is
// made per package, and the statements that narrow by scope compare the pair, so a request costs by the packages the caller
// reads and not by the versions they have. loadPackages is nil where the versions themselves are needed (the package views).
func (s *Service) build(sn *Snapshot, c Caller, formats []string, withWhole bool,
	load func(repoIDs []int64, max int) ([]store.Candidate, error),
	loadPackages func(repoIDs []int64, max int) ([]store.Pair, int, error),
) (Scope, error) {
	sc := Scope{snap: sn, virtual: map[int64]bool{}}
	for _, v := range c.VirtualReadable {
		sc.virtual[v] = true
	}
	whole := map[int64]bool{}
	var loadIDs []int64
	for _, id := range sn.Topo.Physical() {
		r, _ := sn.Topo.Repository(id)
		if len(formats) > 0 && !slices.Contains(formats, r.Format) {
			continue
		}
		switch shape := c.Reads.Shape(r.Name); {
		case shape == authz.ReadAll && !sn.Topo.Restricts(id):
			sc.Whole = append(sc.Whole, id)
			whole[id] = true
			sc.Readable = append(sc.Readable, id)
			if withWhole {
				loadIDs = append(loadIDs, id)
			}
		case shape != authz.ReadNone:
			sc.Readable = append(sc.Readable, id)
			loadIDs = append(loadIDs, id)
		}
	}
	if load == nil || len(loadIDs) == 0 {
		return sc, nil
	}
	if vi, ok := c.Reads.(authz.VersionIndependent); ok && vi.VersionIndependent() && loadPackages != nil && !withWhole {
		return s.buildPackages(sn, c, sc, loadIDs, loadPackages)
	}
	cands, err := load(loadIDs, s.cfg.MaxScopeRows+1)
	if err != nil {
		return Scope{}, err
	}
	if len(cands) > s.cfg.MaxScopeRows {
		return Scope{}, tooBroad()
	}
	decided := make(map[store.Candidate]bool, len(cands))
	for _, cd := range cands {
		if _, done := decided[cd]; done {
			continue
		}
		if whole[cd.RepoID] {
			decided[cd] = true
			sc.Holders = append(sc.Holders, cd)
			continue
		}
		name := sn.Topo.Name(cd.RepoID)
		ok := sn.Topo.Admits(cd.RepoID, cd.Package) && (c.Reads.Shape(name) == authz.ReadAll || c.Reads.MayRead(name, cd.Package, cd.Version))
		decided[cd] = ok
		if ok {
			sc.Rows = append(sc.Rows, store.RowEntry(cd.RepoID, cd.Package, cd.Version))
			sc.Holders = append(sc.Holders, cd)
		}
	}
	return sc, nil
}

// buildPackages decides the packages of the repositories read in part once each (the decision does not depend on the version).
func (s *Service) buildPackages(sn *Snapshot, c Caller, sc Scope, loadIDs []int64,
	loadPackages func(repoIDs []int64, max int) ([]store.Pair, int, error),
) (Scope, error) {
	pairs, findings, err := loadPackages(loadIDs, s.cfg.MaxScopeRows+1)
	if err != nil {
		return Scope{}, err
	}
	if findings > s.cfg.MaxScopeRows {
		return Scope{}, tooBroad()
	}
	for _, p := range pairs {
		name := sn.Topo.Name(p.RepoID)
		if sn.Topo.Admits(p.RepoID, p.Package) && (c.Reads.Shape(name) == authz.ReadAll || c.Reads.MayRead(name, p.Package, "")) {
			sc.Packages = append(sc.Packages, store.PackageEntry(p.RepoID, p.Package))
		}
	}
	return sc, nil
}

// Scope builds what the caller may read of the findings the filter selects: the candidates of the repositories read in part
// are the versions that have such a finding.
func (s *Service) Scope(ctx context.Context, sn *Snapshot, c Caller, f store.Filter) (Scope, error) {
	return s.build(sn, c, f.Formats, false, func(repoIDs []int64, max int) ([]store.Candidate, error) {
		return s.st.Candidates(ctx, narrowIDs(repoIDs, f), f, max)
	}, func(repoIDs []int64, max int) ([]store.Pair, int, error) {
		return s.st.CandidatePackages(ctx, narrowIDs(repoIDs, f), f, max)
	})
}

func narrowIDs(ids []int64, f store.Filter) []int64 {
	if !f.HasRepos {
		return ids
	}
	var out []int64
	for _, id := range ids {
		if slices.Contains(f.Repos, id) {
			out = append(out, id)
		}
	}
	return out
}

// PackageScope builds what the caller may read of one package (every version when version is ""), and the repositories that
// hold it, for the package views.
func (s *Service) PackageScope(ctx context.Context, sn *Snapshot, c Caller, format, name, version string) (Scope, error) {
	return s.build(sn, c, []string{format}, true, func(repoIDs []int64, max int) ([]store.Candidate, error) {
		return s.st.PackageCandidates(ctx, repoIDs, format, name, version, max)
	}, nil)
}

// VirtualContaining returns the readable virtual repositories whose effective member list contains the repository, sorted by name.
func (sc Scope) VirtualContaining(repo int64) []qname.Repo {
	var out []int64
	for _, v := range sc.snap.Topo.Containers(repo) {
		if sc.virtual[v] {
			out = append(out, v)
		}
	}
	return sc.snap.Topo.Names(out)
}

// ExpandFor maps the repositories of a filter to the physical repositories they stand for. A virtual repository the caller
// cannot read stands for nothing: expanding it would tell the caller, by the findings that come back or not, which members
// it has (R-AUTHZ-016); a repository that is no virtual repository of the snapshot is left to the scope, which narrows it.
func (sn *Snapshot) ExpandFor(c Caller, ids []int64) []int64 {
	allowed := make([]int64, 0, len(ids))
	for _, id := range ids {
		if class, _ := sn.Topo.Class(id); class == repository.Virtual && !slices.Contains(c.VirtualReadable, id) {
			continue
		}
		allowed = append(allowed, id)
	}
	return sn.Topo.Expand(allowed)
}

// policy returns the download policy of a repository (R-VULN-015).
func (s *Service) policy(sn *Snapshot, id int64) vuln.Policy {
	r, ok := sn.Topo.Repository(id)
	if !ok {
		return vuln.Policy{}
	}
	return r.VulnerabilityPolicy(s.cfg.Enabled, s.cfg.Enforcement)
}

// withPolicy adds the policy of every physical repository to the filter, for the statements that filter and count by block.
func (s *Service) withPolicy(sn *Snapshot, f store.Filter) store.Filter {
	f.EnabledRepos, f.MaliciousRepos = nil, nil
	for _, id := range sn.Topo.Physical() {
		p := s.policy(sn, id)
		if p.QueryEnforce() {
			f.EnabledRepos = append(f.EnabledRepos, id)
		}
		if p.QueryBlockMalicious() {
			f.MaliciousRepos = append(f.MaliciousRepos, id)
		}
	}
	return f
}

// Finding is a finding with the block it causes under its repository's current policy.
type Finding struct {
	store.Row
	Blocked     bool
	BlockReason vuln.BlockReason
}

// blockOf is the rule of R-VULN-015 applied to one finding: vuln.Verdict over the repository's policy, with the advisory data
// taken as complete, since the finding's own block does not depend on it (a version of an ecosystem without data blocks as
// missing_data through the version's verdict, not through a finding).
func (s *Service) blockOf(sn *Snapshot, r store.Row) (vuln.BlockReason, bool) {
	b := vuln.Verdict(s.policy(sn, r.RepoID), vuln.BlockRow{FindingID: r.ID, AdvisoryID: r.Advisory, Kind: r.Kind, Status: r.Status},
		advisory.DataState{Complete: true})
	return b.Reason, b.Active()
}

func (s *Service) findings(sn *Snapshot, rows []store.Row) []Finding {
	out := make([]Finding, 0, len(rows))
	for _, r := range rows {
		reason, blocked := s.blockOf(sn, r)
		out = append(out, Finding{Row: r, Blocked: blocked, BlockReason: reason})
	}
	return out
}
