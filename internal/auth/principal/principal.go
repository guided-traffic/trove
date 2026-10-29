package principal

import (
	"context"
	"errors"
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

// LoginClaims is what an OIDC login hands to the provisioner (R-AUTH-006,
// R-AUTH-007, R-AUTH-008).
type LoginClaims struct {
	// Provider is the provider ID; Subject is the stable "sub" of the provider.
	Provider string
	Subject  string
	Username string
	Email    string
	// DisplayName is the user's name for display.
	DisplayName string
	// Groups are the values of the configured group claim; the provisioner
	// synchronizes them (R-AUTH-008).
	Groups []string
}

// Provisioner is the seam between the login flow (WP1.1) and the identity
// packages (WP1.2): it creates or updates the user just in time, synchronizes
// groups, refuses a disabled user and records the user.* and
// user.groups.synced events in its own transaction.
type Provisioner interface {
	SignIn(ctx context.Context, c LoginClaims) (Principal, error)
}
