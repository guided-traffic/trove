package claims

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

// Error is a refusal of the claim engine with the reason the API and the audit event carry. The
// management API answers it as a 422 whose `code` is Reason and whose pointer is Pointer
// (R-CI-002); the exchange answers a render failure as `grant_render_failed` and keeps Detail in the
// audit event only, because error_description holds no claim values (R-CI-008).
type Error struct {
	Reason reason.Reason
	// Pointer is the JSON pointer into the request body of the offending member, "" when none.
	Pointer string
	// Detail is a sentence for the administrator; it may name a claim, never a claim value.
	Detail string
}

// Error implements error.
func (e *Error) Error() string { return string(e.Reason) + ": " + e.Detail }

func refuse(r reason.Reason, pointer, detail string) *Error {
	return &Error{Reason: r, Pointer: pointer, Detail: detail}
}

// The codes of a save that the reason leaf does not name. Each is the name of a code of the
// management API's registry (internal/errs) with the same meaning, so a 422 carries one vocabulary.
const (
	// ReasonProjectPrefixRequired: a project mapping's name does not start with "<project>-" (Q-CI-26).
	ReasonProjectPrefixRequired reason.Reason = "project_prefix_required"
	// ReasonInvalidPattern: a repository selector, package pattern or path pattern has invalid syntax
	// or exceeds the limits of R-AUTHZ-004 and R-AUTHZ-005 (1024 characters, 256 patterns per list).
	ReasonInvalidPattern reason.Reason = "invalid_pattern"
	// ReasonInvalidValue: a member holds a value outside its closed set (an ownership, a
	// crossProjectSource, a subject kind) or an inline grant names both or neither of packages and paths.
	ReasonInvalidValue reason.Reason = "invalid_value"
)

// Claims is the claim set of a verified subject token, or of a dry-run request. The values are the
// decoded JSON values: string, bool, json.Number or float64, nil, []any and map[string]any. Claim
// names are the issuer's spelling (R-CI-010).
type Claims map[string]any

// Normalized returns the normalized string values a claim addresses, for the comparison of every
// leaf and for the substitution of every template (R-CI-010 "Value normalization"): booleans become
// "true" and "false", integers their decimal form, a JSON null counts as absent (ok is false), an
// array is multi-valued and contributes each scalar element (elements that are objects or arrays
// never do), and an object is reachable only through a path of member names. Strings are returned
// byte for byte.
//
// A number that is not an integer contributes no value (Q-CI-28), and neither does an object: the
// claim is present (ok is true), so `present: true` holds, but no comparison matches it.
//
// The first argument is a top-level claim name; a non-empty path (the second) descends through nested objects from it
// (`["job_config","url"]`, dots in names are never interpreted). A path through a value that is not
// an object, or to a member that is missing or null, is absent.
func (c Claims) Normalized(name string, path []string) (values []string, ok bool) {
	v, ok := c.lookup(name, path)
	if !ok {
		return nil, false
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, el := range t {
			if s, ok := scalar(el); ok {
				out = append(out, s)
			}
		}
		return out, true
	case map[string]any:
		return []string{}, true
	}
	if s, ok := scalar(v); ok {
		return []string{s}, true
	}
	return []string{}, true
}

// lookup returns the raw value a claim and a path address; ok is false when it is absent or null.
func (c Claims) lookup(name string, path []string) (any, bool) {
	v, ok := c[name]
	if !ok || v == nil {
		return nil, false
	}
	for _, p := range path {
		m, isObject := v.(map[string]any)
		if !isObject {
			return nil, false
		}
		if v, ok = m[p]; !ok || v == nil {
			return nil, false
		}
	}
	return v, true
}

// scalar normalizes one JSON scalar: a string as it is, a boolean as "true" or "false", an integer
// as its decimal form. Every other value (null, a non-integer number, an object, an array) has no
// normalized form.
func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case json.Number:
		return integerText(string(t))
	case float64:
		// A claim set decoded without UseNumber: an integral value that a float64 holds exactly.
		if t != math.Trunc(t) || math.IsInf(t, 0) || math.Abs(t) > 1<<53 {
			return "", false
		}
		return strconv.FormatInt(int64(t), 10), true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case int32:
		return strconv.FormatInt(int64(t), 10), true
	case uint64:
		return strconv.FormatUint(t, 10), true
	}
	return "", false
}

// integerText returns the decimal form of a JSON number text that is an integer literal
// (`-?(0|[1-9][0-9]*)`, RFC 8259 §6 without fraction and exponent). The JSON grammar already makes
// that text canonical; only "-0" becomes "0". A fraction or an exponent is not an integer (Q-CI-28).
func integerText(s string) (string, bool) {
	digits := s
	if len(digits) > 0 && digits[0] == '-' {
		digits = digits[1:]
	}
	if digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return "", false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false
		}
	}
	if digits == "0" {
		return "0", true
	}
	return s, true
}

// First is the first normalized value of a top-level claim, or "" and false when the claim is
// absent, null, an object or an empty array. The exchange reads project_id, job_id, ref and the other
// scalar claims of the issuance record through it.
func (c Claims) First(name string) (string, bool) {
	vals, ok := c.Normalized(name, nil)
	if !ok || len(vals) == 0 {
		return "", false
	}
	return vals[0], true
}

// Cross-project classification (R-CI-012): the claims that name a project.
const (
	ClaimProjectID       = "project_id"
	ClaimJobProjectID    = "job_project_id"
	ClaimSourceProjectID = "source_project_id"
)

// CrossProject classifies a subject token before mapping evaluation: true when two of project_id,
// job_project_id and source_project_id are both present and differ (R-CI-012). The opt-in is decided
// per matched mapping afterwards, by that mapping's options.
//
// A present project claim that is not one scalar (an array, an object, a non-integer number) cannot
// be compared and classifies the token as cross-project: the default of a cross-project token is
// deny, so the odd shape fails closed.
func (c Claims) CrossProject() bool {
	var seen []string
	for _, name := range []string{ClaimProjectID, ClaimJobProjectID, ClaimSourceProjectID} {
		v, ok := c.lookup(name, nil)
		if !ok {
			continue
		}
		s, isScalar := scalar(v)
		if !isScalar {
			return true
		}
		seen = append(seen, s)
	}
	for _, s := range seen[min(1, len(seen)):] {
		if s != seen[0] {
			return true
		}
	}
	return false
}

// snapshotClaims is the allowlist of the issuance record (R-CI-020): the claims of the preset table
// of R-CI-006 and sub. user_email and user_identities are not in it.
var snapshotClaims = [...]string{
	"iss", "aud", "exp", "iat", "nbf", "jti", "sub",
	"project_id", "project_path", "namespace_id", "namespace_path",
	"job_project_id", "job_project_path", "job_namespace_id", "job_namespace_path",
	"source_project_id", "source_project_path",
	"ref", "ref_type", "ref_path", "ref_protected",
	"pipeline_source", "pipeline_id", "job_id",
	"environment", "environment_protected", "deployment_tier", "environment_action",
	"user_id", "user_login",
	"sha", "runner_id", "runner_environment", "project_visibility",
}

// Snapshot returns the allowlisted claim snapshot an issuance record stores (R-CI-020): the claims
// of the preset table of R-CI-006 plus sub, without user_email and user_identities, each value as
// the token carried it. A claim outside the allowlist is dropped, so the record cannot grow without
// bound from a hostile token.
func (c Claims) Snapshot() map[string]any {
	out := make(map[string]any, len(snapshotClaims))
	for _, name := range snapshotClaims {
		if v, ok := c[name]; ok {
			out[name] = v
		}
	}
	return out
}
