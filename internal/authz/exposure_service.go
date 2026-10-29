package authz

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
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
}

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
			Packages: d.Packages, Paths: d.Paths, Versions: d.Versions, Prereleases: d.Prereleases, DistTags: d.DistTags,
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
	f := s.o.Ceiling.formats(format)
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
	set, err := compileSet(dimPackage, r.Packages.Include, r.Packages.Exclude, foldFor(r.Format))
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
	in.Packages, in.Paths, in.DistTags = norm(in.Packages), norm(in.Paths), norm(in.DistTags)
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
	name := func(p string) error { _, err := compileName(p, false); return err }
	path := func(p string) error { _, err := compilePath(p); return err }
	checkList("/packages/include", in.Packages.Include, name)
	checkList("/packages/exclude", in.Packages.Exclude, name)
	checkList("/paths/include", in.Paths.Include, path)
	checkList("/paths/exclude", in.Paths.Exclude, path)
	checkList("/versions/include", in.Versions.Include, name)
	checkList("/versions/exclude", in.Versions.Exclude, name)
	checkList("/distTags/include", in.DistTags.Include, name)
	checkList("/distTags/exclude", in.DistTags.Exclude, name)
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
	f := s.o.Ceiling.formats(format)
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
	if !f.Tags && len(in.DistTags.Include)+len(in.DistTags.Exclude) > 0 {
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
		rows, err := qs.EndpointsRepositoryByKey(ctx, in.Repository)
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
	rr.dims = ruleDims{Packages: in.Packages, Paths: in.Paths, Versions: in.Versions, Prereleases: in.Prereleases, DistTags: in.DistTags}
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
	DistTags    PatternLists `json:"distTags"`
}

func stateOfRule(in ExposureRuleInput) *exposureState {
	in = normInput(in)
	return &exposureState{
		Enabled: in.Enabled, Packages: in.Packages, Paths: in.Paths, Versions: in.Versions,
		Prereleases: in.Prereleases, DistTags: in.DistTags,
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
		{"distTags", b.DistTags, a.DistTags},
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
func exposureEvent(ctx context.Context, name string, actor Subject, r ExposureRule, before, after *exposureState, changes []string, epoch int64, cause string) audit.Event {
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
		Repository: r.Repository, Endpoint: EndpointOf(ctx), Details: mustJSON(details),
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
		Versions: mustText(rr.dims.Versions), Prereleases: rr.dims.Prereleases, DistTags: mustText(rr.dims.DistTags),
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
		return s.o.Recorder.RecordTx(ctx, tx, exposureEvent(ctx, EventExposureCreated, actor, out, nil, stateOfRule(out.ExposureRuleInput), nil, epoch, ""))
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
			DistTags: mustText(rr.dims.DistTags), Actor: actorID(actor), Endpoint: endpoint, Name: name, Expected: expected,
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
			return s.o.Recorder.RecordTx(ctx, tx, exposureEvent(ctx, EventExposureUpdated, actor, out, before, after, changes, epoch, ""))
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
			return s.o.Recorder.RecordTx(ctx, tx, exposureEvent(ctx, EventExposureDeleted, actor, current, before, nil, nil, epoch, "api"))
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
}

// ExposureCheckItem is the verdict on one version or path.
type ExposureCheckItem struct {
	Value   string
	Exposed bool
	// Rule is the rule that exposes the item; Reason the reason of a hidden one.
	Rule   string
	Reason string
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
	// Fingerprint is the exposure fingerprint of the stored rules (R-EP-018).
	Fingerprint string
	Warnings    []string
}

// Check is the dry run of R-EP-016: exposed or not, the admitting rule or the
// reason, for the repository or a package and for the versions and paths asked.
// It reads the stored rules from PostgreSQL, so it reflects a write at once on
// every replica (R-API-052), transfers no content and contacts no upstream. The
// stored versions of a package are listed by formats with versions; this build
// has none, so a check without versions lists none. The caller has already
// checked endpoints:read and read on the repository.
func (s *ExposureService) Check(ctx context.Context, endpoint string, in ExposureCheckInput) (ExposureCheckResult, error) {
	res := ExposureCheckResult{Endpoint: endpoint, Repository: in.Repository, Package: in.Package, Warnings: []string{}}
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if _, err := s.requireEndpoint(ctx, qs, endpoint); err != nil {
			return err
		}
		repos, err := qs.EndpointsRepositoryByKey(ctx, in.Repository)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("No repository %q exists.", in.Repository))
		}
		repo := repos[0]
		f := s.o.Ceiling.formats(repo.Format)
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
		fold := foldFor(repo.Format)
		for _, r := range rows {
			if r.RepoID != repo.ID {
				continue
			}
			d, err := decodeDims(r.Packages, r.Paths, r.Versions, r.Prereleases, r.DistTags)
			if err != nil {
				return err
			}
			cr, err := compileRule(r.ID, r.Name, r.Revision, r.Enabled, d, fold)
			if err != nil {
				return err
			}
			stored = append(stored, cr)
		}
		rules := stored
		if in.Draft != nil {
			if rules, err = s.withDraft(ctx, qs, stored, *in.Draft, repo.Key, fold); err != nil {
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
		res.Versions = make([]ExposureCheckItem, 0, len(in.Versions))
		for _, v := range in.Versions {
			ok, rule, reason := judge(exposureItem{pkg: in.Package, version: v})
			res.Versions = append(res.Versions, ExposureCheckItem{Value: v, Exposed: ok, Rule: rule, Reason: reason})
		}
		res.Paths = make([]ExposureCheckItem, 0, len(in.Paths))
		for _, p := range in.Paths {
			ok, rule, reason := judge(exposureItem{pkg: in.Package, path: p})
			res.Paths = append(res.Paths, ExposureCheckItem{Value: p, Exposed: ok, Rule: rule, Reason: reason})
		}
		return nil
	})
	return res, err
}

// withDraft validates the draft of the rule editor and puts it in place of the
// stored rule of its name, or adds it.
func (s *ExposureService) withDraft(ctx context.Context, qs *q.Queries, stored []*compiledRule, draft ExposureRuleInput, repoKey string, fold bool) ([]*compiledRule, error) {
	rr, err := s.resolve(ctx, qs, draft, "/rule")
	if err != nil {
		return nil, err
	}
	if rr.in.Repository != repoKey {
		return nil, problemError([]ProblemField{{Pointer: "/rule/repository", Code: errs.CodeValidation, Detail: "the draft rule must belong to the repository of the check"}})
	}
	cr, err := compileRule("draft", rr.in.Name, 0, rr.in.Enabled, rr.dims, fold)
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
