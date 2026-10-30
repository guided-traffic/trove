package authz

import (
	"context"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Action is a repository action (R-AUTHZ-006). No action implies another.
type Action string

// The repository actions.
const (
	Read      Action = "read"
	Annotate  Action = "annotate"
	Deploy    Action = "deploy"
	Overwrite Action = "overwrite"
	Delete    Action = "delete"
	Manage    Action = "manage"
)

// Capability is a system capability (R-AUTHZ-010). The same names are the system
// scope items of R-AUTH-025 and the values of x-trove-permission.
type Capability string

// The capabilities of R-AUTHZ-010.
const (
	ContentBypass         Capability = "content:bypass"
	IdentitiesRead        Capability = "identities:read"
	IdentitiesWrite       Capability = "identities:write"
	PoliciesRead          Capability = "policies:read"
	PoliciesWrite         Capability = "policies:write"
	RolesRead             Capability = "roles:read"
	RolesWrite            Capability = "roles:write"
	CIRead                Capability = "ci:read"
	CIWrite               Capability = "ci:write"
	TokensRead            Capability = "tokens:read"
	TokensRevoke          Capability = "tokens:revoke"
	RepositoriesRead      Capability = "repositories:read-config"
	RepositoriesWrite     Capability = "repositories:write-config"
	ProjectsRead          Capability = "projects:read"
	ProjectsWrite         Capability = "projects:write"
	WebhooksRead          Capability = "webhooks:read"
	WebhooksWrite         Capability = "webhooks:write"
	SettingsRead          Capability = "settings:read"
	SettingsWrite         Capability = "settings:write"
	SecuritySettingsWrite Capability = "security-settings:write"
	AuditRead             Capability = "audit:read"
	EndpointsRead         Capability = "endpoints:read"
	EndpointsWrite        Capability = "endpoints:write"
)

// RequestKind is the request kind of the request descriptor (R-REPO-003); the
// catalog owns the type, so a format and the decision point name one.
type RequestKind = catalog.RequestKind

// The request kinds.
const (
	KindArtifact = catalog.KindArtifact
	KindMetadata = catalog.KindMetadata
	KindSearch   = catalog.KindSearch
	KindWrite    = catalog.KindWrite
	KindOther    = catalog.KindOther
)

// Subject is what authentication hands to authorization for one request
// (Model at a glance): who, which groups, the ceiling the credential puts on
// them, and the endpoint the request arrived on. It is built by
// internal/api/edge.SubjectFrom from httpserver.Principal and the listener's
// endpoint; the architecture test refuses a Subject built anywhere else
// (R-EP-013, Q-EP-20).
//
// Whether the subject holds a system role is not a field: Decide and Require
// derive it from role bindings, the scope and Kind (a break-glass subject holds
// admin without a binding, R-AUTHZ-010).
type Subject struct {
	Anonymous bool
	Kind      principal.Kind
	// ID is the principal-table ID; empty when anonymous.
	ID   string
	Name string
	// Groups were resolved at request time by the authentication stage.
	Groups []principal.GroupRef
	// Mappings are CI identity mapping references (M4).
	Mappings []string
	// Scope is the credential's ceiling; nil means none (sessions).
	Scope *scope.Scope
	// Endpoint is the name of the endpoint whose listener accepted the request
	// (R-EP-002). It selects the exposure ceiling of every decision on this
	// subject (R-EP-013, Q-EP-20 (b)), so a decision keeps the ceiling of the
	// request on a detached context, in a collapsed single-flight and in a
	// fanned-out probe alike: the subject travels with the decision, the context
	// may not. Empty only for a subject no request carries (a job, a command, a
	// test), which has no ceiling. Nothing reads it from request data.
	Endpoint string
	// CacheFill is the cache_fill setting of that endpoint (R-EP-019), set by the
	// same conversion from the same endpoint: with Endpoint set and CacheFill
	// false no subject passes the populate check (R-AUTHZ-008), so a request on
	// an endpoint that does not fill caches never makes trove contact an
	// upstream, also in a collapsed or fanned-out decision (Q-AUTHZ-153). A
	// subject without an endpoint has no such ceiling.
	CacheFill bool
	// TokenID and SessionID identify the credential for audit.
	TokenID   string
	SessionID string
	// AuthMethod is how the credential was presented (session, bearer, basic,
	// api_key), for the actor of an audit event; empty when unknown.
	AuthMethod string
	// anon is the runtime switch of R-AUTH-037 as the decision point read it for
	// this request; it is set by the engine only.
	anon anonState
	// MinEpoch is the value of the Trove-Authz-Min-Epoch header (R-AUTHZ-032); zero
	// when absent.
	MinEpoch int64
}

type anonState uint8

const (
	anonUnknown anonState = iota
	anonOff
	anonOn
)

// Coordinate is the authorization view of the request descriptor (R-REPO-003).
type Coordinate struct {
	// Repository is the key addressed by the client.
	Repository string
	Class      repository.Class
	Format     string
	Kind       RequestKind
	// Path is the canonical path (R-AUTHZ-005); Package is the normalized
	// package name; both may be empty.
	Path    string
	Package string
	// Version is the normalized version of a coordinate that names one version
	// of a package (R-EP-012); empty for a package document, a path of a
	// format without versions and a repository-level coordinate.
	Version string
	// Actions are the actions the operation needs, e.g. {Deploy, Overwrite}.
	Actions []Action
	// Addressed is the repository the client named when Repository is a member
	// that resolution consults (R-AUTHZ-015 WithRepo); empty means Repository.
	// Exposure is decided on the addressed repository (Q-EP-11).
	Addressed string
}

// Outcome is the class of a decision, one per row of R-AUTHZ-016's table.
type Outcome int

// The outcomes. Deny* values are refusals.
const (
	// Allow admits the request.
	Allow Outcome = iota
	// DenyUnauthenticated: 401 with the challenge of R-AUTH-002.
	DenyUnauthenticated
	// DenyHidden: 404 byte-identical to a missing resource (read-type requests,
	// exposure).
	DenyHidden
	// DenyVisible: 403 naming the missing action (write-type requests and
	// management operations).
	DenyVisible
	// DenyStale: 503 authz_stale with Retry-After (the grant index is not
	// confirmed current).
	DenyStale
	// DenyEpochUnknown: 412 authz_epoch_unknown (Trove-Authz-Min-Epoch names an
	// epoch the database has not reached, R-AUTHZ-032).
	DenyEpochUnknown
)

// Decision is the result of Decide.
type Decision struct {
	Outcome Outcome
	// Reason is a stable reason code of R-AUTHZ-029; empty on Allow.
	Reason string
	// Missing is the first required action that was refused, for the 403 text.
	Missing Action
	// Epoch is the epoch of the index that decided (R-AUTHZ-032).
	Epoch int64
	// Target names the coordinate the caller sent, for the text of a 403: the
	// repository, and the package or path when there is one. It never holds
	// anything the caller did not send.
	Target string
	// Sources names what allowed the request on Allow: policy names, "role:admin"
	// for content:bypass (R-AUTHZ-029).
	Sources []string
}

// Decider is the decision point as a request uses it (R-AUTHZ-015
// `index.ForRequest`): a request takes its index once with Begin and asks every
// question of it with the …On forms, so all of them are answered on one index
// and a swap of the installed index never changes the decisions of one request
// (R-AUTHZ-028). The ceiling of the subject's endpoint is applied by every
// question (R-EP-013); the context carries deadlines and cancellation, never
// authority. *Engine implements it.
//
// The edge holds the index and the subject of a request in internal/api/edge
// (Access); a format receives them in its request. Jobs and commands, which
// have no request to share an index with, use the [Authorizer] forms.
type Decider interface {
	// Begin returns the index a request decides on: it waits for a confirmed
	// index or for the epoch of Trove-Authz-Min-Epoch, and first makes the keys
	// known (R-ARCH-011 step 2, R-API-052). The error is the refusal: 503
	// authz_stale or 412 authz_epoch_unknown.
	Begin(ctx context.Context, sub Subject, keys ...string) (*Index, error)
	// Know advances a request to a newer index when it learns a key the index
	// does not hold (R-ARCH-011 step 2); the request decides every later question
	// on the index returned. An index that holds the key is returned as it is.
	Know(ctx context.Context, idx *Index, sub Subject, key string) (*Index, error)
	// DecideOn decides every action of the coordinate (R-AUTHZ-015); the request
	// proceeds only if it is allowed. The caller turns a refusal into the status
	// of R-AUTHZ-016.
	DecideOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision
	// ReadableOn filters repository keys to those the subject may read, in the
	// order given, for listings and search (R-AUTHZ-016). An error is a refusal to
	// decide.
	ReadableOn(ctx context.Context, idx *Index, sub Subject, keys []string) ([]string, error)
	// ExposedOn filters repository keys to those the subject's endpoint exposes
	// for read, whatever the subject's grants (R-EP-014, Q-EP-12).
	ExposedOn(idx *Index, sub Subject, keys []string) []string
	// PathsAllowedOn decides one action for many paths of one repository, the
	// listing form of DecideOn; it writes no audit event.
	PathsAllowedOn(ctx context.Context, idx *Index, sub Subject, base Coordinate, a Action, paths []string) ([]bool, error)
	// RequireOn checks a system capability for a management operation (R-API-008):
	// 401 for an anonymous subject, 403 otherwise; it runs before any lookup.
	RequireOn(ctx context.Context, idx *Index, sub Subject, permission string) error
	// HoldsOn reports whether the subject holds a permission as RequireOn would
	// decide it, as a question: nothing is audited or counted as a denial.
	HoldsOn(idx *Index, sub Subject, permission string) bool
	// CheckOn evaluates the subject's own rights for up to MaxChecks coordinates
	// (R-AUTHZ-023); it audits nothing.
	CheckOn(idx *Index, sub Subject, items []CheckItem) ([]bool, error)
	// MayPopulateOn asks the populate check of R-AUTHZ-008 for a coordinate of a
	// remote repository: read, the repository's cacheFillAction, not the session
	// cookie, and an endpoint that fills caches (R-EP-019). It is a question:
	// nothing is audited, so the caller that serves what the cache holds instead
	// asks it. A refused read comes back as that refusal (404 stays 404).
	MayPopulateOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision
	// PopulateOn is the populate check as a request's refusal: the decision of
	// MayPopulateOn, counted and, when refused, audited as authz.denied with
	// deny.populate (403 populate_not_permitted). The caller that has nothing to
	// serve without a fetch asks it; the allowed populate is recorded with
	// RecordAllowed in the fill transaction.
	PopulateOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision
	// MemberUsableOn is MemberUsable of R-AUTHZ-015 for a member of the virtual
	// repository the coordinate addresses: read says the resolver may consult the
	// member, fill that it may also populate the member's cache (a remote member
	// only). Whether the member needs its upstream for the coordinate is the
	// resolver's to know; a member it may not use is skipped silently.
	MemberUsableOn(ctx context.Context, idx *Index, sub Subject, member string, c Coordinate) (read, fill bool)
	// RecordAllowed records an allowed write or populate in the transaction of the
	// change (R-AUTHZ-029), with the decision the writer acted on.
	RecordAllowed(ctx context.Context, tx meta.Tx, sub Subject, c Coordinate, d Decision) error
}

// Authorizer is the decision point for a caller that has no request to share an
// index with, a job or a command: each call takes the installed index at that
// moment, as Begin and the matching …On form of [Decider] would. A request
// handler uses [Decider].
type Authorizer interface {
	// Decide runs for every coordinate of an operation; it proceeds only if every
	// coordinate is allowed (R-AUTHZ-015). The caller turns a refusal into the
	// status of R-AUTHZ-016.
	Decide(ctx context.Context, sub Subject, c Coordinate) Decision
	// Readable filters repository keys to those the subject may read, for
	// listings and search (R-AUTHZ-016: listings contain only what the subject
	// may read).
	Readable(ctx context.Context, sub Subject, keys []string) ([]string, error)
	// Require checks a system capability for a management operation. A refusal
	// is an *errs.Error: 401 for an anonymous subject, 403 otherwise
	// (R-API-008). It runs before any lookup, so it does not depend on whether
	// the addressed object exists.
	Require(ctx context.Context, sub Subject, permission string) error
}

// Prober answers whether a subject holds a permission without recording a denial:
// the question of a handler that chooses between two answers and of the rate
// limit that exempts administrators (R-AUTHZ-029: only a refusal of an operation
// is audited). *Engine implements it next to [Authorizer].
type Prober interface {
	Holds(ctx context.Context, sub Subject, permission string) (bool, error)
}
