package authz

import (
	"sort"

	"github.com/guided-traffic/trove/internal/qname"
)

// Projects as the engine sees them (R-AUTHZ-026, Q-AUTHZ-207, Q-AUTHZ-208). The project
// service (internal/project) owns the rows; this file is the contract of what the grant
// index answers about them. Every method answers on the index the request holds, so a
// decision and the question "may this caller write here" are one moment.
//
// A caller that the index does not know as a member is never one: every question refuses
// (fail closed). Service accounts and CI identities are no members; they reach a project
// through its policies.

// roleOf is the highest role the subject holds in the project: the role of its user ID or of one
// of its groups, whichever is higher. It is ProjectRoleNone for anonymous, for a service
// account, for a CI identity and for a Bastion registration.
func (p *projectInfo) roleOf(sub Subject) ProjectRole {
	if sub.Anonymous || kindOf(sub) != kindUser {
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

// MembershipsOn returns the projects the subject is a member of with its highest role, sorted by
// key (Q-AUTHZ-207). It is empty for anonymous, for a service account, for a CI identity and for
// a subject without a membership. It is the source of Me.Memberships.
func (e *Engine) MembershipsOn(idx *Index, sub Subject) []Membership {
	if idx == nil || len(idx.projects) == 0 {
		return nil
	}
	var out []Membership
	for _, p := range idx.projects {
		if role := p.roleOf(sub); role != ProjectRoleNone {
			if role == ProjectRoleOwner && !mayAdminister(sub) {
				role = ProjectRoleMaintainer // the administration needs a session or a token with admin: true (D2)
			}
			out = append(out, Membership{Key: p.key, Role: role})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// OwnsOn reports whether the subject may administer the project: an Owner of it, through a session
// or a token with admin: true (decision D2 of Q-AUTHZ-208). It is a question: nothing is audited. An
// unknown project is false, as is one the subject is no Owner of, so the answer does not tell them
// apart.
func (e *Engine) OwnsOn(idx *Index, sub Subject, project string) bool {
	if idx == nil || !mayAdminister(sub) {
		return false
	}
	p := idx.projects[project]
	return p != nil && p.roleOf(sub) == ProjectRoleOwner
}

// mayAdminister reports whether the credential can exercise the administration of a project at
// all: a user (the break-glass administrator included) through a session or a token with
// admin: true. A service account, a CI identity and anonymous never administer a project, and a
// token that is not admin: true stays narrow (Q-AUTHZ-183).
func mayAdminister(sub Subject) bool {
	if sub.Anonymous || kindOf(sub) != kindUser {
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
