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
// The coordinate carries the endpoint of the request and the repository the
// client addressed; exposure is decided on the addressed repository while grants
// are decided per member (endpoints.md Q-EP-11).
type Ceiling interface {
	// Admits reports whether the exposure of c.Endpoint admits action a. When it
	// does not, the result says whether the refusal is that of a read, which
	// answers 404 byte-identical to a missing resource (deny.exposure), or that of
	// a write on an endpoint with exposure rules, which answers 403
	// endpoint_read_only (deny.endpoint_read_only).
	Admits(x *Index, c Coordinate, a Action) CeilingVerdict
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
func (NoCeiling) Admits(*Index, Coordinate, Action) CeilingVerdict { return CeilingAdmits }

// endpointKey is the context key of the request's endpoint name.
type endpointKey struct{}

// WithEndpoint returns ctx carrying the name of the endpoint the request arrived
// on. The composition root sets it for every request of an endpoint's chain, from
// the listener (R-EP-002); Decide, DecideOn, Readable and Check then apply that
// endpoint's ceiling whatever endpoint a coordinate names, so no caller can
// forget it or choose another (R-EP-013, R-EP-014).
func WithEndpoint(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, endpointKey{}, name)
}

// EndpointOf returns the endpoint name WithEndpoint stored, "" outside a request
// of an endpoint (a job, a command).
func EndpointOf(ctx context.Context) string {
	s, _ := ctx.Value(endpointKey{}).(string)
	return s
}

// onEndpoint applies the endpoint of the request context to a coordinate.
func onEndpoint(ctx context.Context, c Coordinate) Coordinate {
	if ep := EndpointOf(ctx); ep != "" {
		c.Endpoint = ep
	}
	return c
}
