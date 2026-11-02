package claims

// Action is an action of an inline grant (R-CI-013). `manage` is never grantable to a CI token and
// has no constant here: a spec that names it is refused with 422 action_not_allowed.
type Action string

// The actions an inline grant may hold.
const (
	Read      Action = "read"
	Annotate  Action = "annotate"
	Deploy    Action = "deploy"
	Overwrite Action = "overwrite"
	Delete    Action = "delete"
)

// Writes reports whether the action changes content (`deploy`, `overwrite`, `delete`): the actions
// the unprotected-write, unpinned-write and ownership rules look at.
func (a Action) Writes() bool { return a == Deploy || a == Overwrite || a == Delete }

// Ownership is the ownership value of an inline grant (R-CI-013, R-CI-015, Q-CI-6).
type Ownership string

// The ownership values. The empty value is a request for the default and never stored: a saved
// mapping and every issuance record carry the resolved value (R-AUTHZ-014).
const (
	// OwnershipBindProject binds the names the grant claims to the GitLab project that first
	// publishes them (owner ci:<issuer>:project:<project_id>).
	OwnershipBindProject Ownership = "bind_project"
	// OwnershipNone makes the mapping the owner of the names it claims (R-AUTHZ-018 rule 2), so
	// every project it matches co-owns them; a save with it on a writing grant warns shared_ownership.
	OwnershipNone Ownership = "none"
)

// PatternSet is an include/exclude pair of package-name or path patterns (R-AUTHZ-005). An empty
// include list is unrestricted. In a mapping the patterns may be templates; in a rendered grant they
// are glob patterns.
type PatternSet struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// InlineGrant is one inline grant (R-CI-013): the same shape as a grant of a permission policy
// (R-AUTHZ-004, R-AUTHZ-005, selectors, exactly one of Packages or Paths, actions) plus Ownership.
// The same type is the mapping's template form (patterns may contain {{ … }}) and the rendered form
// (patterns are glob patterns), so that one matcher evaluates policies, scopes and CI grants.
type InlineGrant struct {
	// Repositories are repository keys or patterns (INV-9); templates render in ContextRepositories.
	Repositories []string `json:"repositories"`
	// Packages and Paths: exactly one is set.
	Packages  *PatternSet `json:"packages,omitempty"`
	Paths     *PatternSet `json:"paths,omitempty"`
	Actions   []Action    `json:"actions"`
	Ownership Ownership   `json:"ownership,omitempty"`
}

// Spec is the grant specification of a mapping (R-CI-013): the three combinable ways. The JSON form
// is the stored and the wire form of ci_mappings.grants.
type Spec struct {
	// Policies are names of permission policies; the mapping becomes a principal of each. The
	// binding is stored once, in the policy's principal list (R-CI-013), and this field reads and
	// writes that same binding.
	Policies []string `json:"policies,omitempty"`
	// Groups are trove groups whose grants the token also carries.
	Groups []string `json:"groups,omitempty"`
	// Inline are the grants defined in the mapping, rendered once at exchange time.
	Inline []InlineGrant `json:"inline,omitempty"`
}

// Empty reports whether the spec grants nothing (422 empty_grant).
func (s Spec) Empty() bool { return len(s.Policies) == 0 && len(s.Groups) == 0 && len(s.Inline) == 0 }

// Actions returns every action of the inline grants.
func (s Spec) Actions() []Action {
	var out []Action
	for _, g := range s.Inline {
		out = append(out, g.Actions...)
	}
	return out
}

// Render renders the inline grants for the claims (R-CI-014): repositories in ContextRepositories,
// packages in ContextPackages and paths in ContextPaths. Policies and groups pass through. A render
// error fails the whole exchange (grant_render_failed); evaluation never falls through to another
// mapping (R-CI-009). The result carries the resolved ownership of every grant.
func (s Spec) Render(_ Claims) (Rendered, error) {
	panic("not implemented: W2")
}

// Rendered is what one matched mapping contributes to a token before narrowing: the policy and group
// names and the rendered inline grants. It is frozen into the issuance record and into the token
// ([Frozen]).
type Rendered struct {
	Policies []string      `json:"policies,omitempty"`
	Groups   []string      `json:"groups,omitempty"`
	Inline   []InlineGrant `json:"inline,omitempty"`
}

// Cap bounds a token's grants at use time (R-CI-012): a cap `read` removes every non-read action of
// the token's policy, group and inline grants. No other cap exists in the MVP.
type Cap string

// CapRead is the cap of a cross-project token whose mapping opted in with read_only.
const CapRead Cap = "read"

// Options are the options of a mapping (R-CI-009, R-CI-011, R-CI-012). The JSON form is the stored
// and the wire form of ci_mappings.options.
type Options struct {
	// CrossProjectSource is CrossProjectDeny (the default) or CrossProjectReadOnly.
	CrossProjectSource   string `json:"crossProjectSource,omitempty"`
	AllowUnprotectedRefs bool   `json:"allowUnprotectedRefs,omitempty"`
}

// The values of Options.CrossProjectSource.
const (
	CrossProjectDeny     = "deny"
	CrossProjectReadOnly = "read_only"
)

// Frozen is what a CI token carries to its use (R-AUTHZ-014, Q-CI-17): the identity, the mappings and
// groups that make its principal set, the frozen inline grants and the caps. The exchange writes it as
// api_token.ci_context in the transaction that inserts the token; the token cache entry carries it
// ([internal/auth/token.Info.CI]), so the evaluator reads it without a query (R-AUTHZ-015). The JSON
// form is the stored form of that column.
type Frozen struct {
	// Issuer is the CI issuer name and IssuerID its ID; both are immutable.
	Issuer   string `json:"issuer"`
	IssuerID string `json:"issuerId"`
	// ProjectID and ProjectPath are the verified project claims (ProjectPath is a display value);
	// with the issuer they give the owner identity of R-CI-015.
	ProjectID   string `json:"projectId"`
	ProjectPath string `json:"projectPath,omitempty"`
	// Mappings are the references "ci:<issuer>/<mapping>" of the matched mappings that contribute (a
	// cross-project mapping that denies contributes nothing, R-CI-012). MappingIDs are their IDs.
	Mappings   []string `json:"mappings"`
	MappingIDs []string `json:"mappingIds"`
	// Groups are the group names the contributing mappings list, evaluated at use time.
	Groups []string `json:"groups,omitempty"`
	// Inline are the rendered inline grants, narrowed by the request's scope; a later edit of the
	// mapping does not change them.
	Inline []InlineGrant `json:"inline,omitempty"`
	// Caps bound policy, group and inline grants; empty means none.
	Caps []Cap `json:"caps,omitempty"`
	// CrossProject records the classification of R-CI-012.
	CrossProject bool `json:"crossProject,omitempty"`
	// Project is the key of the project whose mapping contributed, "" for a system-tier mapping. The
	// evaluator compiles the inline grants of a project mapping only against that project's repositories,
	// so `@any` means "every repository of the project" (Q-CI-26, Q-AUTHZ-179).
	Project string `json:"project,omitempty"`
}

// OwnerRef is the owner identity of the token's project, "ci:<issuer>:project:<project_id>"
// (R-CI-015, R-AUTHZ-018): the issuer by name, as the API refers to it. The ownership store keeps the
// issuer ID.
func (f Frozen) OwnerRef() string { return "ci:" + f.Issuer + ":project:" + f.ProjectID }

// HasCap reports whether the token carries the cap.
func (f Frozen) HasCap(c Cap) bool {
	for _, have := range f.Caps {
		if have == c {
			return true
		}
	}
	return false
}

// MappingRef is the reference of a mapping principal, "ci:<issuer>/<mapping>" (R-CI-009).
func MappingRef(issuer, mapping string) string { return "ci:" + issuer + "/" + mapping }
