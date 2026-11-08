package formats

import (
	"context"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/repository"
)

// Strategy is how a virtual repository combines its members for a path
// (R-REPO-019, R-REPO-020).
type Strategy uint8

const (
	// FirstMatch: tiered lookup, the first eligible member that has it wins.
	FirstMatch Strategy = iota + 1
	// Merge: every eligible member contributes.
	Merge
)

// Tier is where a part of a merge came from; resolution walks them in this order.
type Tier uint8

const (
	TierLocal Tier = iota + 1
	TierRemoteCached
	TierRemote
)

// Revision identifies the state a part was built from: rev_seq of a local member,
// the digest of the stored document of a remote member.
type Revision struct {
	RevSeq RevSeq
	Digest [32]byte
}

// MemberDocument is one member's document handed to Merge (C-10).
type MemberDocument struct {
	// Member is never shown on an endpoint with rules (R-EP-018).
	Member RepoRef
	Tier   Tier
	// Position is the stable order after R-REPO-019 step 5.
	Position int
	Revision Revision
	Doc      []byte
	// CachedVersions are the versions of a remote member whose artifact is cached:
	// the source rule of R-AUTHZ-008.
	CachedVersions map[string]bool
	// Restricted says that the caller may not populate this remote member's cache
	// (R-AUTHZ-008): the merge lists only CachedVersions of Doc, so that every version a
	// document lists can be downloaded. CachedVersions of a member that is not restricted only
	// move a version to the second tier of the source rule.
	Restricted bool
	Stale      bool
}

// VirtualAdapter is what a format contributes to virtual resolution.
type VirtualAdapter interface {
	Strategy(p Path) Strategy
	// Merge applies the per-version source rule: a version entry comes from the
	// lowest (Tier, Position) part that holds it.
	Merge(ctx context.Context, rq *Request, p Path, parts []MemberDocument) ([]byte, error)
	// LocalDocument renders a local member's document from its rows (R-ARCH-012)
	// for R-ARCH-015 step 3; the core's merged-metadata cache calls it, so no format
	// code orchestrates that cache (C-9).
	LocalDocument(ctx context.Context, rq *Request, member RepoRef, p Path, variant string) (doc []byte, rev RevSeq, found bool, err error)
	// ArtifactPrefix is the prefix, with a trailing slash, below which a remote member keeps the
	// cached artifacts of a package: the directory its versions live in, which the merge lists to
	// find the versions whose artifact is cached (R-AUTHZ-008). It is the format's to say because
	// only the format knows where its artifacts are: npm's tarballs are below the document of the
	// package, NuGet's below the directory of its version list. "" says that the format's cached
	// artifacts are not below one prefix per package, and a merge then lists no cached version.
	ArtifactPrefix(name string) string
}

// Merged is the result of merging a virtual repository's members.
type Merged struct {
	Bytes []byte
	// MergeFingerprint is an input of the ETag, with the endpoint and the exposure
	// fingerprint.
	MergeFingerprint [32]byte
	// Partial results are never cached; Cache-Status carries detail=partial
	// (R-REPO-020).
	Partial     bool
	CacheStatus string
}

// ShadowOutcome is the result of the shadowing check of R-REPO-048.
type ShadowOutcome struct {
	Proceed bool
	// Code is shadows_remote_package or shadow_check_unavailable.
	Code         string
	RetryAfter   time.Duration
	Undetermined bool
	// Refusal is the error to answer when Proceed is false: 403 shadows_remote_package
	// naming the package and the virtual repositories the caller can read, or 503
	// shadow_check_unavailable with Retry-After. A format returns it as it is.
	Refusal error
	// Shadowed are the pairs (V, R) in which a remote member serves the name, for the
	// security audit event repository.remote_package_shadowed that the commit records
	// when the write proceeds on the strength of manage (R-REPO-048).
	Shadowed []ShadowPair
}

// ShadowPair names a virtual repository and the remote member of it that serves a package.
type ShadowPair struct {
	Virtual, Remote string
}

// PlannedMember is a member that takes part in a request to a virtual repository.
type PlannedMember struct {
	Member RepoRef
	// Tier is TierLocal for a local member and TierRemote for a remote one.
	Tier Tier
	// Position is the member's place in the resolution order (R-REPO-019 step 5).
	Position int
	// MayFill says that the caller may bring content into this member's cache (R-AUTHZ-008); it is
	// false for a member that is not remote. A fan-out to a remote member contacts its upstream
	// only when it is true.
	MayFill bool
}

// VirtualPlan is R-REPO-019 steps 1 to 5 for one caller: the members that take part, after the
// repository's patterns, the pin rules and claims, and the per-member read check
// (R-AUTHZ-009 rule 2).
type VirtualPlan struct {
	// Admitted is false when the virtual repository's own patterns refuse the request; Members is
	// then empty.
	Admitted bool
	// Members are the members the caller may read, in resolution order.
	Members []PlannedMember
}

// ReadShape is how a caller's read of the packages of one repository is decided.
type ReadShape uint8

const (
	// ReadNone: no package of the repository can be read by the caller; a virtual repository
	// holds none of its own.
	ReadNone ReadShape = iota
	// ReadAll: every package and version of the repository can be read; MayRead need not be asked.
	ReadAll
	// ReadRows: the decision depends on the package and the version; MayRead decides each.
	ReadRows
)

// ReadScope is the read scope of one caller on the request's index for a listing (search,
// autocomplete) that names packages of several repositories. It is the unaudited form of the read
// decision: a listing asks it about rows it found itself, so no client has requested a refused
// resource and no denial is recorded. A listing keeps only the rows the scope allows, before it
// counts anything.
type ReadScope interface {
	// Shape classifies a repository by key; a key that was not asked for is ReadNone.
	Shape(repo qname.Repo) ReadShape
	// MayRead decides the read of one version of one package of a repository; version may be empty
	// for a question about the package. A version is decided on the package document's path and on
	// the path of its file (VersionPather), and may be read only if both allow it (Q-AUTHZ-255).
	MayRead(repo qname.Repo, pkg, version string) bool
	// VersionsByPath reports whether the caller's own grants decide the versions of a package of the
	// repository one by one: a grant or the token scope narrows paths, so a version's file may be
	// refused where the package's document is allowed. A document of a package the caller may read
	// then lists only the versions MayRead allows; false means every version is readable as far as the
	// caller's grants go (the endpoint's rules are the exposure view's). A key that was not asked for is
	// true.
	VersionsByPath(repo qname.Repo) bool
}

// Resolver is virtual resolution as a format meets it (C-5).
type Resolver interface {
	// Plan is R-REPO-019 steps 1 to 5 for the request's caller, for a format that fans a request
	// out to the members itself (the forwarded search of R-NUGET-018): the members that take part
	// for the name of info, in resolution order, each with its tier, position and whether the
	// caller may fill it. It makes no request to a member.
	Plan(ctx context.Context, rq *Request, p Path, info PathInfo) (*VirtualPlan, error)
	// KeepsAway reports whether a claim, a pin rule or an include/exclude pattern in effect for
	// the request keeps a search term away from a member: the name term (a normalized package
	// name) is not served by the member, or term is the literal stem of a prefix pattern that
	// would keep names away from it (R-NUGET-016 withheld queries). The member is one of
	// the Plan's, or the repository of a request that is not virtual. It reads the replica's
	// in-memory index and runs no database query.
	KeepsAway(ctx context.Context, rq *Request, member RepoRef, term string) (bool, error)
	// ReadScope is the read scope of the request's caller for the repositories by key, for the
	// rows of a listing.
	ReadScope(ctx context.Context, rq *Request, repos []qname.Repo) (ReadScope, error)
	// Artifact plans (R-REPO-019 steps 1 to 5) and walks the tiers of step 6,
	// serving the first hit.
	Artifact(ctx context.Context, rq *Request, p Path, info PathInfo, w http.ResponseWriter) error
	// Metadata plans and merges through the merged-metadata cache (R-REPO-020), with
	// the request's format's VirtualAdapter as the merger.
	Metadata(ctx context.Context, rq *Request, p Path, info PathInfo, variant string) (*Merged, error)
	// DeployTarget resolves the repository a deploy to a virtual repository goes to
	// (R-REPO-021): 405 no_deploy_target when there is none.
	DeployTarget(ctx context.Context, rq *Request) (*repository.Repository, error)
	// ShadowCheck runs after Authorize allowed the write and before the commit,
	// with no transaction open (R-REPO-048).
	ShadowCheck(ctx context.Context, rq *Request, name string) (ShadowOutcome, error)
}
