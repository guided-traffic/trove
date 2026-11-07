package claims

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

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

// Valid reports whether the action may be granted to a CI token: one of the five above.
func (a Action) Valid() bool {
	switch a {
	case Read, Annotate, Deploy, Overwrite, Delete:
		return true
	}
	return false
}

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

// Writes reports whether the grant holds deploy, overwrite or delete.
func (g InlineGrant) Writes() bool { return slices.ContainsFunc(g.Actions, Action.Writes) }

// ResolvedOwnership is the ownership the grant has: its own value, or the default of R-CI-013 and
// R-AUTHZ-014 when it names none (bind_project for a writing grant, none otherwise).
func (g InlineGrant) ResolvedOwnership() Ownership {
	switch {
	case g.Ownership != "":
		return g.Ownership
	case g.Writes():
		return OwnershipBindProject
	}
	return OwnershipNone
}

func (g InlineGrant) clone() InlineGrant {
	out := InlineGrant{
		Repositories: slices.Clone(g.Repositories),
		Actions:      slices.Clone(g.Actions),
		Ownership:    g.Ownership,
	}
	if g.Packages != nil {
		out.Packages = &PatternSet{Include: slices.Clone(g.Packages.Include), Exclude: slices.Clone(g.Packages.Exclude)}
	}
	if g.Paths != nil {
		out.Paths = &PatternSet{Include: slices.Clone(g.Paths.Include), Exclude: slices.Clone(g.Paths.Exclude)}
	}
	return out
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
//
// The error is an *Error whose Pointer is relative to the grants ("/inline/0/packages/include/1")
// and whose Detail names the claim, never its value.
func (s Spec) Render(c Claims) (Rendered, error) {
	out := Rendered{Policies: slices.Clone(s.Policies), Groups: slices.Clone(s.Groups)}
	for i, g := range s.Inline {
		ptr := "/inline/" + strconv.Itoa(i)
		rg := InlineGrant{Actions: slices.Clone(g.Actions), Ownership: g.ResolvedOwnership()}
		var err error
		if rg.Repositories, err = renderList(g.Repositories, c, ContextRepositories, ptr+"/repositories"); err != nil {
			return Rendered{}, err
		}
		if rg.Packages, err = renderSet(g.Packages, c, ContextPackages, ptr+"/packages"); err != nil {
			return Rendered{}, err
		}
		if rg.Paths, err = renderSet(g.Paths, c, ContextPaths, ptr+"/paths"); err != nil {
			return Rendered{}, err
		}
		out.Inline = append(out.Inline, rg)
	}
	return out, nil
}

func renderSet(set *PatternSet, c Claims, ctx Context, ptr string) (*PatternSet, error) {
	if set == nil {
		return nil, nil
	}
	inc, err := renderList(set.Include, c, ctx, ptr+"/include")
	if err != nil {
		return nil, err
	}
	exc, err := renderList(set.Exclude, c, ctx, ptr+"/exclude")
	if err != nil {
		return nil, err
	}
	return &PatternSet{Include: inc, Exclude: exc}, nil
}

func renderList(list []string, c Claims, ctx Context, ptr string) ([]string, error) {
	if list == nil {
		return nil, nil
	}
	out := make([]string, 0, len(list))
	for i, src := range list {
		p := ptr + "/" + strconv.Itoa(i)
		t, err := ParseTemplate(src)
		if err != nil {
			return nil, renderErrorAt(err, p)
		}
		v, err := t.Render(c, ctx)
		if err != nil {
			return nil, renderErrorAt(err, p)
		}
		out = append(out, v)
	}
	return out, nil
}

// renderErrorAt turns any error of a template into grant_render_failed at the pattern's pointer.
func renderErrorAt(err error, ptr string) *Error {
	var e *Error
	detail := err.Error()
	if errors.As(err, &e) {
		detail = e.Detail
	}
	return &Error{Reason: reason.GrantRenderFailed, Pointer: ptr, Detail: detail}
}

// Rendered is what one matched mapping contributes to a token before narrowing: the policy and group
// names and the rendered inline grants. It is frozen into the issuance record and into the token
// ([Frozen], one [FrozenMapping] per contributing mapping).
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

// ApplyCaps returns the rendered grants bounded by the caps: under CapRead every inline grant keeps
// its read action only, and a grant left without an action is dropped. Policies and groups pass
// through; their caps apply at use time, because their actions are not known here.
func ApplyCaps(r Rendered, caps []Cap) Rendered {
	if !slices.Contains(caps, CapRead) {
		return r
	}
	out := Rendered{Policies: r.Policies, Groups: r.Groups}
	for _, g := range r.Inline {
		if !slices.Contains(g.Actions, Read) {
			continue
		}
		c := g.clone()
		c.Actions = []Action{Read}
		out.Inline = append(out.Inline, c)
	}
	return out
}

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

// FrozenVersion is the version of the JSON form of [Frozen] that this code writes. Version 2 (Q-CI-47) added
// the per-contribution expiry (FrozenMapping.ExpiresAt) and the authorization epoch of the exchange
// (Frozen.AuthzEpoch). [DecodeFrozen] still reads version 1, the form of the tokens issued before, which
// carries neither: its contributions end with the token, whose lifetime was the minimum over every contributing
// mapping, and its contributions are decided against every index. A bundle of any other version is a storage
// fault, never an acceptance.
const FrozenVersion = 2

// FrozenVersion1 is the version of the bundles written before Q-CI-47; DecodeFrozen still reads it.
const FrozenVersion1 = 1

// Frozen is what a CI token carries to its use (R-AUTHZ-014, Q-CI-17): the identity, the mappings that
// make its principal set and, per contributing mapping, its tier, its groups, its frozen inline
// grants and its caps. The exchange writes it as api_token.ci_context in the transaction that
// inserts the token; the token cache entry carries it ([internal/auth/token.Info.CI]), so the
// evaluator reads it without a query (R-AUTHZ-015). The JSON form is the stored form of that column
// and of the issuance record's grants.
//
// A token unites at most one match per tier (R-CI-009): the system tier and any number of project
// tiers. Everything that differs between them is per mapping ([FrozenMapping]), so the evaluator
// bounds each mapping's inline grants by its own tier's project and each mapping's caps bound only
// that mapping's grants (R-CI-012).
type Frozen struct {
	// Version is FrozenVersion.
	Version int `json:"version"`
	// Issuer is the CI issuer name and IssuerID its ID; both are immutable.
	Issuer   string `json:"issuer"`
	IssuerID string `json:"issuerId"`
	// ProjectID and ProjectPath are the verified GitLab project claims (ProjectPath is a display
	// value); with the issuer they give the owner identity of R-CI-015. They are not a trove project.
	ProjectID   string `json:"projectId"`
	ProjectPath string `json:"projectPath,omitempty"`
	// Mappings are the references "ci:<issuer>/<mapping>" of the matched mappings that contribute (a
	// cross-project mapping that denies contributes nothing, R-CI-012), in the order of
	// Contributions; MappingIDs are their IDs. They are the token's principal set.
	Mappings   []string `json:"mappings"`
	MappingIDs []string `json:"mappingIds"`
	// Contributions are the contributing mappings, one per tier that matched, in evaluation order.
	Contributions []FrozenMapping `json:"contributions"`
	// CrossProject records the classification of R-CI-012.
	CrossProject bool `json:"crossProject,omitempty"`
	// AuthzEpoch is the authorization epoch the exchange read in its transaction (version 2, Q-CI-47). Every
	// write that creates, enables, disables or deletes a mapping notifies the change area authz, so a grant
	// index whose epoch is at least this one knows the state of every contributing mapping: the evaluator drops
	// a contribution whose mapping such an index does not hold as active. An older index cannot know a mapping
	// created just before the exchange, so it keeps every contribution. 0 (version 1) is older than no index.
	AuthzEpoch int64 `json:"authzEpoch,omitempty"`
}

// FrozenMapping is what one contributing mapping gives a token: the evaluator applies its Caps to the
// policies naming Ref, to the groups in Groups and to Inline, and compiles Inline against the
// repositories of Project when the tier is a project's.
type FrozenMapping struct {
	// Ref is "ci:<issuer>/<mapping>" and ID the mapping's ID.
	Ref string `json:"ref"`
	ID  string `json:"id"`
	// Tier is "system" or "project:<key>" (Q-CI-26).
	Tier string `json:"tier"`
	// Project is the trove project of a project tier, nil for the system tier. The evaluator compiles
	// Inline only against that project's repositories, so `@any` means "every repository of the
	// project" (Q-CI-26, Q-AUTHZ-179).
	Project *FrozenProject `json:"project,omitempty"`
	// Groups are the group names the mapping lists, evaluated at use time.
	Groups []string `json:"groups,omitempty"`
	// Inline are the mapping's rendered inline grants, narrowed by the request's scope; a later edit
	// of the mapping does not change them.
	Inline []InlineGrant `json:"inline,omitempty"`
	// Caps bound this mapping's policy, group and inline grants; empty means none.
	Caps []Cap `json:"caps,omitempty"`
	// ExpiresAt ends this contribution before the token (version 2, Q-CI-47): a mapping's maxTtlSeconds bounds
	// what that mapping gives, never the token of another tier's mapping (R-CI-016). nil: the contribution lives
	// as long as the token. The evaluator ignores the contribution from this instant on.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// FrozenProject names the trove project of a project-tier mapping by key and ID; the ID survives a
// rename of the key.
type FrozenProject struct {
	Key string `json:"key"`
	ID  string `json:"id"`
}

// HasCap reports whether the mapping's grants carry the cap.
func (m FrozenMapping) HasCap(c Cap) bool { return slices.Contains(m.Caps, c) }

// ExpiredAt reports whether the contribution has ended at now (Q-CI-47); one without an expiry of its own ends
// with the token.
func (m FrozenMapping) ExpiredAt(now time.Time) bool {
	return m.ExpiresAt != nil && !now.Before(*m.ExpiresAt)
}

// Add appends a contributing mapping and keeps Mappings and MappingIDs in step with it.
func (f *Frozen) Add(m FrozenMapping) {
	f.Contributions = append(f.Contributions, m)
	f.Mappings = append(f.Mappings, m.Ref)
	f.MappingIDs = append(f.MappingIDs, m.ID)
}

// Groups returns the group names of every contributing mapping, each once, in order of first use.
// A group listed by a capped and an uncapped mapping is granted uncapped through the latter: the
// evaluator asks per mapping, this list is for display and the issuance record.
func (f Frozen) Groups() []string {
	var out []string
	for _, m := range f.Contributions {
		for _, g := range m.Groups {
			if !slices.Contains(out, g) {
				out = append(out, g)
			}
		}
	}
	return out
}

// Check reports why a bundle must not be used: a version other than FrozenVersion and FrozenVersion1, a
// version-1 bundle with a member of version 2, a negative epoch, no issuer, no contribution, Mappings and
// MappingIDs out of step with Contributions, a contribution without ref or tier, a project tier without its
// project or with another project's key, a system tier with a project.
func (f Frozen) Check() error {
	switch {
	case f.Version != FrozenVersion && f.Version != FrozenVersion1:
		return errors.New("claims: frozen bundle of version " + strconv.Itoa(f.Version) + ", this code reads " +
			strconv.Itoa(FrozenVersion1) + " and " + strconv.Itoa(FrozenVersion))
	case f.Version == FrozenVersion1 && (f.AuthzEpoch != 0 || slices.ContainsFunc(f.Contributions, func(m FrozenMapping) bool { return m.ExpiresAt != nil })):
		return errors.New("claims: frozen bundle of version 1 with a member of version 2")
	case f.AuthzEpoch < 0:
		return errors.New("claims: frozen bundle with a negative authorization epoch")
	case f.Issuer == "" || f.IssuerID == "":
		return errors.New("claims: frozen bundle without issuer")
	case len(f.Contributions) == 0:
		return errors.New("claims: frozen bundle without a contributing mapping")
	case len(f.Mappings) != len(f.Contributions) || len(f.MappingIDs) != len(f.Contributions):
		return errors.New("claims: frozen bundle whose mappings are out of step with its contributions")
	}
	for i, m := range f.Contributions {
		if m.Ref == "" || m.ID == "" || m.Ref != f.Mappings[i] || m.ID != f.MappingIDs[i] {
			return errors.New("claims: frozen contribution " + strconv.Itoa(i) + " without ref or ID, or out of step with mappings")
		}
		switch key, isProject := strings.CutPrefix(m.Tier, "project:"); {
		case m.Tier == "system":
			if m.Project != nil {
				return errors.New("claims: frozen contribution " + strconv.Itoa(i) + " of the system tier names a project")
			}
		case isProject:
			if m.Project == nil || m.Project.Key != key || m.Project.ID == "" {
				return errors.New("claims: frozen contribution " + strconv.Itoa(i) + " of a project tier without its project")
			}
		default:
			return errors.New("claims: frozen contribution " + strconv.Itoa(i) + " of an unknown tier")
		}
	}
	return nil
}

// DecodeFrozen decodes api_token.ci_context or the grants of an issuance record and checks it
// ([Frozen.Check]): the token validator treats an error as a storage fault, never as an acceptance. It reads
// version 2 and version 1 ([FrozenVersion]); a bundle keeps the version it was written in.
func DecodeFrozen(raw []byte) (*Frozen, error) {
	var f Frozen
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, errors.New("claims: frozen bundle is not JSON of the bundle's shape")
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	return &f, nil
}

// OwnerRef is the owner identity of the token's project, "ci:<issuer>:project:<project_id>"
// (R-CI-015, R-AUTHZ-018): the issuer by name, as the API refers to it. The ownership store keeps the
// issuer ID.
func (f Frozen) OwnerRef() string { return "ci:" + f.Issuer + ":project:" + f.ProjectID }

// MappingRef is the reference of a mapping principal, "ci:<issuer>/<mapping>" for a mapping of the
// system tier and "ci:<issuer>/<project>/<mapping>" for one of a project's tier (R-CI-009,
// Q-AUTHZ-214); project is "" for the system tier.
func MappingRef(issuer, project, mapping string) string {
	if project == "" {
		return "ci:" + issuer + "/" + mapping
	}
	return "ci:" + issuer + "/" + project + "/" + mapping
}
