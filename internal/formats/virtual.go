package formats

import (
	"context"
	"net/http"
	"time"

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
	// Newest: Merge, with remote members revalidated first (R-REPO-036).
	Newest
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
	Stale          bool
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
}

// Resolver is virtual resolution as a format meets it (C-5).
type Resolver interface {
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
