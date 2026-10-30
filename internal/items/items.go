// Package items is the item store: the files of a repository on the blob store
// and the items tables, independent of the format that gives them a meaning
// (R-ARCH-009 `Host.Ingest()` and `Host.Items()`, R-STOR-005, R-STOR-007,
// R-STOR-012, R-STOR-013, R-STOR-015 to R-STOR-018). It spools a body, commits
// it with the link protocol (ancestor folders, the item row, the overwrite
// gate), reads items from PostgreSQL alone, opens their content through the
// verifying reader, lists a folder, removes a subtree (at once or by the
// `items-delete` job) and counts downloads.
//
// The package makes no decision and imports no authorization: whoever calls it
// has decided what it asks for. The generic layer (internal/files) decides on the
// request's index before it calls and hands the store the checks that must run in
// the store's transactions as callbacks; the proxy commits a remote fill with no
// caller to decide for; a format commits an upload through the host. The
// architecture test refuses an import of internal/authz here (R-ARCH-002), which
// is how "no authz calls" is held by construction.
//
// A format's storage hooks run in the transaction of the change they concern
// ([Hooks]); the audit events of a commit and of a removal are written in that
// transaction too. Nothing here keeps state between requests (R-ARCH-032) except
// the write-only buffer of the download statistics.
package items

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Spooled is a body in the replica-local spool with its digests, ready to commit
// (R-STOR-004). The store owns neither it nor its lifetime: the caller closes it.
type Spooled = blob.Spooled

// Expected are digests a client announced for a body; a body that differs is
// refused after it was read (R-STOR-007). The same values are stored with the item
// as its declared digests.
type Expected = blob.Expected

// Blobs is the part of the blob store the item store uses; *blob.Store implements
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

// ActivitySink receives "this repository was used" (repository.Activity).
type ActivitySink interface{ Touch(repoID string) }

// Options configure [New].
type Options struct {
	Runner meta.Runner
	Blobs  Blobs
	// Recorder records the artifact.* events; nil discards them.
	Recorder audit.Recorder
	// Jobs enqueues the deletion job of a large subtree; nil makes a subtree above
	// storage.delete.max_sync_items fail with 503.
	Jobs JobEnqueuer
	// Hooks are the storage hooks of the formats; nil runs none.
	Hooks Hooks
	// Storage is the storage section: delete bounds.
	Storage config.Storage
	// RecordDownloads is audit.record_downloads (R-OPS-015).
	RecordDownloads bool
	// Activity and Stats are the write-only buffers; nil disables them.
	Activity ActivitySink
	Stats    *Stats
	Log      *slog.Logger
}

// Store is the item store.
type Store struct {
	run      meta.Runner
	blobs    Blobs
	rec      audit.Recorder
	jobs     JobEnqueuer
	hooks    Hooks
	cfg      config.Storage
	download bool
	activity ActivitySink
	stats    *Stats
	log      *slog.Logger
}

// New creates the store.
func New(o Options) (*Store, error) {
	switch {
	case o.Runner == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "items: no transaction runner")
	case o.Blobs == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "items: no blob store")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Hooks == nil {
		o.Hooks = NoHooks{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Store{
		run: o.Runner, blobs: o.Blobs, rec: o.Recorder, jobs: o.Jobs, hooks: o.Hooks, cfg: o.Storage,
		download: o.RecordDownloads, activity: o.Activity, stats: o.Stats, log: o.Log,
	}, nil
}

// RepoRef names the repository an operation concerns: the numeric id of the items
// tables, the key the audit events carry, and the class and format a storage hook
// dispatches on. Class and Format are empty for a job queued before the job
// carried them.
type RepoRef struct {
	ID     int64
	Key    string
	Class  string
	Format string
}

// RepoOf converts a repository read model. The id of a repository is the decimal
// text of its row; one that is not a number yields 0 and matches no item.
func RepoOf(r repository.Repository) RepoRef {
	var n int64
	for i := range len(r.ID) {
		if r.ID[i] < '0' || r.ID[i] > '9' {
			n = 0
			break
		}
		n = n*10 + int64(r.ID[i]-'0')
	}
	return RepoRef{ID: n, Key: r.Key, Class: string(r.Class), Format: r.Format}
}

// touch records the use of a repository.
func (s *Store) touch(repoID int64) {
	if s.activity != nil {
		s.activity.Touch(strconv.FormatInt(repoID, 10))
	}
}

// ParentOf returns the parent path of a canonical path: "" for a top-level entry.
func ParentOf(p string) string {
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

// actorID returns the actor's principal id when it is one, "" otherwise (a
// break-glass administrator has no row of the principal table).
func actorID(a audit.Actor) string { return principal.IDOrEmpty(a.ID) }
