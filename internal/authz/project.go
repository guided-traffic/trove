package authz

// Projects as the engine sees them (R-AUTHZ-026, Q-AUTHZ-174 to Q-AUTHZ-183). The
// project service (internal/project) owns the rows; this file is the contract of
// what the grant index answers about them. Every method answers on the index the
// request holds, so a decision and the question "may this caller write here" are
// one moment.
//
// The bodies of the questions are stubs that answer "no project, no right" until
// WP-A fills them: a caller is never an administrator of anything, so every
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
	_, _ = idx, sub
	return nil // WP-A
}

// AdministersOn reports whether the subject administers the project with the right
// (a question: nothing is audited). An unknown project key is false, as is a key the
// subject does not administer, so the answer does not tell them apart.
func (e *Engine) AdministersOn(idx *Index, sub Subject, project string, right ProjectRight) bool {
	_, _, _, _ = idx, sub, project, right
	return false // WP-A
}

// AdministersRepositoryOn reports whether the subject administers the project the
// repository is assigned to, with the right, and returns that project's key. A
// repository that belongs to no project, an unknown one and a project the subject
// does not administer all answer ("", false).
func (e *Engine) AdministersRepositoryOn(idx *Index, sub Subject, repository string, right ProjectRight) (string, bool) {
	_, _, _, _ = idx, sub, repository, right
	return "", false // WP-A
}

// ProjectOf returns the key of the project the repository is assigned to on this
// index, "" for a repository without a project or an unknown one.
func (x *Index) ProjectOf(repository string) string {
	_ = repository
	return "" // WP-A
}
