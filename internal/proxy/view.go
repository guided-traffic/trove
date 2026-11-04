package proxy

import (
	"context"

	"github.com/guided-traffic/trove/internal/items"
)

// View is the read-only side of the remote engine: what the stored state of a remote repository says, read without
// contacting an upstream and without storing anything (Q-ARCH-294). It is what the explanation of a resolution
// (R-REPO-019) asks of the remote members on a replica that fills no cache, the role api. It is built on the same
// readers as the engine, so its answers are the engine's; it holds no upstream clients and no blob store, and its
// type has no method that fetches, fills or probes, so such a call cannot be written, not merely not made.
type View struct{ e *Engine }

// NewView builds the view. It takes the options of the engine; Blobs and Clients are not read and may be nil.
func NewView(o Options) (*View, error) {
	e, err := newEngine(o, true)
	if err != nil {
		return nil, err
	}
	return &View{e: e}, nil
}

// Cached is [Engine.Cached]: the item of a stored artifact, unless its entry or its package is gone.
func (v *View) Cached(ctx context.Context, rq Request) (items.Item, bool, error) {
	return v.e.Cached(ctx, rq)
}

// EntryState is [Engine.EntryState]: what the cache entries of a path say.
func (v *View) EntryState(ctx context.Context, repoID int64, path string) (EntryState, error) {
	return v.e.EntryState(ctx, repoID, path)
}

// CachedArtifacts is [Engine.CachedArtifacts]: the cached artifact paths below a prefix.
func (v *View) CachedArtifacts(ctx context.Context, repoID int64, prefix string) ([]string, error) {
	return v.e.CachedArtifacts(ctx, repoID, prefix)
}

// Peek is [Engine.Peek]: a current negative entry, the offline mode and the remaining open time of the circuit.
func (v *View) Peek(ctx context.Context, rem Remote, path, variant string) (Peek, error) {
	return v.e.Peek(ctx, rem, path, variant)
}

// Name, Start and Stop make the view a component of the composition root: Stop ends the background reloads of the
// repositories' upstream state, which a change of the state starts.
func (v *View) Name() string                   { return "proxy-view" }
func (v *View) Start(context.Context) error    { return nil }
func (v *View) Stop(ctx context.Context) error { return v.e.Stop(ctx) }
