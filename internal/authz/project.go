package authz

import (
	"sort"

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

// projectRoles derives the roles of the subject in every project it is a member of, from its user ID and
// its group IDs, once per decision (evalState): the cost depends on the subject's groups and memberships,
// never on the number of projects, and every later question of the decision is a lookup in the short
// list (R-AUTHZ-015 performance target).
func (x *Index) projectRoles(sub Subject) []membership {
	if len(x.memberships) == 0 || !carriesRoles(sub) {
		return nil
	}
	var out []membership
	add := func(id string) {
		if id == "" {
			return
		}
		for _, m := range x.memberships[id] {
			merged := false
			for i := range out {
				if out[i].p == m.p {
					if m.role > out[i].role {
						out[i].role = m.role
					}
					merged = true
					break
				}
			}
			if !merged {
				out = append(out, m)
			}
		}
	}
	add(sub.ID)
	for _, g := range sub.Groups {
		add(g.ID)
	}
	return out
}

// roleIn looks the project up in the roles a decision derived (projectRoles).
func roleIn(roles []membership, p *projectInfo) ProjectRole {
	if p == nil {
		return ProjectRoleNone
	}
	for _, m := range roles {
		if m.p == p {
			return m.role
		}
	}
	return ProjectRoleNone
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
// identity, a Bastion registration and a user without a membership.
func (e *Engine) MembershipsOn(idx *Index, sub Subject) []Membership {
	if idx == nil {
		return nil
	}
	roles := idx.projectRoles(sub)
	if len(roles) == 0 {
		return nil
	}
	out := make([]Membership, 0, len(roles))
	for _, m := range roles {
		out = append(out, Membership{Key: m.p.key, Role: inForce(m.role, sub)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// RoleOn returns the subject's role in the project in force for its credential (as MembershipsOn names
// it): ProjectRoleNone for a project it is no member of and for one the index does not hold, so the
// answer does not tell them apart. It is a question and audits nothing. The declaration of an operation
// with projectRole compares it (R-API-008): reader admits every role, owner only ProjectRoleOwner.
func (e *Engine) RoleOn(idx *Index, sub Subject, project string) ProjectRole {
	if idx == nil {
		return ProjectRoleNone
	}
	return inForce(idx.projects[project].roleOf(sub), sub)
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

// ProjectOf returns the key of the project of a repository on this index, "" for an unknown one.
func (x *Index) ProjectOf(name qname.Repo) string {
	if r := x.repos[name]; r != nil {
		return r.Name.Project
	}
	return ""
}
