package authz

import (
	"context"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/repository"
)

// The read scope of a repository for a listing that aggregates versions across
// repositories (R-API-029, R-EP-014). A listing that counts versions must filter the
// rows before it counts, in the query that counts them, so the decision point tells
// the query which repositories need no per-row decision at all and decides the rows
// of the others one by one.

// ReadShape is how a subject's read of the packages of one repository is decided.
type ReadShape int

const (
	// ReadNone: no package of the repository can be read. A repository the index
	// does not know, a virtual repository (it holds no package rows; its content
	// is decided on its members), and one the subject has no read grant on.
	ReadNone ReadShape = iota
	// ReadAll: every package and every version of the repository can be read. The
	// subject holds an unrestricted read grant or content:bypass, the token scope
	// does not narrow it and the endpoint exposes everything.
	ReadAll
	// ReadRows: the decision depends on the package, the version, or both: a grant
	// narrowed by package or path patterns, a token scope that lists elements, or an
	// endpoint with exposure rules. [ReadScoper.MayReadOn] decides each row.
	ReadRows
)

// String names the shape for logs and test failures.
func (s ReadShape) String() string {
	switch s {
	case ReadAll:
		return "all"
	case ReadRows:
		return "rows"
	}
	return "none"
}

// ReadScoper is the form of the decision point a listing of packages uses. The
// engine implements it next to [Decider]; it is a separate interface so that a
// decider that cannot answer it (a test double) need not.
type ReadScoper interface {
	// ReadScopeOn classifies repository keys for a subject on the request's index
	// and returns the scope that decides single rows. addressed is the repository the
	// client named when the keys are members a virtual repository resolves through: every
	// row is then decided as a read through it (Coordinate.Addressed), so the exposure of
	// the addressed repository and the lift of a token scope that names it apply
	// (R-AUTHZ-009 rule 2, Q-EP-11, Q-AUTHZ-163, Q-AUTHZ-168), and a key the addressed
	// repository does not reach is ReadNone. "" decides each repository on its own, as a
	// listing across repositories does. An error is a refusal to decide (503, 412).
	ReadScopeOn(ctx context.Context, idx *Index, sub Subject, addressed qname.Repo, keys []qname.Repo) (ReadScope, error)
}

// ReadScope answers the two questions of a listing for one subject and one index.
type ReadScope interface {
	// Shape is the classification of a repository key; a key that was not asked
	// for is ReadNone.
	Shape(key qname.Repo) ReadShape
	// MayRead decides the read of one version of one package of a repository: the
	// decision of [Decider.DecideOn] for a read, without the audit event and the
	// denial counters, because the caller asked about rows it lists and no client
	// requested a refused resource. version may be empty for a package-level
	// question. A package is decided on its document's path; a version on that path
	// and on the path of its file, and it may be read only if both allow it
	// (Q-AUTHZ-255).
	MayRead(repo qname.Repo, pkg, version string) bool
	// MayReadWiki decides the read of the wiki of one package (and version) of a repository, the question form of
	// [Decider.WikiReadableOn] for the rows of a management answer that carries a wiki member (R-AUTHZ-034, R-API-058).
	// It never allows what MayRead refuses: a caller who reads the wiki reads the artifacts.
	MayReadWiki(repo qname.Repo, pkg, version string) bool
	// VersionsByPath reports whether the subject's own grants decide the versions of
	// a package of the repository one by one: a grant or a CI contribution that gives
	// it read there, or its token scope, narrows paths, so a version's file may be
	// refused where its package's document is allowed (Q-AUTHZ-255). False means that
	// every version of a package the subject may read is readable as far as its grants
	// go; the endpoint's rules are not part of it (a format's exposure view decides
	// them). A key that was not asked for is true.
	VersionsByPath(key qname.Repo) bool
}

// VersionIndependent is the optional property of a [ReadScope] that a listing of findings uses to decide a package once and not
// once per version (H-488, V-7b of R-VULN-028): when it reports true, MayRead(repo, pkg, v) gives the same answer for every
// version v of the package, so MayRead(repo, pkg, "") stands for all of them. A scope that does not implement it, or reports
// false, is decided one version at a time. It is true when no input of the decision looks at the version: the endpoint exposes
// everything (an exposure rule reads versions, R-EP-018) and no grant, token scope or CI contribution of a classified repository
// narrows paths (a version is decided on its file's path, Q-AUTHZ-255).
type VersionIndependent interface {
	VersionIndependent() bool
}

var (
	_ ReadScoper         = (*Engine)(nil)
	_ VersionIndependent = (*readScope)(nil)
)

type readScope struct {
	e   *Engine
	idx *Index
	sub Subject
	// addressed is the repository the client named, "" for a listing that decides
	// each repository on its own.
	addressed qname.Repo
	shapes    map[qname.Repo]ReadShape
	// st is what the decision derives from the subject, once for the scope: a listing decides up to hundreds of thousands
	// of rows (the findings of R-VULN-019), and deriving the principal set and the capabilities for each would cost more
	// than the decision. It is immutable, so concurrent MayRead calls share it. Nil when the scope reads nothing.
	st *evalState
	// read is the action list of every decision of the scope; evaluate only reads it.
	read []Action
	// versionFree is [VersionIndependent]: the endpoint's ceiling admits every coordinate and no classified repository
	// is read by path.
	versionFree bool
	// byPath is [ReadScope.VersionsByPath] of every classified key.
	byPath map[qname.Repo]bool
	// allEndpoint says the endpoint's ceiling admits every coordinate; otherwise its rules may narrow the path of a
	// version's file, and MayRead decides a version on it whatever byPath says.
	allEndpoint bool
}

// VersionIndependent implements [VersionIndependent].
func (s *readScope) VersionIndependent() bool { return s.versionFree }

func (s *readScope) Shape(key qname.Repo) ReadShape { return s.shapes[key] }

// VersionsByPath implements [ReadScope]: a key the scope did not classify is read by path.
func (s *readScope) VersionsByPath(key qname.Repo) bool {
	by, ok := s.byPath[key]
	return by || !ok
}

func (s *readScope) MayRead(repo qname.Repo, pkg, version string) bool {
	if !s.idx.addressedReaches(s.addressed, repo) {
		return false
	}
	// A row stands for the package's document: it is decided on that document's path,
	// so a path pattern hides from a listing what it hides from the protocol (Q-AUTHZ-250).
	c := Coordinate{Repository: repo, Package: pkg, Version: version, Kind: KindMetadata, Actions: s.read, Addressed: s.addressed}
	if s.e.evaluateWith(s.idx, s.sub, s.st, s.e.atPackage(s.idx, c)).Outcome != Allow {
		return false
	}
	// A row with a version stands for what the version's file holds as well: it is decided on that file's path too,
	// unless neither the subject's grants nor the endpoint's rules can tell the two paths apart (Q-AUTHZ-255).
	if s.allEndpoint && !s.VersionsByPath(repo) {
		return true
	}
	vc, ok := s.e.atVersion(s.idx, c)
	return !ok || s.e.evaluateWith(s.idx, s.sub, s.st, vc).Outcome == Allow
}

// MayReadWiki implements [ReadScope]: MayRead on the coordinate with the part wiki.
func (s *readScope) MayReadWiki(repo qname.Repo, pkg, version string) bool {
	if !s.idx.addressedReaches(s.addressed, repo) {
		return false
	}
	c := Coordinate{Repository: repo, Package: pkg, Version: version, Kind: KindMetadata, Actions: s.read, Addressed: s.addressed, Part: PartWiki}
	return s.e.evaluateWith(s.idx, s.sub, s.st, s.e.atPackage(s.idx, c)).Outcome == Allow
}

// ReadScopeOn implements [ReadScoper].
func (e *Engine) ReadScopeOn(_ context.Context, idx *Index, sub Subject, addressed qname.Repo, keys []qname.Repo) (ReadScope, error) {
	allEndpoint := e.exposesEverything(sub.Endpoint)
	rs := &readScope{
		e: e, idx: idx, sub: sub, addressed: addressed, shapes: make(map[qname.Repo]ReadShape, len(keys)), read: []Action{Read},
		versionFree: allEndpoint, byPath: make(map[qname.Repo]bool, len(keys)), allEndpoint: allEndpoint,
	}
	st := e.newEvalState(idx, sub)
	rs.st = st
	for _, k := range keys {
		if !idx.addressedReaches(addressed, k) {
			rs.shapes[k] = ReadNone
			rs.byPath[k] = false
			continue
		}
		r := idx.repo(k)
		rs.shapes[k] = e.readShape(st, sub, r, allEndpoint)
		by := rs.shapes[k] == ReadRows && e.readsByPath(st, sub, r)
		rs.byPath[k] = by
		rs.versionFree = rs.versionFree && !by
	}
	return rs, nil
}

// addressedReaches reports whether a read of the repository key may be decided as a
// read through the addressed repository: no addressed repository, the repository
// itself, or a virtual repository that resolves through the key (Index.reaches). Any
// other pair is no read the client asked for, so a listing gets nothing of it
// (Q-AUTHZ-168).
func (idx *Index) addressedReaches(addressed, key qname.Repo) bool {
	if addressed.IsZero() || addressed == key {
		return true
	}
	v := idx.repo(addressed)
	return v != nil && v.Class == repository.Virtual && idx.reaches(v, key, 0)
}

// exposesEverything reports whether the ceiling of the endpoint admits every
// coordinate: no ceiling at all, no endpoint, or an endpoint in mode all. A ceiling
// of another kind is not known to be so and is treated as narrowing.
func (e *Engine) exposesEverything(endpoint string) bool {
	switch c := e.o.Ceiling.(type) {
	case nil:
		return true
	case NoCeiling:
		return true
	case *ExposureCeiling:
		return endpoint == "" || c.Mode(endpoint) == ExposureModeAll
	}
	return false
}

// readShape is the classification of one repository. It follows evaluate for a
// package-level read: the grants of the subject's principal set, its project role,
// content:bypass, the token scope and the ceiling. It never answers ReadAll where evaluate would
// refuse some row, and never ReadNone where it would allow one.
func (e *Engine) readShape(st *evalState, sub Subject, r *repoInfo, allEndpoint bool) ReadShape {
	if r == nil || r.Class == repository.Virtual {
		return ReadNone
	}
	scoped := sub.Scope != nil && !sub.Scope.Inherit
	// A level reads every package of the repository (R-AUTHZ-035 rule 4, rule 7): public for every subject and without a scope,
	// internal for an internal subject within a scope that does not narrow.
	levelAll, levelAny := false, false
	switch {
	case r.artifacts == LevelPublic:
		levelAll, levelAny = true, true
	case r.artifacts == LevelInternal && sub.Internal():
		levelAll, levelAny = !scoped, true
	}
	if levelAll && allEndpoint {
		return ReadAll
	}
	if st.bypass || st.metadata {
		// content:metadata-read (Q-AUTHZ-172) lists every package like content:bypass;
		// a row it may not read (a token scope, an exposure rule) is decided by MayRead
		// as a metadata read, which never admits an artifact.
		return rowsUnless(allEndpoint && !scoped)
	}
	unrestricted, any := false, false
	if st.isCI {
		any, unrestricted = e.ciReadShape(st.ci, st.idx, r)
	}
	for _, p := range st.pset {
		if st.isCI {
			break // a CI subject's grants are its contributions (R-AUTHZ-014)
		}
		for _, g := range st.idx.grantsFor(r.ID, p) {
			if !g.actions.Has(Read) {
				continue
			}
			any = true
			if g.res.unrestrictedPackages() {
				unrestricted = true
			}
		}
	}
	if !st.isCI && roleActions(st.roleIn(r.proj), r).Has(Read) {
		// A project role reads every package of every repository of its project (R-AUTHZ-026).
		any, unrestricted = true, true
	}
	any = any || levelAny
	switch {
	case !any:
		return ReadNone
	case unrestricted && allEndpoint && !scoped:
		return ReadAll
	}
	return ReadRows
}

func rowsUnless(all bool) ReadShape {
	if all {
		return ReadAll
	}
	return ReadRows
}

// unrestrictedPackages reports whether a grant's resource set admits every package
// of the repository: no package pattern and no path pattern at all. A row is decided
// on the path of its package document (atPackage), which a path include or exclude
// may leave out (Q-AUTHZ-250).
func (r resourceSet) unrestrictedPackages() bool {
	return len(r.packages.include) == 0 && len(r.packages.exclude) == 0 && len(r.paths.include) == 0 && len(r.paths.exclude) == 0
}

// narrowsPaths reports whether a grant's resource set holds a path pattern, include or exclude.
func (r resourceSet) narrowsPaths() bool { return len(r.paths.include)+len(r.paths.exclude) > 0 }

// readsByPath is [ReadScope.VersionsByPath] for one repository the subject reads row by row: whether its own grants can
// allow one path of a package and refuse another (Q-AUTHZ-255). It never answers false where they could: a token scope
// with any path pattern, and a grant or a CI contribution with read and a path pattern, make it true unless the subject
// also reads every package and path of the repository through a grant without patterns or its project role.
// content:bypass and content:metadata-read read every path of what they read, so only the token scope narrows them.
func (e *Engine) readsByPath(st *evalState, sub Subject, r *repoInfo) bool {
	if r == nil {
		return false
	}
	if sub.Scope != nil && !sub.Scope.Inherit {
		for _, el := range sub.Scope.Repositories {
			if len(el.Paths.Include)+len(el.Paths.Exclude) > 0 {
				return true
			}
		}
	}
	if st.bypass || st.metadata {
		return false
	}
	if st.isCI {
		return e.ciReadsByPath(st.ci, st.idx, r)
	}
	if roleActions(st.roleIn(r.proj), r).Has(Read) {
		return false
	}
	byPath := false
	for _, p := range st.pset {
		for _, g := range st.idx.grantsFor(r.ID, p) {
			switch {
			case !g.actions.Has(Read):
			case g.res.unrestrictedPackages():
				return false
			case g.res.narrowsPaths():
				byPath = true
			}
		}
	}
	return byPath
}
