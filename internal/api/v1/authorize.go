package v1

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
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
	// Permission is the capability Access.Require must grant:
	// {permission: <capability>}, a name of R-AUTHZ-010.
	Permission string
	// Role is the system role the caller must hold: {role: admin}.
	Role string
	// Action is a repository action on the coordinate the request names:
	// {action: deploy, repository: query.repository, path: query.path}. The
	// sources of the coordinate are value expressions, "query.<name>" for a query
	// parameter and "path.<name>" for a route wildcard. Repository is required;
	// Package and Path are optional.
	Action     authz.Action
	Repository string
	Package    string
	Path       string
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
	case q.Role != "":
		return "role " + q.Role
	case q.Action != "":
		return "action " + string(q.Action) + " on repository " + q.Repository
	}
	return "nothing (deny)"
}

// publicSwitches are the configuration switches a declaration may name.
func publicSwitches() []string { return []string{"system.public_version"} }

// sourceRE is a value expression of the action form: "query.<name>" or
// "path.<name>".
var sourceRE = regexp.MustCompile(`^(query|path)\.[A-Za-z][A-Za-z0-9_]*$`)

// parseRequirement decodes one declaration. The forms of R-API-008 that the
// authorization data does not support (system scopes beyond the capability
// names) are refused here, so an operation can never ship with a declaration the
// enforcement step would not understand.
func parseRequirement(raw map[string]any) (Requirement, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if _, has := raw["action"]; has {
		return parseActionForm(raw)
	}
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
		if !authz.Capability(name).Valid() {
			return Requirement{}, fmt.Errorf("permission %q is not a capability of R-AUTHZ-010", name)
		}
		return Requirement{Permission: name}, nil
	case "role":
		name, ok := raw["role"].(string)
		if !ok || !authz.Role(name).Valid() {
			return Requirement{}, fmt.Errorf("role %v is not a system role (R-AUTHZ-010)", raw["role"])
		}
		return Requirement{Role: name}, nil
	}
	return Requirement{}, fmt.Errorf("the declaration %v has a form this build does not enforce (public, authenticated[+publicSwitch], permission, role, action+repository[+package][+path])", raw)
}

// parseActionForm decodes {action, repository[, package][, path]}.
func parseActionForm(raw map[string]any) (Requirement, error) {
	var q Requirement
	for k := range raw {
		switch k {
		case "action", "repository", "package", "path":
		default:
			return Requirement{}, fmt.Errorf("the action form has no member %q (action, repository, package, path)", k)
		}
	}
	a, _ := raw["action"].(string)
	q.Action = authz.Action(a)
	if !q.Action.Valid() {
		return Requirement{}, fmt.Errorf("action %v is not one of the six repository actions", raw["action"])
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"repository", &q.Repository}, {"package", &q.Package}, {"path", &q.Path}} {
		v, present := raw[f.key]
		if !present {
			continue
		}
		str, _ := v.(string)
		if !sourceRE.MatchString(str) {
			return Requirement{}, fmt.Errorf("%s %v is not a value expression (query.<name> or path.<name>)", f.key, v)
		}
		*f.dst = str
	}
	if q.Repository == "" {
		return Requirement{}, fmt.Errorf("the action form needs a repository source")
	}
	return q, nil
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
		r, err := s.begin(r, op.Requirement)
		if err == nil {
			err = s.decide(r, op.Requirement)
		}
		if err != nil {
			httpserver.Problem(w, r, err)
			return
		}
		// Recent authentication comes after the permission: a caller who may not
		// do the action is refused with 403, not asked to sign in again.
		if op.RecentAuth {
			if err := s.requireRecentAuth(r); err != nil {
				httpserver.Problem(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ActionAccess is implemented by an [Access] that also decides the action form
// of x-trove-permission: a repository action on the coordinate a request names.
// [NewAccess] returns one.
type ActionAccess interface {
	// RequireAction decides the coordinate for the caller of ctx and returns nil
	// or the error of R-AUTHZ-016 (401, 404 hidden, 403, 503).
	RequireAction(ctx context.Context, c authz.Coordinate) error
}

// begin takes the index of a request whose declaration decides (a capability, a
// role, an action on a repository) and returns the request carrying the holder,
// so that the check below and every question the handler asks are answered on
// one index (R-AUTHZ-015, Q-API-158). The repository an action declaration names
// is made known first. A declaration that decides nothing (public,
// authenticated) takes no index: an operation that never decided must not start
// to answer 503 while the index is unconfirmed; a handler that decides begins it
// itself.
func (s *Server) begin(r *http.Request, q Requirement) (*http.Request, error) {
	b, ok := s.o.Access.(Beginner)
	if !ok {
		return r, nil
	}
	var permission string
	var keys []string
	switch {
	case q.Permission != "":
		permission = q.Permission
	case q.Role != "":
		permission = authz.RolePermission(authz.Role(q.Role))
	case q.Action != "":
		keys = []string{valueOf(r, q.Repository)}
	default:
		return r, nil
	}
	a, err := b.Begin(r.Context(), permission, keys...)
	if err != nil || a == nil {
		return r, err
	}
	return r.WithContext(edge.WithAccess(r.Context(), a)), nil
}

// decide applies one declaration to the caller of the request.
func (s *Server) decide(r *http.Request, q Requirement) error {
	ctx := r.Context()
	anonymous := httpserver.PrincipalFrom(ctx).Anonymous
	unauthenticated := errs.E(errs.Unauthenticated, errs.CodeUnauthenticated,
		"This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer"))
	refuse := func() error {
		if anonymous {
			return unauthenticated
		}
		return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
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
			return refuse()
		}
		return s.o.Access.Require(ctx, q.Permission)
	case q.Role != "":
		if s.o.Access == nil {
			return refuse()
		}
		return s.o.Access.Require(ctx, authz.RolePermission(authz.Role(q.Role)))
	case q.Action != "":
		aa, ok := s.o.Access.(ActionAccess)
		if !ok {
			return refuse()
		}
		return aa.RequireAction(ctx, coordinateOf(r, q))
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

// coordinateOf builds the coordinate an action declaration names from the
// request. A source that is absent from the request is the empty string, which
// names no repository: authorization then refuses, and the handler never runs.
func coordinateOf(r *http.Request, q Requirement) authz.Coordinate {
	c := authz.Coordinate{
		Repository: valueOf(r, q.Repository), Package: valueOf(r, q.Package), Path: valueOf(r, q.Path),
		Kind: authz.KindWrite, Actions: []authz.Action{q.Action},
	}
	if q.Action == authz.Read {
		c.Kind = authz.KindMetadata
	}
	return c
}

// valueOf reads a value expression: "query.<name>" or "path.<name>".
func valueOf(r *http.Request, src string) string {
	kind, name, ok := strings.Cut(src, ".")
	if !ok {
		return ""
	}
	switch kind {
	case "query":
		return r.URL.Query().Get(name)
	case "path":
		return r.PathValue(name)
	}
	return ""
}

// switchOn reports the value of a public switch; an unknown switch is off.
func (s *Server) switchOn(name string) bool {
	switch name {
	case "system.public_version":
		return s.o.PublicVersion
	}
	return false
}
