package claims

import "github.com/guided-traffic/trove/internal/ci/reason"

// Warning is a finding of a save that does not refuse it (R-CI-010, R-CI-011): the save succeeds and
// the response carries it in `warnings`.
type Warning struct {
	Code    reason.Reason `json:"code"`
	Pointer string        `json:"pointer,omitempty"`
	Detail  string        `json:"detail"`
}

// IssuerFacts are the settings of the mapping's CI issuer that the save-time rules read.
type IssuerFacts struct {
	// Kind is gitlab, github or generic.
	Kind string
	// PublicIssuer turns on the ID-pinning rule (R-CI-005).
	PublicIssuer bool
	// PinClaims are the claims that count as immutable identifiers.
	PinClaims []string
	// RequiredClaims are the claims every token carries (templates may use them unguarded).
	RequiredClaims []string
	// Project is the key of the project a project administrator saves the mapping for, "" for the
	// system tier. In a project every rule applies unchanged, `groups` is refused
	// (groups_not_allowed_in_project) and the name must start with "<Project>-" (project_prefix_required).
	// Whether the policies and repositories are the project's needs the database and is the service's.
	Project string
}

// SaveInput is a mapping as submitted, before any storage.
type SaveInput struct {
	Match   Node
	Grants  Spec
	Options Options
	// SubjectKinds is the list of the mapping; the MVP accepts only id_token (R-CI-022 adds
	// job_token, P2: 422 job_token_fallback_not_available).
	SubjectKinds []string
}

// Checked is a mapping that passed the save-time rules: Grants carries the resolved ownership of every
// inline grant (R-CI-013, R-AUTHZ-014) and Options the resolved cross_project_source; the stored and
// the returned record show these.
type Checked struct {
	Grants  Spec
	Options Options
	// Templates are the parsed templates by JSON pointer, so the save does not parse twice.
	Templates map[string]*Template
}

// Check applies every save-time rule of a mapping that needs no database (R-CI-005, R-CI-010 to
// R-CI-014) and returns the checked mapping and the warnings, or the first *Error: condition limits
// and syntax (condition_invalid), catch_all_condition, pin_required, unprotected_write (unless
// Options.AllowUnprotectedRefs), unpinned_write, action_not_allowed (`manage` or an unknown action),
// empty_grant, template_invalid, template_unanchored, and job_token_fallback_not_available.
//
// Rules that need the database (the policy and group names exist, the issuer's mapping count, the
// priority) are the mapping service's (internal/ci Mappings), after this check.
//
// The warnings are the list of R-CI-010 and R-CI-011: unpinned_condition, repository_template_unconstrained,
// shared_ownership, a glob or regex on `sub`, a condition on user_login or user_email, a template claim
// that is neither required nor guarded, a write mapping without a pipeline_source condition, and the
// visible opt-ins (unprotected refs, cross-project read_only).
func Check(_ IssuerFacts, _ SaveInput, _ Limits) (Checked, []Warning, error) {
	panic("not implemented: W2")
}
