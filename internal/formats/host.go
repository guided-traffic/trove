package formats

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Host is the core as a format reaches it (R-ARCH-009). One Host serves a replica
// and holds no request state (R-ARCH-032): what belongs to a request - the
// subject, the index, the endpoint - is in the [Request] that every call takes.
// internal/formathost implements it over the item store, the package rows, the
// remote and virtual engines and the decision point.
type Host interface {
	// Authorize decides the target for rq.Subject on rq.Index (authz.Decider.DecideOn)
	// and returns the refusal of R-AUTHZ-016, or nil. The host completes the
	// coordinate - repository, class, format and the kind of the route - so a
	// format names neither another repository nor another endpoint (Q-ARCH-189).
	// It also applies the repository's include and exclude patterns to the
	// target's package or path (R-REPO-005): a read answers 404, a write 403
	// not_admitted.
	Authorize(ctx context.Context, rq *Request, t Target) error
	// Write runs fn in one transaction (meta.Runner.InTx, R-ARCH-019) with the
	// package writer bound to rq. The audit event and the notifications commit
	// with it. fn may run again after a serialization failure.
	Write(ctx context.Context, rq *Request, fn func(tx Tx, w PackageWriter) error) error

	// Ingest is the upload pipeline of storage.md.
	Ingest() Ingestor
	// Items reads repository paths and serves them.
	Items() ItemStore
	// Packages reads the package and version rows.
	Packages() PackageStore
	// Remote is the remote engine (C-4). Until the engine lands every call
	// answers errs.Unavailable "not supported".
	Remote() RemoteFetcher
	// Resolver is virtual resolution (C-5). Same stub rule as Remote.
	Resolver() Resolver
	// Links builds every absolute URL a response hands out (C-11).
	Links(rq *Request) LinkBuilder
	// Renditions is the per-replica cache of rendered documents (C-12).
	Renditions() RenditionCache
	// FormatData returns the data port bound to a format (C-3), nil for a format
	// without Descriptor.DataPort. The format asserts its own port type.
	FormatData(id ID) any
	// RestoredAt returns system_state restored_at (C-16), the zero time until a
	// restore has run (M5).
	RestoredAt(ctx context.Context) (time.Time, error)
}

// Ingestor is the upload pipeline of R-ARCH-014.
type Ingestor interface {
	// Spool reads the body into the replica-local spool and hashes it in one pass
	// (R-STOR-004): admission, single-pass digests, length and digest checks. The
	// coarse check ran before; the body is not read earlier.
	Spool(ctx context.Context, rq *Request, body io.Reader, o IngestOptions) (Spooled, error)
	// Commit stores the spooled body: the blob, the item row, the hooks and the
	// audit row in one transaction (items.Store.Commit with Origin Upload). It
	// marks the request committing (R-ARCH-024 step 5). The format decided deploy
	// and overwrite on the inspected identity before.
	Commit(ctx context.Context, rq *Request, sp Spooled, c Commit) (Committed, error)
	// Close removes the spool file. The dispatcher closes every spool of a request
	// on every path.
	Close(sp Spooled)
}

// Commit is one upload to commit.
type Commit struct {
	Path       Path
	MediaType  string
	CreateOnly bool
	// MayOverwrite says that Authorize(Overwrite) passed before the commit
	// (R-ARCH-014 step 5, Q-STOR-144).
	MayOverwrite bool
	Inspection   *Inspection
	// Payload reaches OnCommit as CommitEvent.Payload.
	Payload any
}

// Committed is the outcome of a commit.
type Committed struct {
	Item ItemRef
	// Created is true for a new path and for a replacement, Identical for the same
	// content at the path.
	Created, Overwritten, Identical bool
}

// Item is a file or folder as the item store reads it.
type Item struct {
	Ref       ItemRef
	Folder    bool
	Size      int64
	Digests   Digests
	MediaType string
	Modified  time.Time
	// Version is the version the item belongs to; 0 for none.
	Version VersionID
	// Handle belongs to the Host implementation: what Stat read, so that Serve
	// does not read the row again. A format never reads or sets it.
	Handle any
}

// ItemStore reads and serves the items of repositories.
type ItemStore interface {
	// Stat reads the item at a path from PostgreSQL alone.
	Stat(ctx context.Context, repo RepoRef, p Path) (Item, bool, error)
	// Serve runs ServeCheck(mode), then the ETag, the conditionals, Range,
	// Repr-Digest, the streaming and the download statistics (edge.ServeItem).
	Serve(ctx context.Context, rq *Request, w http.ResponseWriter, it Item, mode ServeMode) error
	// Open reads the content of an item through the verifying reader, whole.
	Open(ctx context.Context, it Item) (io.ReadCloser, error)
}

// PackageStore reads the package and version rows (FMT-007). Writes go through
// the [PackageWriter] of a storage hook or of [Host.Write].
type PackageStore interface {
	// Package returns the row of a normalized name.
	Package(ctx context.Context, repo RepoRef, name string) (PackageRow, bool, error)
	// Versions lists the live versions with the package's revision.
	Versions(ctx context.Context, repo RepoRef, name string) ([]VersionRow, RevSeq, error)
	// RevSeqs returns packages.rev_seq per repository id; a repository without the
	// package maps to 0 (R-ARCH-015).
	RevSeqs(ctx context.Context, repos []RepoRef, name string) (map[int64]RevSeq, error)
}

// LinkBuilder builds every absolute URL a response hands out on the response base
// URL of the request's endpoint and host (R-EP-003, R-ARCH-006, R-ARCH-007).
// Nothing else reads the host (C-11).
type LinkBuilder interface {
	// Base is the response base URL: "https://trove.example.com".
	Base() string
	// Registry is "<base>/api/<format>/<servingKey>/"; the key may be
	// "<remote>~cache".
	Registry(servingKey string) string
	// Repository is Registry(rq.Repo.Key).
	Repository() string
	// Path is Repository() + p with every segment escaped once.
	Path(p string) string
	// File is the /files/ URL of a stored item (downloadUrl).
	File(repoKey, p string) string
}

// RenditionKey identifies a rendered document. The core hashes every field; a
// rendition and its ETag use the same inputs (R-ARCH-012, R-NPM-013; C-12).
type RenditionKey struct {
	Repo     RepoRef
	Package  string
	RevSeq   RevSeq
	Variant  string
	Encoding string
	BaseURL  string
	// Endpoint and Exposure make a rendition per endpoint (R-EP-018); Exposure is
	// "" in mode all.
	Endpoint string
	Exposure string
	// Settings is config_version of the serving repository plus the format's
	// byte-changing trove.yaml keys.
	Settings      string
	RenderVersion int
}

// RenditionCache is the per-replica, size-weighted cache of rendered documents
// (cache.rendition_bytes). A document above an eighth of the budget is not stored.
type RenditionCache interface {
	Get(k RenditionKey) ([]byte, bool)
	Put(k RenditionKey, doc []byte)
}
