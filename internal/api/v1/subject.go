package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// ActorFrom converts the caller into the actor of an audit event ([edge.ActorFrom]).
func ActorFrom(p httpserver.Principal) audit.Actor { return edge.ActorFrom(p) }

// SubjectFrom is the subject of the request whose context ctx is
// ([edge.SubjectOf]): the caller the authentication stage stored and the
// endpoint whose listener accepted the request. The one conversion lives in
// internal/api/edge, where the raw /files/ routes and the format dispatcher use
// it too; the architecture test refuses a subject built anywhere else.
func SubjectFrom(ctx context.Context) authz.Subject { return edge.SubjectOf(ctx) }

// NewAccess adapts the decision point to [Access]: the permission of an
// operation's x-trove-permission is checked for the caller of the request.
func NewAccess(a authz.Authorizer) Access { return authzAccess{a: a} }

type authzAccess struct{ a authz.Authorizer }

func (x authzAccess) Require(ctx context.Context, permission string) error {
	return x.a.Require(ctx, SubjectFrom(ctx), permission)
}

// Holds implements [Prober]: the question of whether the caller holds a
// permission, asked of an authorizer that can answer it without an audit event.
func (x authzAccess) Holds(ctx context.Context, permission string) (bool, error) {
	sub := SubjectFrom(ctx)
	if p, ok := x.a.(authz.Prober); ok {
		return p.Holds(ctx, sub, permission)
	}
	return Holds(ctx, requireOnly{x}, permission)
}

// requireOnly hides the Prober of an Access, so that Holds falls back to Require.
type requireOnly struct{ Access }

// RequireAction decides a repository action on the coordinate an operation's
// declaration names, for the caller of the request (the action form of
// x-trove-permission). The endpoint the request arrived on is part of the
// subject, because the exposure of an endpoint is a ceiling (R-EP-013).
func (x authzAccess) RequireAction(ctx context.Context, c authz.Coordinate) error {
	return x.a.Decide(ctx, SubjectFrom(ctx), c).Err()
}

var (
	_ ActionAccess = authzAccess{}
	_ Prober       = authzAccess{}
)
