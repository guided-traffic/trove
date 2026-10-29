package app

import (
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// endpointRules turns an endpoint's facts into the rules the authentication
// stage applies after it selected the credential (R-EP-010).
//
// WP1.9 (endpoints) replaces the body with the configured values of
// auth.token_types, auth.bound_tokens_only, auth.anonymous and the surface;
// until then it is the default endpoint's defaults (R-EP-010).
func endpointRules(rt *endpointRuntime) auth.EndpointRules {
	return auth.EndpointRules{
		Name:           rt.cfg.Name,
		AcceptsSession: true,
		TokenTypes:     []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT},
		Anonymous:      true,
	}
}

// principalFromResult is the one conversion from the authentication stage's
// result to the value the chain stores. The identity area owns it; it holds IDs
// and states only, never a token value or a hash.
func principalFromResult(r auth.Result) httpserver.Principal {
	if r.Anonymous {
		return httpserver.AnonymousPrincipal()
	}
	p := httpserver.Principal{
		Kind: httpserver.PrincipalKind(r.Principal.Kind),
		ID:   r.Principal.ID,
		Name: r.Principal.Name,
		Credential: httpserver.Credential{
			Method:        httpserver.AuthMethod(r.Credential.Method),
			SessionID:     r.Credential.SessionID,
			TokenID:       r.Credential.TokenID,
			TokenType:     httpserver.TokenType(r.Credential.TokenType),
			BasicUsername: r.Credential.BasicUsername,
		},
	}
	for _, g := range r.Groups {
		p.Groups = append(p.Groups, httpserver.GroupRef{ID: g.ID, Name: g.Name})
	}
	if sc := r.Credential.Scope; sc != nil {
		out := httpserver.Scope{Inherit: sc.Inherit, Admin: sc.Admin, System: sc.System}
		for _, g := range sc.Repositories {
			out.Repositories = append(out.Repositories, httpserver.ScopeGrant{
				Repositories: g.Repositories, Actions: g.Actions,
				Packages: httpserver.Patterns{Include: g.Packages.Include, Exclude: g.Packages.Exclude},
				Paths:    httpserver.Patterns{Include: g.Paths.Include, Exclude: g.Paths.Exclude},
			})
		}
		p.Credential.Scope = &out
		p.Credential.Endpoints = sc.Endpoints
	}
	return p
}
