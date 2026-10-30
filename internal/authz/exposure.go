package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// Exposure rules (endpoints.md R-EP-012, R-EP-013). A rule admits content of one
// repository on one endpoint, narrowed by package, path, version and tag
// patterns. The rules are rows of endpoint_exposure_rule; the grant index holds
// them compiled, so the ceiling is decided from memory like every grant, and a
// rule change follows the bound, the stale wait and the epochs of a policy change
// (R-AUTHZ-028, R-AUTHZ-032).

// Exposure modes of an endpoint (R-EP-012); the values of server.endpoints.*.exposure.
const (
	ExposureModeAll   = "all"
	ExposureModeRules = "rules"
)

// The reasons of the exposure check (R-EP-016 check-exposure).
const (
	ExposureExposed         = "exposed"
	ExposureNoRule          = "no_rule"
	ExposureRuleDisabled    = "rule_disabled"
	ExposurePackageExcluded = "package_excluded"
	ExposurePathExcluded    = "path_excluded"
	ExposureVersionExcluded = "version_excluded"
	ExposureOutOfRange      = "out_of_range"
	ExposurePrerelease      = "prerelease"
	ExposureTagExcluded     = "tag_excluded"
)

// VersionLists is the versions dimension of a rule: include and exclude globs
// over the normalized version, and at most one range in the format's native
// syntax.
type VersionLists struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
	Range   *string  `json:"range"`
}

// ExposureRuleInput is what a client writes (R-EP-012). Name and Repository are
// immutable once the rule exists.
type ExposureRuleInput struct {
	Name        string
	Description string
	Repository  string
	Enabled     bool
	Packages    PatternLists
	Paths       PatternLists
	Versions    VersionLists
	Prereleases bool
	DistTags    PatternLists
}

// ExposureRule is a stored rule as the API returns it.
type ExposureRule struct {
	ExposureRuleInput
	// ID is the immutable identifier (UUIDv7); the API addresses a rule by
	// endpoint and name.
	ID       string
	Endpoint string
	// RepositoryID, Format and Class are the repository's, resolved by ID.
	RepositoryID int64
	Format       string
	Class        repository.Class
	Revision     int64
	CreatedAt    time.Time
	CreatedBy    string
	UpdatedAt    time.Time
	UpdatedBy    string
	// Inert is true when the endpoint is not configured: the rule is stored and
	// has no effect (H-58).
	Inert    bool
	Warnings []string
}

// ruleDims is the stored form of a rule's dimensions, one JSON column each.
type ruleDims struct {
	Packages    PatternLists
	Paths       PatternLists
	Versions    VersionLists
	Prereleases bool
	DistTags    PatternLists
}

// compiledRule is a rule as the ceiling evaluates it.
type compiledRule struct {
	id, name    string
	revision    int64
	enabled     bool
	packages    globSet
	paths       globSet
	versions    globSet
	vrange      string
	prereleases bool
	tags        globSet
	// constrainsVersions is true when the versions dimension or the pre-release
	// rule can refuse a version.
	constrainsVersions bool
}

// endpointExposure is the compiled exposure of one endpoint: the rules by the ID
// of the repository they belong to, each list ordered by rule name.
type endpointExposure struct {
	byRepo map[int64][]*compiledRule
	// enabled counts the enabled rules, for trove_exposure_rules.
	enabled int
}

func decodeLists(raw string) (PatternLists, error) {
	var l PatternLists
	if raw == "" {
		return l, nil
	}
	err := json.Unmarshal([]byte(raw), &l)
	return l, err
}

func decodeDims(packages, paths, versions string, prereleases bool, tags string) (ruleDims, error) {
	d := ruleDims{Prereleases: prereleases}
	var err error
	if d.Packages, err = decodeLists(packages); err != nil {
		return d, fmt.Errorf("packages: %w", err)
	}
	if d.Paths, err = decodeLists(paths); err != nil {
		return d, fmt.Errorf("paths: %w", err)
	}
	if versions != "" {
		if err = json.Unmarshal([]byte(versions), &d.Versions); err != nil {
			return d, fmt.Errorf("versions: %w", err)
		}
	}
	if d.DistTags, err = decodeLists(tags); err != nil {
		return d, fmt.Errorf("distTags: %w", err)
	}
	return d, nil
}

// compileRule compiles the dimensions of one rule with the name rule of the
// repository's format (R-AUTHZ-005).
func compileRule(id, name string, revision int64, enabled bool, d ruleDims, names catalog.Names) (*compiledRule, error) {
	r := &compiledRule{id: id, name: name, revision: revision, enabled: enabled, prereleases: d.Prereleases}
	var err error
	if r.packages, err = compileSet(dimPackage, d.Packages.Include, d.Packages.Exclude, names); err != nil {
		return nil, fmt.Errorf("packages: %w", err)
	}
	if r.paths, err = compileSet(dimPath, d.Paths.Include, d.Paths.Exclude, nil); err != nil {
		return nil, fmt.Errorf("paths: %w", err)
	}
	// Versions and tags are names: the package-name glob without case folding.
	if r.versions, err = compileSet(dimPackage, d.Versions.Include, d.Versions.Exclude, nil); err != nil {
		return nil, fmt.Errorf("versions: %w", err)
	}
	if r.tags, err = compileSet(dimPackage, d.DistTags.Include, d.DistTags.Exclude, nil); err != nil {
		return nil, fmt.Errorf("distTags: %w", err)
	}
	if d.Versions.Range != nil {
		r.vrange = *d.Versions.Range
	}
	r.constrainsVersions = len(d.Versions.Include)+len(d.Versions.Exclude) > 0 || r.vrange != "" || !d.Prereleases
	return r, nil
}

// buildExposure compiles the rule rows of a snapshot. A rule whose repository the
// index does not know (deleted since) is left out; a rule that does not compile
// passed validation when written, so this signals a corrupt row: it is logged and
// left out, which only hides content.
func buildExposure(rows []q.EndpointsExposureBuildRow, byID map[int64]*repoInfo, reg *catalog.Registry, log *slog.Logger) map[string]*endpointExposure {
	out := map[string]*endpointExposure{}
	for _, row := range rows {
		repo := byID[row.RepoID]
		if repo == nil {
			continue
		}
		d, err := decodeDims(row.Packages, row.Paths, row.Versions, row.Prereleases, row.DistTags)
		var cr *compiledRule
		if err == nil {
			cr, err = compileRule(row.ID, row.Name, row.Revision, row.Enabled, d, namesIn(reg, repo.Format))
		}
		if err != nil {
			log.Error("authz: exposure rule skipped, it does not compile", "endpoint", row.Endpoint, "rule", row.Name, "err", err)
			continue
		}
		ee := out[row.Endpoint]
		if ee == nil {
			ee = &endpointExposure{byRepo: map[int64][]*compiledRule{}}
			out[row.Endpoint] = ee
		}
		ee.byRepo[row.RepoID] = append(ee.byRepo[row.RepoID], cr)
		if cr.enabled {
			ee.enabled++
		}
	}
	for _, ee := range out {
		for _, rules := range ee.byRepo {
			sort.Slice(rules, func(i, j int) bool { return rules[i].name < rules[j].name })
		}
	}
	return out
}

// exposureItem is what one check of a rule looks at.
type exposureItem struct {
	pkg, path, version, tag string
}

// stage numbers how far an item got through a rule; a higher stage is a better
// explanation of a refusal (R-EP-016 reasons).
const (
	stagePackage = iota + 1
	stagePath
	stageVersion
	stageRange
	stagePrerelease
	stageTag
	stageExposed
)

func reasonOfStage(s int) string {
	switch s {
	case stagePackage:
		return ExposurePackageExcluded
	case stagePath:
		return ExposurePathExcluded
	case stageVersion:
		return ExposureVersionExcluded
	case stageRange:
		return ExposureOutOfRange
	case stagePrerelease:
		return ExposurePrerelease
	case stageTag:
		return ExposureTagExcluded
	}
	return ExposureExposed
}

// evaluate runs one rule on an item as if the rule were enabled and returns the
// stage at which the item failed, or stageExposed. A repository-level item (no
// package, no path) passes every pattern: listed items are filtered per item
// (R-EP-012). An item with a version needs the format's versioning; without it
// the version cannot be judged and the rule refuses (fail closed).
func (r *compiledRule) evaluate(it exposureItem, v catalog.Versioning) int {
	if it.pkg == "" && it.path == "" {
		return stageExposed
	}
	if !r.packages.match(it.pkg, it.pkg != "") {
		return stagePackage
	}
	if !r.paths.match(it.path, it.path != "") {
		return stagePath
	}
	if it.version != "" {
		if !r.versions.match(it.version, true) {
			return stageVersion
		}
		if r.constrainsVersions && v == nil {
			return stageVersion
		}
		if r.vrange != "" && !v.InRange(it.version, r.vrange) {
			return stageRange
		}
		if !r.prereleases {
			if pre, ok := v.Prerelease(it.version); !ok || pre {
				return stagePrerelease
			}
		}
	}
	if it.tag != "" && !r.tags.match(it.tag, true) {
		return stageTag
	}
	return stageExposed
}

// admits reports whether at least one enabled rule of the list admits the item:
// rules add up like grants, and an exclude wins inside a rule only (R-EP-012).
func admitsItem(rules []*compiledRule, it exposureItem, v catalog.Versioning) bool {
	for _, r := range rules {
		if r.enabled && r.evaluate(it, v) == stageExposed {
			return true
		}
	}
	return false
}

// explainItem says why an item is exposed or hidden: the first rule by name that
// exposes it, else rule_disabled when a disabled rule would, else the reason of
// the rule that came furthest (the first by name among equals), else no_rule.
func explainItem(rules []*compiledRule, it exposureItem, v catalog.Versioning) (exposed bool, rule, reason string) {
	if len(rules) == 0 {
		return false, "", ExposureNoRule
	}
	best, disabled := 0, false
	for _, r := range rules {
		s := r.evaluate(it, v)
		if s == stageExposed {
			if r.enabled {
				return true, r.name, ExposureExposed
			}
			disabled = true
			continue
		}
		if s > best {
			best = s
		}
	}
	if disabled {
		return false, "", ExposureRuleDisabled
	}
	return false, "", reasonOfStage(best)
}

// fingerprint is the exposure fingerprint of R-EP-018: SHA-256 over the mode and
// the ID, revision and enabled flag of every rule of the endpoint for the
// addressed repository, in ID order; a constant for mode all.
func fingerprint(mode string, rules []*compiledRule) string {
	h := sha256.New()
	h.Write([]byte(mode))
	if mode == ExposureModeRules {
		ids := make([]*compiledRule, len(rules))
		copy(ids, rules)
		sort.Slice(ids, func(i, j int) bool { return ids[i].id < ids[j].id })
		for _, r := range ids {
			h.Write([]byte("\n" + r.id + ":" + strconv.FormatInt(r.revision, 10) + ":" + strconv.FormatBool(r.enabled)))
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ExposureCeiling is the endpoint exposure of R-EP-013 over the compiled rules of
// the index: endpoints in mode all admit everything; an endpoint in mode rules
// admits reads that an enabled rule of the addressed repository admits and no
// write (Q-EP-9). An endpoint name the configuration does not hold is treated as
// mode rules (fail closed); a coordinate without an endpoint (a job, a command)
// has no ceiling. What a format lets a rule narrow is the format's catalog entry
// ([catalog.Exposure]); a format the catalog does not know is not exposable.
type ExposureCeiling struct {
	modes map[string]string
	reg   *catalog.Registry
}

// NewExposureCeiling builds the ceiling from the configured endpoints' modes
// (name to "all" or "rules") and the format catalog; nil reg means
// catalog.Default.
func NewExposureCeiling(modes map[string]string, reg *catalog.Registry) *ExposureCeiling {
	m := make(map[string]string, len(modes))
	for k, v := range modes {
		m[k] = v
	}
	return &ExposureCeiling{modes: m, reg: catalog.Or(reg)}
}

// exposure returns what rules may narrow for a format; the zero value (not
// exposable) for a format the catalog does not know.
func (e *ExposureCeiling) exposure(format string) catalog.Exposure {
	en, _ := e.reg.Lookup(format)
	return en.Exposure
}

// names returns the name rule of a format, nil when it has none.
func (e *ExposureCeiling) names(format string) catalog.Names { return namesIn(e.reg, format) }

// Mode returns the exposure mode of an endpoint; an endpoint the configuration
// does not hold is mode rules.
func (e *ExposureCeiling) Mode(endpoint string) string {
	if m, ok := e.modes[endpoint]; ok && m == ExposureModeAll {
		return ExposureModeAll
	}
	return ExposureModeRules
}

// Endpoints lists the configured endpoints, sorted: the label values of the
// exposure metrics.
func (e *ExposureCeiling) Endpoints() []string {
	out := make([]string, 0, len(e.modes))
	for k := range e.modes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Admits implements [Ceiling].
func (e *ExposureCeiling) Admits(x *Index, endpoint string, c Coordinate, a Action) CeilingVerdict {
	if endpoint == "" || e.Mode(endpoint) == ExposureModeAll {
		return CeilingAdmits
	}
	if a != Read {
		return CeilingReadOnly
	}
	addressed := c.Addressed
	if addressed == "" {
		addressed = c.Repository
	}
	r := x.repo(addressed)
	if r == nil {
		return CeilingHidesRead
	}
	f := e.exposure(r.Format)
	if !f.Exposable {
		return CeilingHidesRead
	}
	ee := x.exposure[endpoint]
	if ee == nil {
		return CeilingHidesRead
	}
	if admitsItem(ee.byRepo[r.ID], exposureItem{pkg: c.Package, path: c.Path, version: c.Version}, f.Versioning) {
		return CeilingAdmits
	}
	return CeilingHidesRead
}

// ExposureFingerprint returns the exposure fingerprint of an endpoint and an
// addressed repository from the index, without a query (R-EP-018).
func (e *ExposureCeiling) ExposureFingerprint(x *Index, endpoint, repoKey string) string {
	mode := e.Mode(endpoint)
	if mode == ExposureModeAll {
		return fingerprint(mode, nil)
	}
	var rules []*compiledRule
	if r := x.repo(repoKey); r != nil {
		if ee := x.exposure[endpoint]; ee != nil {
			rules = ee.byRepo[r.ID]
		}
	}
	return fingerprint(mode, rules)
}

// exposedRules counts the enabled rules of an endpoint in the index, for the
// gauge trove_exposure_rules.
func (x *Index) exposedRules(endpoint string) int {
	if ee := x.exposure[endpoint]; ee != nil {
		return ee.enabled
	}
	return 0
}
