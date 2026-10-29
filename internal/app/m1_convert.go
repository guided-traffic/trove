package app

import (
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// endpointRules turns an endpoint's configuration into the rules the
// authentication stage applies after it selected the credential (R-EP-010):
// sessions only where the surface holds ui, the accepted token types, whether
// only tokens bound to the endpoint pass, and whether a request without a
// credential may become anonymous. The defaults are those of R-EP-001: the
// default endpoint accepts sessions, PATs, SATs and CI tokens, unbound, and
// defers to R-AUTH-037; a further endpoint accepts only SATs bound to it and no
// anonymous request (Q-EP-10). Configuration validation refuses an empty list
// of token types, which the stage would read as no restriction.
func endpointRules(rt *endpointRuntime) auth.EndpointRules {
	types := make([]token.Type, 0, len(rt.cfg.TokenTypes))
	for _, t := range rt.cfg.TokenTypes {
		types = append(types, token.Type(t))
	}
	return auth.EndpointRules{
		Name:            rt.cfg.Name,
		AcceptsSession:  rt.cfg.Serves(config.SurfaceUI),
		TokenTypes:      types,
		BoundTokensOnly: rt.cfg.BoundTokensOnly,
		Anonymous:       rt.cfg.Anonymous,
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
