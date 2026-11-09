package deb

import (
	"context"
	"slices"
	"sync"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// RefreshHook is one part of the periodic task deb-index-refresh (Q-DEB-33): it enqueues what is due and deletes nothing. The local
// part (retention) is the task's own; the remote part (a remote's housekeeping, refresh_remote.go) and the virtual part (the virtual
// repositories whose check is due, refresh_virtual.go) register theirs from their files.
type RefreshHook func(ctx context.Context, run *jobs.Run) error

var refreshHooks = struct {
	mu sync.Mutex
	l  []RefreshHook
}{}

// RegisterRefreshHook adds a part to the task. The packages register from init, before the registry is frozen.
func RegisterRefreshHook(h RefreshHook) {
	refreshHooks.mu.Lock()
	defer refreshHooks.mu.Unlock()
	refreshHooks.l = append(refreshHooks.l, h)
}

// RunRefreshJob is the handler of the task deb-index-refresh (R-DEB-010, R-DEB-026, Q-DEB-33): it enqueues a deb-index job for each
// local repository whose retention is due or that has changes no build has published, and calls the registered hooks. It deletes
// nothing itself: the removals happen in the commit of the build.
func RunRefreshJob(ctx context.Context, run *jobs.Run) error {
	if x := bound.Load(); x != nil {
		if err := x.EnqueueDue(ctx); err != nil {
			return err
		}
	}
	refreshHooks.mu.Lock()
	hooks := append([]RefreshHook(nil), refreshHooks.l...)
	refreshHooks.mu.Unlock()
	for _, h := range hooks {
		if err := h(ctx, run); err != nil {
			return err
		}
	}
	return nil
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
