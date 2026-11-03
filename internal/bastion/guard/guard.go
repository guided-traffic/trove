// Package guard is the Bastion's abuse protection (R-BAS-035 to R-BAS-037, Q-BAS-16): the block list, the
// per-address request and miss limits, the failed-authentication timeout and the blocking of addresses that
// keep being refused. It is the Bastion's own stage; the coarse one is the ingress's (R-BAS-038).
//
// Work package 6 implements [Guard] over internal/bastion/shared (local, or Valkey when configured); the
// engine (work package 4) asks it at the steps of R-BAS-018 and reports what it observes. The guard counts and
// decides; it never answers a request itself: the engine renders the refusal ([Verdict]) as the problem
// details of R-BAS-029 with Retry-After and the RateLimit fields.
package guard

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// Credentialed says whether a request carries a credential: it selects the stricter anonymous limits
// (R-BAS-035).
type Credentialed bool

// Verdict is the guard's answer for a request.
type Verdict struct {
	// Allowed is true when the request may proceed.
	Allowed bool
	// Status and Code are the refusal: 429 with rate_limited, auth_timeout or address_blocked, using the codes
	// of internal/bastion/iface.
	Status int
	Code   string
	// RetryAfter is the Retry-After of the refusal.
	RetryAfter time.Duration
	// Reason is the label of trove_bastion_refused_total (metrics.Refused*).
	Reason string
}

// Allow is the verdict that admits.
func Allow() Verdict { return Verdict{Allowed: true} }

// Guard decides on the client address of a request (R-BAS-019). Every method takes the address as
// [AddressKey] returns it, so an IPv6 host and its /64 count as one.
type Guard interface {
	// Blocked is step 2 of R-BAS-018: a blocked address answers 429 address_blocked before any further work.
	Blocked(ctx context.Context, addr string) Verdict
	// Request is step 4: the per-address request limit of the class (R-BAS-035). A refusal counts toward the
	// block threshold. Addresses in bastion.blocking.exempt_cidrs are never limited.
	Request(ctx context.Context, addr string, c Credentialed) Verdict
	// Miss is asked before a request to the main installation (a forward, a check, a conditional request, a
	// probe): the per-address miss limit of the class (R-BAS-035). Hits of the same address keep being served.
	Miss(ctx context.Context, addr string, c Credentialed) Verdict
	// AuthTimeout reports whether the address is in the failed-authentication timeout (R-BAS-036); the engine
	// consults it only for a credential it holds no usable answer for.
	AuthTimeout(ctx context.Context, addr string) Verdict
	// FailedAuth records a failed authentication: a credential the Bastion refuses itself or one the main
	// installation called invalid (R-BAS-036). More than bastion.failed_auth.max_failures within the window
	// start the timeout.
	FailedAuth(ctx context.Context, addr string)
}

// AddressKey is the key under which the limits, the timeout and the blocks count an address: an IPv4 address
// as itself, an IPv6 address as its /64 (R-BAS-019, Q-BAS-37), because one IPv6 host holds a whole /64 and
// counting single addresses would be no limit. An IPv4-mapped IPv6 address counts as the IPv4 address.
func AddressKey(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// ParseAddressKey parses a client address in text form and returns its key; an unparsable address is an
// error, which the caller treats as an anonymous address of its own ("invalid").
func ParseAddressKey(s string) (string, error) {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", fmt.Errorf("guard: %q is not an address: %w", s, err)
	}
	return AddressKey(a), nil
}

// Stub is the placeholder guard of the contract work. It admits every request, which is safe only because
// nothing serves a request through it yet: the engine placeholder answers bastion_not_served to everything.
// Work package 6 replaces it; a Bastion must never run with the stub once the engine serves reads, which the
// engine's constructor checks (engine.Deps.Validate).
type Stub struct{}

var _ Guard = Stub{}

// Blocked implements [Guard].
func (Stub) Blocked(context.Context, string) Verdict { return Allow() }

// Request implements [Guard].
func (Stub) Request(context.Context, string, Credentialed) Verdict { return Allow() }

// Miss implements [Guard].
func (Stub) Miss(context.Context, string, Credentialed) Verdict { return Allow() }

// AuthTimeout implements [Guard].
func (Stub) AuthTimeout(context.Context, string) Verdict { return Allow() }

// FailedAuth implements [Guard].
func (Stub) FailedAuth(context.Context, string) {}

// IsStub reports whether g is the placeholder.
func IsStub(g Guard) bool { _, ok := g.(Stub); return ok }

// Codes the guard's refusals carry, from the interface package so the engine renders one set.
const (
	CodeRateLimited    = iface.CodeRateLimited
	CodeAuthTimeout    = iface.CodeAuthTimeout
	CodeAddressBlocked = iface.CodeAddressBlocked
)
