package items

import (
	"context"

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

// NoHooks is the Hooks of a store without formats: it does nothing.
type NoHooks struct{}

// OnCommit implements [Hooks].
func (NoHooks) OnCommit(context.Context, meta.Tx, CommitEvent) error { return nil }

// OnItemsRemoved implements [Hooks].
func (NoHooks) OnItemsRemoved(context.Context, meta.Tx, RemovedEvent) error { return nil }
