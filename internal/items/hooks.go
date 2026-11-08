package items

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta"
)

// Origin says why an item was committed. A format's hook tells an upload by a
// client from the fill of a remote cache by it (R-ARCH-009, R-REPO-025).
type Origin uint8

const (
	// OriginUpload is a client's upload through the generic layer or through a
	// format's route.
	OriginUpload Origin = iota + 1
	// OriginFill is the artifact of a remote repository the proxy stored as an
	// item of that repository (R-REPO-025). Nobody decided it for a caller: the
	// request that asked for the artifact was decided before the fill started.
	OriginFill
)

func (o Origin) String() string {
	switch o {
	case OriginUpload:
		return "upload"
	case OriginFill:
		return "fill"
	}
	return "unknown"
}

// Ref names an item: its row id, the repository and its canonical path.
type Ref struct {
	ID   int64
	Repo RepoRef
	Path string
}

// CommitEvent is what a storage hook learns of a commit. It is raised once per
// commit that changed an item row, in the transaction that wrote the row.
type CommitEvent struct {
	Origin Origin
	Repo   RepoRef
	Item   Ref
	// Lead is the lead item of the commit when Item is the file of a
	// [Companion]; the zero Ref for the lead itself.
	Lead Ref
	// Digests are those of the stored content.
	Digests   blob.Digests
	MediaType string
	// UpstreamURL is the URL an OriginFill came from, without user info.
	UpstreamURL string
	// Payload is CommitSpec.Payload: format data the committer parsed before the
	// commit, such as a manifest.
	Payload any
	// Actor is the principal id behind the commit, "" for none.
	Actor string
	// Replaced is true when the commit replaced other content at the path.
	Replaced bool
}

// Removed is an item a removal took away.
type Removed struct {
	Item   Ref
	Digest blob.Digest
	Size   int64
	// Version is the id of the package version the item belonged to, 0 for none:
	// a hook that keeps package rows needs it after the item row is gone.
	Version int64
}

// RemovedEvent is what a storage hook learns of the removal of files: the sync
// delete and every batch of the `items-delete` job raise one per statement that
// removed files (at most 5,000), in the removing transaction. Folders are not
// reported.
type RemovedEvent struct {
	Repo  RepoRef
	Items []Removed
	// Reason is "delete" (the only reason before the trash can).
	Reason string
	// Actor is the principal id behind the removal, "" for none.
	Actor string
	// By is the audit actor of the removal, the zero value for a removal without a request (a retention run, a trash purge, a job).
	By audit.Actor
	// Source is the source of the request behind the removal, the zero value for none.
	Source audit.Source
	// InScope reports whether the live file at a path belongs to the same removal as this event, in this batch, an earlier one or a later
	// one: a delete of a subtree tells the hook in events of at most 5,000 files, or in the batches of a job, and a hook that refuses a
	// removal for what still references an item must not count what the same removal takes next. Nil: only the items of this event.
	InScope func(ctx context.Context, path string) (bool, error)
}

// Hooks are the storage hooks of the formats, injected by the composition root
// (internal/formathost turns the events into the format contract's). A hook runs
// in the transaction of the change; an error rolls the change back, and the
// hook's own writes with it (R-ARCH-009).
type Hooks interface {
	// OnCommit runs after the item row and CommitSpec.Extra, before the audit row.
	OnCommit(ctx context.Context, tx meta.Tx, e CommitEvent) error
	// OnItemsRemoved runs after the rows were deleted, before their audit rows.
	OnItemsRemoved(ctx context.Context, tx meta.Tx, e RemovedEvent) error
}

// CascadeGuard decides the paths a storage hook cascades to (RemovedEvent's Cascade): the caller of a removal that has a subject installs
// one in the context, and the host refuses the cascade when it returns an error, so the items a hook takes besides the ones the caller
// named are held to the grants of the caller (Q-OCI-76). A removal without a guard (a job, a retention run) cascades unchecked.
type CascadeGuard func(ctx context.Context, paths []string) error

type cascadeGuardKey struct{}

// WithCascadeGuard returns ctx carrying g.
func WithCascadeGuard(ctx context.Context, g CascadeGuard) context.Context {
	return context.WithValue(ctx, cascadeGuardKey{}, g)
}

// CascadeGuardFrom returns the guard ctx carries, nil for none.
func CascadeGuardFrom(ctx context.Context) CascadeGuard {
	g, _ := ctx.Value(cascadeGuardKey{}).(CascadeGuard)
	return g
}

// RemovalLocker is an optional interface of Hooks: LockRemoval runs in the transaction of a removal before the rows of the files at paths
// are deleted, so that a format can take the locks its own writers take first (a package row) and the removal waits in the order a commit
// does. Without it a removal that deletes item rows first and takes the package lock in the hook can meet a commit that holds the package
// lock and waits for those rows (R-ARCH-019 retries the one the database ends, but the order is the cheaper answer).
type RemovalLocker interface {
	LockRemoval(ctx context.Context, tx meta.Tx, repo RepoRef, paths []string) error
}

// NoHooks is the Hooks of a store without formats: it does nothing.
type NoHooks struct{}

// OnCommit implements [Hooks].
func (NoHooks) OnCommit(context.Context, meta.Tx, CommitEvent) error { return nil }

// OnItemsRemoved implements [Hooks].
func (NoHooks) OnItemsRemoved(context.Context, meta.Tx, RemovedEvent) error { return nil }
