package repository

import (
	"context"
	"fmt"
	"sync"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// CreatedRepo is the repository a creation hook is run for: the row the creating transaction has just written.
type CreatedRepo struct {
	// ID is the numeric id of the repository row.
	ID   int64
	Name qname.Repo
	// Format is the format ID of the repository.
	Format string
}

// CreateHook adds rows of a format's data port in the transaction that creates a repository of the format (R-REPO-009, Q-REPO-273):
// the first signing key of a Debian local or virtual repository is one (R-DEB-015). class is the repository's class ("local",
// "remote" or "virtual"). An error rolls the creation back.
type CreateHook func(ctx context.Context, tx meta.Tx, repo CreatedRepo, class string) error

var createHooks = struct {
	mu sync.RWMutex
	by map[string]CreateHook
}{by: map[string]CreateHook{}}

// RegisterCreateHook registers the hook of a format. It is a seam of the repository service and not of the format contract: the
// composition root binds the hook of a format, next to the data port it writes to, and the service runs it inside the creation
// transaction, after the repository row and before the audit event. A format has at most one hook; registering a second panics, as
// every registration does (a programming error found at start-up).
func RegisterCreateHook(format string, fn CreateHook) {
	if fn == nil {
		panic(fmt.Sprintf("repository: nil create hook for format %q", format))
	}
	createHooks.mu.Lock()
	defer createHooks.mu.Unlock()
	if _, dup := createHooks.by[format]; dup {
		panic(fmt.Sprintf("repository: a create hook is registered twice for format %q", format))
	}
	createHooks.by[format] = fn
}

// runCreateHooks runs the hook registered for the format of a repository that was just created.
func runCreateHooks(ctx context.Context, tx meta.Tx, repo CreatedRepo, class string) error {
	createHooks.mu.RLock()
	fn := createHooks.by[repo.Format]
	createHooks.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, tx, repo, class)
}

// resetCreateHooks empties the registry; tests use it.
func resetCreateHooks() {
	createHooks.mu.Lock()
	createHooks.by = map[string]CreateHook{}
	createHooks.mu.Unlock()
}
