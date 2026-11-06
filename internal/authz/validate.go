package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/guided-traffic/trove/internal/qname"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
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
	// Formats is the format catalog (422 unknown_format for a selector that names
	// a format it does not hold, warnings for a path pattern on a format whose
	// patterns match names). Nil is catalog.Default.
	Formats *catalog.Registry
	// Projects answers the two questions about projects that a policy write asks.
	// Nil reads them from PostgreSQL through DB; a test supplies a fake.
	Projects ProjectLookup
}

// ProjectLookup is what the validation asks of the project tables (R-AUTHZ-026).
type ProjectLookup interface {
	// ProjectID returns the ID of the project with the key, "" when none exists.
	ProjectID(ctx context.Context, key string) (string, error)
	// AccountProject returns the key of the project a service account (by
	// principal ID) belongs to, "" for a global account.
	AccountProject(ctx context.Context, principalID string) (string, error)
	// AutoJoinGroup reports whether the principal is an auto-join group (Q-AUTHZ-200).
	AutoJoinGroup(ctx context.Context, principalID string) (bool, error)
}

// WriteOption changes how a policy write or validation is judged.
type WriteOption func(*writeOpts)

type writeOpts struct {
	// delegate is the key of the project a project administrator writes for, "" for a
	// caller that holds the capability (Q-AUTHZ-197, Q-AUTHZ-199).
	delegate string
	// projects answers the lookups of the write; the writer sets the queries of its
	// own transaction here, so the checks and the insert see one state (Q-AUTHZ-199).
	projects ProjectLookup
	// expectProject, when expectSet, is the project the policy under change must
	// belong to, nil for a global policy ([ExpectProject]).
	expectProject *string
	expectSet     bool
	// create is set for a new policy, whose name is checked against the reserved
	// prefixes of the projects (Q-AUTHZ-200); a stored global policy keeps its name.
	create bool
}

// AsDelegate marks the write as that of a project administrator of the project:
// the policy under change must be the project's (checked under the policy lock)
// and the answers of the validation do not tell what lies outside the project
// (Q-AUTHZ-197, Q-AUTHZ-199).
func AsDelegate(project string) WriteOption { return func(o *writeOpts) { o.delegate = project } }

// ExpectProject makes an update or a delete refuse, under the policy lock, a policy
// that does not belong to the project: a key names a project, nil a global policy. A
// caller that read the policy by name earlier (the delete of a project, which removes
// the policies it listed) uses it so that a policy deleted and created again under
// the same name, in another project or as a global one, is never touched; the answer
// is 404, as for a policy that does not exist (Q-AUTHZ-199).
func ExpectProject(project *string) WriteOption {
	return func(o *writeOpts) { o.expectProject, o.expectSet = project, true }
}

func collectOptions(opts []WriteOption) writeOpts {
	var o writeOpts
	for _, f := range opts {
		f(&o)
	}
	return o
}

// lookup returns the project lookups of the write.
func (o writeOpts) lookup(s *Service) ProjectLookup {
	if o.projects != nil {
		return o.projects
	}
	return s.o.Projects
}

// inTx binds the project lookups to the queries of the writer's transaction when the
// service reads from PostgreSQL; a test double stays what it is.
func (s *Service) inTx(qs *q.Queries) WriteOption {
	return func(o *writeOpts) {
		if _, ok := s.o.Projects.(pgProjects); ok {
			o.projects = txProjects{qs}
		}
	}
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
	o.Formats = catalog.Or(o.Formats)
	if o.Projects == nil {
		o.Projects = pgProjects{db: o.DB}
	}
	return &Service{o: o}
}

// known reports whether the catalog holds a format.
func (s *Service) known(format string) bool {
	_, ok := s.o.Formats.Lookup(format)
	return ok
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
	// project is the ID of the policy's project, "" for a global policy.
	project string
	// o is the options of the write that asked for the resolution.
	o writeOpts
	// outside counts the key selectors of a stored project policy whose repository
	// left the project (Q-AUTHZ-179): they stay and cover nothing.
	outside int
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
func (s *Service) Validate(ctx context.Context, in PolicyInput, opts ...WriteOption) (Report, error) {
	rp, problems, err := s.resolve(ctx, in, false, append(opts, func(o *writeOpts) { o.create = true })...)
	if err != nil {
		return Report{}, err
	}
	r := Report{Errors: problems}
	if rp != nil {
		r.Warnings, r.Covers = rp.warnings, rp.covers
	}
	return r, nil
}

// pgProjects reads the project lookups from PostgreSQL, each in a transaction of
// its own: the validation of a read-only caller does not run in a write transaction.
// A policy write binds the lookups to its own transaction instead ([txProjects]).
type pgProjects struct{ db meta.Runner }

func (p pgProjects) read(ctx context.Context, fn func(qs *q.Queries) error) error {
	return p.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	})
}

// ProjectID implements [ProjectLookup].
func (p pgProjects) ProjectID(ctx context.Context, key string) (id string, err error) {
	err = p.read(ctx, func(qs *q.Queries) (err error) { id, err = txProjects{qs}.ProjectID(ctx, key); return })
	return id, err
}

// AccountProject implements [ProjectLookup].
func (p pgProjects) AccountProject(ctx context.Context, principalID string) (key string, err error) {
	err = p.read(ctx, func(qs *q.Queries) (err error) { key, err = txProjects{qs}.AccountProject(ctx, principalID); return })
	return key, err
}

// AutoJoinGroup implements [ProjectLookup].
func (p pgProjects) AutoJoinGroup(ctx context.Context, principalID string) (auto bool, err error) {
	err = p.read(ctx, func(qs *q.Queries) (err error) { auto, err = txProjects{qs}.AutoJoinGroup(ctx, principalID); return })
	return auto, err
}

// txProjects answers the project lookups on the queries of a transaction, so a policy
// write judges and stores in one transaction and opens no second connection
// (Q-AUTHZ-199).
type txProjects struct{ qs *q.Queries }

// ProjectID implements [ProjectLookup].
func (t txProjects) ProjectID(ctx context.Context, key string) (string, error) {
	rows, err := t.qs.AuthzProjectByKey(ctx, key)
	if len(rows) > 0 {
		return rows[0], err
	}
	return "", err
}

// AccountProject implements [ProjectLookup].
func (t txProjects) AccountProject(ctx context.Context, principalID string) (string, error) {
	rows, err := t.qs.AuthzServiceAccountProject(ctx, principalID)
	if len(rows) > 0 {
		return rows[0], err
	}
	return "", err
}

// AutoJoinGroup implements [ProjectLookup].
func (t txProjects) AutoJoinGroup(ctx context.Context, principalID string) (bool, error) {
	rows, err := t.qs.AuthzGroupAutoJoin(ctx, principalID)
	if len(rows) > 0 {
		return rows[0], err
	}
	return false, err
}

// projectID returns the ID of the project with the key, "" when none exists.
func (s *Service) projectID(ctx context.Context, key string) (string, error) {
	return s.o.Projects.ProjectID(ctx, key)
}

// accountProject returns the key of the project of a service account, "" for a
// global one.
func (s *Service) accountProject(ctx context.Context, principalID string) (string, error) {
	return s.o.Projects.AccountProject(ctx, principalID)
}

// resolveProject applies the project rules of R-AUTHZ-026 to the policy: the
// project exists and a new policy's name starts with its key (Q-AUTHZ-176). A
// stored policy (stored is true) was valid when it was written, so only the
// existence is read.
func (s *Service) resolveProject(ctx context.Context, rp *resolvedPolicy, stored bool) ([]ProblemField, error) {
	pl := rp.o.lookup(s)
	if rp.in.Project == nil {
		if !rp.o.create || stored {
			return nil, nil
		}
		// A global policy never takes the name of a project's policies, so a project
		// administrator's create never meets a global policy of the same name
		// (Q-AUTHZ-176, Q-AUTHZ-200).
		return nil, nil
	}
	key := *rp.in.Project
	id, err := pl.ProjectID(ctx, key)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return []ProblemField{field("/project", errs.CodeUnknownProject, fmt.Sprintf("No project %q exists or it is not administered by the caller.", key))}, nil
	}
	rp.project = id
	return nil, nil
}

// resolve validates the input against R-AUTHZ-021 and R-AUTHZ-026 and resolves
// selectors and principals. Problems of the input are returned as fields; an
// error is a failure of a dependency. stored is true for a policy that was read
// from the store to compute its warnings: a key selector whose repository left the
// project is then kept and warned about (Q-AUTHZ-179), where a write refuses it.
func (s *Service) resolve(ctx context.Context, in PolicyInput, stored bool, opts ...WriteOption) (*resolvedPolicy, []ProblemField, error) {
	var ps []ProblemField
	rp := &resolvedPolicy{in: in, o: collectOptions(opts)}
	if in.ResourceType == "" {
		rp.in.ResourceType = "repository"
	}
	if !policyNameRE.MatchString(in.Name) {
		ps = append(ps, field("/name", "invalid_name", "The name matches ^[a-z0-9][a-z0-9._-]{0,127}$."))
	}
	if utf8.RuneCountInString(in.Description) > 1024 {
		ps = append(ps, field("/description", "invalid_description", "The description has at most 1024 characters."))
	}
	pps, err := s.resolveProject(ctx, rp, stored)
	if err != nil {
		return nil, nil, err
	}
	ps = append(ps, pps...)
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

	ps = append(ps, s.resolveSelectors(ctx, rp, stored)...)
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
		if ref.Kind == principal.KindBastion {
			// R-BAS-004: a Bastion registration is granted nothing; 422 principal_not_allowed.
			return nil, nil, principal.BastionNotAllowed("named in a policy")
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
		pp, perr := s.projectRule(ctx, ptr, ref, id, rp)
		if perr != nil {
			return nil, nil, perr
		}
		if pp != nil {
			ps = append(ps, *pp)
			continue
		}
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
	rp.warnings, rp.covers = warningsFor(rp, all, s.o.Formats)
	return rp, nil, nil
}

// projectRule applies the principal rules of R-AUTHZ-026 to one entry: a project
// policy never names anonymous or authenticated (R-AUTHZ-030) nor the service
// account of another project, and a global policy names no project service account
// (Q-AUTHZ-175). It returns the problem, nil when the entry may stand.
func (s *Service) projectRule(ctx context.Context, ptr string, ref Ref, id string, rp *resolvedPolicy) (*ProblemField, error) {
	if rp.in.Project != nil && (ref.Kind == principal.KindAnonymous || ref.Kind == principal.KindAuthenticated) {
		p := field(ptr+"/principal", errs.CodePrincipalOutsideProject, "A project policy grants to users, groups, service accounts and CI mappings, never to "+ref.String()+".")
		return &p, nil
	}
	pl := rp.o.lookup(s)
	if ref.Kind == principal.KindGroup && rp.in.Project != nil {
		// An auto-join group holds every user that signs in: in a project policy it
		// would stand in for authenticated, which the policy never names (Q-AUTHZ-200).
		auto, err := pl.AutoJoinGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		if auto {
			p := field(ptr+"/principal", errs.CodePrincipalOutsideProject, ref.String()+" is an auto-join group: every user is a member, so a project policy does not grant to it.")
			return &p, nil
		}
	}
	if ref.Kind != principal.KindServiceAccount {
		return nil, nil
	}
	project, err := pl.AccountProject(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case project == "":
	case rp.in.Project == nil:
		p := field(ptr+"/principal", errs.CodePrincipalOutsideProject, fmt.Sprintf("%s belongs to the project %q and is granted by its policies only.", ref, project))
		return &p, nil
	case project != *rp.in.Project && rp.o.delegate != "":
		// A project administrator learns nothing about the accounts of other
		// projects: the answer is the one of a name that does not exist (Q-AUTHZ-197).
		p := field(ptr+"/principal", errs.CodeUnknownPrincipal, fmt.Sprintf("No principal %s exists.", ref))
		return &p, nil
	case project != *rp.in.Project:
		p := field(ptr+"/principal", errs.CodePrincipalOutsideProject, fmt.Sprintf("%s belongs to another project and is granted by its policies only.", ref))
		return &p, nil
	}
	return nil, nil
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
func (s *Service) resolveSelectors(ctx context.Context, rp *resolvedPolicy, stored bool) []ProblemField {
	var ps []ProblemField
	for i, text := range rp.in.Resources.Repositories {
		ptr := "/resources/repositories/" + strconv.Itoa(i)
		in := instanceContext
		if rp.in.Project != nil {
			in = projectContext(*rp.in.Project)
		}
		sel, err := parseSelector(text, in)
		if err != nil {
			ps = append(ps, field(ptr, errs.CodeInvalidPattern, err.Error()))
			continue
		}
		if sel.format != "" && !s.known(sel.format) {
			ps = append(ps, field(ptr, errs.CodeUnknownFormat, fmt.Sprintf("The format %q is not known.", sel.format)))
			continue
		}
		if sel.kind == selKey {
			r, err := s.o.Repositories.ByName(ctx, qname.Repo{Project: sel.project, Key: sel.key})
			if err != nil {
				var ee *errs.Error
				if errors.As(err, &ee) && ee.Kind == errs.NotFound {
					if rp.in.Project != nil {
						// An unknown key answers as one outside the project does, so the
						// answer never tells whether such a repository exists.
						ps = append(ps, field(ptr, errs.CodeRepositoryOutsideProject, fmt.Sprintf("The repository %q is not a repository of the project %q.", sel.key, *rp.in.Project)))
						continue
					}
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
			if rp.project != "" && r.ProjectID != rp.project {
				if !stored {
					ps = append(ps, field(ptr, errs.CodeRepositoryOutsideProject, fmt.Sprintf("The repository %q is not a repository of the project %q.", sel.key, *rp.in.Project)))
					continue
				}
				rp.outside++
			}
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
func warningsFor(rp *resolvedPolicy, all []repository.Repository, reg *catalog.Registry) (warnings, covers []string) {
	coveredBy := make([]int, len(rp.selectors))
	var covered []repository.Repository
	if rp.outside > 0 {
		warnings = append(warnings, WarnSelectorOutsideProject)
	}
	for _, r := range all {
		if rp.project != "" && r.ProjectID != rp.project {
			continue // a project policy covers only the repositories of its project (Q-AUTHZ-179)
		}
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		v := repoView{ID: id, Name: r.Name, Class: r.Class, Format: r.Format}
		hit := false
		for i, sel := range rp.selectors {
			if sel.matches(v) {
				coveredBy[i]++
				hit = true
			}
		}
		if hit {
			covered = append(covered, r)
			covers = append(covers, r.Name.String())
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
			if e, _ := reg.Lookup(r.Format); e.PatternSubject == catalog.SubjectName {
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
	ResourceType string `json:"resourceType"`
	// Project is the key of the policy's project, empty for a global policy: the
	// replay compares resource type and project (R-AUTHZ-031).
	Project   string            `json:"project,omitempty"`
	Selectors []journalSelector `json:"selectors"`
	Packages  PatternLists      `json:"packages"`
	Paths     PatternLists      `json:"paths"`
	Entries   []journalEntry    `json:"entries"`
}

func stateOf(rp *resolvedPolicy) *journalState {
	st := &journalState{
		ResourceType: rp.in.ResourceType, Packages: normLists(rp.in.Resources.Packages), Paths: normLists(rp.in.Resources.Paths),
		Selectors: []journalSelector{}, Entries: []journalEntry{},
	}
	if rp.in.Project != nil {
		st.Project = *rp.in.Project
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
