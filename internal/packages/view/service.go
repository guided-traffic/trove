package view

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// The bounds of what one request decides or matches in process. A request above a
// bound is refused, never answered with a partial set, because a partial set would
// make a count wrong without saying so.
const (
	// DefaultMaxRows bounds the versions of the repositories whose rows the decision
	// point decides one by one (authz.ReadRows).
	DefaultMaxRows = 200_000
	// DefaultMaxMatches bounds the versions a search term may match.
	DefaultMaxMatches = 100_000
)

// Options configure [New].
type Options struct {
	DB       meta.Runner
	Adapters Adapters
	// MaxRows and MaxMatches override the defaults; zero keeps them.
	MaxRows, MaxMatches int
}

// Service is the read model.
type Service struct {
	db         meta.Runner
	adapters   Adapters
	maxRows    int
	maxMatches int
}

// New creates the service.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Adapters == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "view: a transaction runner and the adapters are required")
	}
	s := &Service{db: o.DB, adapters: o.Adapters, maxRows: o.MaxRows, maxMatches: o.MaxMatches}
	if s.maxRows <= 0 {
		s.maxRows = DefaultMaxRows
	}
	if s.maxMatches <= 0 {
		s.maxMatches = DefaultMaxMatches
	}
	return s, nil
}

func (s *Service) read(ctx context.Context, fn func(*q.Queries) error) error {
	return s.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("view: the transaction is not one of internal/meta/postgres")
		}
		return fn(q.New(ptx))
	})
}

func tooBroad(what string) error {
	return errs.E(errs.Invalid, errs.CodeInvalid, what+" covers more versions than one request decides; narrow it by format, repository or name.")
}

// Scope is what a caller may read, as the statements of the view take it.
type Scope struct {
	topo            Topology
	whole           []string
	rows            []string
	virtualReadable map[string]bool
}

// Repos returns the physical repositories the scope has any row of, sorted.
func (sc Scope) Repos() []string {
	out := append(slices.Clone(sc.whole), rowRepos(sc.rows)...)
	slices.Sort(out)
	return slices.Compact(out)
}

func rowRepos(rows []string) []string {
	var out []string
	for _, r := range rows {
		if k, _, ok := strings.Cut(r, "\x1f"); ok && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// Topology returns the topology the scope was built on.
func (sc Scope) Topology() Topology { return sc.topo }

// holders returns the repositories that show a version held by the physical
// repositories: those, and the virtual repositories that contain them and that the
// caller may read. Local repositories come first, then remote, then virtual; each
// group is sorted.
func (sc Scope) holders(physical []string) []string {
	set := map[string]bool{}
	for _, k := range physical {
		set[k] = true
		for _, v := range sc.topo.Containers(k) {
			if sc.virtualReadable[v] {
				set[v] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	rank := func(k string) int {
		c, _ := sc.topo.Class(k)
		switch c {
		case repository.Local:
			return 0
		case repository.Remote:
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// ScopeRequest narrows the rows the scope has to decide one by one: the statements
// of the request intersect with it anyway, so a narrower request decides less.
type ScopeRequest struct {
	Formats    []string
	NamePrefix string
	ExactName  string
}

// Scope builds the scope of a caller: the repositories that need no per-row
// decision, and the readable rows of the others. virtualReadable are the virtual
// repositories the caller may read; they decide which of them a version is shown
// in.
func (s *Service) Scope(ctx context.Context, rs authz.ReadScope, topo Topology, virtualReadable []string, r ScopeRequest) (Scope, error) {
	sc := Scope{topo: topo, virtualReadable: map[string]bool{}}
	for _, v := range virtualReadable {
		sc.virtualReadable[v] = true
	}
	var rowKeys []string
	for _, k := range topo.Physical() {
		switch shape := rs.Shape(k); {
		case shape == authz.ReadAll && !topo.Restricts(k):
			sc.whole = append(sc.whole, k)
		case shape != authz.ReadNone:
			// A repository with include or exclude patterns serves only the names they
			// admit (R-REPO-005), whatever the caller may read: it is decided row by row.
			rowKeys = append(rowKeys, k)
		}
	}
	if len(rowKeys) == 0 {
		return sc, nil
	}
	formats := s.formatsOf(r.Formats)
	if len(formats) == 0 {
		return sc, nil
	}
	var cands []q.PackageViewCandidatesRow
	err := s.read(ctx, func(qs *q.Queries) (err error) {
		cands, err = qs.PackageViewCandidates(ctx, q.PackageViewCandidatesParams{
			Keys: keys(rowKeys), Formats: keys(formats), NamePrefix: r.NamePrefix, ExactName: r.ExactName, MaxRows: rows32(s.maxRows + 1),
		})
		return err
	})
	if err != nil {
		return Scope{}, err
	}
	if len(cands) > s.maxRows {
		return Scope{}, tooBroad("The repositories you may read only in part")
	}
	for _, c := range cands {
		if topo.Admits(c.RepoKey, c.Name) && (rs.Shape(c.RepoKey) == authz.ReadAll || rs.MayRead(c.RepoKey, c.Name, c.Version)) {
			sc.rows = append(sc.rows, rowKey(c.RepoKey, c.Name, c.Version))
		}
	}
	return sc, nil
}

// keys and ids turn a nil slice into an empty one: the driver sends a nil slice as
// NULL, and "= ANY(NULL)" is NULL, which would drop every row of a statement that
// negates it.
func keys(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func ids(in []int64) []int64 {
	if in == nil {
		return []int64{}
	}
	return in
}

// rows32 is a row limit as the statements take it.
func rows32(n int) int32 { return int32(min(max(n, 0), math.MaxInt32)) }

func rowKey(repo, name, version string) string { return repo + "\x1f" + name + "\x1f" + version }

// formatsOf narrows the asked formats to those with an adapter; no asked format
// means every format that has one.
func (s *Service) formatsOf(asked []string) []string {
	have := s.adapters.Formats()
	if len(asked) == 0 {
		return have
	}
	var out []string
	for _, f := range asked {
		if slices.Contains(have, f) && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Filter narrows the list of packages (R-API-029).
type Filter struct {
	Formats []string
	// Repositories are the keys as sent; a virtual repository stands for its
	// members. HasRepositories distinguishes no filter from a filter that names
	// nothing.
	Repositories    []string
	HasRepositories bool
	Q               string
	Keyword         string
	NamePrefix      string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time
}

// Order is the sort of the list: name, createdAt or updatedAt.
type Order struct {
	Sort string
	Desc bool
}

// After is the position after which a page starts: the sort-key values of the last
// package of the previous page.
type After struct {
	Time   time.Time
	Name   string
	Format string
}

// Key returns the sort-key values of a package in the order the statement compares
// them, for the cursor. The statement compares the normalized name, which the display
// name of a case-preserving format does not equal.
func (o Order) Key(p Package) []string {
	switch o.Sort {
	case "createdAt":
		return []string{p.CreatedAt.UTC().Format(time.RFC3339Nano), p.Key, p.Format}
	case "updatedAt":
		return []string{p.UpdatedAt.UTC().Format(time.RFC3339Nano), p.Key, p.Format}
	}
	return []string{p.Key, p.Format}
}

// ParseAfter reads the cursor values of [Order.Key].
func (o Order) ParseAfter(key []string) (*After, error) {
	switch o.Sort {
	case "createdAt", "updatedAt":
		if len(key) != 3 {
			return nil, errBadCursor
		}
		t, err := time.Parse(time.RFC3339Nano, key[0])
		if err != nil {
			return nil, errBadCursor
		}
		return &After{Time: t, Name: key[1], Format: key[2]}, nil
	}
	if len(key) != 2 {
		return nil, errBadCursor
	}
	return &After{Name: key[0], Format: key[1]}, nil
}

var errBadCursor = errors.New("view: the cursor does not hold the values of the sort")

// IsBadCursor reports whether err came from [Order.ParseAfter].
func IsBadCursor(err error) bool { return errors.Is(err, errBadCursor) }

// stamp renders a time for a statement that casts it to timestamptz.
func stamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// reposOf expands the repository filter to physical keys.
func (sc Scope) reposOf(f Filter) []string {
	if !f.HasRepositories {
		return nil
	}
	return sc.topo.Expand(f.Repositories)
}

// List returns at most limit+1 packages after the position, in the order, so that
// the caller learns whether a next page exists. Every member counts the versions the
// scope holds.
func (s *Service) List(ctx context.Context, sc Scope, f Filter, o Order, after *After, limit int) ([]Package, error) {
	formats := s.formatsOf(f.Formats)
	if len(formats) == 0 {
		return nil, nil
	}
	repos := sc.reposOf(f)
	universe := intersectOrAll(sc.Repos(), repos, f.HasRepositories)
	var qIDs, kwIDs []int64
	var err error
	if f.Q != "" {
		if qIDs, err = s.find(ctx, formats, Find{Repos: universe, Text: f.Q}, sc); err != nil {
			return nil, err
		}
	}
	if f.Keyword != "" {
		if kwIDs, err = s.find(ctx, formats, Find{Repos: universe, Keyword: f.Keyword}, sc); err != nil {
			return nil, err
		}
	}
	withheld, err := s.withheld(ctx, formats, universe)
	if err != nil {
		return nil, err
	}
	p := q.PackageViewListParams{
		Sort: o.Sort, Descending: o.Desc, MaxRows: rows32(limit + 1),
		Withheld: ids(withheld), Formats: keys(formats), HasRepos: f.HasRepositories, Repos: keys(repos),
		NamePrefix: f.NamePrefix, Whole: keys(sc.whole), Rows: keys(sc.rows),
		Q: f.Q, QIds: ids(qIDs), Keyword: f.Keyword, KeywordIds: ids(kwIDs),
		HasFrom: f.CreatedFrom != nil, CreatedFrom: stamp(f.CreatedFrom),
		HasTo: f.CreatedTo != nil, CreatedTo: stamp(f.CreatedTo),
	}
	if after != nil {
		p.HasAfter, p.AfterName, p.AfterFormat = true, after.Name, after.Format
		p.AfterTime = stamp(&after.Time)
	}
	return s.packages(ctx, sc, p, withheld, f.HasRepositories, repos)
}

// Package returns one package, found false when the scope holds no version of it.
func (s *Service) Package(ctx context.Context, sc Scope, format, name string) (Package, bool, error) {
	if len(s.formatsOf([]string{format})) == 0 {
		return Package{}, false, nil
	}
	universe := sc.Repos()
	withheld, err := s.withheld(ctx, []string{format}, universe)
	if err != nil {
		return Package{}, false, err
	}
	p := q.PackageViewListParams{
		Sort: "name", MaxRows: 1, Withheld: ids(withheld), Formats: []string{format},
		ExactFormat: format, ExactName: name, Whole: keys(sc.whole), Rows: keys(sc.rows),
		Repos: []string{}, QIds: []int64{}, KeywordIds: []int64{},
	}
	ps, err := s.packages(ctx, sc, p, withheld, false, nil)
	if err != nil || len(ps) == 0 {
		return Package{}, false, err
	}
	return ps[0], true, nil
}

func intersectOrAll(all, only []string, restricted bool) []string {
	if !restricted {
		return all
	}
	var out []string
	for _, k := range all {
		if slices.Contains(only, k) {
			out = append(out, k)
		}
	}
	return out
}

// find asks the adapters of the formats for the versions that match and keeps the
// ones in scope. The list is a superset the statement intersects with the rows the
// caller may read.
func (s *Service) find(ctx context.Context, formats []string, f Find, _ Scope) ([]int64, error) {
	var ids []int64
	for _, fm := range formats {
		a, ok := s.adapters.For(fm)
		if !ok {
			continue
		}
		got, err := a.Find(ctx, f, s.maxMatches)
		if err != nil {
			return nil, err
		}
		ids = append(ids, got...)
	}
	if len(ids) > s.maxMatches {
		return nil, tooBroad("The search term")
	}
	return ids, nil
}

func (s *Service) withheld(ctx context.Context, formats, repos []string) ([]int64, error) {
	var ids []int64
	for _, fm := range formats {
		a, ok := s.adapters.For(fm)
		if !ok {
			continue
		}
		got, err := a.Withheld(ctx, repos)
		if err != nil {
			return nil, err
		}
		ids = append(ids, got...)
	}
	return ids, nil
}

// packages runs the list statement and decorates its rows.
func (s *Service) packages(ctx context.Context, sc Scope, p q.PackageViewListParams, withheld []int64, hasRepos bool, repos []string) ([]Package, error) {
	var rows []q.PackageViewListRow
	if err := s.read(ctx, func(qs *q.Queries) (err error) {
		rows, err = qs.PackageViewList(ctx, p)
		return err
	}); err != nil || len(rows) == 0 {
		return nil, err
	}
	pkgs := make([]Package, len(rows))
	keys := make([]string, len(rows))
	for i, r := range rows {
		pkgs[i] = Package{
			ID: ID(r.Format, r.Name), Format: r.Format, Name: r.DisplayName, Key: r.Name,
			CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
			VersionCount: r.VersionCount, DownloadCount: r.Downloads,
		}
		keys[i] = r.Format + "\x1f" + r.Name
	}
	vrows, err := s.versionRows(ctx, sc, keys, false, withheld, hasRepos, repos)
	if err != nil {
		return nil, err
	}
	byPkg := map[string][]q.PackageViewVersionsRow{}
	for _, v := range vrows {
		k := v.Format + "\x1f" + v.Name
		byPkg[k] = append(byPkg[k], v)
	}
	// The latest version of each package, by the format's order, and the row to
	// describe it by.
	type latest struct {
		version string
		row     q.PackageViewVersionsRow
	}
	latests := make([]latest, len(pkgs))
	byFormat := map[string][]Version{}
	for i := range pkgs {
		vs := byPkg[keys[i]]
		a, _ := s.adapters.For(pkgs[i].Format)
		l, ok := latestOf(a, vs)
		if !ok {
			continue
		}
		latests[i] = latest{version: l.Version, row: l}
		pkgs[i].LatestVersion = l.Version
		var held []string
		for _, v := range vs {
			held = append(held, v.RepoKey)
		}
		pkgs[i].Repositories = sc.holders(held)
		byFormat[pkgs[i].Format] = append(byFormat[pkgs[i].Format], Version{
			ID: l.VersionID, Format: l.Format, Repo: l.RepoKey, Package: l.Name, Version: l.Version,
		})
	}
	data := map[int64]Data{}
	for format, vs := range byFormat {
		a, _ := s.adapters.For(format)
		got, err := a.Describe(ctx, vs, FieldSummary, func(string) bool { return true })
		if err != nil {
			return nil, err
		}
		for id, d := range got {
			data[id] = d
		}
	}
	for i := range pkgs {
		if d, ok := data[latests[i].row.VersionID]; ok {
			pkgs[i].Keywords = d.Keywords
			if d.Description != "" {
				desc := d.Description
				pkgs[i].Description = &desc
			}
		}
		if pkgs[i].Keywords == nil {
			pkgs[i].Keywords = []string{}
		}
		if pkgs[i].Repositories == nil {
			pkgs[i].Repositories = []string{}
		}
	}
	return pkgs, nil
}

// latestOf is the latest visible stable version, or the latest visible pre-release
// while no stable version is visible (R-API-029).
func latestOf(a Adapter, rows []q.PackageViewVersionsRow) (q.PackageViewVersionsRow, bool) {
	var best q.PackageViewVersionsRow
	found, bestPre := false, false
	for _, r := range rows {
		pre := a.Prerelease(r.Version)
		switch {
		case !found:
		case bestPre && !pre:
		case !bestPre && pre:
			continue
		case a.Compare(r.Version, best.Version) <= 0:
			continue
		}
		best, found, bestPre = r, true, pre
	}
	return best, found
}

// versionRows reads the rows of the given packages ("<format>\x1f<name>").
func (s *Service) versionRows(ctx context.Context, sc Scope, pkgs []string, withSize bool, withheld []int64, hasRepos bool, repos []string) ([]q.PackageViewVersionsRow, error) {
	var rows []q.PackageViewVersionsRow
	err := s.read(ctx, func(qs *q.Queries) (err error) {
		rows, err = qs.PackageViewVersions(ctx, q.PackageViewVersionsParams{
			WithSize: withSize, Withheld: ids(withheld), Packages: pkgs, HasRepos: hasRepos, Repos: keys(repos),
			Whole: keys(sc.whole), Rows: keys(sc.rows),
		})
		return err
	})
	return rows, err
}

// A VersionSet is the visible rows of one package, grouped per version.
type versionGroup struct {
	version   string
	rows      []q.PackageViewVersionsRow
	published time.Time
}

func (g versionGroup) primary() q.PackageViewVersionsRow { return g.rows[0] }

func (g versionGroup) downloads() (n int64) {
	for _, r := range g.rows {
		n += r.Downloads
	}
	return n
}

func (g versionGroup) repoKeys() []string {
	out := make([]string, 0, len(g.rows))
	for _, r := range g.rows {
		out = append(out, r.RepoKey)
	}
	return out
}

// groups reads and groups the visible versions of one package; found is false when
// the scope holds none.
func (s *Service) groups(ctx context.Context, sc Scope, format, name string, withSize bool) ([]versionGroup, Adapter, error) {
	a, ok := s.adapters.For(format)
	if !ok {
		return nil, nil, nil
	}
	withheld, err := s.withheld(ctx, []string{format}, sc.Repos())
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.versionRows(ctx, sc, []string{format + "\x1f" + name}, withSize, withheld, false, nil)
	if err != nil {
		return nil, nil, err
	}
	idx := map[string]int{}
	var out []versionGroup
	for _, r := range rows { // ordered by creation, so the first row of a group is its oldest
		i, ok := idx[r.Version]
		if !ok {
			i = len(out)
			idx[r.Version] = i
			out = append(out, versionGroup{version: r.Version, published: r.CreatedAt.Time})
		}
		out[i].rows = append(out[i].rows, r)
	}
	return out, a, nil
}

// VersionFilter narrows the versions of a package.
type VersionFilter struct {
	Prerelease      *bool
	Repositories    []string
	HasRepositories bool
}

// VersionOrder is the sort of the version list: publishedAt or version.
type VersionOrder struct {
	Sort string
	Desc bool
}

// VersionKey is the sort-key of a version summary for the cursor: the sort value and
// the version.
func (o VersionOrder) VersionKey(v VersionSummary) []string {
	if o.Sort == "version" {
		return []string{v.Version}
	}
	return []string{v.PublishedAt.UTC().Format(time.RFC3339Nano), v.Version}
}

// Versions returns at most limit+1 versions after the cursor values, found false
// when the scope holds no version of the package.
func (s *Service) Versions(ctx context.Context, sc Scope, format, name string, f VersionFilter, o VersionOrder, after []string, limit int) ([]VersionSummary, bool, error) {
	gs, a, err := s.groups(ctx, sc, format, name, true)
	if err != nil || len(gs) == 0 {
		return nil, false, err
	}
	var only []string
	if f.HasRepositories {
		only = sc.topo.Expand(f.Repositories)
	}
	out := make([]VersionSummary, 0, len(gs))
	for _, g := range gs {
		pre := a.Prerelease(g.version)
		if f.Prerelease != nil && *f.Prerelease != pre {
			continue
		}
		if f.HasRepositories && !slices.ContainsFunc(g.rows, func(r q.PackageViewVersionsRow) bool { return slices.Contains(only, r.RepoKey) }) {
			continue
		}
		out = append(out, VersionSummary{
			Version: g.version, Prerelease: pre, PublishedAt: g.published,
			SizeBytes: g.primary().SizeBytes, Repositories: sc.holders(g.repoKeys()), DownloadCount: g.downloads(),
		})
	}
	cmp := func(x, y VersionSummary) int {
		var c int
		if o.Sort == "version" {
			c = a.Compare(x.Version, y.Version)
		} else {
			c = x.PublishedAt.Compare(y.PublishedAt)
		}
		if c == 0 {
			c = strings.Compare(x.Version, y.Version)
		}
		if o.Desc {
			return -c
		}
		return c
	}
	slices.SortFunc(out, cmp)
	if after != nil {
		var pos VersionSummary
		switch {
		case o.Sort == "version" && len(after) == 1:
			pos.Version = after[0]
		case o.Sort != "version" && len(after) == 2:
			t, err := time.Parse(time.RFC3339Nano, after[0])
			if err != nil {
				return nil, true, errBadCursor
			}
			pos.PublishedAt, pos.Version = t, after[1]
		default:
			return nil, true, errBadCursor
		}
		i := sort.Search(len(out), func(i int) bool { return cmp(out[i], pos) > 0 })
		out = out[i:]
	}
	if len(out) > limit+1 {
		out = out[:limit+1]
	}
	return out, true, nil
}

// Version returns the details of one version, found false when the scope does not
// hold it.
func (s *Service) Version(ctx context.Context, sc Scope, format, name, version string) (VersionDetail, bool, error) {
	g, a, ok, err := s.oneVersion(ctx, sc, format, name, version, true)
	if err != nil || !ok {
		return VersionDetail{}, false, err
	}
	pri := g.primary()
	d := VersionDetail{
		PackageID: ID(format, name), Format: format, Name: pri.DisplayName, Version: version,
		Prerelease: a.Prerelease(version), PublishedAt: g.published,
		SizeBytes: pri.SizeBytes, DownloadCount: g.downloads(), Repo: pri.RepoKey,
		Licenses: []string{}, Metadata: map[string]any{},
	}
	earliest := map[string]time.Time{}
	var held []string
	for _, r := range g.rows {
		held = append(held, r.RepoKey)
		if t, ok := earliest[r.RepoKey]; !ok || r.CreatedAt.Time.Before(t) {
			earliest[r.RepoKey] = r.CreatedAt.Time
		}
	}
	// A virtual repository shows the oldest copy of its members.
	for _, key := range sc.holders(held) {
		class, _ := sc.topo.Class(key)
		at, ok := earliest[key]
		if !ok {
			for _, m := range sc.topo.members[key] {
				if t, found := earliest[m]; found && (at.IsZero() || t.Before(at)) {
					at = t
				}
			}
		}
		d.Repositories = append(d.Repositories, RepositoryEntry{Key: key, Class: string(class), PublishedAt: at})
	}
	visible := map[string]bool{}
	if all, _, err := s.groups(ctx, sc, format, name, false); err == nil {
		for _, g := range all {
			visible[g.version] = true
		}
	}
	got, err := a.Describe(ctx, []Version{{ID: pri.VersionID, Format: format, Repo: pri.RepoKey, Package: pri.Name, Version: version}},
		FieldSummary|FieldDetails, func(v string) bool { return visible[v] })
	if err != nil {
		return VersionDetail{}, false, err
	}
	if data, ok := got[pri.VersionID]; ok {
		d.HasReadme = data.HasReadme
		if data.Licenses != nil {
			d.Licenses = data.Licenses
		}
		if data.Metadata != nil {
			d.Metadata = data.Metadata
		}
	}
	err = s.read(ctx, func(qs *q.Queries) error {
		files, err := qs.PackageViewFiles(ctx, pri.VersionID)
		for _, f := range files {
			d.Files = append(d.Files, File{Path: f.Path, SizeBytes: f.Size.Int64, SHA256: f.Sha256, Repo: pri.RepoKey})
		}
		return err
	})
	if err != nil {
		return VersionDetail{}, false, err
	}
	if d.Files == nil {
		d.Files = []File{}
	}
	if d.Repositories == nil {
		d.Repositories = []RepositoryEntry{}
	}
	return d, true, nil
}

// ReadmeOf returns the README source of a version, found false when the scope does
// not hold the version or it has none.
func (s *Service) ReadmeOf(ctx context.Context, sc Scope, format, name, version string) (Readme, bool, error) {
	g, a, ok, err := s.oneVersion(ctx, sc, format, name, version, false)
	if err != nil || !ok {
		return Readme{}, false, err
	}
	pri := g.primary()
	got, err := a.Describe(ctx, []Version{{ID: pri.VersionID, Format: format, Repo: pri.RepoKey, Package: pri.Name, Version: version}},
		FieldDetails|FieldReadme, func(string) bool { return false })
	if err != nil {
		return Readme{}, false, err
	}
	data, ok := got[pri.VersionID]
	if !ok || data.Readme == "" {
		return Readme{}, false, nil
	}
	return Readme{Source: data.Readme, FileName: data.ReadmeFile}, true, nil
}

func (s *Service) oneVersion(ctx context.Context, sc Scope, format, name, version string, withSize bool) (versionGroup, Adapter, bool, error) {
	gs, a, err := s.groups(ctx, sc, format, name, withSize)
	if err != nil {
		return versionGroup{}, nil, false, err
	}
	for _, g := range gs {
		if g.version == version {
			return g, a, true, nil
		}
	}
	return versionGroup{}, nil, false, nil
}
