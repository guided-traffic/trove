package claims

import "github.com/guided-traffic/trove/internal/ci/reason"

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
// The first argument is a top-level claim name; a non-empty path (the second) descends through nested objects from it
// (`["job_config","url"]`, dots in names are never interpreted).
func (c Claims) Normalized(_ string, _ []string) (values []string, ok bool) {
	panic("not implemented: W2")
}

// First is the first normalized value of a top-level claim, or "" and false when the claim is
// absent, null, an object or an empty array. The exchange reads project_id, job_id, ref and the other
// scalar claims of the issuance record through it.
func (c Claims) First(_ string) (string, bool) {
	panic("not implemented: W2")
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
func (c Claims) CrossProject() bool {
	panic("not implemented: W2")
}

// Snapshot returns the allowlisted claim snapshot an issuance record stores (R-CI-020): the claims
// of the preset table of R-CI-006 plus sub, without user_email and user_identities, each value as
// the token carried it. A claim outside the allowlist is dropped, so the record cannot grow without
// bound from a hostile token.
func (c Claims) Snapshot() map[string]any {
	panic("not implemented: W2")
}
