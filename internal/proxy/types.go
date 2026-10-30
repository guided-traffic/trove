package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Kind says what a request fetches: an immutable artifact or a metadata document
// that expires (R-REPO-003).
type Kind uint8

// The kinds of a request.
const (
	Artifact Kind = iota + 1
	Metadata
)

// Mode is how a request treats the upstream.
type Mode uint8

const (
	// Normal follows the steps of R-REPO-025.
	Normal Mode = iota
	// CacheView serves stored entries only, ignores expiry and negative entries and
	// never contacts the upstream (R-REPO-032, `<key>~cache`).
	CacheView
	// OnlyIfCached is a request with Cache-Control: only-if-cached on the remote
	// address: answered from storage or with 504 (R-REPO-032).
	OnlyIfCached
)

// Remote is a remote repository as the engine works with it: the snapshot entry and
// its parsed settings.
type Remote struct {
	Repo     repository.Repository
	Settings repository.RemoteSettings
	id       int64
}

// NewRemote reads the settings of a remote repository. A repository that is not
// remote, or whose id is not a number, is an error: the dispatcher routes only
// remote repositories here.
func NewRemote(repo repository.Repository) (Remote, error) {
	id, err := strconv.ParseInt(repo.ID, 10, 64)
	if err != nil {
		return Remote{}, fmt.Errorf("proxy: repository id %q: %w", repo.ID, err)
	}
	s, ok := repo.Remote()
	if !ok {
		return Remote{}, fmt.Errorf("proxy: repository %q is not a remote repository", repo.Key)
	}
	return Remote{Repo: repo, Settings: s, id: id}, nil
}

// ID is the numeric repository id: the id of the cache entries, the invalidations
// and the negative keys.
func (r Remote) ID() int64 { return r.id }

// Digests are the digests the upstream announced for an artifact (R-REPO-041). An
// absent digest is nil.
type Digests struct{ SHA256, SHA512, SHA1 []byte }

// Empty reports whether no digest is announced.
func (d Digests) Empty() bool { return len(d.SHA256) == 0 && len(d.SHA512) == 0 && len(d.SHA1) == 0 }

// Strong reports whether a digest of SHA-256 or stronger is announced
// (checksumPolicy: require).
func (d Digests) Strong() bool { return len(d.SHA256) > 0 || len(d.SHA512) > 0 }

// CallerFacts are what the engine records of the principal behind a request: the
// upstream log, the populate audit event and the item's creator.
type CallerFacts struct {
	// Principal is the principal id, "" for the system; it is the on_behalf_of of the
	// upstream log record.
	Principal string
	Actor     audit.Actor
	Source    audit.Source
	// Endpoint is the name of the endpoint the request arrived on.
	Endpoint string
}

// Populate is the populate check of R-AUTHZ-008 as the engine asks it. The format
// host implements it over the decision point, the request's index, subject and
// endpoint, so the engine knows neither (Q-ARCH-200). A nil Populate refuses.
type Populate interface {
	// May answers the check as a question: nothing is audited. The engine asks it
	// where a refusal is answered from the cache (an expired document served stale).
	May(ctx context.Context) bool
	// Deny is the refusal of a request that has nothing to serve: it audits the
	// refusal and returns the answer (403 populate_not_permitted, or the read
	// refusal itself).
	Deny(ctx context.Context) error
	// Record writes the allowed populate event in the cache-fill transaction.
	Record(ctx context.Context, tx meta.Tx) error
	// Endpoint names the endpoint and says whether it fills caches; the engine counts a
	// refusal on an endpoint that does not (R-EP-019).
	Endpoint() (name string, fills bool)
}

// ServeFunc serves a stored item to the client with the storage streaming rules:
// Range, conditional requests, the strong ETag (internal/api/edge). The host
// supplies it, because the engine does not import the edge.
type ServeFunc func(ctx context.Context, w http.ResponseWriter, it items.Item) error

// Request is one request of a client for a path of a remote repository. The
// dispatcher has already resolved the repository, matched the route and decided
// R-REPO-025 steps 1 to 3 for the coordinate (the patterns, the claims and `read`):
// the engine does not decide them again.
type Request struct {
	Remote Remote
	// Path is the canonical path; Variant is the stored variant of the document, ""
	// for none (npm passes "full" for both of its request variants, R-NPM-024).
	Path    string
	Variant string
	Kind    Kind
	// Package is the normalized package name the path belongs to, "" for none. It
	// drives the gone-package rule and the ownership of URL-map rows.
	Package string
	Mode    Mode
	// Populate is the populate check; see [Populate].
	Populate Populate
	// Expected are the digests the upstream announced for an artifact.
	Expected Digests
	// AlwaysVerify makes Expected binding whatever integrity.checksumPolicy says (a
	// format whose protocol addresses by digest, npm: R-REPO-041).
	AlwaysVerify bool
	// MaxBytes bounds a document or artifact below the repository's and the system's
	// limits (a format's MaxDocumentBytes); 0 keeps them.
	MaxBytes int64
	Adapter  Adapter
	Caller   CallerFacts
	// Serve serves a stored artifact; required for [Engine.Artifact].
	Serve ServeFunc
}

// Document is a metadata document as the engine hands it to the format.
type Document struct {
	Digest [32]byte
	Size   int64
	// Bytes reads the document, from the replica's cache by digest or the object
	// store.
	Bytes                             func(ctx context.Context) ([]byte, error)
	FetchedAt, ValidatedAt, ExpiresAt time.Time
	// GoneAt is when the document was marked gone; zero when it is not.
	GoneAt time.Time
	// Stale says the document is served beyond its period.
	Stale  bool
	Status CacheStatus
}

// Currency says how current a document is after [Engine.EnsureCurrent] (Q-NPM-13).
type Currency uint8

// The currencies.
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
	// GoneAt is when the entries were marked gone; zero unless every stored entry
	// is.
	GoneAt time.Time
}

// ProbeOutcome is the result of a shadowing-check probe (R-REPO-048).
type ProbeOutcome uint8

// The probe outcomes.
const (
	// Served: the upstream answered 2xx.
	Served ProbeOutcome = iota + 1
	// NotServed: 404 or 410, or the repository is offline with nothing stored.
	NotServed
	// Undetermined: a failure, 401, 403, 429 or an open circuit.
	Undetermined
)

// ProbeResult is the outcome and, for Undetermined, how long a caller should wait
// before it asks again.
type ProbeResult struct {
	Outcome    ProbeOutcome
	RetryAfter time.Duration
}

// UpstreamBase is what an adapter builds an upstream URL from: the normalized url
// of the repository's settings.
type UpstreamBase struct{ URL *url.URL }

// Path returns the upstream URL for a canonical path (R-REPO-025): the base path
// plus every segment percent-encoded again, so that a canonical segment `%2e%2e`
// (which a client sent as `%252e%252e`) becomes `%252e%252e` and `a b` becomes
// `a%20b`. A canonical path is never concatenated into a URL as it is.
func (b UpstreamBase) Path(canonical string) (*url.URL, error) {
	if canonical == "" {
		return b.Segments()
	}
	return b.Segments(strings.Split(canonical, "/")...)
}

// Segments returns the upstream URL below the base for decoded segments, each
// encoded as one path segment: a "/" inside a segment (npm's scoped name) is
// encoded as %2F, which the format page prescribes where the upstream expects it.
// A segment that is empty, ".", ".." or contains a NUL is refused, so no input
// produces a path that decodes to a dot segment.
func (b UpstreamBase) Segments(segs ...string) (*url.URL, error) {
	u := *b.URL
	u.User, u.Fragment, u.RawQuery = nil, "", ""
	path := strings.TrimSuffix(b.URL.Path, "/")
	raw := strings.TrimSuffix(b.URL.EscapedPath(), "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." || strings.ContainsRune(s, 0) {
			return nil, errs.E(errs.Invalid, errs.CodeInvalidPath, "The path has a segment that cannot be sent to an upstream.")
		}
		path += "/" + s
		raw += "/" + url.PathEscape(s)
	}
	u.Path, u.RawPath = path, raw
	if len(segs) == 0 {
		u.Path, u.RawPath = path+"/", raw+"/"
	}
	return &u, nil
}

// Prepared is what [Adapter.PrepareFill] derived from a fetched document outside any
// transaction (C-6).
type Prepared struct {
	// Mappings are the URL-map rows of R-REPO-043, for the document's own package.
	Mappings []URLMapping
	// Data is format data handed to OnFill.
	Data any
}

// URLMapping maps a repository path to the upstream URL it came from.
type URLMapping struct {
	Path     string
	Upstream string
}

// FillEvent is what OnFill learns of a metadata fill. It runs in the fill
// transaction, under the entry's row lock when the entry exists.
type FillEvent struct {
	Path     string
	Variant  string
	Digest   [32]byte
	Replaces bool
	Prepared Prepared
	// StoredAt is when the stored copy was fetched; zero when there is none.
	StoredAt     time.Time
	StoredDigest [32]byte
	ETag         string
	LastModified time.Time
}

// Adapter is what the engine needs from a format. internal/formathost adapts a
// formats.RemoteAdapter to it for one request (Q-ARCH-200).
type Adapter interface {
	// UpstreamRequest maps a repository path to the upstream request. The URL it
	// builds comes from [UpstreamBase]; the engine refuses a URL on a host that is
	// neither the upstream's nor an extraHosts entry. The request carries no
	// Authorization header: the client adds the credential where it may go.
	UpstreamRequest(ctx context.Context, b UpstreamBase, path, variant string) (*http.Request, error)
	// AcceptResponse checks status, headers and declared type before a byte is
	// stored or sent.
	AcceptResponse(path string, resp *http.Response) error
	// PrepareFill validates a fetched metadata document before anything is stored
	// and outside every transaction (R-ARCH-019); an error keeps the previous entry.
	PrepareFill(ctx context.Context, path, variant string, body io.ReaderAt, size int64) (Prepared, error)
	// OnFill runs inside the metadata fill transaction. keepStored keeps the stored
	// document and renews only its freshness.
	OnFill(ctx context.Context, tx meta.Tx, f FillEvent) (keepStored bool, err error)
	// TracksVersionRemoval says that the format's removal marks replace the generic
	// gone-package rule (R-REPO-029).
	TracksVersionRemoval() bool
	// PackageDocumentPath is the canonical path of the document that stands for a
	// package upstream.
	PackageDocumentPath(name string) (path string, ok bool)
	// OwnPath reports whether a path belongs to the package: the URL map records
	// only such paths (R-REPO-043).
	OwnPath(pkg, path string) bool
}

// ExpectedTypes is an optional interface of an [Adapter]: it declares a media type
// expected for a path, which is then not blocked by
// integrity.blockUnexpectedContentTypes (R-REPO-042: the HTML index pages of the
// PyPI simple API).
type ExpectedTypes interface {
	ExpectsType(path, mediaType string) bool
}
