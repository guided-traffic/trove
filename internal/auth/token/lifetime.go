package token

import (
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// minTTL is the shortest lifetime a request may ask for (R-AUTH-026).
const minTTL = 60 * time.Second

// TTLRequest is the lifetime part of a create request (R-AUTH-026): ttlSeconds
// or expiresAt, or neither, which applies the default of the creation path.
type TTLRequest struct {
	// Seconds is ttlSeconds. 0 asks for a non-expiring token.
	Seconds *int64
	// ExpiresAt is an absolute expiry.
	ExpiresAt *time.Time
	// Default overrides the default of the creation path (the npm web login and
	// the admin CLI have their own); zero uses the type's default.
	Default time.Duration
}

// lifetime is the outcome of resolving a request.
type lifetime struct {
	ttl         time.Duration // meaningful unless nonExpiring
	nonExpiring bool
}

func ttlErr(code, detail string) error {
	return errs.E(errs.Invalid, code, detail)
}

// resolveLifetime applies R-AUTH-026 to a request:
//   - ttlSeconds and expiresAt together, a negative value, 1 to 59 seconds and an
//     expiresAt less than 60 s ahead answer 400 invalid_ttl;
//   - a lifetime above the maximum answers 400 ttl_exceeds_maximum (no clamping);
//   - 0 asks for a non-expiring token: only a service-account token, only while
//     allow_non_expiring is set (400 non_expiring_not_allowed), only by an
//     administrator (403 non_expiring_restricted), never under the derivation
//     rule (400 non_expiring_not_allowed).
//
// For a derived token the default is capped at the presenting token's expiry
// (parentExpires non-nil); an explicit lifetime beyond it is the caller's
// derivation_exceeds_parent, checked by the Service.
func resolveLifetime(t Type, req TTLRequest, now time.Time, def, maxTTL time.Duration, allowNonExpiring, admin, derived bool, parentExpires *time.Time) (lifetime, error) {
	if req.Seconds != nil && req.ExpiresAt != nil {
		return lifetime{}, ttlErr(errs.CodeInvalidTTL, "Give ttlSeconds or expiresAt, not both.")
	}
	if req.Default > 0 {
		def = req.Default
	}
	var ttl time.Duration
	switch {
	case req.Seconds != nil:
		s := *req.Seconds
		if s == 0 {
			if t != TypeSAT || !allowNonExpiring || derived {
				return lifetime{}, ttlErr(errs.CodeNonExpiringNotAllowed, "A non-expiring token is not allowed here.")
			}
			if !admin {
				return lifetime{}, errs.E(errs.Forbidden, errs.CodeNonExpiringRestricted, "Only an administrator may create a non-expiring service-account token.")
			}
			return lifetime{nonExpiring: true}, nil
		}
		if s < int64(minTTL/time.Second) {
			return lifetime{}, ttlErr(errs.CodeInvalidTTL, "ttlSeconds must be 0 or at least 60.")
		}
		if s > int64(maxTTL/time.Second) {
			return lifetime{}, ttlErr(errs.CodeTTLExceedsMaximum, "The lifetime exceeds the maximum of "+maxTTL.String()+".")
		}
		ttl = time.Duration(s) * time.Second
	case req.ExpiresAt != nil:
		ttl = req.ExpiresAt.Sub(now)
		if ttl < minTTL {
			return lifetime{}, ttlErr(errs.CodeInvalidTTL, "expiresAt must be at least 60 seconds ahead.")
		}
	default:
		ttl = def
		if parentExpires != nil {
			if rest := parentExpires.Sub(now); rest < ttl {
				ttl = rest
			}
		}
	}
	if ttl > maxTTL {
		return lifetime{}, ttlErr(errs.CodeTTLExceedsMaximum, "The lifetime exceeds the maximum of "+maxTTL.String()+".")
	}
	return lifetime{ttl: ttl}, nil
}
