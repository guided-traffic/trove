package formats

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/qname"
)

// Host is the core as a format reaches it (R-ARCH-009). One Host serves a replica
// and holds no request state (R-ARCH-032): what belongs to a request - the
// subject, the index, the endpoint - is in the [Request] that every call takes.
// internal/formathost implements it over the item store, the package rows, the
// remote and virtual engines and the decision point.
type Host interface {
	// Begin gives a request the state the host keeps for it - the write decisions the
	// transaction of a write records (R-AUTHZ-029) and the spool files - and returns the
	// function that releases it: the spool files, on every path. The dispatcher calls it
	// once, after it built rq and before the handler runs, and defers the result. The
	// state is bound to rq and to every request derived from it ([Request.Member],
	// [Request.WriteTarget]); the host itself holds none (R-ARCH-032, Q-ARCH-265).
	Begin(rq *Request) (finish func())
	// Authorize decides the target for rq.Subject on rq.Index (authz.Decider.DecideOn)
	// and returns the refusal of R-AUTHZ-016, or nil. The host completes the
	// coordinate - repository, class, format and the kind of the route - so a
	// format names neither another repository nor another endpoint (Q-ARCH-189).
	// It also applies the repository's include and exclude patterns to the
	// target's package or path (R-REPO-005): a read answers 404, a write 403
	// not_admitted.
	Authorize(ctx context.Context, rq *Request, t Target) error
	// CheckOwnership is the ownership rule of R-AUTHZ-018 asked before a body is read: the
	// early, read-only form of what [PackageWriter.Claim] decides in the commit transaction. It
	// locks and claims nothing and refuses a caller who is not allowed to write the package, so
	// that a request nobody could commit is not read, spooled and validated first. The commit
	// decides again and is the decision that counts.
	CheckOwnership(ctx context.Context, rq *Request, t Target, w OwnershipWrite) error
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
	// Remote is the remote engine (C-4). A Host built without it answers every
	// call with an internal error that says so, never as if nothing was found.
	Remote() RemoteFetcher
	// Resolver is virtual resolution (C-5). Same rule as Remote.
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
	// Uploads is the multi-request upload session port of R-STOR-011: a blob sent over several requests and replicas,
	// whose state is PostgreSQL and one S3 multipart upload and never a replica's memory or spool (INV-18).
	Uploads() UploadSessions
	// Vulnerabilities is the scanning's data as an audit route reads it (R-VULN-017, R-VULN-018, ADR-150): the state
	// of an ecosystem's advisory data, its advisories, the internal names and the matcher. A host that has not wired
	// the scanning answers [NoVulnerabilityData], so a format never meets nil.
	Vulnerabilities() VulnerabilityData
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
	// and overwrite on the inspected identity before. The [Commit.Companions] go in
	// the same transaction: everything one client request writes commits as a unit
	// (R-ARCH-009), so a failure of any file leaves none of them.
	Commit(ctx context.Context, rq *Request, sp Spooled, c Commit) (Committed, error)
	// Close removes the spool file. The dispatcher closes every spool of a request
	// on every path.
	Close(sp Spooled)
	// Link commits an item at c.Path with the content of the item named by from, with no body (R-STOR-008, deploy by checksum):
	// the mount of an OCI blob from another repository, the link step of a push through a virtual repository, the tag item of a
	// push by tag whose manifest is stored. The source is named by the qualified name of its repository and a path, never by a
	// digest and never by a repository reference the caller supplies: the host resolves the repository by name (its id, class and
	// format), and for a virtual repository the item through its members in member order, as a read would. It then checks, as the
	// caller of the request, that the item is a live untrashed file that holds a blob the store serves, that no finding blocks its
	// version, and that rq.Subject may read it (a read decision on the named repository, taken after the lookup and before anything
	// is said of it; whatever is wrong answers errs.NotFound, the same for an item or a repository that does not exist and one the
	// caller may not read). The size and the digests are those of the blob row. It does not depend on storage.deploy_by_checksum, which is
	// the switch of a client's deploy by checksum header. It is the commit of [Ingestor.Commit] without the body: the item
	// row, the storage hook, the audit row and the recorded write decisions in one transaction. c.Companions must be empty.
	// The write decisions (deploy, overwrite) on the target are the caller's, as for Commit.
	Link(ctx context.Context, rq *Request, from SourceRef, c Commit) (Committed, error)
}

// SourceRef names the item a link takes its content from: the qualified name of its repository and the canonical path of the item in
// it. For a virtual repository the item is the first member's that holds the path.
type SourceRef struct {
	Repo qname.Repo
	Path string
}

// SessionRef names a session in a call: its id and the binding the format started it with. Every call repeats both, so a session
// of another binding (another OCI image name) is not found, whatever its id.
type SessionRef struct {
	ID      uuid.UUID
	Binding string
}

// UploadSessions is the multi-request upload port of R-STOR-011 as a format reaches it. The host decides on every call, as the
// request's subject and on the request's index, that the principal is the session's, that the session was started for the
// binding of the call, and that `deploy` is still allowed on the session's Target (a permission revoked during an upload
// ends it); the format decides nothing about a session. Whatever is wrong, a session of another principal or binding, an
// unknown, expired or finished one, a permission that is gone, answers errs.NotFound (OCI BLOB_UPLOAD_UNKNOWN).
type UploadSessions interface {
	// Start opens a session for binding (a string of the format: the OCI image name) that writes to target. The host validates
	// target against the request: it is rq.Repo for a local repository and the deployment target of rq.Repo for a virtual one
	// (R-OCI-028), and any other repository is refused with an internal error. The decision of `deploy` on it is the host's.
	Start(ctx context.Context, rq *Request, target RepoRef, binding string) (UploadSession, error)
	// Get reads a session.
	Get(ctx context.Context, rq *Request, ref SessionRef) (UploadSession, error)
	// Append writes body at offset; an offset below zero appends at the received count. An offset other than the received
	// count, and a claim another request holds, is an *errs.Error of kind RangeNotSatisfiable (416), and the bytes the request
	// had read are not recorded. Whatever the outcome, received is the byte count the session has recorded after the call, so
	// that the format renders the Range header of a 416 from a typed value and not from the error text.
	Append(ctx context.Context, rq *Request, ref SessionRef, offset int64, body io.Reader) (received int64, err error)
	// Finalize completes the session with the SHA-256 the client names, after the bytes of last, and commits the blob as
	// the item c names. A digest that is not the computed one is an error of kind Invalid and keeps the session open; a
	// finalize cut by a drain is repeated by the client's retry and continues at the recorded state.
	Finalize(ctx context.Context, rq *Request, ref SessionRef, sha256 [32]byte, last io.Reader, c Commit) (Committed, error)
	// Abort ends the session and removes what it staged.
	Abort(ctx context.Context, rq *Request, ref SessionRef) error
}

// UploadSession is a session as a format sees it.
type UploadSession struct {
	ID uuid.UUID
	// Target is the repository the session writes to.
	Target RepoRef
	// Binding is the string the session was started with.
	Binding string
	// Received is the byte count recorded so far.
	Received int64
	// PartSize is the size of the parts the session cuts the blob into (storage.uploads.part_size, fixed when it started);
	// a client is told it as the minimum chunk length.
	PartSize int64
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
	// Companions are the other files of the same request: they commit in the
	// transaction of this one, after it, each with its own OnCommit
	// (CommitEvent.Lead is this item). They take the lead's CreateOnly and
	// MayOverwrite, since the format decided the write as a whole. A companion
	// goes with a lead that changed: when the lead is identical to what is stored
	// nothing is written, and [Committed.Companions] is empty.
	Companions []Companion
}

// Companion is a file committed with the lead of a [Commit]: the attachment of a
// publish (an npm provenance bundle) or a file derived from the lead (a NuGet
// package's generated manifest), spooled with [Ingestor.Spool] like the lead.
type Companion struct {
	Spooled   Spooled
	Path      Path
	MediaType string
	// Generated marks a file the format derived from the lead, not one a client
	// sent; the item row records it, so that a raw write to it can be refused.
	Generated bool
	// Payload reaches OnCommit as CommitEvent.Payload of this file.
	Payload any
}

// Committed is the outcome of a commit.
type Committed struct {
	Item ItemRef
	// Created is true for a new path and for a replacement, Identical for the same
	// content at the path.
	Created, Overwritten, Identical bool
	// Companions are the items of [Commit.Companions], in order; empty when the lead
	// was Identical.
	Companions []ItemRef
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
	// Registry is the base URL followed by the protocol base path of the format with the project and the key filled in:
	// "<base>/api/<format>/<project>/<key>/", or "<base>/v2/<project>/<key>/" for the format that declares the registry
	// root; the key may be "<remote>~cache".
	Registry(serving qname.Repo) string
	// ClientName is the name of a package as the client of the request's host spells it: "<project>/<key>/<name>" on the shared host,
	// where the repository is part of the image name, and the bare name on a host whose root is bound to the repository
	// ([EndpointFacts.Bound]). It must be called on the addressed request, the one the dispatcher built, and not on one derived
	// through [Request.Member] for a member of a virtual repository: the derived request names the member, and the client spelled
	// the name for the repository it addressed. Registry on such a host renders the base path without the project and the key: "<base>/v2/".
	ClientName(name string) string
	// Repository is Registry(rq.Repo.Key).
	Repository() string
	// Path is Repository() + p with every segment escaped once.
	Path(p string) string
	// File is the /files/ URL of a stored item (downloadUrl).
	File(repoKey qname.Repo, p string) string
}

// RenditionKey identifies a rendered document. The cache is keyed by every field of
// the key, so a rendition is found only under all of its inputs (R-ARCH-012,
// R-NPM-013; C-12). The ETag of a rendition is the format's own: it hashes the
// inputs it keeps (npm: document.Inputs; NuGet: the weak ETag of its document leaf,
// Q-NUGET-9) and keeps them in step with this key by hand. One ETag derived from the
// key by the core is not part of the frozen contract; it would be a helper added
// later (Q-ARCH-261).
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
