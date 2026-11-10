package imagescan

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
)

// Hooks wraps the storage hooks so that the commit of a manifest wakes the scan job in the commit's transaction: the job finds the new row
// by itself (RunScan's discovery), and the wake-up only spares waiting for the next slot of the task. A manifest whose commit does not pass
// through here is still found by that slot, which is why nothing depends on the wake-up.
func Hooks(inner items.Hooks, wake func(ctx context.Context, tx meta.Tx) error) items.Hooks {
	return &hooks{inner: inner, wake: wake}
}

type hooks struct {
	inner items.Hooks
	wake  func(ctx context.Context, tx meta.Tx) error
}

var _ items.Hooks = (*hooks)(nil)

// OnCommit implements [items.Hooks].
func (h *hooks) OnCommit(ctx context.Context, tx meta.Tx, e items.CommitEvent) error {
	if err := h.inner.OnCommit(ctx, tx, e); err != nil {
		return err
	}
	if e.Repo.Format == "oci" && strings.Contains(e.Item.Path, "/_manifests/") {
		return h.wake(ctx, tx)
	}
	return nil
}

// OnItemsRemoved implements [items.Hooks].
func (h *hooks) OnItemsRemoved(ctx context.Context, tx meta.Tx, e items.RemovedEvent) error {
	return h.inner.OnItemsRemoved(ctx, tx, e)
}

// LockRemoval implements [items.RemovalLocker] by handing the call to the wrapped hooks when they implement it.
func (h *hooks) LockRemoval(ctx context.Context, tx meta.Tx, repo items.RepoRef, paths []string) error {
	if l, ok := h.inner.(items.RemovalLocker); ok {
		return l.LockRemoval(ctx, tx, repo, paths)
	}
	return nil
}
