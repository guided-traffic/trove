package formats

import (
	"context"
	"encoding/json"
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
	StoredAt     time.Time
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
	// (R-ARCH-008 "Remote fill"). keepStored keeps the stored document and renews
	// only its freshness.
	OnFill(ctx context.Context, tx Tx, f FillEvent) (keepStored bool, err error)
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
}
