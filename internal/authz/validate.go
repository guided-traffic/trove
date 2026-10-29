package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// ServiceOptions configure [NewService].
type ServiceOptions struct {
	// DB runs the writes in READ COMMITTED transactions.
	DB meta.Runner
	// Changes notifies the authz change area in the transaction of a write.
	Changes Changes
	// Principals resolves principal references and IDs.
	Principals principal.Store
	// Repositories resolves key selectors and lists the repositories a selector
	// covers.
	Repositories repository.Reader
	// Recorder records the policy and role events in the transaction of the write.
	Recorder audit.Recorder
	// KnownFormat reports whether the server knows a format (422 unknown_format).
	// The composition root passes the format registry (internal/formats), so a
	// selector names only a format that is registered; nil accepts every name,
	// which is for a caller that has no registry (a unit test).
	KnownFormat func(string) bool
}

// Service is the domain API of policies, role bindings and caller checks
// (R-AUTHZ-021 to R-AUTHZ-023). The HTTP operations are thin handlers over it:
// they add pagination, ETags and idempotency and call these functions.
type Service struct {
	o ServiceOptions
}

// NewService creates the service.
func NewService(o ServiceOptions) *Service {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.KnownFormat == nil {
		o.KnownFormat = func(string) bool { return true }
	}
	return &Service{o: o}
}

var policyNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// resolvedEntry is an entry with its principal resolved.
type resolvedEntry struct {
	ref       Ref
	id        string
	actions   ActionSet
	pointer   string
	fromInput Entry
}

// resolvedPolicy is a validated policy ready to be stored.
type resolvedPolicy struct {
	in        PolicyInput
	selectors []selector
	entries   []resolvedEntry
	warnings  []string
	covers    []string
}

func field(pointer, code, detail string) ProblemField {
	return ProblemField{Pointer: pointer, Code: code, Detail: detail}
}

// problemError turns validation problems into the 422 of R-AUTHZ-021: the code
// of the answer is validation_failed whatever the number of problems, and every
// problem with its own code is in errors[] (R-API-009, Q-AUTHZ-60 ff.).
func problemError(ps []ProblemField) error {
	if len(ps) == 0 {
		return nil
	}
	fields := make([]errs.FieldError, len(ps))
	for i, p := range ps {
		fields[i] = errs.FieldError{Pointer: p.Pointer, Code: p.Code, Detail: p.Detail}
	}
	detail := ps[0].Detail
	if len(ps) > 1 {
		detail = fmt.Sprintf("%d fields are invalid.", len(ps))
	}
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(fields...))
}

// Validate checks a policy without storing it and reports what it would cover
// (POST /api/v1/authz/validate-policy).
func (s *Service) Validate(ctx context.Context, in PolicyInput) (Report, error) {
	rp, problems, err := s.resolve(ctx, in)
	if err != nil {
		return Report{}, err
	}
	r := Report{Errors: problems}
	if rp != nil {
		r.Warnings, r.Covers = rp.warnings, rp.covers
	}
	return r, nil
}

// resolve validates the input against R-AUTHZ-021 and resolves selectors and
// principals. Problems of the input are returned as fields; an error is a
// failure of a dependency.
func (s *Service) resolve(ctx context.Context, in PolicyInput) (*resolvedPolicy, []ProblemField, error) {
	var ps []ProblemField
	rp := &resolvedPolicy{in: in}
	if in.ResourceType == "" {
		rp.in.ResourceType = "repository"
	}
	if !policyNameRE.MatchString(in.Name) {
		ps = append(ps, field("/name", "invalid_name", "The name matches ^[a-z0-9][a-z0-9._-]{0,127}$."))
	}
	if utf8.RuneCountInString(in.Description) > 1024 {
		ps = append(ps, field("/description", "invalid_description", "The description has at most 1024 characters."))
	}
	switch rp.in.ResourceType {
	case "repository":
	case "build":
		ps = append(ps, field("/resourceType", "unsupported_resource_type", "Build policies arrive with R-AUTHZ-027."))
	default:
		ps = append(ps, field("/resourceType", "invalid_resource_type", "The resource type is repository."))
	}

	var repos []repository.Repository
	haveRepos := false
	listRepos := func() ([]repository.Repository, error) {
		if !haveRepos {
			var err error
			if repos, err = s.o.Repositories.List(ctx); err != nil {
				return nil, err
			}
			haveRepos = true
		}
		return repos, nil
	}

	ps = append(ps, s.resolveSelectors(ctx, rp)...)
	for _, e := range []struct {
		pointer string
		dim     dimension
		list    []string
		what    string
	}{
		{"/resources/packages/include", dimPackage, in.Resources.Packages.Include, "package"},
		{"/resources/packages/exclude", dimPackage, in.Resources.Packages.Exclude, "package"},
		{"/resources/paths/include", dimPath, in.Resources.Paths.Include, "path"},
		{"/resources/paths/exclude", dimPath, in.Resources.Paths.Exclude, "path"},
	} {
		if err := ValidatePatternCount(e.list); err != nil {
			ps = append(ps, field(e.pointer, errs.CodeInvalidPattern, err.Error()))
			continue
		}
		for i, p := range e.list {
			var err error
			if e.dim == dimPackage {
				err = ValidatePackagePattern(p)
			} else {
				err = ValidatePathPattern(p)
			}
			if err != nil {
				ps = append(ps, field(e.pointer+"/"+strconv.Itoa(i), errs.CodeInvalidPattern, err.Error()))
			}
		}
	}

	seen := map[string]bool{}
	for i, e := range in.Entries {
		ptr := "/entries/" + strconv.Itoa(i)
		ref, err := ParseRef(e.Principal)
		if err != nil {
			ps = append(ps, field(ptr+"/principal", errs.CodeUnknownPrincipal, err.Error()))
			continue
		}
		id, perr := s.principalID(ctx, ref)
		if perr != nil {
			if errors.Is(perr, principal.ErrNotFound) {
				ps = append(ps, field(ptr+"/principal", errs.CodeUnknownPrincipal, fmt.Sprintf("No principal %s exists.", e.Principal)))
				continue
			}
			return nil, nil, perr
		}
		if seen[id] {
			ps = append(ps, field(ptr+"/principal", errs.CodeDuplicatePrincipal, "A policy holds at most one entry per principal."))
			continue
		}
		seen[id] = true
		acts, bad := ParseActions(e.Actions)
		for _, b := range bad {
			ps = append(ps, field(ptr+"/actions", errs.CodeInvalidAction, fmt.Sprintf("%q is neither a repository action nor a preset.", b)))
		}
		if len(bad) == 0 && acts == 0 {
			ps = append(ps, field(ptr+"/actions", errs.CodeInvalidAction, "An entry holds at least one action."))
		}
		if len(bad) > 0 || acts == 0 {
			continue
		}
		ps = append(ps, s.entryRules(ptr, ref, acts, rp)...)
		rp.entries = append(rp.entries, resolvedEntry{ref: ref, id: id, actions: acts, pointer: ptr, fromInput: e})
	}
	if len(ps) > 0 {
		return nil, ps, nil
	}

	all, err := listRepos()
	if err != nil {
		return nil, nil, err
	}
	rp.warnings, rp.covers = warningsFor(rp, all)
	return rp, nil, nil
}

// principalID resolves a reference to a principal-table ID; the two built-in
// principals have fixed IDs.
func (s *Service) principalID(ctx context.Context, ref Ref) (string, error) {
	switch ref.Kind {
	case principal.KindAnonymous:
		return AnonymousID, nil
	case principal.KindAuthenticated:
		return AuthenticatedID, nil
	}
	p, err := s.o.Principals.ByName(ctx, ref.Kind, ref.Name)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// resolveSelectors parses the repository selectors and resolves key selectors
// by repository ID.
func (s *Service) resolveSelectors(ctx context.Context, rp *resolvedPolicy) []ProblemField {
	var ps []ProblemField
	for i, text := range rp.in.Resources.Repositories {
		ptr := "/resources/repositories/" + strconv.Itoa(i)
		sel, err := parseSelector(text)
		if err != nil {
			ps = append(ps, field(ptr, errs.CodeInvalidPattern, err.Error()))
			continue
		}
		if sel.format != "" && !s.o.KnownFormat(sel.format) {
			ps = append(ps, field(ptr, errs.CodeUnknownFormat, fmt.Sprintf("The format %q is not known.", sel.format)))
			continue
		}
		if sel.kind == selKey {
			r, err := s.o.Repositories.ByKey(ctx, sel.key)
			if err != nil {
				var ee *errs.Error
				if errors.As(err, &ee) && ee.Kind == errs.NotFound {
					ps = append(ps, field(ptr, errs.CodeUnknownRepository, fmt.Sprintf("No repository %q exists.", sel.key)))
					continue
				}
				ps = append(ps, field(ptr, errs.CodeUnknownRepository, "The repository could not be read."))
				continue
			}
			id, perr := strconv.ParseInt(r.ID, 10, 64)
			if perr != nil {
				ps = append(ps, field(ptr, errs.CodeUnknownRepository, "The repository has no usable identifier."))
				continue
			}
			sel.repoID = id
			sel.class = string(r.Class)
		}
		rp.selectors = append(rp.selectors, sel)
	}
	return ps
}

// entryRules applies the per-principal rules of R-AUTHZ-003, R-AUTHZ-004,
// R-AUTHZ-014 and R-AUTHZ-030 to one entry.
func (s *Service) entryRules(ptr string, ref Ref, acts ActionSet, rp *resolvedPolicy) []ProblemField {
	var ps []ProblemField
	switch ref.Kind {
	case principal.KindAnonymous:
		if acts != bitRead {
			ps = append(ps, field(ptr+"/actions", errs.CodeAnonymousWriteAction, "Entries for anonymous hold only read."))
		}
		for _, sel := range rp.selectors {
			if sel.dynamic() {
				ps = append(ps, field("/resources/repositories", errs.CodeAnonymousDynamicSelector,
					"A policy with an entry for anonymous selects repositories by key only."))
				break
			}
		}
	case principal.KindAuthenticated:
		if acts != bitRead {
			ps = append(ps, field(ptr+"/actions", errs.CodeAuthenticatedWriteAction, "Entries for authenticated hold only read."))
		}
	case principal.KindCI:
		if acts.Has(Manage) {
			ps = append(ps, field(ptr+"/actions", errs.CodeActionNotAllowed, "A CI identity mapping never holds manage."))
		}
	}
	if acts&^virtualActions != 0 {
		for _, sel := range rp.selectors {
			if sel.kind == selKey && sel.class == string(repository.Virtual) {
				ps = append(ps, field(ptr+"/actions", errs.CodeVirtualActionNotApplicable,
					fmt.Sprintf("The repository %q is virtual and accepts only read and manage.", sel.key)))
				break
			}
		}
	}
	return ps
}

// warningsFor computes the warnings of R-AUTHZ-021 and the keys of the
// repositories the selectors currently cover.
func warningsFor(rp *resolvedPolicy, all []repository.Repository) (warnings, covers []string) {
	coveredBy := make([]int, len(rp.selectors))
	var covered []repository.Repository
	for _, r := range all {
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		v := repoView{ID: id, Key: r.Key, Class: r.Class, Format: r.Format}
		hit := false
		for i, sel := range rp.selectors {
			if sel.matches(v) {
				coveredBy[i]++
				hit = true
			}
		}
		if hit {
			covered = append(covered, r)
			covers = append(covers, r.Key)
		}
	}
	sort.Strings(covers)
	for i, sel := range rp.selectors {
		if sel.dynamic() && coveredBy[i] == 0 {
			warnings = append(warnings, WarnSelectorMatchesNothing)
			break
		}
	}
	if len(rp.in.Resources.Paths.Include) > 0 {
		for _, r := range covered {
			if r.Format == "npm" || r.Format == "nuget" {
				warnings = append(warnings, WarnPathPatternsOnPackageFormat)
				break
			}
		}
	}
	for _, e := range rp.entries {
		if e.ref.Kind != principal.KindAnonymous || !e.actions.Has(Read) {
			continue
		}
		for _, r := range covered {
			if r.Class == repository.Remote && cacheFillOf(r.Config) == "read" {
				warnings = append(warnings, WarnAnonymousPopulatesRemote)
				return warnings, covers
			}
		}
	}
	return warnings, covers
}

// cacheFillOf reads the cacheFillAction of a remote repository's document; the
// default is read (R-AUTHZ-008).
func cacheFillOf(cfg []byte) string {
	c, ok := parseRepoConfig(cfg)
	if !ok || c.Remote.CacheFillAction == "" {
		return "read"
	}
	return c.Remote.CacheFillAction
}

// journalSelector, journalEntry and journalState are the access-bearing state of
// a policy as the revocation journal and the audit event carry it: identified by
// ID, with names for display only (R-AUTHZ-031).
type journalSelector struct {
	Kind         string `json:"kind"`
	RepositoryID int64  `json:"repositoryId,omitempty"`
	Key          string `json:"key,omitempty"`
	Pattern      string `json:"pattern,omitempty"`
	Class        string `json:"class,omitempty"`
	Format       string `json:"format,omitempty"`
}

type journalEntry struct {
	PrincipalID string   `json:"principalId"`
	Principal   string   `json:"principal"`
	Actions     []string `json:"actions"`
}

type journalState struct {
	ResourceType string            `json:"resourceType"`
	Selectors    []journalSelector `json:"selectors"`
	Packages     PatternLists      `json:"packages"`
	Paths        PatternLists      `json:"paths"`
	Entries      []journalEntry    `json:"entries"`
}

func stateOf(rp *resolvedPolicy) *journalState {
	st := &journalState{
		ResourceType: rp.in.ResourceType, Packages: normLists(rp.in.Resources.Packages), Paths: normLists(rp.in.Resources.Paths),
		Selectors: []journalSelector{}, Entries: []journalEntry{},
	}
	for _, sel := range rp.selectors {
		st.Selectors = append(st.Selectors, journalSelector{
			Kind: sel.kind, RepositoryID: sel.repoID, Key: sel.key, Pattern: sel.pattern, Class: sel.class, Format: sel.format,
		})
	}
	for _, e := range rp.entries {
		st.Entries = append(st.Entries, journalEntry{PrincipalID: e.id, Principal: e.ref.String(), Actions: e.actions.Strings()})
	}
	sort.Slice(st.Entries, func(i, j int) bool { return st.Entries[i].PrincipalID < st.Entries[j].PrincipalID })
	return st
}

func normLists(l PatternLists) PatternLists {
	out := PatternLists{Include: append([]string{}, l.Include...), Exclude: append([]string{}, l.Exclude...)}
	sort.Strings(out.Include)
	sort.Strings(out.Exclude)
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil { // the types above hold only strings, numbers and slices
		panic(err)
	}
	return b
}
