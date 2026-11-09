package repository

import (
	"context"
	"fmt"
	"sync"

	"github.com/guided-traffic/trove/internal/meta"
)

// KeysRemovedHook runs in the transaction of an update (and of the replay of its journal entry) that took fingerprints out of
// deb.upstreamKeys of a remote repository (R-DEB-020, Q-DEB-42): the format's data port clears verified_by of the releases those keys
// verified and enqueues the remote's job that verifies them again from their stored bytes. An error rolls the update back.
type KeysRemovedHook func(ctx context.Context, tx meta.Tx, repo CreatedRepo, fingerprints []string) error

var keysRemovedHooks = struct {
	mu sync.RWMutex
	by map[string]KeysRemovedHook
}{by: map[string]KeysRemovedHook{}}

// RegisterKeysRemovedHook registers the hook of a format, like [RegisterCreateHook]: a seam of the repository service that the
// composition root binds next to the format's data port. A format has at most one; a second panics.
func RegisterKeysRemovedHook(format string, fn KeysRemovedHook) {
	if fn == nil {
		panic(fmt.Sprintf("repository: nil keys-removed hook for format %q", format))
	}
	keysRemovedHooks.mu.Lock()
	defer keysRemovedHooks.mu.Unlock()
	if _, dup := keysRemovedHooks.by[format]; dup {
		panic(fmt.Sprintf("repository: a keys-removed hook is registered twice for format %q", format))
	}
	keysRemovedHooks.by[format] = fn
}

// runKeysRemovedHooks runs the hook of the repository's format; it does nothing without removed fingerprints.
func runKeysRemovedHooks(ctx context.Context, tx meta.Tx, repo CreatedRepo, fingerprints []string) error {
	if len(fingerprints) == 0 {
		return nil
	}
	keysRemovedHooks.mu.RLock()
	fn := keysRemovedHooks.by[repo.Format]
	keysRemovedHooks.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, tx, repo, fingerprints)
}
