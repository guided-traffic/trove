package authz

import (
	"context"

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
	// and returns the scope that decides single rows. An error is a refusal to
	// decide (503, 412).
	ReadScopeOn(ctx context.Context, idx *Index, sub Subject, keys []string) (ReadScope, error)
}

// ReadScope answers the two questions of a listing for one subject and one index.
type ReadScope interface {
	// Shape is the classification of a repository key; a key that was not asked
	// for is ReadNone.
	Shape(key string) ReadShape
	// MayRead decides the read of one version of one package of a repository: the
	// decision of [Decider.DecideOn] for a read, without the audit event and the
	// denial counters, because the caller asked about rows it lists and no client
	// requested a refused resource. version may be empty for a package-level
	// question.
	MayRead(repo, pkg, version string) bool
}

var _ ReadScoper = (*Engine)(nil)

type readScope struct {
	e      *Engine
	idx    *Index
	sub    Subject
	shapes map[string]ReadShape
}

func (s *readScope) Shape(key string) ReadShape { return s.shapes[key] }

func (s *readScope) MayRead(repo, pkg, version string) bool {
	if s.sub.Anonymous && s.sub.anon != anonOn {
		return false
	}
	d := s.e.evaluate(s.idx, s.sub, Coordinate{
		Repository: repo, Package: pkg, Version: version,
		Kind: KindMetadata, Actions: []Action{Read},
	})
	return d.Outcome == Allow
}

// ReadScopeOn implements [ReadScoper].
func (e *Engine) ReadScopeOn(ctx context.Context, idx *Index, sub Subject, keys []string) (ReadScope, error) {
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	rs := &readScope{e: e, idx: idx, sub: sub, shapes: make(map[string]ReadShape, len(keys))}
	if sub.Anonymous && sub.anon != anonOn {
		return rs, nil
	}
	st := e.newEvalState(idx, sub)
	allEndpoint := e.exposesEverything(sub.Endpoint)
	for _, k := range keys {
		rs.shapes[k] = e.readShape(st, sub, idx.repo(k), allEndpoint)
	}
	return rs, nil
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
// package-level read: the grants of the subject's principal set, content:bypass,
// the token scope and the ceiling. It never answers ReadAll where evaluate would
// refuse some row, and never ReadNone where it would allow one.
func (e *Engine) readShape(st *evalState, sub Subject, r *repoInfo, allEndpoint bool) ReadShape {
	if r == nil || r.Class == repository.Virtual {
		return ReadNone
	}
	if sub.Anonymous && r.requireAuth {
		return ReadNone
	}
	scoped := sub.Scope != nil && !sub.Scope.Inherit
	if st.bypass {
		return rowsUnless(allEndpoint && !scoped)
	}
	unrestricted, any := false, false
	for _, p := range st.pset {
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
// of the repository: no package pattern at all, and no path include list (a path
// include list needs a path, which a package coordinate does not have). Path
// exclusions never apply to a coordinate without a path.
func (r resourceSet) unrestrictedPackages() bool {
	return len(r.packages.include) == 0 && len(r.packages.exclude) == 0 && len(r.paths.include) == 0
}
