package jobs

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
)

// The kinds and tasks of the coordination layer itself (R-ARCH-016 table,
// R-OPS-019).
const (
	// KindClusterChangePrune deletes change-log rows (R-ARCH-018).
	KindClusterChangePrune = "cluster-change-prune"
	// KindReplicaRegistryPrune deletes registry rows stale for 24 h (R-OPS-003).
	KindReplicaRegistryPrune = "replica-registry-prune"
)

// replicaPruneAfter is how long a registry row must be stale before the prune
// task deletes it (R-OPS-003).
const replicaPruneAfter = 24 * time.Hour

// RegisterCoordinationKinds registers cluster-change-prune and
// replica-registry-prune and their tasks, both every ten minutes
// (`*/10 * * * *`), exclusive, in the maintenance queue. The composition root
// calls it once, before Freeze, on every role: a web replica must know the
// kinds to enqueue them (the manual trigger of R-OPS-019).
func RegisterCoordinationKinds(r *Registry, c config.Cluster) {
	r.RegisterKind(KindClusterChangePrune, KindSpec{
		Queue: "maintenance", DedupMode: Exclusive, MaxAttempts: 3,
		Handler: func(ctx context.Context, run *Run) error {
			n, err := cluster.PruneChanges(ctx, run.DB(), c.ChangeRetention)
			if err == nil {
				err = run.SetResult(map[string]int64{"deleted": n})
			}
			return err
		},
	})
	r.RegisterTask(KindClusterChangePrune, TaskSpec{Schedule: "*/10 * * * *"})

	r.RegisterKind(KindReplicaRegistryPrune, KindSpec{
		Queue: "maintenance", DedupMode: Exclusive, MaxAttempts: 3,
		Handler: func(ctx context.Context, run *Run) error {
			n, err := cluster.PruneReplicas(ctx, run.DB(), replicaPruneAfter)
			if err == nil {
				err = run.SetResult(map[string]int64{"deleted": n})
			}
			return err
		},
	})
	r.RegisterTask(KindReplicaRegistryPrune, TaskSpec{Schedule: "*/10 * * * *"})
}
