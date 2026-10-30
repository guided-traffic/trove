// Package files is the generic layer: the raw view of the items of any
// repository (R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-016 to R-STOR-018,
// R-GENERIC-004 to R-GENERIC-007, R-EP-014). It decides, resolves and admits; the
// items store (internal/items) stores. A deploy, a download, a delete, the
// description of an item, the listing of a folder and the download statistics all
// go through it, so that /files/ (internal/api/rawfiles) and the items operations
// of the management API (internal/api/v1) apply the rules once.
//
// Every operation asks the decision point first, with the action of R-AUTHZ-006
// and the coordinate it concerns, on the index the request began with
// (R-AUTHZ-015; [Caller.Access]), and answers a refusal with the error of the
// decision, so that a hidden item is the same 404 as a missing one. The
// coordinate is the one the repository's format gives the path (R-EP-014:
// package name and version for npm), so the one lookup before the decision is
// the repository's in the replica's snapshot (R-ARCH-011 step 2); its outcome is
// answered only once the decision allowed the request, and no item is looked up
// before it (R-AUTHZ-016). The checks that must run in the item store's
// transactions are handed to it as callbacks; the store itself calls no decision
// point. The package reaches the object store and the items tables only through
// the store: the architecture test refuses an import of internal/blob or
// internal/meta/postgres here.
//
// Nothing here keeps state between requests (R-ARCH-032).
package files

import (
	"context"
	"io"
	"log/slog"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Items is the part of the item store the generic layer uses; *items.Store
// implements it.
type Items interface {
	Spool(ctx context.Context, body io.Reader, o items.IngestOptions) (*items.Spooled, error)
	Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error)
	CreateFolder(ctx context.Context, spec items.FolderSpec) (items.FolderResult, error)
	Live(ctx context.Context, repo items.RepoRef, path string) (items.Item, bool, error)
	Open(ctx context.Context, it items.Item, rng *items.Range) (io.ReadCloser, error)
	Children(ctx context.Context, repo items.RepoRef, cq items.ChildrenQuery, visible items.Visibility) (items.ChildrenPage, error)
	DownloadStats(ctx context.Context, itemID int64) (items.DownloadStats, error)
	Delete(ctx context.Context, spec items.DeleteSpec) (items.DeleteResult, error)
	Served(ctx context.Context, e items.ServedEvent)
}

var _ Items = (*items.Store)(nil)

// Access is what a request holds to ask the decision point: the decisions of one
// request, all on the index it began with (internal/api/edge.Access implements
// it). The subject, and with it the endpoint whose exposure is the ceiling of
// every decision (R-EP-013), is bound into it.
type Access interface {
	// Decide decides every action of the coordinate.
	Decide(ctx context.Context, c authz.Coordinate) authz.Decision
	// PathsAllowed decides one action for many paths of a repository without an
	// audit event per refused entry: a listing omits what the caller may not read,
	// and a folder delete needs `delete` on every item of the subtree (R-STOR-017,
	// R-STOR-018). classify gives each path the coordinate of its format.
	PathsAllowed(ctx context.Context, base authz.Coordinate, a authz.Action, paths []string, classify authz.Classifier) ([]bool, error)
	// RecordAllowed records an allowed write in the transaction of the change
	// (R-AUTHZ-029).
	RecordAllowed(ctx context.Context, tx meta.Tx, c authz.Coordinate, d authz.Decision) error
	// Index is the index the decisions are made on: a format's rule that is
	// decided in a write transaction (package ownership, R-AUTHZ-018) is decided
	// on it too.
	Index() *authz.Index
}

// Options configure [New].
type Options struct {
	Items Items
	Repos repository.Reader
	// Formats is the seam to the formats (Q-ARCH-206); nil treats every repository
	// as the generic format.
	Formats Formats
	Log     *slog.Logger
}

// Service is the generic layer over the item store.
type Service struct {
	items   Items
	repos   repository.Reader
	formats Formats
	log     *slog.Logger
}

// New creates the service.
func New(o Options) (*Service, error) {
	switch {
	case o.Items == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "files: no item store")
	case o.Repos == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "files: no repository reader")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{items: o.Items, repos: o.Repos, formats: o.Formats, log: o.Log}, nil
}

// Caller is who acts: the request's decision holder, the subject behind it (whose
// Endpoint the audit events record, R-EP-013), and the actor and source of the
// audit events. The holder is begun once per request by the shared step of the
// entry point (edge.Begin); the service never begins one.
type Caller struct {
	Access  Access
	Subject authz.Subject
	Actor   audit.Actor
	Source  audit.Source
	// Recheck begins a holder on the index the replica holds now, with the key
	// made known. A deploy decides again after its body arrived, and that
	// decision must see a grant removed while the body was on the wire: it is
	// made against the index of the moment of the commit, not the one the request
	// began with (R-AUTHZ-015, Q-ARCH-208). Nil decides again on Access, which is
	// right for a caller whose request is short.
	Recheck func(ctx context.Context, key string) (Access, error)
}

// afterBody returns the caller whose decisions are made on the index of this
// moment, for the decisions that follow the arrival of a request body.
func (c Caller) afterBody(ctx context.Context, key string) (Caller, error) {
	if c.Recheck == nil {
		return c, nil
	}
	a, err := c.Recheck(ctx, key)
	if err != nil {
		return c, err
	}
	c.Access = a
	return c, nil
}

// decide asks the decision point for the actions on a path, on the coordinate
// its format gives it, and returns the decision for the caller to keep
// (RecordAllowed) or the error of its refusal.
//
// A key the grant index does not hold yet was made known when the request began
// its index (authz.Decider.Begin, R-ARCH-011 step 2, R-API-052).
func (c Caller) decide(ctx context.Context, repo, path string, pc PathClass, kind authz.RequestKind, actions ...authz.Action) (authz.Decision, error) {
	d := c.Access.Decide(ctx, coordinate(repo, path, pc, kind, actions...))
	if !d.Allowed() {
		return d, d.Err()
	}
	return d, nil
}

// coordinate is the coordinate of a path of a repository with the package and
// the version its format gives it (R-EP-014).
func coordinate(repo, path string, pc PathClass, kind authz.RequestKind, actions ...authz.Action) authz.Coordinate {
	return authz.Coordinate{Repository: repo, Path: path, Package: pc.Package, Version: pc.Version, Kind: kind, Actions: actions}
}

// recorder returns the callback that records an allowed write in the item
// store's transaction (R-AUTHZ-029).
func (c Caller) recorder(repo, path string, pc PathClass, d authz.Decision, actions ...authz.Action) func(context.Context, meta.Tx) error {
	return func(ctx context.Context, tx meta.Tx) error {
		return c.Access.RecordAllowed(ctx, tx, coordinate(repo, path, pc, authz.KindWrite, actions...), d)
	}
}

// notFound is the one answer for a missing and for a hidden resource.
func notFound() error { return authz.NotFound() }

// target is what a request names before it is decided: the repository as the
// replica's snapshot holds it, the outcome of that lookup, and the class the
// repository's format gives the path.
type target struct {
	repo  repository.Repository
	err   error
	class PathClass
}

// locate looks up the repository a request names and classifies the path in it,
// so that the request is decided on the coordinate the format gives the path
// (R-EP-014). The lookup reads the replica's snapshot, the one lookup that
// precedes a decision (R-ARCH-011 step 2, R-AUTHZ-016); its outcome is answered
// by [target.resolved] once the decision allowed the request, so a missing
// repository and a hidden one answer alike. A repository the snapshot does not
// hold gets the class of no format, and the decision refuses it.
func (s *Service) locate(ctx context.Context, key, path string, folder bool) target {
	r, err := s.repos.ByKey(ctx, key)
	t := target{repo: r, err: err}
	if err == nil {
		t.class = s.classify(r, path, folder)
	}
	return t
}

// resolved answers the lookup after the decision allowed the request. A
// repository being deleted is not addressable. A disabled one is refused for
// /files (strict) and served to the management API.
func (t target) resolved(strict bool) (repository.Repository, error) {
	if t.err != nil {
		return repository.Repository{}, t.err
	}
	if t.repo.Lifecycle == repository.Deleting {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key.")
	}
	if t.repo.Disabled && strict {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	}
	return t.repo, nil
}

// errForbidden is the 403 of an action the caller does not hold on a path.
func errForbidden(a authz.Action, repo, path string) error {
	target := repo
	if path != "" {
		target += "/" + path
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The action "+string(a)+" is not permitted on "+target+".")
}
