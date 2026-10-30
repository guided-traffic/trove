package virtual

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// Descriptor is the R-REPO-003 view of a request path as resolution needs it. The
// names are normalized by the format; the engine never normalizes.
type Descriptor struct {
	// Format is the format ID of the virtual repository.
	Format string
	// Path is the canonical repository path.
	Path string
	// Kind is the request kind: artifact or metadata decide what resolution does; any
	// other kind has no document and no tiers.
	Kind catalog.RequestKind
	// Package and Version are the normalized identity; empty when the path belongs to none.
	Package, Version string
	// RoutingName is the name of an artifact that belongs to no package, for patterns, claims
	// and pin rules.
	RoutingName string
	// Variant is the stored variant of a metadata request ("" for a format with one).
	Variant string
}

// Name is the name routing works on: the package, else the routing name.
func (d Descriptor) Name() string {
	if d.Package != "" {
		return d.Package
	}
	return d.RoutingName
}

// Caller is who asks, as resolution needs it: the subject (which carries the endpoint and
// whether the endpoint fills caches, R-EP-019), the index the request began on, and whether the
// endpoint has exposure rules.
type Caller struct {
	Subject authz.Subject
	Index   *authz.Index
	// Rules is true for an endpoint with exposure: rules; the Trove-Resolved-From header is
	// never sent there (R-EP-018).
	Rules bool
}

// Strategy is how a format asks a path to be resolved (formats.Strategy).
type Strategy uint8

// The strategies. Newest is resolved as Merge here: revalidating remote members first
// (R-REPO-036) arrives with the first format that has a latest-version request.
const (
	FirstMatch Strategy = iota + 1
	Merge
	Newest
)

// Tier is where a part of a merge came from.
type Tier uint8

// The tiers of R-REPO-019 step 6. A part is Local or Remote; TierRemoteCached is the tier of one
// version of a remote part whose artifact is cached (Part.CachedVersions), which the per-version
// source rule of R-REPO-020 prefers to an uncached remote version.
const (
	TierLocal Tier = iota + 1
	TierRemoteCached
	TierRemote
)

// Member is a repository that takes part in a request, after the per-member read check
// (R-AUTHZ-009 rule 2).
type Member struct {
	Repo repository.Repository
	// ID is the numeric id of the repository.
	ID int64
	// Position is the member's place in the resolution order.
	Position int
	// MayFill is the populate check of R-AUTHZ-008 for this caller and this member: always
	// false for a member that is not remote.
	MayFill bool
}

// Local reports whether the member is a local repository.
func (m Member) Local() bool { return m.Repo.Class == repository.Local }

// Remote reports whether the member is a remote repository.
func (m Member) Remote() bool { return m.Repo.Class == repository.Remote }

// Plan is R-REPO-019 steps 1 to 5 for one caller.
type Plan struct {
	Virtual    repository.Repository
	Descriptor Descriptor
	// Admitted is false when the virtual repository's own patterns refuse the request, or the
	// repository is disabled: the answer is 404 and no member is consulted.
	Admitted bool
	Route    repository.Route
	// Members are the members that take part for this caller, in resolution order.
	Members []Member
	// Unreadable is the number of members the caller cannot read (R-REPO-019 step 4). A plan for
	// Explain counts every member of the repository; the plan of a request only those the route
	// kept.
	Unreadable int
	// Fingerprint is the SHA-256 over the endpoint and the ordered (member id, class, mayFill)
	// of Members (D-7, Q-REPO-110): two callers share a merged document only when it is equal.
	Fingerprint [32]byte
	usable      map[string]usable
}

type usable struct{ read, fill bool }

// Local lists the local members of the plan in order.
func (p *Plan) Local() []Member { return p.byClass(repository.Local) }

// Remote lists the remote members of the plan in order.
func (p *Plan) Remote() []Member { return p.byClass(repository.Remote) }

func (p *Plan) byClass(c repository.Class) []Member {
	var out []Member
	for _, m := range p.Members {
		if m.Repo.Class == c {
			out = append(out, m)
		}
	}
	return out
}

// repoID parses the numeric id of a repository; 0 for one that has none.
func repoID(r repository.Repository) int64 {
	n, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Revision identifies the state a part was built from: rev_seq of a local member, the digest of the
// stored document of a remote member.
type Revision struct {
	RevSeq int64
	Digest [32]byte
}

// Part is one member's document handed to Merge.
type Part struct {
	Member   Member
	Tier     Tier
	Revision Revision
	Doc      []byte
	// CachedVersions are the versions of a remote member whose artifact is cached.
	CachedVersions map[string]bool
	// Restricted says that the caller may not fill this member's cache (R-AUTHZ-008): the merge
	// lists only CachedVersions of the part, so that every listed version can be downloaded.
	Restricted bool
	Stale      bool
}

// Merged is the result of merging the members of a virtual repository.
type Merged struct {
	Bytes []byte
	// MergeFingerprint is an input of the ETag, with the endpoint and the exposure fingerprint:
	// it changes whenever the bytes could.
	MergeFingerprint [32]byte
	// Partial results are never cached.
	Partial bool
	// CacheStatus is the Cache-Status header value of the response (R-REPO-047).
	CacheStatus string
}

// Merger is what a format contributes to the merge; formathost builds it from the format's
// VirtualAdapter for one request.
type Merger interface {
	Strategy(d Descriptor) Strategy
	// LocalDocument renders a local member's document from its rows (R-ARCH-012); rev is the
	// rev_seq it was rendered at.
	LocalDocument(ctx context.Context, m Member, d Descriptor) (doc []byte, rev int64, found bool, err error)
	// Merge applies the per-version source rule of R-REPO-020 to the parts, which come in
	// resolution order.
	Merge(ctx context.Context, d Descriptor, parts []Part) ([]byte, error)
	// ArtifactPrefix is the prefix below which the cached artifacts of the package are stored in
	// a remote repository; CachedVersion names the version a cached artifact path belongs to
	// ("" when it belongs to none).
	ArtifactPrefix(d Descriptor) string
	CachedVersion(path string) string
}

// Hit is a found artifact that writes itself to the response.
type Hit func(w http.ResponseWriter) error

// Tiers are the three tiers of R-REPO-019 step 6 for an artifact, bound to the request by
// formathost.
type Tiers interface {
	// Local looks the artifact up in a local member.
	Local(ctx context.Context, m Member, d Descriptor) (Hit, bool, error)
	// RemoteCached looks it up in the cache of a remote member; it never contacts the upstream.
	RemoteCached(ctx context.Context, m Member, d Descriptor) (Hit, bool, error)
	// RemoteFetch fetches it through the remote request path (R-REPO-025). It writes the response
	// when it found the artifact and writes nothing when it did not.
	RemoteFetch(ctx context.Context, m Member, d Descriptor, w http.ResponseWriter) (found bool, err error)
}

// The seams of the engine. Each is the part of a neighbour this package uses, declared here so
// that the engine and the fakes of the tests meet in one place.

// Router is the caller-independent part of resolution; *repository.Routing implements it.
type Router interface {
	Route(ctx context.Context, virtual repository.Repository, req repository.RouteRequest) (repository.Route, error)
	RemoteRefused(ctx context.Context, remote repository.Repository, name string, via *repository.Repository) (bool, error)
	CoveredByOwnClaim(ctx context.Context, local repository.Repository, name string) (bool, error)
	ExposureSet(ctx context.Context, local repository.Repository, name string) ([]repository.Exposure, error)
}

var _ Router = (*repository.Routing)(nil)

// Decider is the decision point; authz.Decider implements it.
type Decider interface {
	MemberUsableOn(ctx context.Context, idx *authz.Index, sub authz.Subject, member string, c authz.Coordinate) (read, fill bool)
	ReadableOn(ctx context.Context, idx *authz.Index, sub authz.Subject, keys []string) ([]string, error)
	HoldsOn(idx *authz.Index, sub authz.Subject, permission string) bool
	CheckOn(idx *authz.Index, sub authz.Subject, items []authz.CheckItem) ([]bool, error)
}

var _ Decider = authz.Decider(nil)

// Locals reads local members. formathost implements it over the package rows and the item store;
// *packages.Store implements RevSeqs.
type Locals interface {
	// RevSeqs returns packages.rev_seq of the package per repository id, 0 for a repository
	// without it, in one query.
	RevSeqs(ctx context.Context, repoIDs []int64, name string) (map[int64]int64, error)
	// Holds reports whether the member holds the path of the descriptor: the package for a
	// metadata path, the stored item for an artifact.
	Holds(ctx context.Context, m repository.Repository, d Descriptor) (bool, error)
	// HoldsVersion reports whether the member holds a live version of the package.
	HoldsVersion(ctx context.Context, m repository.Repository, name string) (bool, error)
}

// RemoteCall names one request to the remote engine.
type RemoteCall struct {
	Remote repository.Repository
	// Via is the virtual repository the request was addressed to.
	Via      repository.Repository
	Path     string
	Variant  string
	Package  string
	Subject  authz.Subject
	Endpoint string
	// MayFill is the populate check for this caller (R-AUTHZ-008). Without it the engine
	// serves what the cache holds, an expired document included, and never contacts the
	// upstream; a path the cache lacks answers NotFound or populate_not_permitted, which resolution
	// counts as a miss.
	MayFill bool
}

// RemoteDoc is a stored or fetched metadata document of a remote repository.
type RemoteDoc struct {
	Digest [32]byte
	// Bytes reads the document.
	Bytes     func(ctx context.Context) ([]byte, error)
	ExpiresAt time.Time
	Stale     bool
	// Fetched is true when this call contacted the upstream.
	Fetched bool
}

// EntryState is what the cache of a remote repository says of a path without contacting the
// upstream. Stored and GoneAt answer steps 1 and 2 of R-REPO-048 (negative entries play no part
// there); the other fields are what the resolution explanation reports.
type EntryState struct {
	// Stored: any variant of the path is stored.
	Stored bool
	// GoneAt is when the stored document was marked gone upstream; zero when it is not.
	GoneAt time.Time
	// Negative: a negative entry for the path is current.
	Negative bool
	// Offline: the repository is offline (R-REPO-030, R-REPO-031).
	Offline bool
	// CircuitOpenFor is how long the circuit stays open (R-REPO-029); zero when it is closed.
	CircuitOpenFor time.Duration
}

// ProbeOutcome is the result of a shadowing probe.
type ProbeOutcome uint8

// The outcomes of R-REPO-048 steps 3 to 5.
const (
	NotServed ProbeOutcome = iota + 1
	Served
	Undetermined
)

// ProbeResult is the outcome of a probe with, for Undetermined, how long to wait.
type ProbeResult struct {
	Outcome    ProbeOutcome
	RetryAfter time.Duration
}

// Remotes is what the virtual engine asks of the remote engine (plan section 3.4: Cached,
// Metadata, EntryState, Probe, CachedArtifacts). Each call is made on behalf of a caller, and none of
// them stores a negative entry for a shadowing probe.
type Remotes interface {
	// Cached reports whether the path is stored in the cache of the remote member (an artifact
	// item, or a metadata entry of any variant) without contacting the upstream.
	Cached(ctx context.Context, rc RemoteCall) (bool, error)
	// Metadata is the metadata path of the remote request path (R-REPO-025): fresh, refreshed or
	// stale. An error of kind NotFound says the member does not know the package; one of kind
	// UpstreamFailed, UpstreamTimeout or Unavailable says it could not be asked and has no copy.
	Metadata(ctx context.Context, rc RemoteCall) (*RemoteDoc, error)
	// EntryState reads the cache entries of a path.
	EntryState(ctx context.Context, repoID int64, path string) (EntryState, error)
	// Probe is R-REPO-048 steps 3 to 5 for a path with nothing stored: offline is NotServed, an
	// open circuit is Undetermined, else one GET through the fetch client, storing nothing.
	Probe(ctx context.Context, rc RemoteCall) (ProbeResult, error)
	// CachedArtifacts lists the cached artifact paths below a prefix.
	CachedArtifacts(ctx context.Context, repoID int64, prefix string) ([]string, error)
}
