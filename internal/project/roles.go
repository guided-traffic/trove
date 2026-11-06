package project

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
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
// users and groups are members (Q-AUTHZ-183): a service account reaches a project through its
// policies.
type Member struct {
	// Project is the project key.
	Project   string
	Principal PrincipalRef
	Role      Role
}

func notImplemented(what string) error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The operation "+what+" is not implemented yet.")
}

// Members implements [Service].
func (s *service) Members(ctx context.Context, key string) ([]Member, error) {
	return nil, notImplemented("project.Members")
}

// AddMember implements [Service].
func (s *service) AddMember(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypassLastOwner bool) (Member, bool, int64, error) {
	return Member{}, false, 0, notImplemented("project.AddMember")
}

// SetRole implements [Service].
func (s *service) SetRole(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypassLastOwner bool) (Member, int64, error) {
	return Member{}, 0, notImplemented("project.SetRole")
}

// RemoveMember implements [Service].
func (s *service) RemoveMember(ctx context.Context, actor authz.Subject, key, ref string, bypassLastOwner bool) (int64, error) {
	return 0, notImplemented("project.RemoveMember")
}

// MoveRepository implements [Service].
func (s *service) MoveRepository(ctx context.Context, actor authz.Subject, from qname.Repo, toProject string) (qname.Repo, int64, error) {
	return qname.Repo{}, 0, notImplemented("project.MoveRepository")
}
