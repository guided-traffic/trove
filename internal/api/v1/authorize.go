package v1

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// Requirement is a decoded x-trove-permission declaration: what the shared
// authorization step checks before an operation's handler runs (R-API-008).
// Exactly one of the forms holds.
type Requirement struct {
	// Public admits every caller: {public: true}.
	Public bool
	// Bastion admits a request of a Bastion on the Bastion interface: {bastion: true} (R-BAS-005, R-BAS-008).
	// It decides on the recognition stage's result alone ([bastionmain.RecognitionFrom]): the Bastion token
	// was checked there, it has no subject of the decision point, and it grants nothing else. The form is
	// valid only with x-trove-bastion-interface, which the document checks.
	Bastion bool
	// Authenticated admits every authenticated caller: {authenticated: true}.
	// With PublicSwitch ({authenticated: true, publicSwitch: <key>}) the
	// operation is public while that configuration switch is on.
	Authenticated bool
	PublicSwitch  string
	// Permission is the capability Access.Require must grant:
	// {permission: <capability>}, a name of R-AUTHZ-010.
	Permission string
	// ProjectRole widens a Permission declaration to the members of a project
	// (Q-AUTHZ-207, Q-AUTHZ-208): {permission: <capability>, project: path.projectKey,
	// projectRole: owner} admits a caller that holds the capability, or is an Owner of the
	// addressed project (through a session or a token with admin: true); projectRole: reader
	// admits a member of the addressed project, whatever its role. Without project the
	// declaration admits a member of any project (GET /projects), and the handler narrows
	// the answer to the caller's projects. Project is a value expression (path.<name>).
	ProjectRole string
	// Project is the value expression of the project ProjectRole is about.
	Project string
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
	case q.Bastion:
		return "a recognized Bastion"
	case q.Authenticated && q.PublicSwitch != "":
		return "authenticated, or public with " + q.PublicSwitch
	case q.Authenticated:
		return "authenticated"
	case q.Permission != "" && q.ProjectRole != "":
		return "permission " + q.Permission + ", or " + q.ProjectRole + " of a project"
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
	case "bastion":
		if v, ok := raw["bastion"].(bool); ok && v {
			return Requirement{Bastion: true}, nil
		}
		return Requirement{}, fmt.Errorf("bastion must be true (leave it out instead of false)")
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
	case "permission", "permission,projectRole", "permission,project,projectRole":
		name, ok := raw["permission"].(string)
		if !ok || name == "" {
			return Requirement{}, fmt.Errorf("permission must name a permission")
		}
		if !authz.Capability(name).Valid() {
			return Requirement{}, fmt.Errorf("permission %q is not a capability of R-AUTHZ-010", name)
		}
		q := Requirement{Permission: name}
		if v, has := raw["projectRole"]; has {
			role, _ := v.(string)
			if role != "owner" && role != "reader" {
				return Requirement{}, fmt.Errorf("projectRole %v is not owner or reader", v)
			}
			q.ProjectRole = role
		}
		if v, has := raw["project"]; has {
			src, _ := v.(string)
			if !sourceRE.MatchString(src) {
				return Requirement{}, fmt.Errorf("project %v is not a value expression (query.<name> or path.<name>)", v)
			}
			if q.ProjectRole == "" {
				return Requirement{}, fmt.Errorf("project needs projectRole")
			}
			q.Project = src
		}
		return q, nil
	case "role":
		name, ok := raw["role"].(string)
		if !ok || !authz.Role(name).Valid() {
			return Requirement{}, fmt.Errorf("role %v is not a system role (R-AUTHZ-010)", raw["role"])
		}
		return Requirement{Role: name}, nil
	}
	return Requirement{}, fmt.Errorf("the declaration %v has a form this build does not enforce (public, authenticated[+publicSwitch], permission[+project][+projectRole], role, action+project+repository[+package][+path])", raw)
}

// parseActionForm decodes {action, repository[, project][, package][, path]}.
func parseActionForm(raw map[string]any) (Requirement, error) {
	var q Requirement
	for k := range raw {
		switch k {
		case "action", "repository", "project", "package", "path":
		default:
			return Requirement{}, fmt.Errorf("the action form has no member %q (action, repository, project, package, path)", k)
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
	}{{"repository", &q.Repository}, {"project", &q.Project}, {"package", &q.Package}, {"path", &q.Path}} {
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
	if q.Project == "" {
		return Requirement{}, fmt.Errorf("the action form needs a project source: a repository is named by its project and its key")
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
		if err := ciGate(r, op); err != nil {
			httpserver.Problem(w, r, err)
			return
		}
		r, err := s.begin(r, op.Requirement)
		if err == nil {
			err = s.decide(r, op.Requirement)
		}
		if err == nil {
			err = ownerRightsGate(r, op)
		}
		if err == nil {
			err = s.maintenanceGate(r, op)
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

// ciGate is the admission of CI tokens on the management API (R-CI-013, R-CI-017, Q-CI-18): a CI
// token is refused with 403 ci_token_restricted on every operation except the public ones, the
// operations of the action form (a repository action on a coordinate: the repository-content
// operations that need only repository actions) and the operations that declare x-trove-ci:
// admitted (GET and DELETE /tokens/current). The default is the refusal, so a new operation is
// closed to CI tokens until its declaration says otherwise. The package-protocol endpoints and
// /files are not operations of this table; they admit CI tokens as they admit any token. The gate
// runs before the capability check, so the answer does not depend on what the owner principal holds.
func ciGate(r *http.Request, op Operation) error {
	if p := httpserver.PrincipalFrom(r.Context()); p.Kind != httpserver.KindCI {
		return nil
	}
	if q := op.Requirement; q.Public || q.Action != "" || op.CIAdmitted {
		return nil
	}
	return errs.E(errs.Forbidden, errs.CodeCITokenRestricted, "A CI token cannot call this operation.")
}

// narrowedToken reports whether the caller presents a token whose scope does not inherit its owner's rights
// (R-AUTH-024): a token made for some repositories, some system capabilities or the owner's roles
// (admin: true) without inherit. A session and the break-glass session carry no scope.
func narrowedToken(p httpserver.Principal) bool {
	return p.Credential.Scope != nil && !p.Credential.Scope.Inherit
}

// ownerRightsGate is the rule of x-trove-owner-rights (R-API-021, Q-API-214): an operation on the caller's
// own account (profile, preferences, sessions, the revocation of all own tokens) is refused with 403
// forbidden to a token whose scope does not inherit its owner's rights, because a scope names repository
// and system rights and no right over the owner's profile. A session, the break-glass session and an
// inheriting token pass. It runs after the declaration was decided, so an anonymous caller is still asked
// to authenticate, and before the handler: no handler repeats it.
func ownerRightsGate(r *http.Request, op Operation) error {
	if !op.OwnerRights || !narrowedToken(httpserver.PrincipalFrom(r.Context())) {
		return nil
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, ownerRightsRefusal)
}

// ownerRightsRefusal is the detail of the refusal of ownerRightsGate.
const ownerRightsRefusal = "A token limited to part of its owner's rights cannot act on the owner's account; sign in or use a token that inherits the owner's rights."

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
	var keys []qname.Repo
	switch {
	case q.Permission != "":
		permission = q.Permission
	case q.Role != "":
		permission = authz.RolePermission(authz.Role(q.Role))
	case q.Action != "":
		keys = []qname.Repo{nameOf(r, q)}
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
	case q.Bastion:
		// The recognition stage stored the proof; without it the Bastion token was missing or the stage is
		// not wired, and the answer is the one of an invalid Bastion token (R-BAS-008).
		if rec, ok := bastionmain.RecognitionFrom(ctx); ok && rec.Route {
			return nil
		}
		return errs.E(errs.Unauthenticated, errs.CodeBastionTokenInvalid, "The Bastion token is missing or invalid.", errs.WithChallenge("Bearer"))
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
		if q.ProjectRole != "" && s.projectRoleAdmits(r, q) {
			return nil
		}
		err := s.o.Access.Require(ctx, q.Permission)
		if err != nil && q.ProjectRole != "" && q.Project != "" && errs.KindOf(err) == errs.Forbidden {
			return s.hideProject(r, valueOf(r, q.Project), err)
		}
		return err
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
		return aa.RequireAction(ctx, s.classified(ctx, coordinateOf(r, q)))
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

// coordinateOf builds the coordinate an action declaration names from the
// request. A source that is absent from the request is the empty string, which
// names no repository: authorization then refuses, and the handler never runs.
func coordinateOf(r *http.Request, q Requirement) authz.Coordinate {
	c := authz.Coordinate{
		Repository: nameOf(r, q), Package: valueOf(r, q.Package), Path: valueOf(r, q.Path),
		Kind: authz.KindWrite, Actions: []authz.Action{q.Action},
	}
	if q.Action == authz.Read {
		c.Kind = authz.KindMetadata
	}
	return c
}

// classified completes a coordinate that names an item path with the package and
// the version the repository's format gives it, so the shared step decides an
// items operation on the coordinate the files service decides it on (R-EP-014,
// R-AUTHZ-005): a path of a format with package names decided without its
// package fails every grant that narrows names (Q-AUTHZ-160). A path that is not
// canonical stays as it is; the handler refuses it with 400.
func (s *Server) classified(ctx context.Context, c authz.Coordinate) authz.Coordinate {
	if s.o.Files == nil || c.Path == "" || c.Package != "" {
		return c
	}
	if _, err := CheckItemPath(c.Path, PathOptions{}); err != nil {
		return c
	}
	return s.o.Files.Classified(ctx, c)
}

// nameOf is the repository an action declaration names: the project and the key sources. A source that is
// absent from the request gives the zero name, which no repository has.
func nameOf(r *http.Request, q Requirement) qname.Repo {
	return qname.Repo{Project: valueOf(r, q.Project), Key: valueOf(r, q.Repository)}
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

// hideProject turns the refusal of an operation on a project into the answer of a missing one when the
// caller is no member of the project (R-AUTHZ-026, R-AUTHZ-016): a project the caller cannot see answers like
// one that does not exist, and a member whose role is too low keeps its 403, and so does a holder of
// projects:read (the auditor reads every project, so the project is not hidden from it). The refusal (and its
// audit event) was decided by Require; an Access that cannot list memberships leaves it as it is.
func (s *Server) hideProject(r *http.Request, key string, refusal error) error {
	pa, ok := s.o.Access.(ProjectAccess)
	if !ok {
		return refusal
	}
	if sees, err := s.holds(r.Context(), string(authz.ProjectsRead)); err != nil || sees {
		return refusal
	}
	members, err := pa.Memberships(r.Context())
	if err != nil {
		return refusal
	}
	for _, m := range members {
		if m.Key == key {
			return refusal
		}
	}
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The project %q does not exist.", key))
}

// projectRoleAdmits is the extra way in of a declaration that names projectRole (Q-AUTHZ-207): the
// caller does not hold the capability, as a question without an audit event, and is an Owner of the
// addressed project (projectRole owner), or a member of it (reader); without a project source a member
// of any project. A token is a member only of the projects its scope reaches (Q-AUTHZ-215: no scope,
// inherit, admin: true, the system item projects:read, or a repository element that names the project). A caller that holds the capability, or is no member, is left to Access.Require, so the
// refusal and its audit event are the ones of any capability. An Access that cannot answer the question
// admits nobody here.
func (s *Server) projectRoleAdmits(r *http.Request, q Requirement) bool {
	ctx := r.Context()
	if held, err := s.holds(ctx, q.Permission); err != nil || held {
		return false
	}
	pa, ok := s.o.Access.(ProjectAccess)
	if !ok {
		return false
	}
	project := ""
	if q.Project != "" {
		if project = valueOf(r, q.Project); project == "" {
			return false
		}
	}
	if q.ProjectRole == "owner" {
		owns, err := pa.Owns(ctx, project)
		return err == nil && owns
	}
	members, err := pa.Memberships(ctx)
	if err != nil {
		return false
	}
	for _, m := range members {
		if project == "" || m.Key == project {
			return true
		}
	}
	return false
}
