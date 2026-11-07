package authz

import (
	"slices"
	"sort"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/qname"
)

// Projects as the engine sees them (R-AUTHZ-026, Q-AUTHZ-207, Q-AUTHZ-208, Q-AUTHZ-215). The project
// service (internal/project) owns the rows; this file is the contract of what the grant index answers
// about them. Every method answers on the index the request holds, so a decision and the question "may
// this caller write here" are one moment.
//
// A caller that the index does not know as a member is never one: every question refuses (fail
// closed). Service accounts, CI identities, Bastion registrations and anonymous are no members and carry
// no role; they reach a project through its policies.

// carriesRoles reports whether the credential acts as a user that can hold a project role: a UI session
// or a personal access token of a user (Q-AUTHZ-215). A service account, a CI identity, a Bastion
// registration and anonymous never carry one.
func carriesRoles(sub Subject) bool { return !sub.Anonymous && kindOf(sub) == kindUser }

// roleOf is the highest role the subject holds in the project: the role of its user ID or of one of its
// groups, whichever is higher (R-AUTHZ-026). It is ProjectRoleNone for a subject that carries no role.
func (p *projectInfo) roleOf(sub Subject) ProjectRole {
	if p == nil || !carriesRoles(sub) {
		return ProjectRoleNone
	}
	role := ProjectRoleNone
	if sub.ID != "" {
		role = p.members[sub.ID]
	}
	for _, g := range sub.Groups {
		if r := p.members[g.ID]; r > role {
			role = r
		}
	}
	return role
}

// subjectRoles are the roles of one subject, one per project it is a member of: the highest over its user
// and its groups (R-AUTHZ-026). A decision derives them once (evalState) and asks them per repository.
type subjectRoles struct {
	list []membership
	// at indexes list by project once the list is longer than smallRoles, so that both the derivation and
	// every question stay constant per membership however many projects the subject's groups are members of.
	at map[*projectInfo]int
}

// smallRoles is the length up to which a scan of the list is cheaper than a map.
const smallRoles = 8

// find returns the index of the project in the list, -1 for none.
func (r *subjectRoles) find(p *projectInfo) int {
	if r.at != nil {
		if i, ok := r.at[p]; ok {
			return i
		}
		return -1
	}
	for i := range r.list {
		if r.list[i].p == p {
			return i
		}
	}
	return -1
}

// in is the subject's role in the project, ProjectRoleNone for no membership and for no project.
func (r *subjectRoles) in(p *projectInfo) ProjectRole {
	if p == nil {
		return ProjectRoleNone
	}
	if i := r.find(p); i >= 0 {
		return r.list[i].role
	}
	return ProjectRoleNone
}

// merge adds one membership row, keeping the higher role of a project the list holds.
func (r *subjectRoles) merge(m membership) {
	if i := r.find(m.p); i >= 0 {
		r.list[i].role = max(r.list[i].role, m.role)
		return
	}
	r.list = append(r.list, m)
	switch {
	case r.at != nil:
		r.at[m.p] = len(r.list) - 1
	case len(r.list) > smallRoles:
		r.at = make(map[*projectInfo]int, 2*len(r.list))
		for i, o := range r.list {
			r.at[o.p] = i
		}
	}
}

// projectRoles derives the roles of the subject from its user ID and its group IDs through the index's
// memberships by principal (R-AUTHZ-015 performance target, R-ARCH-033): no query, a map lookup per ID and
// work per membership of the subject, none per project of the installation. A credential that carries no
// role (Q-AUTHZ-215) and an index without memberships answer the empty set without work.
func (x *Index) projectRoles(sub Subject) subjectRoles {
	var out subjectRoles
	if len(x.memberships) == 0 || !carriesRoles(sub) {
		return out
	}
	add := func(id string) {
		if id == "" {
			return
		}
		for _, m := range x.memberships[id] {
			out.merge(m)
		}
	}
	add(sub.ID)
	for _, g := range sub.Groups {
		add(g.ID)
	}
	return out
}

// inForce is the role as the credential may exercise it: the administration of an Owner needs a UI
// session or a token with admin: true (Q-AUTHZ-215), so through any other credential the Owner holds what
// a Maintainer holds, which is every action on the project's repositories.
func inForce(role ProjectRole, sub Subject) ProjectRole {
	if role == ProjectRoleOwner && !mayAdminister(sub) {
		return ProjectRoleMaintainer
	}
	return role
}

// MembershipsOn returns the projects the subject is a member of with its role in force for the
// credential, sorted by key (Q-AUTHZ-207): GET /authz/me lists it, and the project list and the
// declarations of the management API read it. It is empty for anonymous, a service account, a CI
// identity, a Bastion registration and a user without a membership. A token lists only the projects
// its scope reaches ([Engine.scopeReachesProject]): a role reaches a personal access token within its
// scope (Q-AUTHZ-215), so a token scoped to one repository's deploy does not see its owner's other
// projects and their members.
func (e *Engine) MembershipsOn(idx *Index, sub Subject) []Membership {
	if idx == nil {
		return nil
	}
	roles := idx.projectRoles(sub)
	if len(roles.list) == 0 {
		return nil
	}
	out := make([]Membership, 0, len(roles.list))
	for _, m := range roles.list {
		if e.scopeReachesProject(sub.Scope, m.p.key) {
			out = append(out, Membership{Key: m.p.key, Role: inForce(m.role, sub)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// RoleOn returns the subject's role in the project in force for its credential (as MembershipsOn names
// it): ProjectRoleNone for a project it is no member of, for one its token's scope does not reach and
// for one the index does not hold, so the answer does not tell them apart. It is a question and audits
// nothing. The declaration of an operation with projectRole compares it (R-API-008): reader admits
// every role, owner only ProjectRoleOwner.
func (e *Engine) RoleOn(idx *Index, sub Subject, project string) ProjectRole {
	if idx == nil || !e.scopeReachesProject(sub.Scope, project) {
		return ProjectRoleNone
	}
	return inForce(idx.projects[project].roleOf(sub), sub)
}

// scopeReachesProject reports whether a credential's scope reaches the project for the member's role
// outside the repositories' content, which the scope decides per repository (R-AUTHZ-013): reading the
// project and its members, and being known as its member (Q-AUTHZ-215). No scope (a session), inherit,
// admin: true and the system item projects:read reach every project; a restricted scope reaches the
// projects one of its repository elements names, whatever the element's actions, since the token acts
// in them.
func (e *Engine) scopeReachesProject(sc *scope.Scope, project string) bool {
	if sc == nil || sc.Inherit || sc.Admin || slices.Contains(sc.System, string(ProjectsRead)) {
		return true
	}
	for _, el := range sc.Repositories {
		if e.compile.selectorsCoverProject(el.Repositories, project) {
			return true
		}
	}
	return false
}

// OwnsOn reports whether the subject may administer the project: an Owner of it, through a session or a
// token with admin: true (Q-AUTHZ-208, Q-AUTHZ-215). It is a question: nothing is audited. An unknown
// project is false, as is one the subject is no Owner of, so the answer does not tell them apart.
func (e *Engine) OwnsOn(idx *Index, sub Subject, project string) bool {
	return e.RoleOn(idx, sub, project) == ProjectRoleOwner
}

// mayAdminister reports whether the credential can exercise the administration of a project at all: a
// user (the break-glass administrator included) through a session or a token with admin: true. A service
// account, a CI identity and anonymous never administer a project, and a token that is not admin: true
// stays narrow (Q-AUTHZ-215).
func mayAdminister(sub Subject) bool {
	if !carriesRoles(sub) {
		return false
	}
	return sub.Scope == nil || sub.Scope.Admin
}

// HasProject reports whether the index holds the project (R-AUTHZ-026): every project is on the index, with or
// without members and repositories. The management API answers a missing project on the index a request's
// decisions are made on.
func (x *Index) HasProject(key string) bool { return x != nil && x.projects[key] != nil }

// ProjectOf returns the key of the project of a repository on this index, "" for an unknown one.
func (x *Index) ProjectOf(name qname.Repo) string {
	if r := x.repos[name]; r != nil {
		return r.Name.Project
	}
	return ""
}
