package formats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// RemoteContext is what a remote adapter learns of the repository it works for.
type RemoteContext struct {
	Repo *repository.Repository
	// Upstream is remote.url.
	Upstream *url.URL
	// Settings is the format's member of the repository document.
	Settings json.RawMessage
	// Discovery is what [Discoverer.ParseDiscovery] returned for the stored discovery document: nil for a
	// format without one, and for the request of the discovery document itself.
	Discovery any
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
	// ExpectsType declares the media types the format expects for a path, which the engine
	// then does not block even when integrity.blockUnexpectedContentTypes lists them
	// (R-REPO-042). A format that declares nothing embeds [RemoteBase] and expects nothing.
	ExpectsType(p Path, mediaType string) bool
	// BeforeArtifact runs before the engine looks at or fetches an artifact for a request: the
	// format decides whether its stored marks allow it and which digests its metadata announces
	// for it. The host calls it on every road to a remote artifact: the format's own route and
	// a remote member of a virtual repository (R-NPM-025, R-NPM-026, Q-NPM-13). It runs on the
	// request addressed to the remote repository (for a member of a virtual repository, the
	// request derived through [Request.Member]) and may revalidate the format's document through
	// h.Remote(), unless rq.CacheView is set. An errs.NotFound refuses the artifact. The digests
	// are those the engine verifies a download against; none known is the zero value, which is
	// what [RemoteBase] answers. They were optional interfaces found by type assertion; they are
	// methods of the contract so that a format cannot forget them unnoticed (Q-ARCH-254).
	BeforeArtifact(ctx context.Context, h Host, rq *Request, p Path) (Digests, error)
	// UpstreamQuery maps a pass-through [Query] to the upstream request (R-ARCH-013 step 6): a GET
	// or HEAD without a body, whose method and URL are the key of the cached answer. A format
	// whose routes are not Volatile embeds [RemoteBase], whose default refuses.
	UpstreamQuery(ctx context.Context, rc RemoteContext, q Query) (*http.Request, error)
}

// Discoverer is an optional interface of a [RemoteAdapter]: the format's upstream addressing follows a
// discovery document, such as the service index of NuGet (R-NUGET-014). Register reads it off the adapter
// once ([RemoteCapabilities]).
//
// The engine resolves the document before it asks [RemoteAdapter.UpstreamRequest] for any other path: as a
// metadata document of the repository, stored, revalidated and collapsed like every other, ahead of the
// fetch's slot. It parses it with ParseDiscovery and hands the result to every call of that fetch as
// [RemoteContext.Discovery]. The request for the document itself carries none. The host resolves it the
// same way for a pass-through query and hands it to [RemoteAdapter.UpstreamQuery] (Q-ARCH-282).
type Discoverer interface {
	// DiscoveryDocument names the document: its canonical path and stored variant.
	DiscoveryDocument() (p Path, variant string, ok bool)
	// ParseDiscovery turns the stored document into the data the adapter's other calls read. It runs
	// outside every transaction. An error answers the fetch with 502. The result is shared by requests
	// that run at once and must not be changed.
	ParseDiscovery(ctx context.Context, rc RemoteContext, doc []byte) (any, error)
}

// FillInspector is an optional interface of a [RemoteAdapter]: the format inspects an artifact it fetched
// before it is stored (R-NUGET-015). Register reads it off the adapter once ([RemoteCapabilities]).
//
// The hook runs after the digest check and before the item commit, outside any transaction. No byte of an
// artifact with a hook reaches a client before the hook accepted it: the request that leads the fetch is
// served from the stored entry like every request that waited, so a refusal answers all of them.
type FillInspector interface {
	// InspectsFill declares the hook for the artifact at p; false stores and serves it as any artifact.
	InspectsFill(p Path) bool
	// InspectFill reads the spooled artifact (limits as for [Format.Inspect]). Its result reaches OnCommit
	// as [CommitEvent.Inspection]. An error is the answer to every request of the flight, 502 unless it is
	// an *errs.Error of another class, and nothing is stored: no blob, no item, no entry, no negative entry.
	InspectFill(ctx context.Context, rc RemoteContext, p Path, sp Spooled) (*Inspection, error)
}

// MirrorLister is an optional interface of a [RemoteAdapter]: the packages of the format's upstream can be discovered,
// so a remote repository of the format can run in mode mirror (repositories-and-proxy.md R-REPO-050 to R-REPO-055,
// ADR-152). Register reads it off the adapter once ([RemoteCapabilities]) and records the catalog fact Mirror.
//
// The interface and the types its methods speak are the leaf internal/mirror/discover, which the contract and the
// formats import and the mirror engine, below the contract, imports too: the engine takes a format's lister without
// importing this package. The contract of each method is on discover.Lister; the format's half of discovery is on the
// format page's section "Mirror discovery".
type MirrorLister = discover.Lister

// RemoteCaps are the optional interfaces a format's remote adapter implements, as Register found them.
// A field is nil for a capability the adapter does not have.
type RemoteCaps struct {
	Discovery Discoverer
	Fill      FillInspector
	// Mirror is the adapter's discovery of upstream packages; nil for a format whose remote repositories cannot be
	// mirrors (R-REPO-050).
	Mirror MirrorLister
}

// RemoteCapabilities returns the capabilities Register recorded for a format; the zero value for an ID that
// is not registered.
func RemoteCapabilities(id ID) RemoteCaps {
	return CapabilitiesFor(id).Remote
}

// RemoteBase gives a RemoteAdapter the defaults of the methods most formats leave alone:
// no expected media types, no artifact gate and no pass-through query. Embed it.
type RemoteBase struct{}

// UpstreamQuery refuses: the format has no Volatile route that is passed through.
func (RemoteBase) UpstreamQuery(context.Context, RemoteContext, Query) (*http.Request, error) {
	return nil, errs.E(errs.Internal, errs.CodeInternal, "The format passes no query through to an upstream.")
}

// ExpectsType expects no media type.
func (RemoteBase) ExpectsType(Path, string) bool { return false }

// BeforeArtifact allows every artifact and announces no digest.
func (RemoteBase) BeforeArtifact(context.Context, Host, *Request, Path) (Digests, error) {
	return Digests{}, nil
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
	// Query passes a Volatile request through to the upstream of the remote repository rq
	// addresses (R-ARCH-013 step 6): the engine sends it through the repository's client under
	// its egress rules, credential policy, fetch slots and circuit, and keeps the answer, as
	// received, in a registered per-replica cache for q.TTL. The answer is stored as neither a
	// cache entry nor an item. A repository that may not contact its upstream, and a caller who
	// may not fill it, are served from the cache only; a miss answers [ErrOffline] and sends
	// nothing. A missing resource at the upstream is errs.NotFound.
	Query(ctx context.Context, rq *Request, q Query) (*QueryResult, error)
	// Gate runs [RemoteAdapter.BeforeArtifact] for a request addressed to a remote repository
	// without fetching: the check a remote member of a virtual repository meets before its
	// cached artifact is served (the second tier never asks the upstream for the artifact, but a
	// stale document may have to be revalidated first, Q-NPM-13, R-NPM-026). It is a method of
	// the fetcher, so that a wrapper around a fetcher cannot drop the gate unnoticed.
	Gate(ctx context.Context, rq *Request, p Path) error
}

// Query is one Volatile request a format passes through to an upstream: a package search.
type Query struct {
	// Path is the repository path of the request: "v3/search".
	Path Path
	// Params are the parameters to forward, already limited, clamped and normalised by the
	// format. The cached answer is found under the upstream request the adapter builds from
	// them, so equal parameters share one answer.
	Params url.Values
	// TTL is how long the answer is kept; 0 keeps nothing. The engine cuts it to one hour.
	TTL time.Duration
}

// QueryResult is the upstream's answer to a [Query].
type QueryResult struct {
	// ContentType is the Content-Type of the answer.
	ContentType string
	// Body is the answer as received, shared with the cache: a format must not change it, and
	// filters and rewrites a copy on every use.
	Body []byte
	// FetchedAt is when the upstream answered.
	FetchedAt time.Time
	// Cached says the answer came from the replica's cache; Collapsed that it is the result of
	// another request's contact of the upstream.
	Cached, Collapsed bool
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

// ErrOffline is the answer of [RemoteFetcher.Exchange] and of [RemoteFetcher.Query] for a remote
// repository in offline mode, and for every one while proxy.global_offline is on (R-REPO-030,
// R-REPO-031); Query answers it as well for a caller who may not fill the repository and for the
// cache view.
var ErrOffline = errors.New("formats: the remote repository may not contact its upstream")
