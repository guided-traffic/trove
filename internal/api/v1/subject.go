package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// SubjectFrom converts the caller the authentication stage stored into the
// subject authorization evaluates. It is the one conversion between the edge
// type and the domain type (docs/developer/m1-contracts.md); every handler that
// asks authz a question builds its Subject here, so the two types cannot drift.
func SubjectFrom(p httpserver.Principal) authz.Subject {
	s := authz.Subject{
		Anonymous: p.Anonymous,
		Kind:      principal.Kind(p.Kind),
		ID:        p.ID,
		Name:      p.Name,
		Mappings:  p.Mappings,
		TokenID:   p.Credential.TokenID,
		SessionID: p.Credential.SessionID,
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
