package authz

import (
	"time"
)

// PatternLists is the include and exclude list of one dimension of a resource
// set (R-AUTHZ-005).
type PatternLists struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// Resources is the resource set of a policy: repository selectors plus optional
// package-name and path patterns (R-AUTHZ-004, R-AUTHZ-005).
type Resources struct {
	Repositories []string     `json:"repositories"`
	Packages     PatternLists `json:"packages"`
	Paths        PatternLists `json:"paths"`
}

// Entry gives actions to a principal. Principal is the API reference
// (user:alice, group:team-web, sa:ci-bot, anonymous, authenticated); Actions are
// the six actions or presets on input and the expanded, alphabetical list on
// output (R-AUTHZ-001).
type Entry struct {
	Principal string   `json:"principal"`
	Actions   []string `json:"actions"`
}

// PolicyInput is what a client writes (R-AUTHZ-021).
type PolicyInput struct {
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	ResourceType string    `json:"resourceType"`
	Resources    Resources `json:"resources"`
	Entries      []Entry   `json:"entries"`
}

// Policy is a stored permission policy as the API returns it.
type Policy struct {
	PolicyInput
	// ID is the immutable identifier; the API addresses the policy by name.
	ID string `json:"-"`
	// Inert is true when no selector is left (a key selector goes with its
	// repository): the policy stays and grants nothing.
	Inert    bool     `json:"inert"`
	Warnings []string `json:"warnings"`
	// Version is the ETag source.
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy"`
	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// Warnings of R-AUTHZ-021; they do not block saving.
const (
	WarnPathPatternsOnPackageFormat = "path_patterns_on_package_format"
	WarnSelectorMatchesNothing      = "selector_matches_nothing"
	WarnAnonymousPopulatesRemote    = "anonymous_populates_remote"
)

// Report is the answer of the validate-policy operation (R-AUTHZ-021): the
// errors and warnings, and the repositories the selectors currently cover.
type Report struct {
	Errors   []ProblemField
	Warnings []string
	Covers   []string
}

// ProblemField is one validation error with the code of R-AUTHZ-021.
type ProblemField struct {
	Pointer string
	Code    string
	Detail  string
}

// Binding is a role bound to a principal (R-AUTHZ-022).
type Binding struct {
	Role      Role
	Principal string
	GrantedAt time.Time
	GrantedBy string
}
