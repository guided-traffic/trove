package v1

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// Requirement is a decoded x-trove-permission declaration: what the shared
// authorization step checks before an operation's handler runs (R-API-008).
// Exactly one of the forms holds.
type Requirement struct {
	// Public admits every caller: {public: true}.
	Public bool
	// Authenticated admits every authenticated caller: {authenticated: true}.
	// With PublicSwitch ({authenticated: true, publicSwitch: <key>}) the
	// operation is public while that configuration switch is on.
	Authenticated bool
	PublicSwitch  string
	// Permission is the permission Access.Require must grant: {permission: <name>}.
	Permission string
}

// String renders the declaration for error texts.
func (q Requirement) String() string {
	switch {
	case q.Public:
		return "public"
	case q.Authenticated && q.PublicSwitch != "":
		return "authenticated, or public with " + q.PublicSwitch
	case q.Authenticated:
		return "authenticated"
	case q.Permission != "":
		return "permission " + q.Permission
	}
	return "nothing (deny)"
}

// publicSwitches are the configuration switches a declaration may name.
func publicSwitches() []string { return []string{"system.public_version"} }

// parseRequirement decodes one declaration. The forms of R-API-008 that need
// authorization data that does not exist yet (repository actions, roles,
// system scopes) are refused here, so an operation can never ship with a
// declaration the enforcement step would not understand.
func parseRequirement(raw map[string]any) (Requirement, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	switch strings.Join(keys, ",") {
	case "public":
		if v, ok := raw["public"].(bool); ok && v {
			return Requirement{Public: true}, nil
		}
		return Requirement{}, fmt.Errorf("public must be true (leave it out instead of false)")
	case "authenticated", "authenticated,publicSwitch":
		if v, ok := raw["authenticated"].(bool); !ok || !v {
			return Requirement{}, fmt.Errorf("authenticated must be true")
		}
		q := Requirement{Authenticated: true}
		if sw, has := raw["publicSwitch"]; has {
			name, _ := sw.(string)
			if !contains(publicSwitches(), name) {
				return Requirement{}, fmt.Errorf("publicSwitch %v is not one of %v", sw, publicSwitches())
			}
			q.PublicSwitch = name
		}
		return q, nil
	case "permission":
		name, ok := raw["permission"].(string)
		if !ok || name == "" {
			return Requirement{}, fmt.Errorf("permission must name a permission")
		}
		return Requirement{Permission: name}, nil
	}
	return Requirement{}, fmt.Errorf("the declaration %v has a form this build does not enforce (M0 knows public, authenticated[+publicSwitch], permission)", raw)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// authorizingMux is the router the generated code registers on. Every
// operation's handler is wrapped in the authorization step of its route
// pattern at registration, so no operation reaches its handler without the
// check its declaration asks for (R-API-008), and the handlers carry no checks
// of their own.
type authorizingMux struct {
	r *httpserver.Router
	s *Server
}

func (m authorizingMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.r.Handle(pattern, m.s.authorize(pattern, http.HandlerFunc(h)))
}

func (m authorizingMux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.r.ServeHTTP(w, r) }

// authorize returns next behind the check that the operation of pattern
// declares. An operation without a declaration in the document is denied to
// everyone (Document.CheckRoutes stops start-up for it as well), and so is a
// request whose matched pattern is not the one the handler was registered for.
func (s *Server) authorize(pattern string, next http.Handler) http.Handler {
	op, declared := Operation{}, false
	if s.o.Document != nil {
		op, declared = s.o.Document.ops[pattern]
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !declared || (r.Pattern != "" && r.Pattern != pattern) {
			httpserver.Problem(w, r, errs.E(errs.Forbidden, errs.CodeForbidden, "The operation has no permission declaration."))
			return
		}
		if err := s.decide(r.Context(), op.Requirement); err != nil {
			httpserver.Problem(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decide applies one declaration to the caller of ctx.
func (s *Server) decide(ctx context.Context, q Requirement) error {
	anonymous := httpserver.PrincipalFrom(ctx).Anonymous
	unauthenticated := errs.E(errs.Unauthenticated, errs.CodeUnauthenticated,
		"This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer"))
	switch {
	case q.Public:
		return nil
	case q.Authenticated:
		if q.PublicSwitch != "" && s.switchOn(q.PublicSwitch) {
			return nil
		}
		if anonymous {
			return unauthenticated
		}
		return nil
	case q.Permission != "":
		if s.o.Access == nil {
			if anonymous {
				return unauthenticated
			}
			return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
		}
		return s.o.Access.Require(ctx, q.Permission)
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

// switchOn reports the value of a public switch; an unknown switch is off.
func (s *Server) switchOn(name string) bool {
	switch name {
	case "system.public_version":
		return s.o.PublicVersion
	}
	return false
}
