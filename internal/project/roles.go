package project

import (
	"github.com/guided-traffic/trove/internal/authz"
)

// Role is the role of a member of a project: reader, developer, maintainer or owner
// (Q-AUTHZ-207, Q-AUTHZ-208). The type is the authorization engine's, so that the service, the
// snapshot and the API spell one role.
type Role = authz.ProjectRole

// The roles, lowest first.
const (
	RoleReader     = authz.ProjectRoleReader
	RoleDeveloper  = authz.ProjectRoleDeveloper
	RoleMaintainer = authz.ProjectRoleMaintainer
	RoleOwner      = authz.ProjectRoleOwner
)

// Member is a user or a group that is a member of a project, with its role (Q-AUTHZ-207). Only
// users and groups are members (Q-AUTHZ-215): a service account reaches a project through its
// policies.
type Member struct {
	// Project is the project key.
	Project   string
	Principal PrincipalRef
	Role      Role
}
