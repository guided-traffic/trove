package authz

import "context"

// Ceiling is the endpoint exposure of R-EP-013 (R-AUTHZ-001 item 3,
// R-AUTHZ-015): a ceiling of the same kind as a token scope, which binds every
// subject, content:bypass included, and never grants. Decide consults it first
// for every action of every coordinate, before the repository lookup, so that a
// refusal is the same for a missing repository and for every subject. A nil
// Ceiling is no ceiling.
//
// The ceiling is evaluated on the index the request decides on: the exposure
// rules are compiled into the grant index and follow its staleness rules and its
// epochs (R-EP-012, R-AUTHZ-028). [ExposureCeiling] is the implementation of the
// endpoints area; tests may supply their own.
//
// The endpoint is the one the subject carries ([Subject.Endpoint], R-EP-013,
// Q-EP-20), and the coordinate carries the repository the client addressed;
// exposure is decided on the addressed repository while grants are decided per
// member (endpoints.md Q-EP-11).
type Ceiling interface {
	// Admits reports whether the exposure of the endpoint admits action a on the
	// coordinate. When it does not, the result says whether the refusal is that of
	// a read, which answers 404 byte-identical to a missing resource
	// (deny.exposure), or that of a write on an endpoint with exposure rules, which
	// answers 403 endpoint_read_only (deny.endpoint_read_only).
	Admits(x *Index, endpoint string, c Coordinate, a Action) CeilingVerdict
}

// CeilingVerdict is the answer of a Ceiling.
type CeilingVerdict int

// The verdicts.
const (
	// CeilingAdmits: the exposure does not restrict the action.
	CeilingAdmits CeilingVerdict = iota
	// CeilingHidesRead: a read the exposure does not admit.
	CeilingHidesRead
	// CeilingReadOnly: a write on an endpoint with exposure rules.
	CeilingReadOnly
)

// NoCeiling admits everything.
type NoCeiling struct{}

// Admits admits.
func (NoCeiling) Admits(*Index, string, Coordinate, Action) CeilingVerdict { return CeilingAdmits }

// ExposureFilter is implemented by an [Authorizer] that applies the exposure
// ceiling alone: the repository keys the subject's endpoint exposes for read,
// whatever the subject's grants. A holder of repositories:read-config lists and
// reads repositories it holds no read grant on, but on an endpoint with
// exposure rules no more than the endpoint exposes, as the ceiling binds every
// subject (R-EP-013, R-EP-014, Q-EP-12). [Engine] implements it.
type ExposureFilter interface {
	Exposed(ctx context.Context, sub Subject, keys []string) ([]string, error)
}

// Exposed implements [ExposureFilter] on the installed index. A refusal to
// decide (503, 412) is returned as an error.
func (e *Engine) Exposed(ctx context.Context, sub Subject, keys []string) ([]string, error) {
	if e.o.Ceiling == nil || sub.Endpoint == "" {
		return keys, nil
	}
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	return e.ExposedOn(idx, sub, keys), nil
}

// ExposedOn implements [Decider]: the keys the subject's endpoint exposes for
// read on the index the request decides on. A subject without an endpoint (a job,
// a command) has no ceiling and sees every key.
func (e *Engine) ExposedOn(idx *Index, sub Subject, keys []string) []string {
	cl := e.o.Ceiling
	if cl == nil || sub.Endpoint == "" {
		return keys
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		c := Coordinate{Repository: k, Kind: KindMetadata, Actions: []Action{Read}}
		if cl.Admits(idx, sub.Endpoint, c, Read) == CeilingAdmits {
			out = append(out, k)
			continue
		}
		e.m.exposureHidden(sub.Endpoint, "item")
	}
	return out
}
