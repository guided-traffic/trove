package claims

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

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
	// Name is the mapping's name; a project mapping's starts with "<project>-" and goes on after it
	// (Q-CI-26). The name's own syntax is the service's and the schema's.
	Name    string
	Match   Node
	Grants  Spec
	Options Options
	// SubjectKinds is the list of the mapping; the MVP accepts only id_token (R-CI-022 adds
	// job_token, P2: 422 job_token_fallback_not_available). Empty means the default, id_token.
	SubjectKinds []string
	// PolicyActions are the actions that the policies and groups of Grants give the mapping, which
	// only the mapping service can read (from the policy store) before it calls Check. The rules
	// unprotected_write and unpinned_write count writes granted through policies and groups
	// (R-CI-010, R-CI-011), and `manage` among them is action_not_allowed (R-AUTHZ-014). Nil when the
	// grants list no policy and no group.
	PolicyActions []Action
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

// maxPatterns is the limit of a pattern list of R-AUTHZ-005.
const maxPatterns = 256

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
//
// The order of the refusals: the subject kinds, the project's name prefix, the condition
// (catch_all_condition, condition_invalid), the grants (empty_grant, groups_not_allowed_in_project,
// action_not_allowed, the shape and templates of each inline grant), the options, then pin_required,
// unprotected_write and unpinned_write. Every pointer is relative to the mapping's body ("/match/all/1",
// "/grants/inline/0/packages/include/1").
func Check(f IssuerFacts, in SaveInput, l Limits) (Checked, []Warning, error) {
	if err := checkSubjectKinds(in.SubjectKinds); err != nil {
		return Checked{}, nil, err
	}
	if err := CheckProjectName(f.Project, in.Name); err != nil {
		return Checked{}, nil, err
	}
	if err := in.Match.Validate(l); err != nil {
		return Checked{}, nil, under("/match", err)
	}
	out := Checked{Templates: map[string]*Template{}}
	var err error
	if out.Grants, err = checkGrants(f, in, out.Templates); err != nil {
		return Checked{}, nil, err
	}
	if out.Options, err = checkOptions(in.Options); err != nil {
		return Checked{}, nil, err
	}
	if err := checkWriteRules(f, in, out.Grants); err != nil {
		return Checked{}, nil, err
	}
	return out, warnings(f, in, out), nil
}

// under prefixes the pointer of an *Error with the member that holds the checked value.
func under(prefix string, err error) error {
	var e *Error
	if errors.As(err, &e) {
		c := *e
		c.Pointer = prefix + c.Pointer
		return &c
	}
	return err
}

func checkSubjectKinds(kinds []string) error {
	for i, k := range kinds {
		if k != "id_token" {
			return refuse(reason.JobTokenFallbackNotAvailable, "/subjectKinds/"+strconv.Itoa(i),
				"the subject kind "+strconv.Quote(k)+" is not available; mappings in this release accept id_token only (the job-token fallback is P2)")
		}
	}
	return nil
}

// CheckProjectName is the name rule of a project's mapping (Q-CI-26): "<project>-" and more; "" is the system tier,
// which has none. The mapping service applies it before it looks the name up, so a taken name answers 409 only for a
// name under the caller's own prefix (H-413, Q-CI-51).
func CheckProjectName(project, name string) error {
	if project == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(name, project+"-"); !ok || rest == "" {
		return refuse(reason.ProjectPrefixRequired, "/name", "the name of a mapping of the project "+project+" starts with "+project+"- and goes on after it")
	}
	return nil
}

// checkGrants checks the grant specification and returns it with the resolved ownership of every
// inline grant; it fills templates with every parsed pattern.
func checkGrants(f IssuerFacts, in SaveInput, templates map[string]*Template) (Spec, error) {
	g := in.Grants
	if g.Empty() {
		return Spec{}, refuse(reason.EmptyGrant, "/grants", "the mapping grants nothing: list a policy, a group or an inline grant")
	}
	if f.Project != "" && len(g.Groups) > 0 {
		return Spec{}, refuse(reason.GroupsNotAllowedInProject, "/grants/groups", "a project's mapping grants through the project's policies and inline grants; groups are global grants")
	}
	for _, a := range in.PolicyActions {
		if !a.Valid() {
			return Spec{}, refuse(reason.ActionNotAllowed, "/grants/policies", "a listed policy or group grants "+strconv.Quote(string(a))+", which a CI token never holds")
		}
	}
	out := Spec{Policies: slices.Clone(g.Policies), Groups: slices.Clone(g.Groups)}
	for i, ig := range g.Inline {
		resolved, err := checkInline(ig, "/grants/inline/"+strconv.Itoa(i), templates)
		if err != nil {
			return Spec{}, err
		}
		out.Inline = append(out.Inline, resolved)
	}
	return out, nil
}

func checkInline(g InlineGrant, ptr string, templates map[string]*Template) (InlineGrant, error) {
	if len(g.Repositories) == 0 {
		return InlineGrant{}, refuse(reason.EmptyGrant, ptr+"/repositories", "an inline grant names at least one repository selector")
	}
	if len(g.Actions) == 0 {
		return InlineGrant{}, refuse(reason.EmptyGrant, ptr+"/actions", "an inline grant names at least one action")
	}
	for i, a := range g.Actions {
		if !a.Valid() {
			return InlineGrant{}, refuse(reason.ActionNotAllowed, ptr+"/actions/"+strconv.Itoa(i),
				"the action "+strconv.Quote(string(a))+" is not grantable to a CI token (read, annotate, deploy, overwrite, delete; never manage)")
		}
	}
	if (g.Packages == nil) == (g.Paths == nil) {
		return InlineGrant{}, refuse(reason.InvalidValue, ptr, "an inline grant names exactly one of packages and paths")
	}
	switch g.Ownership {
	case "", OwnershipBindProject, OwnershipNone:
	default:
		return InlineGrant{}, refuse(reason.InvalidValue, ptr+"/ownership", "ownership is bind_project or none")
	}
	if err := checkRepositories(g.Repositories, ptr+"/repositories", templates); err != nil {
		return InlineGrant{}, err
	}
	if g.Packages != nil {
		if err := checkPatternSet(*g.Packages, ptr+"/packages", templates); err != nil {
			return InlineGrant{}, err
		}
	}
	if g.Paths != nil {
		if err := checkPatternSet(*g.Paths, ptr+"/paths", templates); err != nil {
			return InlineGrant{}, err
		}
	}
	out := g.clone()
	out.Ownership = g.ResolvedOwnership()
	return out, nil
}

func checkRepositories(list []string, ptr string, templates map[string]*Template) error {
	if len(list) > maxPatterns {
		return refuse(reason.InvalidPattern, ptr, "a selector list holds at most 256 selectors")
	}
	for i, src := range list {
		p := ptr + "/" + strconv.Itoa(i)
		t, err := ParseTemplate(src)
		if err != nil {
			return under(p, err)
		}
		templates[p] = t
		switch {
		case t.IsLiteral():
			if _, err := parseSelector(src); err != nil {
				return refuse(reason.InvalidPattern, p, "invalid repository selector: "+err.Error())
			}
		case t.classLike():
			return refuse(reason.TemplateInvalid, p, "a template never renders a class selector (@…); write the class without a substitution")
		case !t.Anchored():
			return refuse(reason.TemplateUnanchored, p, "a repositories template needs literal text other than * and ? next to every claim substitution, for example {{ … }}-releases, so a claim value alone cannot select a repository")
		}
	}
	return nil
}

func checkPatternSet(set PatternSet, ptr string, templates map[string]*Template) error {
	for _, part := range []struct {
		name string
		list []string
	}{{"include", set.Include}, {"exclude", set.Exclude}} {
		if len(part.list) > maxPatterns {
			return refuse(reason.InvalidPattern, ptr+"/"+part.name, "a pattern list holds at most 256 patterns")
		}
		for i, src := range part.list {
			p := ptr + "/" + part.name + "/" + strconv.Itoa(i)
			t, err := ParseTemplate(src)
			if err != nil {
				return under(p, err)
			}
			templates[p] = t
			if t.IsLiteral() && (src == "" || utf8.RuneCountInString(src) > MaxRenderedLength) {
				return refuse(reason.InvalidPattern, p, "a pattern has 1 to 1024 characters")
			}
		}
	}
	return nil
}

func checkOptions(o Options) (Options, error) {
	switch o.CrossProjectSource {
	case "":
		o.CrossProjectSource = CrossProjectDeny
	case CrossProjectDeny, CrossProjectReadOnly:
	default:
		return Options{}, refuse(reason.InvalidValue, "/options/crossProjectSource", "crossProjectSource is deny or read_only")
	}
	return o, nil
}

// writeFacts are the writes of a mapping as the rules see them.
type writeFacts struct {
	// policy: a policy or group grants deploy, overwrite or delete.
	policy bool
	// inline: an inline grant does; unbound: one of those is not bind_project.
	inline, unbound bool
}

func writesOf(in SaveInput, grants Spec) writeFacts {
	var w writeFacts
	w.policy = slices.ContainsFunc(in.PolicyActions, Action.Writes)
	for _, g := range grants.Inline {
		if g.Writes() {
			w.inline = true
			w.unbound = w.unbound || g.ResolvedOwnership() != OwnershipBindProject
		}
	}
	return w
}

func (w writeFacts) any() bool { return w.policy || w.inline }

// checkWriteRules is pin_required (R-CI-005), unprotected_write (R-CI-011) and unpinned_write
// (R-CI-010), in that order.
func checkWriteRules(f IssuerFacts, in SaveInput, grants Spec) error {
	pinned := in.Match.Pins(f.PinClaims)
	w := writesOf(in, grants)
	if f.PublicIssuer && !pinned {
		return refuse(reason.PinRequired, "/match", "the CI issuer is public: the condition needs an equals or in leaf on one of "+strings.Join(f.PinClaims, ", ")+" at the top level of its all")
	}
	if w.any() && !in.Match.RequiresProtectedRef() && !in.Options.AllowUnprotectedRefs {
		return refuse(reason.UnprotectedWrite, "/match", "the mapping grants deploy, overwrite or delete: its top-level all needs {\"claim\": \"ref_protected\", \"equals\": true}, or options.allowUnprotectedRefs")
	}
	if !f.PublicIssuer && !pinned && (w.policy || w.unbound) {
		return refuse(reason.UnpinnedWrite, "/match", "the mapping writes through a policy, a group or an inline grant without ownership bind_project: its condition needs an equals or in leaf on one of "+strings.Join(f.PinClaims, ", ")+" at the top level of its all")
	}
	return nil
}

// PinViolation applies the two rules of a saved mapping that the issuer's pin settings decide: pin_required on a
// public issuer (R-CI-005) and unpinned_write on a private one (R-CI-010). in carries the stored condition, the
// stored grants (their ownership resolved) and the actions its policies and groups give. An issuer change that
// switches public_issuer on or removes a claim from pin_claims is refused while a mapping of the issuer would fail
// one of them (Q-CI-50), so no mapping holds a grant that its save would refuse.
func PinViolation(f IssuerFacts, in SaveInput) error {
	pinned := in.Match.Pins(f.PinClaims)
	w := writesOf(in, in.Grants)
	switch {
	case f.PublicIssuer && !pinned:
		return refuse(reason.PinRequired, "/match", "the condition pins none of "+strings.Join(f.PinClaims, ", "))
	case !f.PublicIssuer && !pinned && (w.policy || w.unbound):
		return refuse(reason.UnpinnedWrite, "/match", "the mapping writes without ownership bind_project and its condition pins none of "+strings.Join(f.PinClaims, ", "))
	}
	return nil
}

// warnings collects the findings of R-CI-010 and R-CI-011 that do not refuse the save, in a fixed
// order: the condition's, the templates', the grants', the options'.
func warnings(f IssuerFacts, in SaveInput, c Checked) []Warning {
	out := []Warning{}
	add := func(code reason.Reason, ptr, detail string) {
		out = append(out, Warning{Code: code, Pointer: ptr, Detail: detail})
	}
	pinned := in.Match.Pins(f.PinClaims)
	if !f.PublicIssuer && !pinned {
		add(reason.WarnUnpinnedCondition, "/match", "the condition pins no immutable ID claim ("+strings.Join(f.PinClaims, ", ")+"); a path claim follows renames and transfers, and a project that inherits a released path matches (H-30)")
	}
	walkLeaves(in.Match, "/match", func(leaf Node, ptr string) {
		switch name := leaf.claimName(); {
		case name == "sub" && (leaf.Glob != nil || leaf.Regex != nil):
			add(reason.WarnSubjectPattern, ptr, "a glob or regex on sub: a project can reconfigure the components of sub; test the separate claims (project_id, ref_type, ref, …)")
		case name == "user_login" || name == "user_email":
			add(reason.WarnMutableUserClaim, ptr, "a condition on "+name+": GitLab users can change it")
		}
	})
	writes := writesOf(in, c.Grants).any()
	if writes && !in.Match.TestsPipelineSource() {
		add(reason.WarnNoPipelineSource, "/match", "a write mapping without a pipeline_source condition: allowlist push, web, schedule; merge_request_event, external_pull_request_event and trigger should not write")
	}
	out = append(out, templateWarnings(f, in, c)...)
	for i, g := range c.Grants.Inline {
		if g.Writes() && in.Grants.Inline[i].Ownership == OwnershipNone {
			add(reason.WarnSharedOwnership, "/grants/inline/"+strconv.Itoa(i)+"/ownership", "a writing grant with ownership none: the mapping owns the names it claims, so every project it matches can publish versions of them")
		}
	}
	if in.Options.AllowUnprotectedRefs {
		add(reason.WarnUnprotectedRefsAllowed, "/options/allowUnprotectedRefs", "the mapping may write from unprotected refs: anyone who can push a branch matching the condition gets its grants")
	}
	if c.Options.CrossProjectSource == CrossProjectReadOnly {
		add(reason.WarnCrossProjectReadOnly, "/options/crossProjectSource", "cross-project (fork) pipelines receive this mapping's grants capped to read; fork code can read everything the token reads")
	}
	return out
}

// templateWarnings are repository_template_unconstrained and unguarded_template_claim, ordered by the
// pointer of the pattern (numbers numerically), so a save answers the same list every time.
func templateWarnings(f IssuerFacts, in SaveInput, c Checked) []Warning {
	ptrs := make([]string, 0, len(c.Templates))
	for p := range c.Templates {
		ptrs = append(ptrs, p)
	}
	slices.SortFunc(ptrs, comparePointers)
	guarded := in.Match.GuardedClaims()
	var out []Warning
	for _, p := range ptrs {
		t := c.Templates[p]
		if strings.Contains(p, "/repositories/") && t.unconstrained() {
			out = append(out, Warning{
				Code: reason.WarnRepositoryTemplateUnconstraint, Pointer: p,
				Detail: "a repositories template without a require filter: every project inside the pin chooses the substituted value through its own path, so it can select any existing repository whose key fits the literal part",
			})
		}
		for _, name := range t.ClaimNames() {
			if !slices.Contains(f.RequiredClaims, name) && !slices.Contains(guarded, name) {
				out = append(out, Warning{
					Code: reason.WarnUnguardedTemplateClaim, Pointer: p,
					Detail: "the template uses the claim " + name + ", which the issuer does not require and no top-level present: true guards; a token without it fails the exchange with grant_render_failed",
				})
			}
		}
	}
	return out
}

// comparePointers orders JSON pointers by their tokens, numbers numerically.
func comparePointers(a, b string) int {
	at, bt := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(at) && i < len(bt); i++ {
		if at[i] == bt[i] {
			continue
		}
		an, errA := strconv.Atoi(at[i])
		bn, errB := strconv.Atoi(bt[i])
		if errA == nil && errB == nil {
			return an - bn
		}
		return strings.Compare(at[i], bt[i])
	}
	return len(at) - len(bt)
}
