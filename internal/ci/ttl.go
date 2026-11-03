package ci

import (
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

// The lifetime of a CI token (R-CI-016, Q-CI-41): one formula for the exchange and the dry run.

// TTLInput is what the lifetime of a CI token depends on (R-CI-016).
type TTLInput struct {
	// Requested is the `ttl` parameter; nil takes Default.
	Requested *time.Duration
	// Default, Max, Min are ci.token.default_ttl, ci.token.max_ttl and ci.token.min_ttl.
	Default, Max, Min time.Duration
	// IssuerMax is the issuer's max_ttl; MappingMax the max_ttl of the mapping that owns the token
	// ([ComputeLifetime] sets it, Q-CI-47). Nil is unbounded.
	IssuerMax, MappingMax *time.Duration
	// SubjectExpiresAt is the `exp` of the subject token and Now the exchange's clock; a zero
	// SubjectExpiresAt leaves the term out (the dry run of a claim set without `now`).
	SubjectExpiresAt, Now time.Time
}

// ComputeTTL is the formula of R-CI-016: the minimum of the requested or default ttl, the mapping and
// issuer bounds, ci.token.max_ttl and the remaining lifetime of the subject token, in whole seconds
// (expires_in is an integer). A result below Min names its cause (Q-CI-41):
//
//   - a requested ttl below the floor is the caller's error: ttl_below_minimum (the exchange refuses it at
//     step 1 already, before the subject token costs a signature check);
//   - the subject token's own remaining lifetime below the floor: subject_token_expires_too_soon;
//   - a mapping's or the issuer's max_ttl below the floor (ci.token.min_ttl raised after the save):
//     ttl_below_minimum, the detail names the bound.
//
// detail names the configuration key or the parameter and holds no claim value.
func ComputeTTL(in TTLInput) (ttl time.Duration, failure Reason, detail string) {
	ttl, bound := in.Default, "ci.token.default_ttl"
	if in.Requested != nil {
		ttl, bound = *in.Requested, "the requested ttl"
	}
	if in.Requested != nil && *in.Requested < in.Min {
		return 0, reason.TTLBelowMinimum, "the requested ttl is below ci.token.min_ttl (" + seconds(in.Min) + ")"
	}
	if !in.SubjectExpiresAt.IsZero() {
		if left := in.SubjectExpiresAt.Sub(in.Now).Truncate(time.Second); left < in.Min {
			return 0, reason.SubjectTokenExpiresTooSoon, "the subject token expires in less than ci.token.min_ttl (" + seconds(in.Min) + ")"
		} else if left < ttl {
			ttl, bound = left, "the subject token's remaining lifetime"
		}
	}
	for _, b := range []struct {
		d    *time.Duration
		name string
	}{
		{in.MappingMax, "the maxTtlSeconds of the mapping that owns the token"},
		{in.IssuerMax, "the maxTtlSeconds of the issuer"},
		{&in.Max, "ci.token.max_ttl"},
	} {
		if b.d != nil && *b.d < ttl {
			ttl, bound = *b.d, b.name
		}
	}
	ttl = ttl.Truncate(time.Second)
	if ttl < in.Min {
		return 0, reason.TTLBelowMinimum, bound + " is below ci.token.min_ttl (" + seconds(in.Min) + ")"
	}
	return ttl, "", ""
}

func seconds(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) + " s" }

// Lifetime is the lifetime of a token and of each of its contributions (R-CI-016, Q-CI-47).
type Lifetime struct {
	// TTL is the token's: expires_in and the token row's expiry.
	TTL time.Duration
	// Owner is the index of the contribution whose mapping owns the token (its principal is the token's owner).
	Owner int
	// Windows are the lifetimes of the contributions, in their order, each at most TTL: a contribution whose
	// window is shorter than TTL ends before the token (claims.FrozenMapping.ExpiresAt).
	Windows []time.Duration
}

// tokenOwner is the contribution that owns a token (Q-CI-47): the system tier's when it contributes, the
// administrators' mapping, so no project's mapping decides the token of a job the system tier gives grants to;
// otherwise the project contribution whose own bound is the longest (none is longest; a tie goes to the first in
// evaluation order), so the token never outlives its owner's contribution.
func tokenOwner(cs []Contribution) int {
	owner := -1
	for i, c := range cs {
		if c.Mapping.Tier == TierSystem {
			return i
		}
		if owner < 0 || longer(c.Mapping.MaxTTLSeconds, cs[owner].Mapping.MaxTTLSeconds) {
			owner = i
		}
	}
	return max(owner, 0)
}

// longer reports whether bound a is longer than b; nil is unbounded.
func longer(a, b *int64) bool {
	switch {
	case a == nil:
		return b != nil
	case b == nil:
		return false
	}
	return *a > *b
}

// ComputeLifetime is R-CI-016 over the contributions of one token (Q-CI-47): the token's lifetime is ComputeTTL with
// the bound of its owner's mapping (tokenOwner) and no other mapping's, so a project tier's maxTtlSeconds never
// shortens the token of another tier and never fails the exchange beside it; each other contribution lives for
// the token's lifetime or its own mapping's bound, whichever is shorter. in.MappingMax is ignored. A failure is
// ComputeTTL's, for the owner's bound.
func ComputeLifetime(in TTLInput, cs []Contribution) (Lifetime, Reason, string) {
	owner := tokenOwner(cs)
	in.MappingMax = nil
	if owner < len(cs) {
		in.MappingMax = maxTTL(cs[owner].Mapping)
	}
	ttl, failure, detail := ComputeTTL(in)
	if failure != "" {
		return Lifetime{}, failure, detail
	}
	out := Lifetime{TTL: ttl, Owner: owner, Windows: make([]time.Duration, len(cs))}
	for i, c := range cs {
		out.Windows[i] = ttl
		if b := maxTTL(c.Mapping); b != nil && *b < ttl {
			out.Windows[i] = *b
		}
	}
	return out, "", ""
}

// maxTTL is a mapping's maxTtlSeconds as a duration, nil when it has none.
func maxTTL(m Mapping) *time.Duration {
	if m.MaxTTLSeconds == nil {
		return nil
	}
	d := time.Duration(*m.MaxTTLSeconds) * time.Second
	return &d
}
