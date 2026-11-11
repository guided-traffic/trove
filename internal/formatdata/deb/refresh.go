package deb

import (
	"context"
	"slices"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// runRefresh is the handler of the task deb-index-refresh (R-DEB-010, R-DEB-026, Q-DEB-33): it enqueues a deb-index job for each
// local repository whose retention is due or that has changes no build has published, for each virtual repository whose check is due
// (refresh_virtual.go), and runs the remote part (refresh_remote.go). It deletes nothing itself: the removals happen in the commit of
// the build. The remote part is the one of the registry the handler was built for, never one a process-wide list collected from
// earlier replicas (Q-DEB-120): a process that builds a second replica, as the integration tests do, would otherwise run the part of
// a stopped one on its closed pools. The local and virtual parts are those of the registry's own Indexer (Q-DEB-121).
func runRefresh(remote jobs.Handler, indexer func() *Indexer) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		var x *Indexer
		if indexer != nil {
			x = indexer()
		}
		if x != nil {
			if err := x.EnqueueDue(ctx); err != nil {
				return err
			}
			if x.Virtual != nil {
				if err := x.EnqueueVirtualsDue(ctx); err != nil {
					return err
				}
			}
		}
		return remote(ctx, run)
	}
}

// EnqueueDue enqueues the builds that are due without a change to trigger them: retention gives up a generation's by-hash files only in
// a build (Q-DEB-33), and a change whose build never ran (a lost job, a restore) is built here (the safety net of R-DEB-010).
func (x *Indexer) EnqueueDue(ctx context.Context) error {
	var ids []int64
	err := x.Store.buildStore.read(ctx, nil, func(qs *q.Queries) error {
		due, err := qs.DebRetentionDue(ctx, x.ByHashKeep.Seconds())
		if err != nil {
			return err
		}
		changed, err := qs.DebChangedRepositories(ctx)
		if err != nil {
			return err
		}
		ids = append(due, changed...)
		return nil
	})
	if err != nil {
		return err
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return x.Store.buildStore.o.DB.InTx(ctx, func(tx meta.Tx) error {
		for _, id := range ids {
			if err := x.Store.EnqueueBuild(ctx, tx, id, 0); err != nil {
				return err
			}
		}
		return nil
	})
}
