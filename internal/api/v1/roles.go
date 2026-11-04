package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/config"
)

// The role vocabulary of the contract (Q-API-194). The registry, trove_build_info{role} and the logs name the role a
// replica runs in: api, packages, worker, web, all (Q-OPS-161). The three enums of the released contract that listed
// roles (`answeredBy.role`, `Replica.roles`, `EncryptionReplica.role`) were closed at web, worker and all, and a value
// added to a closed response enum fails the compatibility check (R-ARCH-025, oasdiff
// response-property-enum-value-added). They keep their meaning of a role class: web is a replica that serves client
// endpoints, which is what api, packages and the upgrade bridge are. The exact role is the new member roleName.

// roleClass returns the value of the closed role enums for a role: web for every role that serves client endpoints.
func roleClass(role string) string {
	switch role {
	case config.RoleAPI, config.RolePackages:
		return config.RoleWeb
	}
	return role
}

// describeRole sets the role members of a replica that the registry's role name came with: Roles keeps the class
// vocabulary, RoleName the exact role. A replica listed with only Roles (a source that predates the member) takes its
// first entry as the name.
func describeRole(r *gen.Replica) {
	if r.RoleName == "" && len(r.Roles) > 0 {
		r.RoleName = string(r.Roles[0])
	}
	if r.RoleName == "" {
		return
	}
	r.Roles = []gen.ReplicaRoles{gen.ReplicaRoles(roleClass(r.RoleName))}
}

// listedReplicas reads the replica source and gives every replica its role members (Q-API-194).
func (s *Server) listedReplicas(ctx context.Context) ([]gen.Replica, error) {
	items, err := s.o.Replicas.Replicas(ctx)
	for i := range items {
		describeRole(&items[i])
	}
	return items, err
}
