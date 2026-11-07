package authz

import "errors"

// ProjectRole is the role of a user or group in a project (Q-AUTHZ-207, Q-AUTHZ-208). The roles are
// ordered: each holds what the role before it holds. A role grants its actions on every repository of the
// project; the administration of the project (members, repositories, policies, service accounts, settings)
// belongs to Owner and needs a session or a token with admin: true (decision D2 of Q-AUTHZ-208).
type ProjectRole uint8

// The roles, lowest first. ProjectRoleNone is the absence of a role.
const (
	ProjectRoleNone ProjectRole = iota
	ProjectRoleReader
	ProjectRoleDeveloper
	ProjectRoleMaintainer
	ProjectRoleOwner
)

// ParseProjectRole reads the API spelling of a role: reader, developer, maintainer or owner.
func ParseProjectRole(s string) (ProjectRole, bool) {
	switch s {
	case "reader":
		return ProjectRoleReader, true
	case "developer":
		return ProjectRoleDeveloper, true
	case "maintainer":
		return ProjectRoleMaintainer, true
	case "owner":
		return ProjectRoleOwner, true
	}
	return ProjectRoleNone, false
}

// String is the API spelling of the role, empty for ProjectRoleNone and for a value that is no role.
func (r ProjectRole) String() string {
	switch r {
	case ProjectRoleReader:
		return "reader"
	case ProjectRoleDeveloper:
		return "developer"
	case ProjectRoleMaintainer:
		return "maintainer"
	case ProjectRoleOwner:
		return "owner"
	}
	return ""
}

// Valid reports whether r is one of the four roles.
func (r ProjectRole) Valid() bool { return r >= ProjectRoleReader && r <= ProjectRoleOwner }

// AtLeast reports whether r holds at least the rights of o.
func (r ProjectRole) AtLeast(o ProjectRole) bool { return r.Valid() && r >= o }

// Actions are the repository actions the role grants on every repository of its project:
// Reader read; Developer read, annotate and deploy; Maintainer and Owner every action.
// ProjectRoleNone grants none.
func (r ProjectRole) Actions() ActionSet {
	switch r {
	case ProjectRoleReader:
		return bitRead
	case ProjectRoleDeveloper:
		return bitRead | bitAnnotate | bitDeploy
	case ProjectRoleMaintainer, ProjectRoleOwner:
		return bitRead | bitAnnotate | bitDeploy | bitOverwrite | bitDelete | bitManage
	}
	return 0
}

var errInvalidProjectRole = errors.New("authz: a project role is reader, developer, maintainer or owner")

// MarshalText implements encoding.TextMarshaler: the role is the API string. A value that is no role
// (including ProjectRoleNone) is an error, so that a membership never serialises without one.
func (r ProjectRole) MarshalText() ([]byte, error) {
	if !r.Valid() {
		return nil, errInvalidProjectRole
	}
	return []byte(r.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler through [ParseProjectRole].
func (r *ProjectRole) UnmarshalText(b []byte) error {
	v, ok := ParseProjectRole(string(b))
	if !ok {
		return errInvalidProjectRole
	}
	*r = v
	return nil
}

// Membership is a project the subject is a member of with the role in force for the credential that asks
// (GET /authz/me, memberships). Its JSON names are those of the API schema Membership.
type Membership struct {
	Key  string      `json:"key"`
	Role ProjectRole `json:"role"`
}
