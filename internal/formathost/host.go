// Package formathost is the Host of the format contract (R-ARCH-009): the core as a
// format reaches it. It bridges the contract's types to the item store, the
// package rows, the decision point and, when they exist, the remote and virtual
// engines, and it turns the storage hooks of the item store into the calls of the
// format contract.
//
// It is an edge package. The format dispatcher (internal/api/dispatch) imports it;
// it imports neither the management API nor any data package of a format: the
// data ports are bound by the composition root ([Bindings], Q-ARCH-193), so the
// host reaches them only through [formats.Host.FormatData]. It holds no request
// state (R-ARCH-032): what belongs to a request travels in the [formats.Request]
// that every call takes.
package formathost

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

// ItemOps is the part of the item store the host uses; *items.Store implements it.
type ItemOps interface {
	Spool(ctx context.Context, body io.Reader, o items.IngestOptions) (*items.Spooled, error)
	Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error)
	Live(ctx context.Context, repo items.RepoRef, path string) (items.Item, bool, error)
	Open(ctx context.Context, it items.Item, rng *items.Range) (io.ReadCloser, error)
	Served(ctx context.Context, e items.ServedEvent)
}

var _ ItemOps = (*items.Store)(nil)

// ItemRemover removes files in the transaction of the caller; *items.Store implements
// it. The host reaches it through the package writer of [Host.Write] (a format's
// route that removes a version) and reports a writer that has none.
type ItemRemover interface {
	RemoveIn(ctx context.Context, tx meta.Tx, spec items.RemoveSpec) error
}

var _ ItemRemover = (*items.Store)(nil)

// Deps are what the host and the storage hooks share: the registry of formats, the
// data ports and the seams the package writer calls. An absent seam makes the
// matching call refuse or, for a signal, do nothing (see its documentation).
type Deps struct {
	// Formats finds a registered format; nil is formats.Lookup.
	Formats func(id string) (formats.Format, bool)
	// Bindings are the data ports; nil binds none.
	Bindings  *Bindings
	Ownership Ownership
	AutoClaim AutoClaimer
	// Refusal decides the remote refusal of a name a global claim covers (R-REPO-023).
	Refusal RemoteRefuser
	Changes Changer
	Signals Signaler
	Jobs    Jobs
}

// Options configure [New].
type Options struct {
	Runner  meta.Runner
	Decider authz.Decider
	Items   ItemOps
	// Packages reads the package rows.
	Packages *packages.Store
	Deps
	// Renditions is the cache of rendered documents; nil never hits.
	Renditions formats.RenditionCache
	// Engine is the remote engine; the host wraps it in the RemoteFetcher the
	// formats see (remote.go). Remote replaces that wrapper, for a test.
	Engine RemoteEngine
	// Remote and Resolver are the engines; nil answers "not supported" (the virtual
	// engine lands with the virtual class of the first format that has it).
	Remote   formats.RemoteFetcher
	Resolver formats.Resolver
	// DownloadBuffer is storage.download.buffer_size; below 4,096 uses 4,096.
	DownloadBuffer int
	Log            *slog.Logger
}

// Host implements formats.Host.
type Host struct{ o Options }

var _ formats.Host = (*Host)(nil)

// New creates the host.
func New(o Options) (*Host, error) {
	switch {
	case o.Runner == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: no transaction runner")
	case o.Decider == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: no decision point")
	case o.Items == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: no item store")
	case o.Packages == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: no package store")
	}
	if o.Formats == nil {
		o.Formats = formats.Lookup
	}
	if o.Renditions == nil {
		o.Renditions = missCache{}
	}
	h := &Host{}
	if o.Remote == nil && o.Engine != nil {
		o.Remote = &remote{h: h, e: o.Engine}
	}
	if o.Remote == nil {
		o.Remote = unsupportedRemote{}
	}
	if o.Resolver == nil {
		o.Resolver = unsupportedResolver{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	h.o = o
	return h, nil
}

// Ingest implements [formats.Host].
func (h *Host) Ingest() formats.Ingestor { return ingestor{h: h} }

// Items implements [formats.Host].
func (h *Host) Items() formats.ItemStore { return itemStore{h: h} }

// Packages implements [formats.Host].
func (h *Host) Packages() formats.PackageStore { return packageStore{s: h.o.Packages} }

// Remote implements [formats.Host].
func (h *Host) Remote() formats.RemoteFetcher { return h.o.Remote }

// Resolver implements [formats.Host].
func (h *Host) Resolver() formats.Resolver { return h.o.Resolver }

// Renditions implements [formats.Host].
func (h *Host) Renditions() formats.RenditionCache { return h.o.Renditions }

// FormatData implements [formats.Host].
func (h *Host) FormatData(id formats.ID) any { return h.o.Bindings.Get(id) }

// RestoredAt implements [formats.Host].
func (h *Host) RestoredAt(ctx context.Context) (time.Time, error) {
	return packages.RestoredAt(ctx, h.o.Runner)
}

// Links implements [formats.Host].
func (h *Host) Links(rq *formats.Request) formats.LinkBuilder { return newLinks(rq) }

// coordinate completes a target into the coordinate the decision point decides:
// the repository, its class and format and the request kind of the route come from
// the request, so a format names none of them (Q-ARCH-189).
func coordinate(rq *formats.Request, t formats.Target) authz.Coordinate {
	return authz.Coordinate{
		Repository: rq.Repo.Key, Class: rq.Repo.Class, Format: rq.Repo.Format,
		Kind: rq.Route.Kind.RequestKind(), Path: t.Path, Package: t.Package, Version: t.Version, Actions: t.Actions,
	}
}

// Authorize implements [formats.Host]: the decision on the request's index, then
// the repository's include and exclude patterns (R-REPO-005) on the package name
// or the path, as the format's pattern subject says. A hidden or refused target
// is the error of R-AUTHZ-016; a pattern refusal is a missing resource for a read
// and 403 not_admitted for a write, the order the generic layer keeps.
func (h *Host) Authorize(ctx context.Context, rq *formats.Request, t formats.Target) error {
	c := coordinate(rq, t)
	d := h.o.Decider.DecideOn(ctx, rq.Index, rq.Subject, c)
	if !d.Allowed() {
		return d.Err()
	}
	if subject, ok := h.patternSubject(rq, t); ok && !rq.Repo.Admits(subject, false) {
		if writes(t.Actions) {
			return errs.E(errs.Forbidden, errs.CodeNotAdmitted, "The package is not admitted by the include and exclude patterns of the repository.")
		}
		return authz.NotFound()
	}
	if rq.Repo.Class == repository.Remote && t.Package != "" && h.o.Refusal != nil && !writes(t.Actions) {
		// R-REPO-025 step 2: a name a global claim covers is not asked of a remote
		// repository, whatever its cache holds, and the answer is a missing resource.
		refused, err := h.o.Refusal.RemoteRefused(ctx, *rq.Repo, t.Package, nil)
		if err != nil {
			return err
		}
		if refused {
			return authz.NotFound()
		}
	}
	if writes(t.Actions) {
		if st := stateOf(rq); st != nil {
			st.allow(c, d)
		}
	}
	return nil
}

// CheckOwnership implements [formats.Host]: the ownership rule on the request's index and
// subject, read-only and before the body. A host with no ownership rule bound refuses, as the
// claim in the commit does.
func (h *Host) CheckOwnership(ctx context.Context, rq *formats.Request, t formats.Target, w formats.OwnershipWrite) error {
	if h.o.Ownership == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "Package ownership is not wired on this replica; the write is refused.")
	}
	return h.o.Ownership.Precheck(ctx, OwnershipCheck{Index: rq.Index, Subject: rq.Subject, Coordinate: coordinate(rq, t), Write: w})
}

// patternSubject returns what the repository's patterns are matched against for a
// target: the package name when the format's patterns match names, else the path.
// Nothing to match is a repository-level target.
func (h *Host) patternSubject(rq *formats.Request, t formats.Target) (string, bool) {
	if f, ok := h.o.Formats(rq.Repo.Format); ok && f.Descriptor().PatternSubject == formats.SubjectName {
		return t.Package, t.Package != ""
	}
	return t.Path, t.Path != ""
}

// writes reports whether any action changes state.
func writes(as []formats.Action) bool {
	for _, a := range as {
		switch a {
		case formats.ActionDeploy, formats.ActionOverwrite, formats.ActionDelete, formats.ActionManage:
			return true
		}
	}
	return false
}

// Write implements [formats.Host]. The allowed write decisions of the request are
// recorded in the transaction, as the generic layer records them (R-AUTHZ-029).
func (h *Host) Write(ctx context.Context, rq *formats.Request, fn func(tx formats.Tx, w formats.PackageWriter) error) error {
	return h.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		w, err := h.o.writerFor(tx, rq, rq.Repo.Format)
		if err != nil {
			return err
		}
		if rm, ok := h.o.Items.(ItemRemover); ok {
			w.(*writer).d.remove = rm
		}
		if err := fn(tx, w); err != nil {
			return err
		}
		return stateOf(rq).record(ctx, tx, h.o.Decider, rq.Subject)
	})
}
