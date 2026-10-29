// Package files is the items side of the raw file transfer (R-STOR-007,
// R-STOR-012, R-STOR-013, R-STOR-016 to R-STOR-018, R-GENERIC-004 to
// R-GENERIC-007): deploy, download, delete, item information, folder listings
// and download statistics of the files in a repository, on the blob store and
// the items tables. The HTTP layers are thin: internal/api/rawfiles serves
// /files/ and internal/api/v1 serves the items operations of the management API,
// and both call this package, so the rules exist once.
//
// Every operation asks the decision point first, with the action of R-AUTHZ-006
// and the coordinate it concerns (R-AUTHZ-016: authorization runs before any
// lookup of the repository or the item), and answers a refusal with the error of
// the decision, so that a hidden item is the same 404 as a missing one. Nothing
// here keeps state between requests (R-ARCH-032) except the write-only buffers
// of the download statistics and the repository activity.
package files

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Blobs is the part of the blob store the transfer uses; *blob.Store implements
// it.
type Blobs interface {
	Ingest(ctx context.Context, body io.Reader, o blob.IngestOptions) (*blob.Spooled, error)
	Commit(ctx context.Context, sp *blob.Spooled, link blob.LinkFunc) (blob.CommitResult, error)
	Open(ctx context.Context, d blob.Digest, size int64, o blob.OpenOptions) (io.ReadCloser, error)
}

// JobEnqueuer enqueues a job in the caller's transaction; *jobs.Queue implements
// it.
type JobEnqueuer interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// PathDecider is implemented by the decision point beyond the Authorizer
// contract: one action for many paths of a repository on one index, without an
// audit event per refused entry (authz.Engine.PathsAllowed). A listing omits
// what the caller may not read, and a folder delete needs `delete` on every
// item of the subtree (R-STOR-017, R-STOR-018).
type PathDecider interface {
	PathsAllowed(ctx context.Context, sub authz.Subject, base authz.Coordinate, a authz.Action, paths []string) ([]bool, error)
}

// WriteRecorder is implemented by the decision point to record an allowed write in
// the transaction of the change (authz.Engine.RecordAllowed, R-AUTHZ-029).
type WriteRecorder interface {
	RecordAllowed(ctx context.Context, tx meta.Tx, sub authz.Subject, c authz.Coordinate, d authz.Decision) error
}

// ActivitySink receives "this repository was used" (repository.Activity).
type ActivitySink interface{ Touch(repoID string) }

// Options configure [New].
type Options struct {
	Runner meta.Runner
	Blobs  Blobs
	Repos  repository.Reader
	Authz  authz.Authorizer
	// Recorder records artifact.* events; nil discards them.
	Recorder audit.Recorder
	// Jobs enqueues the deletion job of a large subtree; nil makes a subtree above
	// storage.delete.max_sync_items fail with 503.
	Jobs JobEnqueuer
	// Storage is the storage section: delete bounds, the download buffer.
	Storage config.Storage
	// RecordDownloads is audit.record_downloads (R-OPS-015).
	RecordDownloads bool
	// Activity and Stats are the write-only buffers; nil disables them.
	Activity ActivitySink
	Stats    *Stats
	Log      *slog.Logger
}

// Service is the files transfer on the items tables.
type Service struct {
	run      meta.Runner
	blobs    Blobs
	repos    repository.Reader
	authz    authz.Authorizer
	paths    PathDecider
	writes   WriteRecorder
	rec      audit.Recorder
	jobs     JobEnqueuer
	cfg      config.Storage
	download bool
	activity ActivitySink
	stats    *Stats
	log      *slog.Logger
}

// New creates the service.
func New(o Options) (*Service, error) {
	switch {
	case o.Runner == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "files: no transaction runner")
	case o.Blobs == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "files: no blob store")
	case o.Repos == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "files: no repository reader")
	case o.Authz == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "files: no authorizer")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Service{
		run: o.Runner, blobs: o.Blobs, repos: o.Repos, authz: o.Authz, rec: o.Recorder, jobs: o.Jobs,
		cfg: o.Storage, download: o.RecordDownloads, stats: o.Stats, log: o.Log,
	}
	if o.Activity != nil {
		s.activity = o.Activity
	}
	s.paths, _ = o.Authz.(PathDecider)
	s.writes, _ = o.Authz.(WriteRecorder)
	return s, nil
}

// Caller is who acts and through which endpoint: the subject the decision point
// evaluates, the endpoint of the request (the exposure of an endpoint is a
// ceiling, R-EP-013), and the actor and source of the audit events.
type Caller struct {
	Subject  authz.Subject
	Endpoint string
	Actor    audit.Actor
	Source   audit.Source
}

// actorID returns the actor's principal id when it is one, "" otherwise (a
// break-glass administrator has no row of the principal table).
func (c Caller) actorID() string { return principal.IDOrEmpty(c.Actor.ID) }

// decide asks the decision point for the actions on a path and returns the
// decision for the caller to keep (RecordAllowed) or the error of its refusal.
//
// A key the grant index does not hold yet is looked up and caught up by the
// decision point itself (authz.Engine.Know, R-ARCH-011 step 2, R-API-052).
func (s *Service) decide(ctx context.Context, c Caller, repo, path string, kind authz.RequestKind, actions ...authz.Action) (authz.Decision, error) {
	d := s.authz.Decide(ctx, c.Subject, s.coordinate(c, repo, path, kind, actions...))
	if !d.Allowed() {
		return d, d.Err()
	}
	return d, nil
}

func (s *Service) coordinate(c Caller, repo, path string, kind authz.RequestKind, actions ...authz.Action) authz.Coordinate {
	return authz.Coordinate{Repository: repo, Path: path, Kind: kind, Actions: actions, Endpoint: c.Endpoint}
}

// recordAllowed records an allowed write in the writer's transaction, when the
// decision point can (R-AUTHZ-029).
func (s *Service) recordAllowed(ctx context.Context, tx meta.Tx, c Caller, repo, path string, d authz.Decision, actions ...authz.Action) error {
	if s.writes == nil {
		return nil
	}
	return s.writes.RecordAllowed(ctx, tx, c.Subject, s.coordinate(c, repo, path, authz.KindWrite, actions...), d)
}

// notFound is the one answer for a missing and for a hidden resource.
func notFound() error { return authz.NotFound() }

// resolve looks the repository up after the decision allowed the request
// (R-ARCH-011 step 2). A repository being deleted is not addressable. A disabled
// one is refused for /files (strict) and served to the management API.
func (s *Service) resolve(ctx context.Context, key string, strict bool) (repository.Repository, error) {
	r, err := s.repos.ByKey(ctx, key)
	if err != nil {
		return repository.Repository{}, err
	}
	if r.Lifecycle == repository.Deleting {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key.")
	}
	if r.Disabled && strict {
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	}
	return r, nil
}

// repoID parses the id of a repository read model.
func repoID(r repository.Repository) int64 {
	var n int64
	for i := range len(r.ID) {
		if r.ID[i] < '0' || r.ID[i] > '9' {
			return 0
		}
		n = n*10 + int64(r.ID[i]-'0')
	}
	return n
}

// parentOf returns the parent path of a canonical path: "" for a top-level entry.
func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// ancestors lists the ancestor folders of a canonical path from the top:
// "a/b/c" has "a" and "a/b".
func ancestors(p string) []string {
	var out []string
	for i := range len(p) {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

// subtreeBounds returns the range [lo, hi) of the paths below a folder in the "C"
// collation: lo is the folder plus "/", hi the folder plus "0" (the byte after
// "/"). The root has no bounds.
func subtreeBounds(p string) (lo, hi string) {
	if p == "" {
		return "", ""
	}
	return p + "/", p + "0"
}
