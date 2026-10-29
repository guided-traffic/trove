package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// ActorFrom converts the caller into the actor of an audit event: kind, principal
// ID, name, and the credential that proved it (token ID and method). Every
// handler and every Caller of a domain call builds its actor here, so an event
// names the credential whatever area records it (Q-OPS-49).
func ActorFrom(p httpserver.Principal) audit.Actor {
	return audit.Actor{
		Kind: string(p.Kind), ID: p.ID, Name: p.Name,
		TokenID: p.Credential.TokenID, AuthMethod: string(p.Credential.Method),
	}
}

// SubjectFrom converts the caller the authentication stage stored into the
// subject authorization evaluates. It is the one conversion between the edge
// type and the domain type (docs/developer/m1-contracts.md); every handler that
// asks authz a question builds its Subject here, so the two types cannot drift.
func SubjectFrom(p httpserver.Principal) authz.Subject {
	s := authz.Subject{
		Anonymous:  p.Anonymous,
		Kind:       principal.Kind(p.Kind),
		ID:         p.ID,
		Name:       p.Name,
		Mappings:   p.Mappings,
		TokenID:    p.Credential.TokenID,
		SessionID:  p.Credential.SessionID,
		AuthMethod: string(p.Credential.Method),
		MinEpoch:   p.MinEpoch,
	}
	for _, g := range p.Groups {
		s.Groups = append(s.Groups, principal.GroupRef{ID: g.ID, Name: g.Name})
	}
	if sc := p.Credential.Scope; sc != nil {
		out := token.Scope{
			Inherit: sc.Inherit, Admin: sc.Admin, System: sc.System,
			Endpoints: p.Credential.Endpoints,
		}
		for _, g := range sc.Repositories {
			out.Repositories = append(out.Repositories, token.ScopeGrant{
				Repositories: g.Repositories, Actions: g.Actions,
				Packages: token.Patterns{Include: g.Packages.Include, Exclude: g.Packages.Exclude},
				Paths:    token.Patterns{Include: g.Paths.Include, Exclude: g.Paths.Exclude},
			})
		}
		s.Scope = &out
	}
	return s
}

// NewAccess adapts the decision point to [Access]: the permission of an
// operation's x-trove-permission is checked for the caller of the request.
func NewAccess(a authz.Authorizer) Access { return authzAccess{a: a} }

type authzAccess struct{ a authz.Authorizer }

func (x authzAccess) Require(ctx context.Context, permission string) error {
	return x.a.Require(ctx, SubjectFrom(httpserver.PrincipalFrom(ctx)), permission)
}

// Holds implements [Prober]: the question of whether the caller holds a
// permission, asked of an authorizer that can answer it without an audit event.
func (x authzAccess) Holds(ctx context.Context, permission string) (bool, error) {
	sub := SubjectFrom(httpserver.PrincipalFrom(ctx))
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
// coordinate, because the exposure of an endpoint is a ceiling (R-EP-013).
func (x authzAccess) RequireAction(ctx context.Context, c authz.Coordinate) error {
	if ep := httpserver.EndpointFrom(ctx); ep != nil {
		c.Endpoint = ep.Name()
	}
	return x.a.Decide(ctx, SubjectFrom(httpserver.PrincipalFrom(ctx)), c).Err()
}

var (
	_ ActionAccess = authzAccess{}
	_ Prober       = authzAccess{}
)
