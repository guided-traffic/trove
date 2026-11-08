package deb

import (
	"context"
	"sync"

	"github.com/guided-traffic/trove/internal/jobs"
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

// RunRefreshJob is the handler of the task deb-index-refresh (R-DEB-010, R-DEB-026): it enqueues a deb-index job for each repository
// whose retention is due and calls the registered hooks. It is the stub the work package of the index builder replaces (WP4); it runs
// the hooks and nothing more until then.
func RunRefreshJob(ctx context.Context, run *jobs.Run) error {
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
