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
	// IssuerMax is the issuer's max_ttl; MappingMax the smallest max_ttl of the contributing mappings.
	// Nil is unbounded.
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
		{in.MappingMax, "the maxTtlSeconds of a contributing mapping"},
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
