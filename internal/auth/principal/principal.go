package principal

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/audit"
)

// Kind is the kind of a principal-table row (R-AUTHZ-002). The values match
// httpserver.PrincipalKind.
type Kind string

// The kinds.
const (
	KindUser           Kind = "user"
	KindGroup          Kind = "group"
	KindServiceAccount Kind = "service_account"
	// KindCI is a CI identity mapping row (M4).
	KindCI Kind = "ci"
	// KindAnonymous and KindAuthenticated are the built-in rows (R-AUTHZ-003,
	// R-AUTHZ-030).
	KindAnonymous     Kind = "anonymous"
	KindAuthenticated Kind = "authenticated"
	// KindBreakGlass is the break-glass administrator (R-AUTH-014). It is a user
	// row whose origin is break-glass; it is a separate kind here because
	// authorization treats it differently (R-AUTHZ-010).
	KindBreakGlass Kind = "break_glass"
)

// Status is the lifecycle state of a principal. Authentication refuses the
// credentials of any principal that is not active; authorization never sees
// them (R-AUTHZ-002).
type Status string

// The statuses.
const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusLocked   Status = "locked"
)

// Principal is a row of the principal table in the form other packages need.
type Principal struct {
	// ID is the immutable ID.
	ID     string
	Kind   Kind
	Name   string
	Status Status
}

// GroupRef names a group.
type GroupRef struct {
	ID   string
	Name string
}

// ErrNotFound is returned by a Store when the principal does not exist.
var ErrNotFound = errors.New("principal: not found")

// Store is the read side other packages use. Lookups are answered from the
// replica's auth_principals cache and are invalidated through the change area
// auth (R-AUTH-023); they never go to Valkey.
type Store interface {
	// ByID returns the principal, or ErrNotFound.
	ByID(ctx context.Context, id string) (Principal, error)
	// ByName returns the principal of a kind by its name, or ErrNotFound.
	ByName(ctx context.Context, kind Kind, name string) (Principal, error)
	// Groups returns every group of the principal: local memberships plus the
	// ones synchronized from the identity provider's group claim (R-AUTH-008),
	// resolved when called, never when a token was issued (R-AUTHZ-002).
	Groups(ctx context.Context, id string) ([]GroupRef, error)
}

// GroupClaimState says whether the provider's group claim was usable
// (R-AUTH-008).
type GroupClaimState string

// The states of the group claim.
const (
	// ClaimPresent: the claim was a string or an array of strings, possibly empty.
	ClaimPresent GroupClaimState = ""
	// ClaimAbsent: the claim is in neither the ID token nor userinfo.
	ClaimAbsent GroupClaimState = "absent"
	// ClaimWrongType: the claim has another type.
	ClaimWrongType GroupClaimState = "wrong_type"
	// ClaimOverage: the claim is replaced by an overage marker.
	ClaimOverage GroupClaimState = "overage"
)

// LoginClaims is what an OIDC login hands to the provisioner (R-AUTH-006,
// R-AUTH-007, R-AUTH-008). The login flow has applied the provider's claim
// mapping, the gates that need the raw claims (require_verified_email,
// login_requires_groups) and the group mapping rules; the provisioner applies the
// result to the principal tables. The fields after Groups carry the provider
// policy the provisioner needs; they are additions to the first contract.
type LoginClaims struct {
	// Provider is the provider ID; Issuer is its issuer and Subject the stable
	// "sub": the pair is the identity key (R-AUTH-007).
	Provider string
	Issuer   string
	Subject  string
	// Username is the mapped username (NFC, lower case, prefix applied).
	Username      string
	Email         string
	EmailVerified bool
	// DisplayName is the user's name for display.
	DisplayName string
	// Groups are the trove group names the provider's mapping rules produced from
	// the group claim (R-AUTH-008); the provisioner makes the user's
	// oidc:<provider> memberships exactly this set.
	Groups []string

	// GroupSync is false when group_sync.enabled is off for the provider: the
	// memberships are not touched.
	GroupSync bool
	// ClaimState says whether the group claim was usable. With a missing claim
	// the memberships are cleared, or kept with KeepOnMissing (on_missing_claim),
	// and user.groups.claim_missing is recorded. The deny policy is the login
	// flow's: it never calls the provisioner.
	ClaimState    GroupClaimState
	KeepOnMissing bool
	// CreateGroups creates a missing target group with origin oidc
	// (group_sync.create_groups).
	CreateGroups bool
	// JIT lets an unknown identity become a user (provider jit).
	JIT bool
	// SyncUsername renames the user to a changed mapped username (sync_username).
	SyncUsername bool

	// Source is where the login came from, for the audit events.
	Source audit.Source
}

// Provisioner is the seam between the login flow (WP1.1) and the identity
// packages (WP1.2): it creates or updates the user just in time, synchronizes
// groups, refuses a disabled user and records the user.* and
// user.groups.synced events in its own transaction.
type Provisioner interface {
	SignIn(ctx context.Context, c LoginClaims) (Principal, error)
}
