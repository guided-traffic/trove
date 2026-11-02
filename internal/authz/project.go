package authz

import "sort"

// Projects as the engine sees them (R-AUTHZ-026, Q-AUTHZ-174 to Q-AUTHZ-183). The
// project service (internal/project) owns the rows; this file is the contract of
// what the grant index answers about them. Every method answers on the index the
// request holds, so a decision and the question "may this caller write here" are
// one moment.
//
// A caller that the index does not know as an administrator is never one: every
// delegated operation refuses (fail closed).

// ProjectRight names what a project administrator needs for an operation: one of the
// two privilege switches of the project, or just administration of it.
type ProjectRight string

// The rights a declaration or a question can name.
const (
	// ProjectAny asks for administration of the project, whatever its switches say:
	// reads of the project, its policies and its accounts, the removals that only
	// narrow (disable and delete a project service account, revoke its tokens).
	ProjectAny ProjectRight = "any"
	// ProjectManagePolicies asks for the switch managePolicies: project policies,
	// creating and enabling project service accounts, issuing their tokens, looking
	// up users and groups.
	ProjectManagePolicies ProjectRight = "managePolicies"
	// ProjectManageRepositories asks for the switch manageRepositories: creating,
	// changing and deleting the project's repositories, the upstream test of its
	// remotes.
	ProjectManageRepositories ProjectRight = "manageRepositories"
)

// Valid reports whether r is one of the three rights.
func (r ProjectRight) Valid() bool {
	return r == ProjectAny || r == ProjectManagePolicies || r == ProjectManageRepositories
}

// AdministeredProject is a project the subject administers, with the switches in
// force on the index that answered. It is the element of Me.Projects and of the
// answers below. Its JSON names are those of GET /authz/me (R-AUTHZ-022).
type AdministeredProject struct {
	Key                string `json:"key"`
	ManagePolicies     bool   `json:"managePolicies"`
	ManageRepositories bool   `json:"manageRepositories"`
}

// Holds reports whether the switches give the administrator the right.
func (p AdministeredProject) Holds(r ProjectRight) bool {
	switch r {
	case ProjectAny:
		return true
	case ProjectManagePolicies:
		return p.ManagePolicies
	case ProjectManageRepositories:
		return p.ManageRepositories
	}
	return false
}

// ProjectsOn returns the projects the subject may exercise administration of,
// sorted by key (Q-AUTHZ-183): a user or a member of a project's administrator group,
// through a session or a token with admin: true. It is empty for anonymous, for a
// service account, for a CI identity and for a token without admin: true. It is the
// source of Me.Projects and of the coarse pre-check of a declaration that names
// projectAdmin (Q-AUTHZ-180).
func (e *Engine) ProjectsOn(idx *Index, sub Subject) []AdministeredProject {
	if idx == nil || len(idx.projects) == 0 || !mayAdminister(sub) {
		return nil
	}
	ids := make([]string, 0, len(sub.Groups)+1)
	if sub.ID != "" {
		ids = append(ids, sub.ID)
	}
	for _, g := range sub.Groups {
		ids = append(ids, g.ID)
	}
	var out []AdministeredProject
	for _, p := range idx.projects {
		for _, id := range ids {
			if p.admins[id] {
				out = append(out, AdministeredProject{Key: p.key, ManagePolicies: p.managePolicies, ManageRepositories: p.manageRepositories})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// mayAdminister reports whether the credential can exercise a project right at
// all: a user (the break-glass administrator included) through a session or a
// token with admin: true. A service account, a CI identity and anonymous never
// administer a project, and a token that is not admin: true stays narrow
// (Q-AUTHZ-183).
func mayAdminister(sub Subject) bool {
	if sub.Anonymous || kindOf(sub) != kindUser {
		return false
	}
	return sub.Scope == nil || sub.Scope.Admin
}

// AdministersOn reports whether the subject administers the project with the right
// (a question: nothing is audited). An unknown project key is false, as is a key the
// subject does not administer, so the answer does not tell them apart.
func (e *Engine) AdministersOn(idx *Index, sub Subject, project string, right ProjectRight) bool {
	if !right.Valid() {
		return false
	}
	for _, p := range e.ProjectsOn(idx, sub) {
		if p.Key == project {
			return p.Holds(right)
		}
	}
	return false
}

// AdministersRepositoryOn reports whether the subject administers the project the
// repository is assigned to, with the right, and returns that project's key. A
// repository that belongs to no project, an unknown one and a project the subject
// does not administer all answer ("", false).
func (e *Engine) AdministersRepositoryOn(idx *Index, sub Subject, repository string, right ProjectRight) (string, bool) {
	if idx == nil {
		return "", false
	}
	project := idx.ProjectOf(repository)
	if project == "" || !e.AdministersOn(idx, sub, project, right) {
		return "", false
	}
	return project, true
}

// ProjectOf returns the key of the project the repository is assigned to on this
// index, "" for a repository without a project or an unknown one.
func (x *Index) ProjectOf(repository string) string {
	if r := x.repos[repository]; r != nil {
		return r.project
	}
	return ""
}
