// Package edge holds the conversions every HTTP entry point of the edge layer
// shares: the request to the subject authorization decides on and to the actor of
// an audit event, and the holder of the index a request decides on. It imports
// httpserver, authz and audit and none of the entry points (internal/api/v1, the
// raw /files/ routes, the format dispatcher), which all import it; the
// architecture test keeps it that way.
package edge

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
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

// SubjectFrom converts a request into the subject authorization evaluates: the
// caller the authentication stage stored, and the endpoint whose listener
// accepted the request (R-EP-002), which selects the exposure ceiling of every
// decision on the subject (R-EP-013, Q-EP-20). It is the one conversion between
// the edge types and the domain type; the architecture test refuses a Subject
// built anywhere else, so a decision cannot lose its ceiling by a hand-made
// subject.
func SubjectFrom(r *http.Request) authz.Subject { return SubjectOf(r.Context()) }

// SubjectOf is [SubjectFrom] for code that holds the context of the request and
// not the request itself. The context is the one the endpoint's chain stored the
// caller and the endpoint in; outside a request it yields the anonymous subject
// without an endpoint, which a job or a command may not be.
func SubjectOf(ctx context.Context) authz.Subject {
	p := httpserver.PrincipalFrom(ctx)
	s := authz.Subject{
		Anonymous:  p.Anonymous,
		Kind:       principal.Kind(p.Kind),
		ID:         p.ID,
		Name:       p.Name,
		Mappings:   p.Mappings,
		Scope:      p.Credential.Scope,
		TokenID:    p.Credential.TokenID,
		SessionID:  p.Credential.SessionID,
		AuthMethod: string(p.Credential.Method),
		MinEpoch:   p.MinEpoch,
	}
	for _, g := range p.Groups {
		s.Groups = append(s.Groups, principal.GroupRef{ID: g.ID, Name: g.Name})
	}
	if ep := httpserver.EndpointFrom(ctx); ep != nil {
		// The endpoint's cache-fill policy travels with its name, so the populate
		// check reads one source (R-EP-019, Q-AUTHZ-153).
		s.Endpoint, s.CacheFill = ep.Name(), ep.CacheFill()
	}
	return s
}
