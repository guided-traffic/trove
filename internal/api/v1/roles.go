package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// The role vocabulary of the contract (Q-ARCH-341, which supersedes Q-API-194): the registry, trove_build_info{role},
// the logs and the API name the role a replica runs in with the same values: api, packages, worker, web, all
// (Q-OPS-161). The role enums of the contract list them as they are; no class stands in for a role.

// listedReplicas reads the replica source (Q-ARCH-341: every replica is listed with its own role).
func (s *Server) listedReplicas(ctx context.Context) ([]gen.Replica, error) {
	return s.o.Replicas.Replicas(ctx)
}
