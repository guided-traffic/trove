package authz

import (
	"context"
	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/repository"
)

// The reach of a subject on a repository: whether the subject may read the whole of it, something of it, or
// nothing (R-BAS-012). It is the question the main installation answers about a caller for a Bastion: a full
// reader may be handed a document that is the same for every full reader, and every artifact of the
// repository, without a question per item. The rules are those of the decision point, so a full reach never
// meets a refused read and a none reach never an allowed one; TestR_BAS_012_ShapeAgreesWithTheDecisionPoint
// checks both against evaluate over the fixtures of this package.

// Reach is how much of a repository a subject may read.
type Reach int

const (
	// ReachNone: the subject may read nothing through the repository.
	ReachNone Reach = iota
	// ReachPartial is every other case in which the subject may read something.
	ReachPartial
	// ReachFull: the subject may read every package and every version reachable through the repository, and
	// every artifact; for a remote repository the subject may also populate it (R-AUTHZ-008).
	ReachFull
)

// String names the reach for logs and test failures.
func (r Reach) String() string {
	switch r {
	case ReachFull:
		return "full"
	case ReachPartial:
		return "partial"
	}
	return "none"
}

// ReachResult is the reach of one repository key and, for ReachNone, the outcome a read of the repository
// would be refused with.
type ReachResult struct {
	Reach Reach
	// Denial is DenyUnauthenticated, DenyVisible or DenyHidden when Reach is ReachNone: the refusal a read of
	// the repository by this subject gets (R-AUTHZ-016). It is zero (Allow) otherwise.
	Denial Outcome
}

// Reacher is the form of the decision point that classifies repositories by reach. The engine implements
// it next to [Decider].
type Reacher interface {
	// ReachOn classifies keys for the subject on the request's index. A key the index does not know is
	// ReachNone with the refusal of a missing repository. An error is a refusal to decide (503, 412).
	ReachOn(ctx context.Context, idx *Index, sub Subject, keys []qname.Repo) (map[qname.Repo]ReachResult, error)
}

var _ Reacher = (*Engine)(nil)

// maxReachDepth bounds the recursion through nested virtual repositories, as Index.reaches does.
const maxReachDepth = 16

// ReachOn implements [Reacher].
//
//   - A local or remote repository is full when the subject's read shape of its packages is [ReadAll] with
//     the capability content:metadata-read left out (it never reads an artifact, Q-AUTHZ-172) and, for a
//     remote repository, the populate check of R-AUTHZ-008 passes; it is none when the shape is [ReadNone]
//     or the endpoint does not expose the repository; partial otherwise. An endpoint with exposure rules or a
//     token scope that narrows makes the shape [ReadRows], so the reach is partial at most.
//   - A virtual repository is none when the read gate of R-AUTHZ-009 rule 1 refuses (this includes the
//     anonymous rule 6), and none when no member is readable; full when the gate also admits an artifact read
//     and every member, recursively, is full on the request's terms (the virtual repository is the addressed
//     one: the exposure and the token scope are decided through it, Q-EP-11, Q-AUTHZ-163; the gate of a nested
//     virtual repository is asked as a member's read); partial otherwise.
//   - An anonymous subject is none while anonymous access is off and on a repository that requires
//     authentication.
func (e *Engine) ReachOn(ctx context.Context, idx *Index, sub Subject, keys []qname.Repo) (map[qname.Repo]ReachResult, error) {
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	out := make(map[qname.Repo]ReachResult, len(keys))
	st := e.newEvalState(idx, sub)
	// The same state without content:metadata-read: the shape of what the grants alone let the subject read,
	// which is what an artifact read is decided on.
	art := *st
	art.metadata = false
	c := &reachCtx{st: st, art: &art, allEndpoint: e.exposesEverything(sub.Endpoint)}
	for _, k := range keys {
		out[k] = e.reachOf(c, k)
	}
	return out, nil
}

type reachCtx struct {
	st, art     *evalState
	allEndpoint bool
}

// reachNone builds the refusal a read of key gets, from the decision point itself.
func (e *Engine) reachNone(c *reachCtx, key qname.Repo) ReachResult {
	d := e.evaluate(c.st.idx, c.st.sub, Coordinate{Repository: key, Kind: KindMetadata, Actions: []Action{Read}})
	if d.Outcome == Allow {
		// Allowed at the gate and still nothing readable: the packages are what is refused, which a read
		// answers as a missing one.
		return ReachResult{Reach: ReachNone, Denial: DenyHidden}
	}
	return ReachResult{Reach: ReachNone, Denial: d.Outcome}
}

func (e *Engine) reachOf(c *reachCtx, key qname.Repo) ReachResult {
	idx, sub := c.st.idx, c.st.sub
	r := idx.repo(key)
	if r == nil || (sub.Anonymous && sub.anon != anonOn) {
		return e.reachNone(c, key)
	}
	if r.Class == repository.Virtual {
		return e.reachVirtual(c, r)
	}
	if len(e.ExposedOn(idx, sub, []qname.Repo{key})) == 0 {
		return e.reachNone(c, key)
	}
	switch e.leafReach(c, r, qname.Repo{}) {
	case ReachNone:
		return e.reachNone(c, key)
	case ReachFull:
		return ReachResult{Reach: ReachFull}
	}
	return ReachResult{Reach: ReachPartial}
}

// leafReach classifies a local or remote repository on the request's terms; addressed is the virtual
// repository the client named, "" for a request on the repository itself.
func (e *Engine) leafReach(c *reachCtx, r *repoInfo, addressed qname.Repo) Reach {
	sub := c.st.sub
	if r.Class == repository.Virtual || (sub.Anonymous && r.requireAuth) {
		return ReachNone
	}
	if e.readShape(c.st, sub, r, c.allEndpoint) == ReadNone {
		return ReachNone
	}
	if e.readShape(c.art, sub, r, c.allEndpoint) != ReadAll {
		return ReachPartial
	}
	if r.Class == repository.Remote {
		d, _ := e.populate(c.st.idx, sub, Coordinate{
			Repository: r.Name, Kind: KindArtifact, Actions: []Action{Read}, Addressed: addressed,
		})
		if d.Outcome != Allow {
			return ReachPartial
		}
	}
	return ReachFull
}

func (e *Engine) reachVirtual(c *reachCtx, v *repoInfo) ReachResult {
	idx, sub := c.st.idx, c.st.sub
	gate := e.evaluate(idx, sub, Coordinate{Repository: v.Name, Kind: KindMetadata, Actions: []Action{Read}})
	if gate.Outcome != Allow {
		return ReachResult{Reach: ReachNone, Denial: gate.Outcome}
	}
	artGate := e.evaluate(idx, sub, Coordinate{Repository: v.Name, Kind: KindArtifact, Actions: []Action{Read}})
	all, any := true, false
	var walk func(r *repoInfo, depth int)
	walk = func(r *repoInfo, depth int) {
		if depth > maxReachDepth {
			all = false
			return
		}
		for _, mk := range r.members {
			m := idx.repo(mk)
			switch {
			case m == nil:
				all = false
			case m.Class == repository.Virtual:
				// A nested virtual repository is a member like another: the resolver skips it when its own gate
				// refuses the read as a read through the addressed repository (MemberUsable, R-AUTHZ-015).
				nested := Coordinate{Repository: m.Name, Kind: KindMetadata, Actions: []Action{Read}, Addressed: v.Name}
				if e.evaluate(idx, sub, nested).Outcome != Allow {
					all = false
					continue
				}
				nested.Kind = KindArtifact
				if e.evaluate(idx, sub, nested).Outcome != Allow {
					all = false
				}
				walk(m, depth+1)
			default:
				switch e.leafReach(c, m, v.Name) {
				case ReachFull:
					any = true
				case ReachPartial:
					any, all = true, false
				default:
					all = false
				}
			}
		}
	}
	walk(v, 0)
	switch {
	case !any:
		return ReachResult{Reach: ReachNone, Denial: DenyHidden}
	case all && artGate.Outcome == Allow:
		return ReachResult{Reach: ReachFull}
	}
	return ReachResult{Reach: ReachPartial}
}
