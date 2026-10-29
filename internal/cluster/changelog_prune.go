package cluster

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// PruneChanges is the body of the cluster-change-prune task (R-ARCH-016,
// R-ARCH-018): it deletes a cluster_change row once the next row of its area is
// older than retention (cluster.change_retention). The newest row of every area
// stays, and a replica that polled within the retention still finds the row at
// its last applied epoch. It is idempotent.
func PruneChanges(ctx context.Context, db q.DBTX, retention time.Duration) (int64, error) {
	return q.New(db).PruneClusterChanges(ctx, retention.Seconds())
}
