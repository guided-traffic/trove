package authz

// Ceiling is the hook for the endpoint exposure of R-EP-013 (R-AUTHZ-001 item 3,
// R-AUTHZ-015): a ceiling of the same kind as a token scope, which binds every
// subject, content:bypass included, and never grants. Decide consults it first
// for every action of every coordinate. The endpoints area (WP1.9) implements it
// from its exposure rules; a nil Ceiling is no ceiling, which is how a default
// endpoint with exposure "all" behaves.
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
	Admits(c Coordinate, a Action) CeilingVerdict
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
func (NoCeiling) Admits(Coordinate, Action) CeilingVerdict { return CeilingAdmits }
