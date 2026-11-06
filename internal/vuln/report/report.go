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
	"github.com/guided-traffic/trove/internal/qname"
	"slices"
	"sort"
	"time"

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
	// Now returns the current time; nil is time.Now.
	Now func() time.Time
}

// Service is the report.
type Service struct {
	st   *store.API
	repo repository.Reader
	cfg  Config
	adv  vuln.Advisories
	rule evaluate.ClaimRule
	now  func() time.Time
}

// New returns the report.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Repositories == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "vuln report: the store and the repository snapshot are required")
	}
	s := &Service{st: o.Store, repo: o.Repositories, cfg: o.Config, adv: o.Advisories, rule: o.Rule, now: o.Now}
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

// Snapshot is the repositories of the replica's snapshot with their topology, read once per request.
type Snapshot struct {
	Repos []repository.Repository
	Topo  view.Topology
	byKey map[string]repository.Repository
}

// Snapshot reads the repositories.
func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	repos, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	sn := &Snapshot{Repos: repos, Topo: view.NewTopology(repos), byKey: make(map[string]repository.Repository, len(repos))}
	for _, r := range repos {
		sn.byKey[r.Name.String()] = r
	}
	return sn, nil
}

// Repository returns a repository of the snapshot.
func (sn *Snapshot) Repository(key string) (repository.Repository, bool) {
	r, ok := sn.byKey[key]
	return r, ok
}

// Keys returns the keys of every repository of the snapshot, for the request's authorization index.
func (sn *Snapshot) Keys() []string { return append(sn.Topo.Physical(), sn.Topo.Virtual()...) }

// Caller is what the decision point says about the caller: the classification of the physical repositories, and the virtual
// repositories the caller may read.
type Caller struct {
	Reads           authz.ReadScope
	VirtualReadable []string
}

// Scope is what a caller may read, with the snapshot it was built on.
type Scope struct {
	store.Scope
	snap *Snapshot
	// Readable are the physical repositories the caller may read at least in part, sorted.
	Readable []string
	// Holders are, for a package view, the (repository, version) pairs of the package the caller may read.
	Holders []store.Candidate
	virtual map[string]bool
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
	load func(keys []string, max int) ([]store.Candidate, error),
	loadPackages func(keys []string, max int) ([]store.Pair, int, error),
) (Scope, error) {
	sc := Scope{snap: sn, virtual: map[string]bool{}}
	for _, v := range c.VirtualReadable {
		sc.virtual[v] = true
	}
	whole := map[string]bool{}
	var loadKeys []string
	for _, k := range sn.Topo.Physical() {
		r := sn.byKey[k]
		if len(formats) > 0 && !slices.Contains(formats, r.Format) {
			continue
		}
		switch shape := c.Reads.Shape(nameOf(k)); {
		case shape == authz.ReadAll && !sn.Topo.Restricts(k):
			sc.Whole = append(sc.Whole, k)
			whole[k] = true
			sc.Readable = append(sc.Readable, k)
			if withWhole {
				loadKeys = append(loadKeys, k)
			}
		case shape != authz.ReadNone:
			sc.Readable = append(sc.Readable, k)
			loadKeys = append(loadKeys, k)
		}
	}
	if load == nil || len(loadKeys) == 0 {
		return sc, nil
	}
	if vi, ok := c.Reads.(authz.VersionIndependent); ok && vi.VersionIndependent() && loadPackages != nil && !withWhole {
		return s.buildPackages(sn, c, sc, loadKeys, loadPackages)
	}
	cands, err := load(loadKeys, s.cfg.MaxScopeRows+1)
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
		if whole[cd.Repository] {
			decided[cd] = true
			sc.Holders = append(sc.Holders, cd)
			continue
		}
		ok := sn.Topo.Admits(cd.Repository, cd.Package) && (c.Reads.Shape(nameOf(cd.Repository)) == authz.ReadAll || c.Reads.MayRead(nameOf(cd.Repository), cd.Package, cd.Version))
		decided[cd] = ok
		if ok {
			sc.Rows = append(sc.Rows, cd.Repository+"\x1f"+cd.Package+"\x1f"+cd.Version)
			sc.Holders = append(sc.Holders, cd)
		}
	}
	return sc, nil
}

// buildPackages decides the packages of the repositories read in part once each (the decision does not depend on the version).
func (s *Service) buildPackages(sn *Snapshot, c Caller, sc Scope, loadKeys []string,
	loadPackages func(keys []string, max int) ([]store.Pair, int, error),
) (Scope, error) {
	pairs, findings, err := loadPackages(loadKeys, s.cfg.MaxScopeRows+1)
	if err != nil {
		return Scope{}, err
	}
	if findings > s.cfg.MaxScopeRows {
		return Scope{}, tooBroad()
	}
	for _, p := range pairs {
		if sn.Topo.Admits(p.Repository, p.Package) && (c.Reads.Shape(nameOf(p.Repository)) == authz.ReadAll || c.Reads.MayRead(nameOf(p.Repository), p.Package, "")) {
			sc.Packages = append(sc.Packages, p.Repository+"\x1f"+p.Package)
		}
	}
	return sc, nil
}

// Scope builds what the caller may read of the findings the filter selects: the candidates of the repositories read in part
// are the versions that have such a finding.
func (s *Service) Scope(ctx context.Context, sn *Snapshot, c Caller, f store.Filter) (Scope, error) {
	return s.build(sn, c, f.Formats, false, func(keys []string, max int) ([]store.Candidate, error) {
		keys = narrowKeys(keys, f)
		return s.st.Candidates(ctx, keys, f, max)
	}, func(keys []string, max int) ([]store.Pair, int, error) {
		return s.st.CandidatePackages(ctx, narrowKeys(keys, f), f, max)
	})
}

func narrowKeys(keys []string, f store.Filter) []string {
	if !f.HasRepos {
		return keys
	}
	var out []string
	for _, k := range keys {
		if slices.Contains(f.Repos, k) {
			out = append(out, k)
		}
	}
	return out
}

// PackageScope builds what the caller may read of one package (every version when version is ""), and the repositories that
// hold it, for the package views.
func (s *Service) PackageScope(ctx context.Context, sn *Snapshot, c Caller, format, name, version string) (Scope, error) {
	return s.build(sn, c, []string{format}, true, func(keys []string, max int) ([]store.Candidate, error) {
		return s.st.PackageCandidates(ctx, keys, format, name, version, max)
	}, nil)
}

// VirtualContaining returns the readable virtual repositories whose effective member list contains the repository, sorted.
func (sc Scope) VirtualContaining(repo string) []string {
	var out []string
	for _, v := range sc.snap.Topo.Containers(repo) {
		if sc.virtual[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// ExpandFor maps the repository keys of a filter to the physical repositories they stand for. A virtual repository the caller
// cannot read stands for nothing: expanding it would tell the caller, by the findings that come back or not, which members
// it has (R-AUTHZ-016); a key that is no virtual repository of the snapshot is left to the scope, which narrows it.
func (sn *Snapshot) ExpandFor(c Caller, keys []string) []string {
	virtual := sn.Topo.Virtual()
	allowed := make([]string, 0, len(keys))
	for _, k := range keys {
		if slices.Contains(virtual, k) && !slices.Contains(c.VirtualReadable, k) {
			continue
		}
		allowed = append(allowed, k)
	}
	return sn.Topo.Expand(allowed)
}

// policy returns the download policy of a repository (R-VULN-015).
func (s *Service) policy(sn *Snapshot, key string) vuln.Policy {
	r, ok := sn.byKey[key]
	if !ok {
		return vuln.Policy{}
	}
	return r.VulnerabilityPolicy(s.cfg.Enabled, s.cfg.Enforcement)
}

// withPolicy adds the policy of every physical repository to the filter, for the statements that filter and count by block.
func (s *Service) withPolicy(sn *Snapshot, f store.Filter) store.Filter {
	f.EnabledRepos, f.MaliciousRepos = nil, nil
	for _, k := range sn.Topo.Physical() {
		p := s.policy(sn, k)
		if p.QueryEnforce() {
			f.EnabledRepos = append(f.EnabledRepos, k)
		}
		if p.QueryBlockMalicious() {
			f.MaliciousRepos = append(f.MaliciousRepos, k)
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
	b := vuln.Verdict(s.policy(sn, r.Repository), vuln.BlockRow{FindingID: r.ID, AdvisoryID: r.Advisory, Kind: r.Kind, Status: r.Status},
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

// nameOf reads the qualified name "<project>/<key>" the findings statements use as the repository key; a string
// that is none gives the zero name, which no scope admits.
func nameOf(k string) qname.Repo {
	r, _ := qname.ParseRepo(k)
	return r
}
