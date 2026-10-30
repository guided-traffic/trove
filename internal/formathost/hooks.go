package formathost

import (
	"context"

	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

// writerFor binds a package writer to tx for a format, and to rq when there is a
// request. The writer may write properties below the format's PropertyPrefix only.
func (d Deps) writerFor(tx meta.Tx, rq *formats.Request, format string) (formats.PackageWriter, error) {
	prefix := ""
	if f, ok := d.lookup(format); ok {
		prefix = f.Descriptor().PropertyPrefix
	}
	pw, err := packages.NewWriter(tx, prefix)
	if err != nil {
		return nil, err
	}
	return &writer{w: pw, tx: tx, req: rq, d: writerDeps{
		own: d.Ownership, auto: d.AutoClaim, changes: d.Changes, signals: d.Signals, enqueuer: d.Jobs,
	}}, nil
}

func (d Deps) lookup(id string) (formats.Format, bool) {
	if d.Formats == nil {
		return formats.Lookup(id)
	}
	return d.Formats(id)
}

// Hooks turns the storage hooks of the item store into the calls of the format
// contract (R-ARCH-009, storage.md): it finds the format of the repository an event
// concerns, binds the package writer of the event's transaction, hands the format its
// data port, and calls OnCommit or OnItemsRemoved. A hook runs in the transaction of
// the change; an error rolls the change back.
type Hooks struct{ d Deps }

var _ items.Hooks = (*Hooks)(nil)

// NewHooks creates the hooks over the shared dependencies.
func NewHooks(d Deps) *Hooks { return &Hooks{d: d} }

// OnCommit implements [items.Hooks]. A repository whose format is not registered, or
// that an old job queued without one, has no hook.
func (h *Hooks) OnCommit(ctx context.Context, tx meta.Tx, e items.CommitEvent) error {
	f, ok := h.d.lookup(e.Repo.Format)
	if !ok {
		return nil
	}
	var rq *formats.Request
	ev := formats.CommitEvent{
		Origin: origin(e.Origin), Repo: refOf(e.Repo), Item: itemRef(e.Item), Path: formats.Path{Path: e.Item.Path},
		Size: e.Digests.Size, Digests: digestsOf(e.Digests), UpstreamURL: e.UpstreamURL, Payload: e.Payload,
		Actor: e.Actor, Replaced: e.Replaced, Data: h.d.Bindings.Get(f.Descriptor().ID),
	}
	if cp, ok := e.Payload.(*commitPayload); ok {
		rq, ev.Inspection, ev.Payload = cp.req, cp.inspection, cp.payload
	}
	w, err := h.d.writerFor(tx, rq, e.Repo.Format)
	if err != nil {
		return err
	}
	ev.Packages = w
	return f.OnCommit(ctx, tx, ev)
}

// OnItemsRemoved implements [items.Hooks].
func (h *Hooks) OnItemsRemoved(ctx context.Context, tx meta.Tx, e items.RemovedEvent) error {
	f, ok := h.d.lookup(e.Repo.Format)
	if !ok {
		return nil
	}
	w, err := h.d.writerFor(tx, nil, e.Repo.Format)
	if err != nil {
		return err
	}
	ev := formats.ItemsEvent{Repo: refOf(e.Repo), Reason: e.Reason, Actor: e.Actor, Packages: w, Data: h.d.Bindings.Get(f.Descriptor().ID)}
	ev.Items = make([]formats.RemovedItem, len(e.Items))
	for i, r := range e.Items {
		ev.Items[i] = formats.RemovedItem{Item: itemRef(r.Item), Version: formats.VersionID(r.Version)}
	}
	return f.OnItemsRemoved(ctx, tx, ev)
}

func origin(o items.Origin) formats.Origin {
	if o == items.OriginFill {
		return formats.OriginFill
	}
	return formats.OriginUpload
}

// FilesSeam implements files.Formats over the registry: the generic layer asks the
// format of a repository to classify a path and to check an item before it is served.
type FilesSeam struct {
	host func() (formats.Host, error)
	d    Deps
}

var _ files.Formats = (*FilesSeam)(nil)

// NewFilesSeam creates the seam. host returns the Host a ServeCheck reads through; it
// is asked when a request needs it, because the Host is built after the generic
// layer, which holds this seam.
func NewFilesSeam(host func() (formats.Host, error), d Deps) *FilesSeam {
	return &FilesSeam{host: host, d: d}
}

// Classify implements [files.Formats].
func (s *FilesSeam) Classify(repo repository.Repository, p repository.Cleaned) files.PathClass {
	f, ok := s.d.lookup(repo.Format)
	if !ok {
		return files.PathClass{}
	}
	info := f.Classify(p)
	return files.PathClass{Kind: info.Kind.RequestKind(), Package: info.Package, Version: info.Version}
}

// ServeCheck implements [files.Formats]: the format's check, for the stored marks only
// (ServeStored): /files/ never contacts an upstream.
func (s *FilesSeam) ServeCheck(ctx context.Context, repo repository.Repository, it items.Item) error {
	f, ok := s.d.lookup(repo.Format)
	if !ok {
		return nil
	}
	h, err := s.host()
	if err != nil {
		return err
	}
	ref := repoRef(repo)
	return f.ServeCheck(ctx, h, ref, formats.ItemRef{ID: it.ID, Repo: ref, Path: it.Path}, formats.ServeStored)
}
