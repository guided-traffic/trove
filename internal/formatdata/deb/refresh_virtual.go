package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The virtual part of the periodic task deb-index-refresh (R-DEB-026, Q-DEB-21): it enqueues a deb-index job for each virtual repository
// whose last check is older than its virtual.mergedMetadataTtlSeconds, so a change upstream reaches the virtual repository without a
// client request. It enqueues and deletes nothing else (Q-DEB-33).
func init() {
	RegisterRefreshHook(func(ctx context.Context, _ *jobs.Run) error {
		if x := bound.Load(); x != nil && x.Virtual != nil {
			return x.EnqueueVirtualsDue(ctx)
		}
		return nil
	})
}

// EnqueueVirtualsDue enqueues the builds of the virtual repositories that are due.
func (x *Indexer) EnqueueVirtualsDue(ctx context.Context) error {
	var ids []int64
	if err := x.Store.virtualStore.read(ctx, nil, func(qs *q.Queries) (err error) {
		ids, err = qs.DebVirtualsDue(ctx)
		return err
	}); err != nil || len(ids) == 0 {
		return err
	}
	return x.Store.virtualStore.o.DB.InTx(ctx, func(tx meta.Tx) error {
		for _, id := range ids {
			if err := x.Store.EnqueueBuild(ctx, tx, id, 0); err != nil {
				return err
			}
		}
		return nil
	})
}
