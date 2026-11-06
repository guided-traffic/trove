package authz

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// Limits of an exposure rule (R-EP-012).
const (
	MaxExposureRules       = 1000
	maxExposureDescription = 2000
	maxExposureRange       = 256
)

// The audit events of exposure rules (R-EP-016, R-EP-020); registered in
// internal/audit/events_endpoints.go.
const (
	EventExposureCreated = "endpoint.exposure.created"
	EventExposureUpdated = "endpoint.exposure.updated"
	EventExposureDeleted = "endpoint.exposure.deleted"
)

// WarnEndpointNotInRulesMode is the warning of a rule saved for an endpoint in
// mode all, where it has no effect (R-EP-012).
const WarnEndpointNotInRulesMode = "endpoint_not_in_rules_mode"

var exposureNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ExposureServiceOptions configure [NewExposureService].
type ExposureServiceOptions struct {
	// DB runs the reads and the writes; every read comes from PostgreSQL
	// (R-API-052).
	DB meta.Runner
	// Changes notifies the change area authz in the transaction of a write, so
	// every replica rebuilds its grant index (R-EP-012).
	Changes Changes
	// Recorder records the endpoint.exposure.* events in the transaction.
	Recorder audit.Recorder
	// Ceiling holds the configured endpoints and their modes, and the format
	// support (R-EP-001, R-EP-018).
	Ceiling *ExposureCeiling
	// Stored lists the versions and tags a repository stores of a package, for the
	// check that names no versions (R-EP-016, Q-EP-61, Q-EP-66). Nil lists nothing:
	// every repository then answers [WarnStoredVersionsNotListed].
	Stored StoredPackages
}

// StoredRepo names the repository a check lists the stored versions of.
type StoredRepo struct {
	ID     int64
	Name   qname.Repo
	Format string
	// Class is local, remote or virtual.
	Class string
}

// StoredPackage is what a repository stores under a package name.
type StoredPackage struct {
	// Listed is false when the repository cannot say what it stores (a virtual
	// repository, or a format that lists nothing).
	Listed   bool
	Versions []string
	// Tags maps a tag to the version it points to; formats with tags only.
	Tags map[string]string
}

// MayRead says whether the caller of a check may read a package (version empty) or
// one version of it; the package is the normalized name. It is the unaudited read
// scope of the caller ([ReadScope.MayRead]): a check asks about rows it lists, and no
// client requested a refused version.
type MayRead func(pkg, version string) bool

// StoredPackages lists the versions and tags a repository stores of a package, for
// the check of an endpoint's exposure rules that names no versions (R-EP-016,
// Q-EP-61). The format owns the answer through its view adapter.
type StoredPackages interface {
	// Stored returns at most limit versions, newest first, and, when withTags is set,
	// the tags of the package. Listed is false for a repository that cannot say what
	// it stores (a virtual one) and for a format without a view adapter.
	Stored(ctx context.Context, repo StoredRepo, pkg string, limit int, withTags bool) (StoredPackage, error)
}

// MaxCheckStored bounds the stored versions, and the tags, one check judges; the
// newest versions are kept, the rest is cut, and the answer carries
// [WarnStoredVersionsTruncated].
const MaxCheckStored = 1000

// The warnings of a check that lists stored versions.
const (
	// WarnStoredVersionsNotListed: the repository cannot list what it stores of
	// the package, so the answer holds no versions.
	WarnStoredVersionsNotListed = "stored_versions_not_listed"
	// WarnStoredVersionsTruncated: the package has more than MaxCheckStored versions or tags.
	WarnStoredVersionsTruncated = "stored_versions_truncated"
)

// ExposureService is the domain API of exposure rules (R-EP-012, R-EP-016): the
// HTTP operations are thin handlers over it that add paging, ETags and
// idempotency.
type ExposureService struct {
	o ExposureServiceOptions
}

// NewExposureService creates the service.
func NewExposureService(o ExposureServiceOptions) *ExposureService {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.Ceiling == nil {
		o.Ceiling = NewExposureCeiling(map[string]string{"default": ExposureModeAll}, nil)
	}
	return &ExposureService{o: o}
}

// Configured reports whether the configuration holds the endpoint.
func (s *ExposureService) Configured(endpoint string) bool {
	_, ok := s.o.Ceiling.modes[endpoint]
	return ok
}

// Mode returns the exposure mode of a configured endpoint.
func (s *ExposureService) Mode(endpoint string) string { return s.o.Ceiling.Mode(endpoint) }

func staleRevision(current int64) error { return errs.Stale("The exposure rule", current) }

func notFoundRule(endpoint, name string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The endpoint %q has no exposure rule %q.", endpoint, name))
}

func notFoundEndpoint(endpoint string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("No endpoint %q is configured or has exposure rules.", endpoint))
}

// RuleCount is the number of rules of an endpoint name.
type RuleCount struct {
	Rules, Enabled int64
}

// Counts returns the rule counts of every endpoint name that has rules, the
// configured and the inert ones (R-EP-016, H-58).
func (s *ExposureService) Counts(ctx context.Context) (map[string]RuleCount, error) {
	out := map[string]RuleCount{}
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err := qs.EndpointsRuleCounts(ctx)
		for _, r := range rows {
			out[r.Endpoint] = RuleCount{Rules: r.Rules, Enabled: r.Enabled}
		}
		return err
	})
	return out, err
}

// requireEndpoint answers 404 for a name that is neither configured nor holds
// rules; inert reports a name that holds rules but is not configured.
func (s *ExposureService) requireEndpoint(ctx context.Context, qs *q.Queries, endpoint string) (inert bool, err error) {
	if s.Configured(endpoint) {
		return false, nil
	}
	rows, err := qs.EndpointsListRules(ctx, endpoint)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, notFoundEndpoint(endpoint)
	}
	return true, nil
}

// ruleRow is the common shape of a list and a get row.
type ruleRow struct {
	ID, Endpoint, Name, Description, Repository, Format, Class string
	RepoID                                                     int64
	Enabled, Prereleases                                       bool
	Packages, Paths, Versions, DistTags                        string
	Revision, CreatedUs, UpdatedUs                             int64
	CreatedByKind, CreatedByName, UpdatedByKind, UpdatedByName string
}

func (s *ExposureService) ruleOf(r ruleRow) (ExposureRule, error) {
	d, err := decodeDims(r.Packages, r.Paths, r.Versions, r.Prereleases, r.DistTags)
	if err != nil {
		return ExposureRule{}, fmt.Errorf("exposure rule %s/%s: %w", r.Endpoint, r.Name, err)
	}
	out := ExposureRule{
		ExposureRuleInput: normInput(ExposureRuleInput{
			Name: r.Name, Description: r.Description, Repository: r.Repository, Enabled: r.Enabled,
			Packages: d.Packages, Paths: d.Paths, Versions: d.Versions, Prereleases: d.Prereleases, Tags: d.Tags,
		}),
		ID: r.ID, Endpoint: r.Endpoint, RepositoryID: r.RepoID, Format: r.Format, Class: repository.Class(r.Class),
		Revision:  r.Revision,
		CreatedAt: time.UnixMicro(r.CreatedUs).UTC(), UpdatedAt: time.UnixMicro(r.UpdatedUs).UTC(),
		CreatedBy: refOfRow(r.CreatedByKind, r.CreatedByName), UpdatedBy: refOfRow(r.UpdatedByKind, r.UpdatedByName),
		Inert: !s.Configured(r.Endpoint),
	}
	out.Warnings = s.warnings(r.Endpoint, r.Format, out.ExposureRuleInput)
	return out, nil
}

func (s *ExposureService) warnings(endpoint, format string, in ExposureRuleInput) []string {
	out := []string{}
	f := s.o.Ceiling.exposure(format)
	if f.Packages && len(in.Paths.Include)+len(in.Paths.Exclude) > 0 {
		out = append(out, WarnPathPatternsOnPackageFormat)
	}
	if s.Configured(endpoint) && s.Mode(endpoint) == ExposureModeAll {
		out = append(out, WarnEndpointNotInRulesMode)
	}
	return out
}

func listRow(r q.EndpointsListRulesRow) ruleRow {
	return ruleRow{
		ID: r.ID, Endpoint: r.Endpoint, Name: r.Name, Description: r.Description, Repository: r.Repository, Format: r.Format,
		Class: r.Class, RepoID: r.RepoID, Enabled: r.Enabled, Prereleases: r.Prereleases, Packages: r.Packages, Paths: r.Paths,
		Versions: r.Versions, DistTags: r.DistTags, Revision: r.Revision, CreatedUs: r.CreatedUs, UpdatedUs: r.UpdatedUs,
		CreatedByKind: r.CreatedByKind, CreatedByName: r.CreatedByName, UpdatedByKind: r.UpdatedByKind, UpdatedByName: r.UpdatedByName,
	}
}

func getRow(r q.EndpointsGetRuleRow) ruleRow { return listRow(q.EndpointsListRulesRow(r)) }

func (s *ExposureService) load(ctx context.Context, qs *q.Queries, endpoint, name string) (ExposureRule, error) {
	rows, err := qs.EndpointsGetRule(ctx, q.EndpointsGetRuleParams{Endpoint: endpoint, Name: name})
	if err != nil {
		return ExposureRule{}, err
	}
	if len(rows) == 0 {
		return ExposureRule{}, notFoundRule(endpoint, name)
	}
	return s.ruleOf(getRow(rows[0]))
}

// List returns the rules of an endpoint sorted by name (R-EP-016). The cursor
// paging and the filters are the handler's: an endpoint holds at most 1,000
// rules, and the package filter is a pattern match, which SQL cannot do.
func (s *ExposureService) List(ctx context.Context, endpoint string) ([]ExposureRule, error) {
	var out []ExposureRule
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err := qs.EndpointsListRules(ctx, endpoint)
		if err != nil {
			return err
		}
		if len(rows) == 0 && !s.Configured(endpoint) {
			return notFoundEndpoint(endpoint)
		}
		out = make([]ExposureRule, 0, len(rows))
		for _, r := range rows {
			rule, err := s.ruleOf(listRow(r))
			if err != nil {
				return err
			}
			out = append(out, rule)
		}
		return nil
	})
	return out, err
}

// Get returns one rule (R-EP-016).
func (s *ExposureService) Get(ctx context.Context, endpoint, name string) (ExposureRule, error) {
	var out ExposureRule
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if _, err := s.requireEndpoint(ctx, qs, endpoint); err != nil {
			return err
		}
		out, err = s.load(ctx, qs, endpoint, name)
		return err
	})
	return out, err
}

// AdmitsPackage reports whether a rule's package patterns admit a name, with the
// format's case rule: the package filter of the rule list (R-EP-016).
func (s *ExposureService) AdmitsPackage(r ExposureRule, pkg string) bool {
	set, err := compileSet(dimPackage, r.Packages.Include, r.Packages.Exclude, s.o.Ceiling.names(r.Format))
	return err == nil && set.match(pkg, true)
}

// normInput gives every list a non-nil value, so a rule reads back the same
// whether a list was sent empty or not at all.
func normInput(in ExposureRuleInput) ExposureRuleInput {
	norm := func(l PatternLists) PatternLists {
		if l.Include == nil {
			l.Include = []string{}
		}
		if l.Exclude == nil {
			l.Exclude = []string{}
		}
		return l
	}
	in.Packages, in.Paths, in.Tags = norm(in.Packages), norm(in.Paths), norm(in.Tags)
	if in.Versions.Include == nil {
		in.Versions.Include = []string{}
	}
	if in.Versions.Exclude == nil {
		in.Versions.Exclude = []string{}
	}
	if in.Versions.Range != nil && *in.Versions.Range == "" {
		in.Versions.Range = nil
	}
	return in
}

// validateSyntax checks what needs no database: the name, the description, the
// limits and the syntax of every pattern (R-EP-012). prefix is the pointer of
// the rule in the request body ("" or "/rule").
func validateSyntax(in ExposureRuleInput, prefix string) []ProblemField {
	var ps []ProblemField
	add := func(ptr, code, detail string) {
		ps = append(ps, ProblemField{Pointer: prefix + ptr, Code: code, Detail: detail})
	}
	if !exposureNameRE.MatchString(in.Name) {
		add("/name", "pattern", "must match "+exposureNameRE.String())
	}
	if utf8.RuneCountInString(in.Description) > maxExposureDescription {
		add("/description", "max_length", fmt.Sprintf("at most %d characters", maxExposureDescription))
	}
	if in.Repository == "" {
		add("/repository", "required", "names the repository the rule exposes")
	}
	checkList := func(ptr string, list []string, compile func(string) error) {
		if len(list) > maxPatternCount {
			add(ptr, errs.CodeInvalidPattern, fmt.Sprintf("a pattern list holds at most %d patterns", maxPatternCount))
			return
		}
		for i, p := range list {
			if err := compile(p); err != nil {
				add(fmt.Sprintf("%s/%d", ptr, i), errs.CodeInvalidPattern, err.Error())
			}
		}
	}
	name := func(p string) error { _, err := compileName(p, nil); return err }
	path := func(p string) error { _, err := compilePath(p); return err }
	checkList("/packages/include", in.Packages.Include, name)
	checkList("/packages/exclude", in.Packages.Exclude, name)
	checkList("/paths/include", in.Paths.Include, path)
	checkList("/paths/exclude", in.Paths.Exclude, path)
	checkList("/versions/include", in.Versions.Include, name)
	checkList("/versions/exclude", in.Versions.Exclude, name)
	checkList("/distTags/include", in.Tags.Include, name)
	checkList("/distTags/exclude", in.Tags.Exclude, name)
	if r := in.Versions.Range; r != nil && utf8.RuneCountInString(*r) > maxExposureRange {
		add("/versions/range", errs.CodeInvalidVersionRange, fmt.Sprintf("a range has at most %d characters", maxExposureRange))
	}
	return ps
}

// validateForFormat checks the dimensions against what the repository's format
// supports and lets the format check the range (R-EP-012, R-EP-014, R-EP-018).
func (s *ExposureService) validateForFormat(in ExposureRuleInput, format, prefix string) []ProblemField {
	var ps []ProblemField
	add := func(ptr, code, detail string) {
		ps = append(ps, ProblemField{Pointer: prefix + ptr, Code: code, Detail: detail})
	}
	f := s.o.Ceiling.exposure(format)
	if !f.Exposable {
		add("/repository", errs.CodeFormatNotExposable, fmt.Sprintf("the format %q cannot be served on an endpoint with exposure rules", format))
		return ps
	}
	if !f.Packages && len(in.Packages.Include)+len(in.Packages.Exclude) > 0 {
		add("/packages", errs.CodeDimensionNotSupported, fmt.Sprintf("%s repositories have no package names; narrow them with paths", format))
	}
	versions := len(in.Versions.Include)+len(in.Versions.Exclude) > 0 || in.Versions.Range != nil
	if !f.Versions && versions {
		add("/versions", errs.CodeDimensionNotSupported, fmt.Sprintf("%s repositories have no versions", format))
	}
	if !f.Versions && in.Prereleases {
		add("/prereleases", errs.CodeDimensionNotSupported, fmt.Sprintf("%s repositories have no versions", format))
	}
	if !f.Tags && len(in.Tags.Include)+len(in.Tags.Exclude) > 0 {
		add("/distTags", errs.CodeDimensionNotSupported, fmt.Sprintf("%s repositories have no tags", format))
	}
	if f.Versions && in.Versions.Range != nil {
		if f.Versioning == nil {
			add("/versions/range", errs.CodeInvalidVersionRange, "the format cannot check a range")
		} else if err := f.Versioning.ValidateRange(*in.Versions.Range); err != nil {
			add("/versions/range", errs.CodeInvalidVersionRange, err.Error())
		}
	}
	return ps
}

// resolvedRule is a validated input with its repository.
type resolvedRule struct {
	in     ExposureRuleInput
	repoID int64
	format string
	dims   ruleDims
}

// resolve validates an input completely in the transaction of the write: the
// syntax, the repository (an active one, read from PostgreSQL), and the format's
// dimensions. Validation answers 422 with the code of the first problem and
// every problem as a field error.
func (s *ExposureService) resolve(ctx context.Context, qs *q.Queries, in ExposureRuleInput, prefix string) (resolvedRule, error) {
	in = normInput(in)
	ps := validateSyntax(in, prefix)
	rr := resolvedRule{in: in}
	if in.Repository != "" {
		rows, err := repositoryByName(ctx, qs, in.Repository)
		if err != nil {
			return rr, err
		}
		if len(rows) == 0 {
			ps = append(ps, ProblemField{Pointer: prefix + "/repository", Code: errs.CodeUnknownRepository, Detail: fmt.Sprintf("no repository %q exists", in.Repository)})
		} else {
			rr.repoID, rr.format = rows[0].ID, rows[0].Format
			ps = append(ps, s.validateForFormat(in, rr.format, prefix)...)
		}
	}
	if err := problemError(ps); err != nil {
		return rr, err
	}
	rr.dims = ruleDims{Packages: in.Packages, Paths: in.Paths, Versions: in.Versions, Prereleases: in.Prereleases, Tags: in.Tags}
	return rr, nil
}

func mustText(v any) string { return string(mustJSON(v)) }

// exposureState is the access-bearing state of a rule as the audit event and the
// revocation journal carry it (R-EP-020).
type exposureState struct {
	Enabled     bool         `json:"enabled"`
	Packages    PatternLists `json:"packages"`
	Paths       PatternLists `json:"paths"`
	Versions    VersionLists `json:"versions"`
	Prereleases bool         `json:"prereleases"`
	Tags        PatternLists `json:"distTags"`
}

func stateOfRule(in ExposureRuleInput) *exposureState {
	in = normInput(in)
	return &exposureState{
		Enabled: in.Enabled, Packages: in.Packages, Paths: in.Paths, Versions: in.Versions,
		Prereleases: in.Prereleases, Tags: in.Tags,
	}
}

// changedMembers lists the members a replacement changes (details.changes).
func changedMembers(before, after ExposureRuleInput) []string {
	b, a := normInput(before), normInput(after)
	var out []string
	eq := func(x, y any) bool { return string(mustJSON(x)) == string(mustJSON(y)) }
	for _, m := range []struct {
		name string
		x, y any
	}{
		{"description", b.Description, a.Description},
		{"enabled", b.Enabled, a.Enabled},
		{"packages", b.Packages, a.Packages},
		{"paths", b.Paths, a.Paths},
		{"versions", b.Versions, a.Versions},
		{"prereleases", b.Prereleases, a.Prereleases},
		{"distTags", b.Tags, a.Tags},
	} {
		if !eq(m.x, m.y) {
			out = append(out, m.name)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// exposureEvent builds the audit event of a rule change. Updated and deleted
// events carry the journal entry of R-EP-020: the rule's ID, endpoint, name and
// repository ID, and for an update the states before and after.
func exposureEvent(name string, actor Subject, r ExposureRule, before, after *exposureState, changes []string, epoch int64, cause string) audit.Event {
	details := map[string]any{"endpoint": r.Endpoint, "name": r.Name, "repository": r.Repository, "epoch": epoch, "revision": r.Revision}
	if before != nil {
		details["before"] = before
	}
	if after != nil {
		details["after"] = after
	}
	if changes != nil {
		details["changes"] = changes
	}
	if cause != "" {
		details["cause"] = cause
	}
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "exposure_rule", ID: r.ID, Name: r.Endpoint + "/" + r.Name},
		Repository: r.Repository, Endpoint: actor.Endpoint, Details: mustJSON(details),
	}
	ev.Actor.TokenID = actor.TokenID
	switch name {
	case EventExposureUpdated:
		ev.Journal = mustJSON(map[string]any{
			"ruleId": r.ID, "endpoint": r.Endpoint, "name": r.Name, "repositoryId": r.RepositoryID, "before": before, "after": after,
		})
	case EventExposureDeleted:
		ev.Journal = mustJSON(map[string]any{"ruleId": r.ID, "endpoint": r.Endpoint, "name": r.Name, "repositoryId": r.RepositoryID})
	}
	return ev
}

// writableEndpoint is the rule of R-EP-016 for writes: rules are created and
// replaced on a configured endpoint only; an inert name's rules can be read and
// deleted.
func (s *ExposureService) writableEndpoint(ctx context.Context, qs *q.Queries, endpoint string) error {
	inert, err := s.requireEndpoint(ctx, qs, endpoint)
	if err != nil {
		return err
	}
	if inert {
		return errs.E(errs.Conflict, errs.CodeEndpointNotConfigured,
			fmt.Sprintf("The endpoint %q is not configured; its rules can be read and deleted, not created or replaced.", endpoint))
	}
	return nil
}

// Create stores a new rule in the caller's transaction (the idempotent
// operation of the handler, R-API-014) and notifies the change area authz there.
// onEpoch, when set, receives the authz epoch of the write just before the
// commit, for Trove-Authz-Epoch (R-AUTHZ-032).
func (s *ExposureService) Create(ctx context.Context, tx meta.Tx, actor Subject, endpoint string, in ExposureRuleInput, onEpoch func(int64)) (ExposureRule, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return ExposureRule{}, err
	}
	if err := s.writableEndpoint(ctx, qs, endpoint); err != nil {
		return ExposureRule{}, err
	}
	rr, err := s.resolve(ctx, qs, in, "")
	if err != nil {
		return ExposureRule{}, err
	}
	n, err := qs.EndpointsLockRuleCount(ctx, endpoint)
	if err != nil {
		return ExposureRule{}, err
	}
	if n >= MaxExposureRules {
		return ExposureRule{}, errs.E(errs.Conflict, errs.CodeExposureRuleLimit,
			fmt.Sprintf("The endpoint %q holds %d exposure rules, the most it may hold.", endpoint, MaxExposureRules))
	}
	rows, err := qs.EndpointsInsertRule(ctx, q.EndpointsInsertRuleParams{
		ID: newID(), Endpoint: endpoint, Name: rr.in.Name, Description: rr.in.Description, RepoID: rr.repoID,
		Enabled: rr.in.Enabled, Packages: mustText(rr.dims.Packages), Paths: mustText(rr.dims.Paths),
		Versions: mustText(rr.dims.Versions), Prereleases: rr.dims.Prereleases, DistTags: mustText(rr.dims.Tags),
		Actor: actorID(actor),
	})
	if err != nil {
		return ExposureRule{}, err
	}
	if len(rows) == 0 {
		return ExposureRule{}, errs.E(errs.Conflict, errs.CodeAlreadyExists,
			fmt.Sprintf("The endpoint %q has an exposure rule named %q.", endpoint, rr.in.Name))
	}
	if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "exposure_rule", ID: rows[0].ID}); err != nil {
		return ExposureRule{}, err
	}
	out, err := s.load(ctx, qs, endpoint, rr.in.Name)
	if err != nil {
		return ExposureRule{}, err
	}
	var epoch int64
	afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
		if onEpoch != nil {
			onEpoch(epoch)
		}
		return s.o.Recorder.RecordTx(ctx, tx, exposureEvent(EventExposureCreated, actor, out, nil, stateOfRule(out.ExposureRuleInput), nil, epoch, ""))
	})
	return out, nil
}

// Replace replaces a rule as a whole (R-EP-016 PUT, and the merge patch the
// handler applies on the stored rule). expected is the revision the client saw
// (If-Match); errs.AnyRevision is the deliberate blind write. Name and repository are
// immutable: a different repository answers 422. A different revision answers
// 412 with [StaleRevision] as the cause, a missing rule 404.
func (s *ExposureService) Replace(ctx context.Context, actor Subject, endpoint, name string, expected int64, in ExposureRuleInput) (ExposureRule, int64, error) {
	in.Name = name
	var out ExposureRule
	var epoch int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if err := s.writableEndpoint(ctx, qs, endpoint); err != nil {
			return err
		}
		if err := qs.EndpointsLockRule(ctx, q.EndpointsLockRuleParams{Endpoint: endpoint, Name: name}); err != nil {
			return err
		}
		current, err := s.load(ctx, qs, endpoint, name)
		if err != nil {
			return err
		}
		if in.Repository != current.Repository {
			return errs.E(errs.Invalid, errs.CodeValidation, "An immutable member was changed.", errs.WithFields(errs.FieldError{
				Pointer: "/repository", Code: "immutable", Detail: "a rule belongs to its repository; create a new rule instead",
			}))
		}
		rr, err := s.resolve(ctx, qs, in, "")
		if err != nil {
			return err
		}
		rows, err := qs.EndpointsUpdateRule(ctx, q.EndpointsUpdateRuleParams{
			Description: rr.in.Description, Enabled: rr.in.Enabled, Packages: mustText(rr.dims.Packages),
			Paths: mustText(rr.dims.Paths), Versions: mustText(rr.dims.Versions), Prereleases: rr.dims.Prereleases,
			DistTags: mustText(rr.dims.Tags), Actor: actorID(actor), Endpoint: endpoint, Name: name, Expected: expected,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			cur, err := qs.EndpointsRuleRevision(ctx, q.EndpointsRuleRevisionParams{Endpoint: endpoint, Name: name})
			if err != nil {
				return err
			}
			if len(cur) == 0 {
				return notFoundRule(endpoint, name)
			}
			return staleRevision(cur[0])
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "exposure_rule", ID: rows[0].ID}); err != nil {
			return err
		}
		if out, err = s.load(ctx, qs, endpoint, name); err != nil {
			return err
		}
		before, after := stateOfRule(current.ExposureRuleInput), stateOfRule(out.ExposureRuleInput)
		changes := changedMembers(current.ExposureRuleInput, out.ExposureRuleInput)
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, exposureEvent(EventExposureUpdated, actor, out, before, after, changes, epoch, ""))
		})
		return nil
	})
	if err != nil {
		return ExposureRule{}, 0, err
	}
	return out, epoch, nil
}

// Delete removes a rule. expected follows the rules of Replace; a rule of an
// inert endpoint can be deleted (H-58).
func (s *ExposureService) Delete(ctx context.Context, actor Subject, endpoint, name string, expected int64) (int64, error) {
	var epoch int64
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if _, err := s.requireEndpoint(ctx, qs, endpoint); err != nil {
			return err
		}
		if err := qs.EndpointsLockRule(ctx, q.EndpointsLockRuleParams{Endpoint: endpoint, Name: name}); err != nil {
			return err
		}
		current, err := s.load(ctx, qs, endpoint, name)
		if err != nil {
			return err
		}
		rows, err := qs.EndpointsDeleteRule(ctx, q.EndpointsDeleteRuleParams{Endpoint: endpoint, Name: name, Expected: expected})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			cur, err := qs.EndpointsRuleRevision(ctx, q.EndpointsRuleRevisionParams{Endpoint: endpoint, Name: name})
			if err != nil {
				return err
			}
			if len(cur) == 0 {
				return notFoundRule(endpoint, name)
			}
			return staleRevision(cur[0])
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "exposure_rule", ID: rows[0].ID}); err != nil {
			return err
		}
		before := stateOfRule(current.ExposureRuleInput)
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, exposureEvent(EventExposureDeleted, actor, current, before, nil, nil, epoch, "api"))
		})
		return nil
	})
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

// ExposureCheckInput is the request of the dry run (R-EP-016 check-exposure).
type ExposureCheckInput struct {
	Repository string
	// Package is the package the check is about; empty checks the repository.
	Package string
	// Versions and Paths are the items to check. Versions of a format with
	// versions are judged under the package; paths are the generic dimension.
	Versions []string
	Paths    []string
	// Draft, when set, is evaluated in place of the stored rule of the same name,
	// or in addition when no rule has that name: the preview of the rule editor
	// (R-EP-017, Q-EP-18). It is validated like a write and stored nowhere.
	Draft *ExposureRuleInput
	// MayRead decides whether the caller may read a package, and a version of it. A
	// check that lists the stored versions and tags shows only what it admits, and
	// answers for a package it refuses exactly as for one the repository does not
	// hold (Q-EP-62). Nil admits nothing: the answer is "listed" with no versions and
	// no tags, so a caller without a read scope learns nothing (fail closed, Q-EP-66).
	MayRead MayRead
}

// ExposureCheckItem is the verdict on one version or path.
type ExposureCheckItem struct {
	Value   string
	Exposed bool
	// Rule is the rule that exposes the item; Reason the reason of a hidden one.
	Rule   string
	Reason string
}

// ExposureCheckTag is the verdict on one tag of the package.
type ExposureCheckTag struct {
	Tag, Version string
	// Visible: a rule admits the tag name and the version it points to is exposed.
	Visible bool
}

// ExposureCheckResult is the answer of the dry run.
type ExposureCheckResult struct {
	Endpoint, Repository, Mode, Package string
	// Exposed is the verdict on the package, or on the repository when the check
	// names no package.
	Exposed      bool
	Rule, Reason string
	Versions     []ExposureCheckItem
	Paths        []ExposureCheckItem
	// Tags are the tags of the package for a format with tags, sorted by name.
	Tags []ExposureCheckTag
	// Fingerprint is the exposure fingerprint of the stored rules (R-EP-018).
	Fingerprint string
	Warnings    []string
}

// Check is the dry run of R-EP-016: exposed or not, the admitting rule or the
// reason, for the repository or a package and for the versions and paths asked.
// It reads the stored rules from PostgreSQL, so it reflects a write at once on
// every replica (R-API-052), transfers no content and contacts no upstream. A check
// that names a package of a format with versions and no versions judges the
// versions the repository stores of it, and for a format with tags answers the
// tags and whether each stays visible (Q-EP-61); a repository that cannot list
// them (a virtual one) answers the warning [WarnStoredVersionsNotListed]. The
// caller has already checked endpoints:read and read on the repository.
func (s *ExposureService) Check(ctx context.Context, endpoint string, in ExposureCheckInput) (ExposureCheckResult, error) {
	res := ExposureCheckResult{Endpoint: endpoint, Repository: in.Repository, Package: in.Package, Warnings: []string{}}
	var held checkHeld
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if _, err := s.requireEndpoint(ctx, qs, endpoint); err != nil {
			return err
		}
		repos, err := repositoryByName(ctx, qs, in.Repository)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("No repository %q exists.", in.Repository))
		}
		repo := repos[0]
		f := s.o.Ceiling.exposure(repo.Format)
		var ps []ProblemField
		switch {
		case !f.Exposable:
			ps = append(ps, ProblemField{Pointer: "/repository", Code: errs.CodeFormatNotExposable, Detail: fmt.Sprintf("the format %q cannot be served on an endpoint with exposure rules", repo.Format)})
		case !f.Packages && in.Package != "":
			ps = append(ps, ProblemField{Pointer: "/package", Code: errs.CodeDimensionNotSupported, Detail: fmt.Sprintf("%s repositories have no package names", repo.Format)})
		case !f.Versions && len(in.Versions) > 0:
			ps = append(ps, ProblemField{Pointer: "/versions", Code: errs.CodeDimensionNotSupported, Detail: fmt.Sprintf("%s repositories have no versions", repo.Format)})
		}
		if err := problemError(ps); err != nil {
			return err
		}
		rows, err := qs.EndpointsListRules(ctx, endpoint)
		if err != nil {
			return err
		}
		var stored []*compiledRule
		names := s.o.Ceiling.names(repo.Format)
		for _, r := range rows {
			if r.RepoID != repo.ID {
				continue
			}
			d, err := decodeDims(r.Packages, r.Paths, r.Versions, r.Prereleases, r.DistTags)
			if err != nil {
				return err
			}
			cr, err := compileRule(r.ID, r.Name, r.Revision, r.Enabled, d, names)
			if err != nil {
				return err
			}
			stored = append(stored, cr)
		}
		rules := stored
		if in.Draft != nil {
			if rules, err = s.withDraft(ctx, qs, stored, *in.Draft, in.Repository, names); err != nil {
				return err
			}
		}
		// An inert endpoint name is mode rules: what it would expose if configured.
		res.Mode = s.Mode(endpoint)
		res.Fingerprint = fingerprint(res.Mode, stored)
		judge := func(it exposureItem) (bool, string, string) {
			if res.Mode == ExposureModeAll {
				return true, "", ExposureExposed
			}
			return explainItem(rules, it, f.Versioning)
		}
		if res.Mode == ExposureModeAll {
			res.Warnings = append(res.Warnings, WarnEndpointNotInRulesMode)
		}
		res.Exposed, res.Rule, res.Reason = judge(exposureItem{pkg: in.Package})
		held = checkHeld{repo: repo, f: f, names: names, rules: rules, judge: judge}
		res.Paths = make([]ExposureCheckItem, 0, len(in.Paths))
		for _, p := range in.Paths {
			ok, rule, reason := judge(exposureItem{pkg: in.Package, path: p})
			res.Paths = append(res.Paths, ExposureCheckItem{Value: p, Exposed: ok, Rule: rule, Reason: reason})
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	// The versions and tags come from the format, outside the transaction above: the
	// format reads its own tables through its own transaction.
	versions, err := s.checkStored(ctx, in, held, &res)
	if err != nil {
		return res, err
	}
	for _, v := range versions {
		ok, rule, reason := held.judge(exposureItem{pkg: in.Package, version: v})
		res.Versions = append(res.Versions, ExposureCheckItem{Value: v, Exposed: ok, Rule: rule, Reason: reason})
	}
	return res, nil
}

// checkStored is the part of a check that asks the source: it fills the tag verdicts and the
// warnings of res and returns the versions to judge, the ones the request named or, when it named
// none, the ones the repository stores (Q-EP-61).
func (s *ExposureService) checkStored(ctx context.Context, in ExposureCheckInput, held checkHeld, res *ExposureCheckResult) ([]string, error) {
	res.Versions = make([]ExposureCheckItem, 0, len(in.Versions))
	res.Tags = []ExposureCheckTag{}
	if !held.f.Versions || in.Package == "" {
		return in.Versions, nil
	}
	// The request names versions: only the tags are needed from the source (the verdict on a
	// named version needs no listing), and no version is decided for the read scope.
	named := len(in.Versions) > 0
	sp, cut, err := s.storedOf(ctx, held.repo, in.Package, held.names, storedWant{versions: !named, tags: held.f.Tags}, in.MayRead)
	if err != nil {
		return nil, err
	}
	versions := in.Versions
	if !named {
		versions = sp.Versions
		if !sp.Listed {
			res.Warnings = append(res.Warnings, WarnStoredVersionsNotListed)
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(sp.Tags)) {
		target := sp.Tags[tag]
		res.Tags = append(res.Tags, ExposureCheckTag{Tag: tag, Version: target, Visible: tagVisible(held, res.Mode, in.Package, tag, target)})
		// The version a tag points to is always judged, whatever the cut kept.
		if !named && !slices.Contains(versions, target) {
			versions = append(slices.Clone(versions), target)
		}
	}
	if cut {
		res.Warnings = append(res.Warnings, WarnStoredVersionsTruncated)
	}
	return versions, nil
}

// tagVisible: a tag stays in a filtered document when a rule admits its name and the version it
// points to is exposed (ExposureView.Tag).
func tagVisible(held checkHeld, mode, pkg, tag, target string) bool {
	if mode == ExposureModeAll {
		return true
	}
	pointed, _, _ := held.judge(exposureItem{pkg: pkg, version: target})
	return pointed && admitsItem(held.rules, exposureItem{pkg: pkg, tag: tag}, held.f.Versioning)
}

// checkHeld is what the transaction of a check hands to the part that lists the
// stored versions: the repository, its format's exposure facts and the compiled
// rules, with the judge over them.
type checkHeld struct {
	repo  q.EndpointsRepositoryByNameRow
	f     catalog.Exposure
	names catalog.Names
	rules []*compiledRule
	judge func(exposureItem) (bool, string, string)
}

// storedWant says what a check needs of the source.
type storedWant struct {
	// versions: the request named none, so the stored versions are judged.
	versions bool
	// tags: the format has tags (catalog.Exposure.Tags).
	tags bool
}

// storedOf asks the source what the repository stores of the package and keeps
// what the caller may read. A service without a source, a format without a view
// adapter and a package name the format does not accept answer the empty package;
// the first two unlisted. A package the caller may not read answers as one the
// repository does not hold, with the same "listed" state and no versions or tags,
// so the answer is no oracle for the names the repository holds (Q-EP-62). A
// version the caller may not read is dropped, and a tag that points to one is
// dropped with it, as the package screens do; a nil may reads nothing (Q-EP-66).
//
// cut says that the source held more than [MaxCheckStored] versions or tags,
// decided on what it returned before the read filter dropped any, so that a list
// the filter made short does not look complete. At most MaxCheckStored versions
// and tags are returned.
func (s *ExposureService) storedOf(ctx context.Context, repo q.EndpointsRepositoryByNameRow, pkg string, names catalog.Names, want storedWant, may MayRead) (sp StoredPackage, cut bool, err error) {
	if s.o.Stored == nil {
		return StoredPackage{}, false, nil
	}
	if names != nil {
		norm, ok := names.Normalize(pkg)
		if !ok {
			return StoredPackage{Listed: true}, false, nil
		}
		pkg = norm
	}
	sp, err = s.o.Stored.Stored(ctx, StoredRepo{ID: repo.ID, Name: qname.Repo{Project: repo.ProjectKey, Key: repo.Key}, Format: repo.Format, Class: repo.Class}, pkg, MaxCheckStored+1, want.tags)
	if err != nil {
		return sp, false, err
	}
	if !want.versions {
		sp.Versions = nil
	}
	if !want.tags {
		sp.Tags = nil
	}
	cut = len(sp.Versions) > MaxCheckStored || len(sp.Tags) > MaxCheckStored
	if may == nil || !may(pkg, "") {
		return StoredPackage{Listed: sp.Listed}, false, nil
	}
	if want.versions {
		sp.Versions = slices.DeleteFunc(slices.Clone(sp.Versions), func(v string) bool { return !may(pkg, v) })
		sp.Versions = sp.Versions[:min(len(sp.Versions), MaxCheckStored)]
	}
	tags := map[string]string{}
	for _, tag := range slices.Sorted(maps.Keys(sp.Tags)) {
		if len(tags) == MaxCheckStored {
			break
		}
		if v := sp.Tags[tag]; may(pkg, v) {
			tags[tag] = v
		}
	}
	sp.Tags = tags
	return sp, cut, nil
}

// withDraft validates the draft of the rule editor and puts it in place of the
// stored rule of its name, or adds it.
func (s *ExposureService) withDraft(ctx context.Context, qs *q.Queries, stored []*compiledRule, draft ExposureRuleInput, repoKey string, names catalog.Names) ([]*compiledRule, error) {
	rr, err := s.resolve(ctx, qs, draft, "/rule")
	if err != nil {
		return nil, err
	}
	if rr.in.Repository != repoKey {
		return nil, problemError([]ProblemField{{Pointer: "/rule/repository", Code: errs.CodeValidation, Detail: "the draft rule must belong to the repository of the check"}})
	}
	cr, err := compileRule("draft", rr.in.Name, 0, rr.in.Enabled, rr.dims, names)
	if err != nil {
		return nil, err
	}
	out := slices.DeleteFunc(slices.Clone(stored), func(r *compiledRule) bool { return r.name == rr.in.Name })
	out = append(out, cr)
	slices.SortFunc(out, func(a, b *compiledRule) int {
		switch {
		case a.name < b.name:
			return -1
		case a.name > b.name:
			return 1
		}
		return 0
	})
	return out, nil
}

// repositoryByName resolves the repository of a rule: a rule names it by <project>/<key> (an instance
// context, Q-AUTHZ-206); a text that is no name resolves to none.
func repositoryByName(ctx context.Context, qs *q.Queries, text string) ([]q.EndpointsRepositoryByNameRow, error) {
	name, err := qname.ParseRepo(text)
	if err != nil {
		return nil, nil
	}
	return qs.EndpointsRepositoryByName(ctx, q.EndpointsRepositoryByNameParams{Project: name.Project, Key: name.Key})
}
