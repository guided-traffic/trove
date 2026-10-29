package authz

import (
	"fmt"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// Reason codes of R-AUTHZ-029, the values of Decision.Reason.
const (
	ReasonAllowGrant              = "allow.grant"
	ReasonAllowRole               = "allow.role"
	ReasonUnauthenticated         = "deny.unauthenticated"
	ReasonNoGrant                 = "deny.no-grant"
	ReasonScope                   = "deny.scope"
	ReasonVirtualGate             = "deny.virtual-gate"
	ReasonAnonymousPartialVirtual = "deny.anonymous-partial-virtual"
	ReasonCapability              = "deny.capability"
	ReasonStale                   = "deny.stale"
	ReasonExposure                = "deny.exposure"
	ReasonEndpointReadOnly        = "deny.endpoint_read_only"
	ReasonEpochUnknown            = "deny.epoch-unknown"
)

// codeEndpointReadOnly is registered by the endpoints area (R-EP-013); authz
// only names it.
const codeEndpointReadOnly = "endpoint_read_only"

// staleRetryAfter is the Retry-After of an authz_stale answer: it does not
// depend on the coordinate, so it reveals nothing about existence (R-AUTHZ-016).
const staleRetryAfter = 2 * time.Second

// NotFound is the one answer for a resource that does not exist and for one the
// caller may not read. A hiding 404 must be indistinguishable from the 404 of a
// missing resource on the same endpoint, body and headers (R-AUTHZ-016):
// Decision.Err returns this error for DenyHidden, and every caller that answers a
// missing resource in the same place builds that answer here as well, so the two
// are the same bytes by construction.
func NotFound() *errs.Error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
}

// Unauthenticated is the 401 of R-AUTHZ-016. The challenge is the generic bearer
// one; the protocol endpoints replace it with the challenge of their family
// (R-AUTH-002) by setting Challenge on the returned error.
func Unauthenticated() *errs.Error {
	return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated,
		"This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer"))
}

// Err maps the decision to the error of R-AUTHZ-016's table, nil for Allow:
//
//	DenyStale            503 authz_stale with Retry-After
//	DenyUnauthenticated  401 with the challenge
//	DenyEpochUnknown     412 authz_epoch_unknown
//	DenyHidden           404, byte-identical to a missing resource (NotFound)
//	DenyVisible          403, naming the missing action and the coordinate the
//	                     caller sent; 403 endpoint_read_only for a write on an
//	                     endpoint that exposes rules
func (d Decision) Err() error {
	switch d.Outcome {
	case Allow:
		return nil
	case DenyStale:
		return errs.E(errs.Unavailable, errs.CodeAuthzStale,
			"The authorization state of this replica could not be confirmed; retry.", errs.WithRetryAfter(staleRetryAfter))
	case DenyUnauthenticated:
		return Unauthenticated()
	case DenyEpochUnknown:
		return errs.E(errs.PreconditionFailed, errs.CodeAuthzEpochUnknown,
			"The authorization epoch named in Trove-Authz-Min-Epoch has not been reached by the database.")
	case DenyHidden:
		return NotFound()
	case DenyVisible:
		if d.Reason == ReasonEndpointReadOnly {
			return errs.E(errs.Forbidden, codeEndpointReadOnly, "This endpoint is read-only.")
		}
		return errs.E(errs.Forbidden, errs.CodeForbidden, d.forbiddenText())
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

func (d Decision) forbiddenText() string {
	switch {
	case d.Missing != "" && d.Target != "":
		return fmt.Sprintf("The action %s is not permitted on %s.", d.Missing, d.Target)
	case d.Missing != "":
		return fmt.Sprintf("The action %s is not permitted.", d.Missing)
	}
	return "The operation is not permitted."
}

// Status returns the HTTP status of the decision, 200 for Allow.
func (d Decision) Status() int {
	switch d.Outcome {
	case Allow:
		return 200
	case DenyUnauthenticated:
		return 401
	case DenyHidden:
		return 404
	case DenyVisible:
		return 403
	case DenyStale:
		return 503
	case DenyEpochUnknown:
		return 412
	}
	return 403
}

// Allowed reports whether the decision admits the request.
func (d Decision) Allowed() bool { return d.Outcome == Allow }

// ParseMinEpoch reads the Trove-Authz-Min-Epoch header (R-AUTHZ-032): absent is
// 0, a decimal integer between 1 and 2^63-1 is that integer, anything else is
// 400 invalid_header. The caller reads the header only for a request that
// authenticated; an anonymous request ignores it.
func ParseMinEpoch(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, invalidMinEpoch()
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return 0, invalidMinEpoch()
	}
	return n, nil
}

func invalidMinEpoch() error {
	return errs.E(errs.Invalid, errs.CodeInvalidHeader,
		"Trove-Authz-Min-Epoch must be a decimal integer between 1 and 9223372036854775807.")
}

// HeaderMinEpoch is the request header that names the epoch a request needs
// (R-AUTHZ-032); HeaderEpoch is the response header that carries the epoch a
// write produced.
const (
	HeaderMinEpoch = "Trove-Authz-Min-Epoch"
	HeaderEpoch    = "Trove-Authz-Epoch"
)
