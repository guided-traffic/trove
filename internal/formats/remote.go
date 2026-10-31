package formats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
)

// RemoteContext is what a remote adapter learns of the repository it works for.
type RemoteContext struct {
	Repo *repository.Repository
	// Upstream is remote.url.
	Upstream *url.URL
	// Settings is the format's member of the repository document.
	Settings json.RawMessage
}

// Prepared is what PrepareFill derived from a fetched document outside any
// transaction (C-6).
type Prepared struct {
	// Mappings are the URL map rows of R-REPO-043. The format builds them from the
	// package identity; the core checks each against the document's package.
	Mappings []URLMapping
	// Data is format data handed to OnFill: npm's document time, version list and
	// removal marks.
	Data any
}

// URLMapping maps a repository path to the upstream URL it came from.
type URLMapping struct {
	Path     Path
	Upstream string
}

// FillDecision is the answer of OnFill to the core.
type FillDecision uint8

const (
	// FillByFetchTime stores the document unless the entry that is stored was fetched
	// later (R-REPO-026): the rule of a format that has no order of its own.
	FillByFetchTime FillDecision = iota
	// FillKeepStored keeps the stored document and renews only its freshness.
	FillKeepStored
	// FillReplaceStored stores the document whatever fetch time the stored entry has: the
	// format recorded it as newer by an order of its own, such as time.modified.
	FillReplaceStored
)

// FillEvent is what OnFill learns of a metadata fill. It runs in the fill
// transaction, under the cache entry's row lock.
type FillEvent struct {
	Repo     RepoRef
	Path     Path
	Variant  string
	Digest   [32]byte
	Replaces bool
	Prepared Prepared
	// StoredAt is when the stored copy was fetched; zero when there is none.
	StoredAt time.Time
	// FetchedAt is when this document was fetched, on PostgreSQL's clock: the time the
	// core stores it with. The core keeps a stored entry that was fetched later
	// (R-REPO-026), so a format that records facts of the document decides on the same
	// comparison and records them only when StoredAt is not after FetchedAt.
	FetchedAt    time.Time
	StoredDigest [32]byte
	ETag         string
	LastModified time.Time
	// Packages: a remote fill is a rev_seq trigger (R-ARCH-012).
	Packages PackageWriter
	Data     any
}

// TypeExpecter is an optional interface of a RemoteAdapter: it declares the media
// types the format expects for a path, which the engine then does not block even when
// integrity.blockUnexpectedContentTypes lists them (R-REPO-042).
type TypeExpecter interface {
	ExpectsType(p Path, mediaType string) bool
}

// ArtifactGate is an optional interface of a RemoteAdapter: before the engine looks at
// or fetches an artifact for a request, the format decides whether its stored marks
// allow it and which digests its metadata announces for it. The host calls it on every
// road to a remote artifact: the format's own route and a remote member of a virtual
// repository (R-NPM-025, R-NPM-026, Q-NPM-13).
type ArtifactGate interface {
	// BeforeArtifact runs on the request addressed to the remote repository (for a member
	// of a virtual repository, a copy of the request addressed to the member). It may
	// revalidate the format's document through h.Remote(), unless rq.CacheView is set. An
	// errs.NotFound refuses the artifact. The digests are those the engine verifies a
	// download against; none known is the zero value.
	BeforeArtifact(ctx context.Context, h Host, rq *Request, p Path) (Digests, error)
}

// RemoteAdapter is what a format contributes to the remote engine (R-ARCH-008).
type RemoteAdapter interface {
	// UpstreamRequest maps a repository path to the upstream request.
	UpstreamRequest(ctx context.Context, rc RemoteContext, p Path, variant string) (*http.Request, error)
	// AcceptResponse checks status, headers and declared type before a byte is
	// stored or sent.
	AcceptResponse(p Path, resp *http.Response) error
	// PrepareFill validates a fetched metadata document before anything is stored
	// and outside every transaction (R-ARCH-019); an error keeps the previous entry
	// and answers 502 in the format's shape (R-NPM-024).
	PrepareFill(ctx context.Context, rc RemoteContext, p Path, variant string, body io.ReaderAt, size int64) (Prepared, error)
	// OnFill runs inside the metadata fill transaction, under the entry's row lock
	// (R-ARCH-008 "Remote fill"). The decision says whether the stored document
	// stays and only its freshness is renewed, or the entry takes this one.
	OnFill(ctx context.Context, tx Tx, f FillEvent) (FillDecision, error)
	// Rewrite renders a stored upstream document for one response.
	Rewrite(ctx context.Context, rc RemoteContext, p Path, doc []byte, links LinkBuilder) ([]byte, error)
	// TracksVersionRemoval says that the format's removal marks replace the
	// generic gone-package rule (R-REPO-029); npm true.
	TracksVersionRemoval() bool
}

// CacheStatus is what the remote engine reports for the Cache-Status header.
type CacheStatus struct {
	Hit, Stale, Stored, Negative bool
	// Collapsed: the response came from another request's fetch.
	Collapsed bool
	TTL       time.Duration
	Detail    string
	// Header is the value of the Cache-Status header (RFC 9211) the engine rendered
	// for the response; a handler sets it as it is (R-REPO-047). A Negative status is
	// the 404 of a negative entry: the document has no content.
	Header string
}

// RemoteDocument is a stored or fetched metadata document of a remote repository.
type RemoteDocument struct {
	Digest [32]byte
	Size   int64
	// Bytes reads the document, bounded by MaxDocumentBytes and
	// proxy.max_metadata_bytes.
	Bytes                             func(ctx context.Context) ([]byte, error)
	FetchedAt, ValidatedAt, ExpiresAt time.Time
	// GoneAt is when the document was marked gone; zero when it is not.
	GoneAt time.Time
	Stale  bool
	Status CacheStatus
}

// Currency says how current a document is after EnsureCurrent (Q-NPM-13).
type Currency uint8

const (
	// Current: the stored document is fresh.
	Current Currency = iota + 1
	// Revalidated: the document was revalidated against the upstream.
	Revalidated
	// Unknown: the upstream could not be asked.
	Unknown
)

// EntryState is what the cache entries of a path say (R-REPO-048 steps 1 and 2).
type EntryState struct {
	// Stored is true when any variant is stored.
	Stored bool
	GoneAt time.Time
}

// RemoteFetcher is the format-facing view of the remote engine. The host computes
// whether a fill may happen from rq.Subject, rq.Index and rq.Endpoint (R-AUTHZ-008,
// R-EP-019), so a format cannot set it (C-4).
type RemoteFetcher interface {
	// Metadata is R-REPO-025 steps 4 to 10.
	Metadata(ctx context.Context, rq *Request, p Path, variant string) (*RemoteDocument, error)
	// Artifact serves an artifact with Range, HEAD and the withheld tail.
	Artifact(ctx context.Context, rq *Request, p Path, expected Digests, w http.ResponseWriter) error
	// Cached reads the cache tier and never contacts the upstream.
	Cached(ctx context.Context, rq *Request, p Path) (Item, bool, error)
	// EnsureCurrent revalidates a stale document first (Q-NPM-13).
	EnsureCurrent(ctx context.Context, rq *Request, p Path, variant string) (*RemoteDocument, Currency, error)
	// EntryState reads the cache entries of a path without contacting the upstream.
	EntryState(ctx context.Context, rq *Request, p Path) (EntryState, error)
	// Exchange sends one request through the upstream client of the remote repository rq
	// addresses, outside the cache: its egress rules, trust store, redirect rules and
	// credential policy apply (R-REPO-033, R-REPO-038, R-REPO-039). It reads and writes no
	// cache entry, takes no fetch slot, neither reads nor moves the circuit and holds no
	// transaction; ctx bounds it. A repository that may not contact its upstream answers
	// [ErrOffline] and sends nothing. The caller closes the response body.
	Exchange(ctx context.Context, rq *Request, x Exchange) (*http.Response, error)
}

// Exchange is one request a format sends to an upstream service on a client's behalf:
// npm's bulk advisory request (R-NPM-038).
type Exchange struct {
	Method string
	// Base is the URL Path is joined to; nil is the repository's upstream URL. Its host
	// may be another than the upstream's: the credential policy decides what it is sent.
	Base *url.URL
	// Path is relative to Base: "-/npm/v1/security/advisories/bulk".
	Path   string
	Header http.Header
	Body   []byte
}

// ErrOffline is the answer of [RemoteFetcher.Exchange] for a remote repository in offline
// mode, and for every one while proxy.global_offline is on (R-REPO-030, R-REPO-031).
var ErrOffline = errors.New("formats: the remote repository may not contact its upstream")
