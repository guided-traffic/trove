package authz

import (
	"context"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
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

// LegacySystemRead is the M0 permission name "system.read". R-AUTHZ-010 defines
// it as the capability SettingsRead; Require accepts the old name as an alias
// until the OpenAPI document is migrated.
const LegacySystemRead = "system.read"

// RequestKind is the request kind of the request descriptor (R-REPO-003).
type RequestKind string

// The request kinds.
const (
	KindArtifact RequestKind = "artifact"
	KindMetadata RequestKind = "metadata"
	KindSearch   RequestKind = "search"
	KindWrite    RequestKind = "write"
)

// Subject is what authentication hands to authorization for one request
// (Model at a glance): who, which groups, and the ceiling the credential puts
// on them. It is built by internal/api/v1 from httpserver.Principal.
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
	Scope *token.Scope
	// TokenID and SessionID identify the credential for audit.
	TokenID   string
	SessionID string
	// MinEpoch is the value of the Trove-Authz-Min-Epoch header (R-AUTHZ-032); zero
	// when absent.
	MinEpoch int64
}

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
	// Actions are the actions the operation needs, e.g. {Deploy, Overwrite}.
	Actions []Action
	// Endpoint is the name of the endpoint the request arrived on; it selects the
	// exposure ceiling (R-EP-013).
	Endpoint string
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
}

// Authorizer is the decision point. The implementation is WP1.3's.
type Authorizer interface {
	// Decide runs for every coordinate of a request; the request proceeds only
	// if every coordinate is allowed (R-AUTHZ-015). The caller turns a refusal
	// into the status of R-AUTHZ-016.
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
