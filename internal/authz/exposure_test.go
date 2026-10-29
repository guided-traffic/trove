package authz

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ruleDef describes one exposure rule of a fixture.
type ruleDef struct {
	endpoint, name, repo string
	disabled             bool
	revision             int64
	dims                 ruleDims
}

// exposure adds an exposure rule row the way EndpointsExposureBuild reads it.
func (f *fixture) exposure(d ruleDef) *fixture {
	id := f.repoID[d.repo]
	if id == 0 {
		panic("unknown repository " + d.repo)
	}
	if d.revision == 0 {
		d.revision = 1
	}
	n := normInput(ExposureRuleInput{Packages: d.dims.Packages, Paths: d.dims.Paths, Versions: d.dims.Versions, DistTags: d.dims.DistTags})
	f.s.Exposure = append(f.s.Exposure, q.EndpointsExposureBuildRow{
		ID: "rule-" + d.endpoint + "-" + d.name, Endpoint: d.endpoint, Name: d.name, RepoID: id, Enabled: !d.disabled,
		Packages: mustText(n.Packages), Paths: mustText(n.Paths), Versions: mustText(n.Versions),
		Prereleases: d.dims.Prereleases, DistTags: mustText(n.DistTags), Revision: d.revision,
	})
	return f
}

func paths(include ...string) ruleDims { return ruleDims{Paths: PatternLists{Include: include}} }

// semverish is a test versioning: MAJOR.MINOR.PATCH[-pre], ranges of the form
// ">=X.Y.Z" or ">=X.Y.Z <X.Y.Z". It stands in for a format's adapter, which no
// format of this build has (R-EP-018).
type semverish struct{}

func parseSemver(v string) (n [3]int, pre bool, ok bool) {
	core, tail, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return n, false, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil {
			return n, false, false
		}
		n[i] = x
	}
	return n, hasPre && tail != "", true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func (semverish) ValidateRange(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return fmt.Errorf("an empty range")
	}
	for _, part := range strings.Fields(expr) {
		var v string
		switch {
		case strings.HasPrefix(part, ">="):
			v = part[2:]
		case strings.HasPrefix(part, "<"):
			v = part[1:]
		default:
			return fmt.Errorf("unsupported comparator %q", part)
		}
		if _, _, ok := parseSemver(v); !ok {
			return fmt.Errorf("%q is not a version", v)
		}
	}
	return nil
}

func (semverish) Prerelease(v string) (bool, bool) {
	_, pre, ok := parseSemver(v)
	return pre, ok
}

func (semverish) InRange(v, expr string) bool {
	n, _, ok := parseSemver(v)
	if !ok {
		return false
	}
	for _, part := range strings.Fields(expr) {
		if strings.HasPrefix(part, ">=") {
			b, _, _ := parseSemver(part[2:])
			if less(n, b) {
				return false
			}
		} else if strings.HasPrefix(part, "<") {
			b, _, _ := parseSemver(part[1:])
			if !less(n, b) {
				return false
			}
		}
	}
	return true
}

// testFormats is generic (paths only, as in this build) plus an npm-like format
// with package names, versions and tags for the version dimensions.
func testFormats(format string) ExposureFormat {
	switch format {
	case "generic":
		return ExposureFormat{Exposable: true}
	case "npm":
		return ExposureFormat{Exposable: true, Packages: true, Versions: true, Tags: true, Versioning: semverish{}}
	}
	return ExposureFormat{}
}

func onEP(c Coordinate, ep string) Coordinate { c.Endpoint = ep; return c }

func ptr[T any](v T) *T { return &v }

func TestR_EP_012_RuleMatching(t *testing.T) {
	compile := func(t *testing.T, d ruleDims, enabled bool) *compiledRule {
		t.Helper()
		n := normInput(ExposureRuleInput{Packages: d.Packages, Paths: d.Paths, Versions: d.Versions, DistTags: d.DistTags})
		d.Packages, d.Paths, d.Versions, d.DistTags = n.Packages, n.Paths, n.Versions, n.DistTags
		r, err := compileRule("id", "r", 1, enabled, d, false)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	t.Run("R-EP-014 a generic rule exposes through paths, an exclude wins inside the rule", func(t *testing.T) {
		r := compile(t, ruleDims{Paths: PatternLists{Include: []string{"releases/**"}, Exclude: []string{"releases/**/*.sig"}}}, true)
		for _, c := range []struct {
			path string
			want bool
		}{
			{"releases/1.0/app.zip", true},
			{"releases/app.zip", true},
			{"internal/x.bin", false},
			{"releases/1.0/app.zip.sig", false},
			{"release/1.0/app.zip", false},
		} {
			if got := admitsItem([]*compiledRule{r}, exposureItem{path: c.path}, nil); got != c.want {
				t.Errorf("%s: %v, want %v", c.path, got, c.want)
			}
		}
	})
	t.Run("R-EP-012 rules add up like grants and a disabled rule admits nothing", func(t *testing.T) {
		a := compile(t, paths("a/**"), true)
		b := compile(t, paths("b/**"), true)
		off := compile(t, paths("c/**"), false)
		rules := []*compiledRule{a, b, off}
		for p, want := range map[string]bool{"a/x": true, "b/y": true, "c/z": false, "d/w": false} {
			if got := admitsItem(rules, exposureItem{path: p}, nil); got != want {
				t.Errorf("%s: %v, want %v", p, got, want)
			}
		}
	})
	t.Run("R-EP-012 a repository-level coordinate is admitted by any enabled rule", func(t *testing.T) {
		if !admitsItem([]*compiledRule{compile(t, paths("x/**"), true)}, exposureItem{}, nil) {
			t.Fatal("an enabled rule does not admit the repository")
		}
		if admitsItem([]*compiledRule{compile(t, paths("x/**"), false)}, exposureItem{}, nil) {
			t.Fatal("a disabled rule admits the repository")
		}
		if admitsItem(nil, exposureItem{}, nil) {
			t.Fatal("no rule admits the repository")
		}
	})
	t.Run("R-EP-012 the fixture: range, version exclude, pre-release and package exclude", func(t *testing.T) {
		r := compile(t, ruleDims{
			Packages: PatternLists{Include: []string{"@acme/sdk", "@acme/sdk-*"}, Exclude: []string{"@acme/sdk-internal"}},
			Versions: VersionLists{Exclude: []string{"2.0.1"}, Range: ptr(">=2.0.0")},
		}, true)
		v := semverish{}
		for _, c := range []struct {
			pkg, version, reason string
		}{
			{"@acme/sdk", "2.0.0", ExposureExposed},
			{"@acme/sdk", "2.1.0", ExposureExposed},
			{"@acme/sdk", "1.9.0", ExposureOutOfRange},
			{"@acme/sdk", "2.0.1", ExposureVersionExcluded},
			{"@acme/sdk", "2.1.0-rc.1", ExposurePrerelease},
			{"@acme/sdk-internal", "1.0.0", ExposurePackageExcluded},
			{"@acme/billing", "3.0.0", ExposurePackageExcluded},
		} {
			ok, rule, reason := explainItem([]*compiledRule{r}, exposureItem{pkg: c.pkg, version: c.version}, v)
			if reason != c.reason || ok != (c.reason == ExposureExposed) || (ok && rule != "r") {
				t.Errorf("%s@%s: %v %q %q, want %q", c.pkg, c.version, ok, rule, reason, c.reason)
			}
		}
		// A package document (no version) is admitted when the package is.
		if ok, _, _ := explainItem([]*compiledRule{r}, exposureItem{pkg: "@acme/sdk"}, v); !ok {
			t.Error("the package document of @acme/sdk is hidden")
		}
	})
	t.Run("R-EP-018 a version without the format's versioning is refused (fail closed)", func(t *testing.T) {
		r := compile(t, ruleDims{}, true)
		if admitsItem([]*compiledRule{r}, exposureItem{path: "a", version: "1.0.0"}, nil) {
			t.Fatal("a version was judged without the format's versioning")
		}
	})
	t.Run("R-EP-012 a tag is visible when the rule admits the tag name", func(t *testing.T) {
		r := compile(t, ruleDims{DistTags: PatternLists{Include: []string{"latest", "lts"}}, Prereleases: true}, true)
		for tag, want := range map[string]bool{"latest": true, "lts": true, "next": false} {
			if got := admitsItem([]*compiledRule{r}, exposureItem{pkg: "p", tag: tag}, semverish{}); got != want {
				t.Errorf("%s: %v", tag, got)
			}
		}
	})
}

func TestR_EP_016_ExplainReasons(t *testing.T) {
	mk := func(name string, enabled bool, d ruleDims) *compiledRule {
		n := normInput(ExposureRuleInput{Packages: d.Packages, Paths: d.Paths, Versions: d.Versions, DistTags: d.DistTags})
		d.Packages, d.Paths, d.Versions, d.DistTags = n.Packages, n.Paths, n.Versions, n.DistTags
		r, err := compileRule("id-"+name, name, 1, enabled, d, false)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rel := mk("releases", true, ruleDims{Paths: PatternLists{Include: []string{"releases/**"}, Exclude: []string{"releases/beta/**"}}})
	off := mk("internal", false, paths("internal/**"))
	rules := []*compiledRule{off, rel}
	sort.Slice(rules, func(i, j int) bool { return rules[i].name < rules[j].name })
	for _, c := range []struct {
		path, rule, reason string
	}{
		{"releases/1.0/app.zip", "releases", ExposureExposed},
		{"internal/x.bin", "", ExposureRuleDisabled},
		{"releases/beta/x.zip", "", ExposurePathExcluded},
		{"other/y", "", ExposurePathExcluded},
	} {
		ok, rule, reason := explainItem(rules, exposureItem{path: c.path}, nil)
		if rule != c.rule || reason != c.reason || ok != (reason == ExposureExposed) {
			t.Errorf("%s: %v %q %q, want %q %q", c.path, ok, rule, reason, c.rule, c.reason)
		}
	}
	if _, _, reason := explainItem(nil, exposureItem{path: "x"}, nil); reason != ExposureNoRule {
		t.Errorf("no rule: %q", reason)
	}
	t.Run("R-EP-018 the fingerprint follows the ID, revision and enabled flag of every rule, and is constant for mode all", func(t *testing.T) {
		a := fingerprint(ExposureModeRules, []*compiledRule{rel, off})
		if a != fingerprint(ExposureModeRules, []*compiledRule{off, rel}) {
			t.Fatal("the fingerprint depends on the order of the rules")
		}
		bumped := *rel
		bumped.revision = 2
		disabled := *rel
		disabled.enabled = false
		if a == fingerprint(ExposureModeRules, []*compiledRule{&bumped, off}) || a == fingerprint(ExposureModeRules, []*compiledRule{&disabled, off}) {
			t.Fatal("a revision or an enabled change keeps the fingerprint")
		}
		if fingerprint(ExposureModeAll, nil) != fingerprint(ExposureModeAll, []*compiledRule{rel}) || !strings.HasPrefix(a, "sha256:") {
			t.Fatal("mode all is not a constant")
		}
	})
}

func exposureFixture() *fixture {
	f := newFixture().
		repo("generic-local", "local", "generic", "").
		repo("generic-other", "local", "generic", "").
		repo("generic-auth", "local", "generic", `{"requireAuthentication": true}`).
		repo("files-virtual", "virtual", "generic", `{"virtual": {"members": ["generic-local"]}}`).
		repo("npm-customers", "local", "npm", "")
	f.policy(policyDef{name: "customers-read", selectors: []string{"@any"}, entries: map[string][]string{idBot: {"read"}, idDevs: {"read", "deploy"}}})
	f.policy(policyDef{name: "anonymous-read", selectors: []string{"generic-local", "generic-other", "generic-auth"}, entries: map[string][]string{AnonymousID: {"read"}}})
	f.bind(RoleAdmin, idOps)
	f.exposure(ruleDef{endpoint: "customers", name: "releases", repo: "generic-local", dims: paths("releases/**")})
	f.exposure(ruleDef{endpoint: "customers", name: "virtual", repo: "files-virtual", dims: paths("releases/**")})
	f.exposure(ruleDef{endpoint: "customers", name: "off", repo: "generic-other", disabled: true, dims: paths("**")})
	f.exposure(ruleDef{endpoint: "customers", name: "sdk", repo: "npm-customers", dims: ruleDims{
		Packages: PatternLists{Include: []string{"@acme/sdk"}}, Versions: VersionLists{Range: ptr(">=2.0.0")},
	}})
	return f
}

func exposureCeiling() *ExposureCeiling {
	return NewExposureCeiling(map[string]string{"default": ExposureModeAll, "customers": ExposureModeRules, "partners": ExposureModeAll}, testFormats)
}

func TestR_EP_013_ExposureCeiling(t *testing.T) {
	f := exposureFixture()
	reg := prometheus.NewRegistry()
	e := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling(); o.Metrics = reg })
	on := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling(); o.AnonymousAccess = func() bool { return true } })
	n := 0
	runAuthzCases(t, e, []authzCase{
		{"R-EP-014 a SAT reads an exposed file on customers", botSub, onEP(read("generic-local", "", "releases/1.0/app.zip"), "customers"), Allow, ReasonAllowGrant, ""},
		{"R-EP-014 a file outside the rule is a hidden 404", botSub, onEP(read("generic-local", "", "internal/x.bin"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-014 the same file on default is readable", botSub, onEP(read("generic-local", "", "internal/x.bin"), "default"), Allow, ReasonAllowGrant, ""},
		{"R-EP-013 a repository the index does not know is the same 404", botSub, onEP(read("ghost", "", "releases/1.0/app.zip"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 a disabled rule exposes nothing", botSub, onEP(read("generic-other", "", "a"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 Q-EP-12 an administrator sees no more than the rules expose", adminSub, onEP(read("generic-local", "", "internal/x.bin"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 Q-EP-12 an administrator reads an exposed file through content:bypass", adminSub, onEP(read("generic-local", "", "releases/a"), "customers"), Allow, ReasonAllowRole, ""},
		{"R-EP-013 Q-EP-12 the break-glass administrator is bound too", breakSub, onEP(read("generic-local", "", "internal/x.bin"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 exposure never grants", carolSub, onEP(read("generic-local", "", "releases/a"), "customers"), DenyHidden, ReasonNoGrant, Read},
		{"R-EP-013 Q-EP-9 a write on customers is 403 read-only", aliceSub, onEP(write("generic-local", "", "releases/a", Deploy), "customers"), DenyVisible, ReasonEndpointReadOnly, Deploy},
		{"R-EP-013 a write is read-only whether or not its target exists", aliceSub, onEP(write("ghost", "", "x", Deploy, Overwrite), "customers"), DenyVisible, ReasonEndpointReadOnly, Deploy},
		{"R-EP-013 a write is read-only before a read is hidden", aliceSub, onEP(Coordinate{Repository: "ghost", Path: "x", Kind: KindWrite, Actions: []Action{Read, Deploy}}, "customers"), DenyVisible, ReasonEndpointReadOnly, Deploy},
		{"R-EP-013 R-AUTHZ-016 an anonymous request is 401 first", anonSub, onEP(read("generic-local", "", "releases/a"), "customers"), DenyUnauthenticated, ReasonUnauthenticated, ""},
		{"R-EP-013 Q-EP-11 exposure is decided on the addressed virtual repository", botSub, onEP(Coordinate{Repository: "generic-local", Addressed: "files-virtual", Path: "releases/a", Kind: KindArtifact, Actions: []Action{Read}}, "customers"), Allow, ReasonAllowGrant, ""},
		{"R-EP-013 Q-EP-11 a member coordinate outside the virtual repository's rule is hidden", botSub, onEP(Coordinate{Repository: "generic-local", Addressed: "files-virtual", Path: "internal/x", Kind: KindArtifact, Actions: []Action{Read}}, "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 the virtual repository itself passes its gate on customers", botSub, onEP(read("files-virtual", "", "releases/a"), "customers"), Allow, "", ""},
		{"R-EP-013 a path of the virtual repository outside its rule is hidden", botSub, onEP(read("files-virtual", "", "internal/x"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 an endpoint name the configuration does not hold is closed", botSub, onEP(read("generic-local", "", "releases/a"), "nowhere"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 a coordinate without an endpoint has no ceiling (a job)", botSub, read("generic-local", "", "internal/x.bin"), Allow, ReasonAllowGrant, ""},
		{"R-EP-018 a version of an npm-like format is decided by the rule's range", botSub, onEP(Coordinate{Repository: "npm-customers", Package: "@acme/sdk", Version: "2.1.0", Kind: KindArtifact, Actions: []Action{Read}}, "customers"), Allow, "", ""},
		{"R-EP-018 a version below the range is hidden", botSub, onEP(Coordinate{Repository: "npm-customers", Package: "@acme/sdk", Version: "1.9.0", Kind: KindArtifact, Actions: []Action{Read}}, "customers"), DenyHidden, ReasonExposure, Read},
	}, &n)
	runAuthzCases(t, on, []authzCase{
		{"R-EP-013 an anonymous read the rules admit passes on an anonymous endpoint", anonSub, onEP(read("generic-local", "", "releases/a"), "customers"), Allow, "", ""},
		{"R-EP-013 an anonymous read the rules do not admit is 404, not 401", anonSub, onEP(read("generic-local", "", "internal/x"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 Q-EP-4 a repository that requires authentication and has no rule is the same 404", anonSub, onEP(read("generic-auth", "", "a"), "customers"), DenyHidden, ReasonExposure, Read},
		{"R-EP-013 Q-EP-4 a missing repository is the same 404 for anonymous", anonSub, onEP(read("ghost", "", "a"), "customers"), DenyHidden, ReasonExposure, Read},
	}, &n)

	t.Run("R-EP-013 Q-EP-4 a hidden file and a missing file answer the same error", func(t *testing.T) {
		hidden := e.Decide(t.Context(), botSub, onEP(read("generic-local", "", "internal/x.bin"), "customers")).Err()
		missing := e.Decide(t.Context(), botSub, onEP(read("ghost", "", "internal/x.bin"), "customers")).Err()
		hp, _ := errs.ProblemOf(hidden, "/x", "r")
		mp, _ := errs.ProblemOf(missing, "/x", "r")
		np, _ := errs.ProblemOf(NotFound(), "/x", "r")
		if fmt.Sprint(hp) != fmt.Sprint(mp) || fmt.Sprint(hp) != fmt.Sprint(np) {
			t.Fatalf("hidden %+v, missing %+v, not found %+v", hp, mp, np)
		}
	})
	t.Run("R-EP-013 R-EP-002 the endpoint of the request context replaces the coordinate's", func(t *testing.T) {
		ctx := WithEndpoint(t.Context(), "customers")
		d := e.Decide(ctx, botSub, onEP(read("generic-local", "", "internal/x.bin"), "default"))
		if d.Outcome != DenyHidden || d.Reason != ReasonExposure {
			t.Fatalf("%+v", d)
		}
		idx, err := e.Begin(ctx, botSub)
		if err != nil {
			t.Fatal(err)
		}
		if d := e.DecideOn(ctx, idx, botSub, read("generic-local", "", "internal/x.bin")); d.Reason != ReasonExposure {
			t.Fatalf("DecideOn: %+v", d)
		}
	})
	t.Run("R-EP-013 on an endpoint in mode all every decision is the default endpoint's", func(t *testing.T) {
		for _, c := range []Coordinate{
			read("generic-local", "", "internal/x.bin"), write("generic-local", "", "x", Deploy), read("ghost", "", "x"), metadata("npm-customers"),
		} {
			for _, s := range []Subject{aliceSub, botSub, carolSub, adminSub} {
				def, partners := e.Decide(t.Context(), s, onEP(c, "default")), e.Decide(t.Context(), s, onEP(c, "partners"))
				if def.Outcome != partners.Outcome || def.Reason != partners.Reason {
					t.Errorf("%s %+v: default %+v, partners %+v", s.Name, c, def, partners)
				}
			}
		}
	})
	t.Run("R-EP-014 listings on customers hold only repositories with an enabled rule", func(t *testing.T) {
		keys := []string{"files-virtual", "generic-auth", "generic-local", "generic-other", "npm-customers"}
		got, err := e.Readable(WithEndpoint(t.Context(), "customers"), adminSub, keys)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"files-virtual", "generic-local", "npm-customers"}; !slices.Equal(got, want) {
			t.Fatalf("customers lists %v, want %v", got, want)
		}
		all, _ := e.Readable(WithEndpoint(t.Context(), "default"), adminSub, keys)
		if !slices.Equal(all, keys) {
			t.Fatalf("default lists %v", all)
		}
		if v := metricValue(t, e.m.hidden.WithLabelValues("customers", "item")); v != 2 {
			t.Fatalf("trove_exposure_hidden_total{kind=item} = %v, want 2", v)
		}
	})
	t.Run("R-EP-009 the exposure metrics count refusals per endpoint and the enabled rules of the index", func(t *testing.T) {
		if v := metricValue(t, e.m.hidden.WithLabelValues("customers", "request")); v == 0 {
			t.Fatal("no refusal counted")
		}
		if v := metricValue(t, e.m.rules.WithLabelValues("customers")); v != 3 {
			t.Fatalf("trove_exposure_rules{customers} = %v, want 3 enabled rules", v)
		}
		if v := metricValue(t, e.m.rules.WithLabelValues("default")); v != 0 {
			t.Fatalf("trove_exposure_rules{default} = %v", v)
		}
	})
	t.Run("R-EP-012 R-AUTHZ-028 a rule change takes effect with the next index", func(t *testing.T) {
		f2 := exposureFixture()
		f2.exposure(ruleDef{endpoint: "customers", name: "internal", repo: "generic-local", dims: paths("internal/**")})
		f2.s.Label.AuthzEpoch++
		src := e.o.Source.(*fakeSource)
		src.set(f2.s)
		if err := e.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if d := e.Decide(t.Context(), botSub, onEP(read("generic-local", "", "internal/x.bin"), "customers")); d.Outcome != Allow {
			t.Fatalf("after the new rule: %+v", d)
		}
		src.set(exposureFixture().s)
		f3 := exposureFixture()
		f3.s.Label.AuthzEpoch += 2
		src.set(f3.s)
		if err := e.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if d := e.Decide(t.Context(), botSub, onEP(read("generic-local", "", "internal/x.bin"), "customers")); d.Outcome != DenyHidden {
			t.Fatalf("after the rule was deleted: %+v", d)
		}
	})
	t.Run("R-EP-018 the fingerprint is read from the index", func(t *testing.T) {
		cl := exposureCeiling()
		x := e.Index()
		a := cl.ExposureFingerprint(x, "customers", "generic-local")
		if a == cl.ExposureFingerprint(x, "customers", "generic-other") || cl.ExposureFingerprint(x, "default", "generic-local") != fingerprint(ExposureModeAll, nil) {
			t.Fatal("fingerprints do not follow the rules")
		}
	})
	t.Logf("%d exposure cases", n)
}

func TestR_EP_013_CeilingPerformance(t *testing.T) {
	// 1,000 rules on one endpoint for one repository, none of which admits the
	// item: the worst case of the ceiling (R-EP-013 "Performance").
	f := newFixture().repo("generic-local", "local", "generic", "")
	f.policy(policyDef{name: "read", selectors: []string{"@any"}, entries: map[string][]string{idBot: {"read"}}})
	for i := range MaxExposureRules {
		f.exposure(ruleDef{endpoint: "customers", name: fmt.Sprintf("r%04d", i), repo: "generic-local", dims: paths(fmt.Sprintf("dir%04d/**", i), "a/*/b/**")})
	}
	// The test measures the decision, not the staleness bound: building 1,000
	// rules and 2,000 decisions can outlast the 10 s of testConfig under the race
	// detector on a loaded machine, and the engine then answers deny.stale.
	e := newTestEngine(t, f, func(o *Options) {
		o.Ceiling = exposureCeiling()
		o.Config.Cache.MaxStaleness = time.Hour
	})
	c := onEP(read("generic-local", "", "nowhere/x/y.bin"), "customers")
	const n = 2000
	took := make([]time.Duration, 0, n)
	for range n {
		start := time.Now()
		d := e.Decide(context.Background(), botSub, c)
		took = append(took, time.Since(start))
		if d.Outcome != DenyHidden {
			t.Fatalf("%+v", d)
		}
	}
	slices.Sort(took)
	p99 := took[n*99/100]
	t.Logf("p99 of one Decide with 1,000 rules: %s", p99)
	if !raceDetector && p99 > time.Millisecond {
		t.Fatalf("p99 %s is above the 1 ms target of R-AUTHZ-015", p99)
	}
}

// metricValue reads a counter or a gauge.
func metricValue(t *testing.T, c prometheus.Metric) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	if m.Counter != nil {
		return m.GetCounter().GetValue()
	}
	return m.GetGauge().GetValue()
}

func TestR_EP_012_Validation(t *testing.T) {
	s := NewExposureService(ExposureServiceOptions{Ceiling: exposureCeiling()})
	codes := func(ps []ProblemField) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Pointer+" "+p.Code)
		}
		return out
	}
	t.Run("R-EP-012 the name, the description, the patterns and the range length", func(t *testing.T) {
		long := strings.Repeat("x", 2001)
		ps := validateSyntax(ExposureRuleInput{
			Name: "Bad Name", Description: long,
			Paths:    PatternLists{Include: []string{"a//b", "releases/**"}},
			Packages: PatternLists{Exclude: []string{`trailing\`}},
			Versions: VersionLists{Range: ptr(strings.Repeat("1", 257))},
		}, "")
		want := []string{
			"/name pattern", "/description max_length", "/repository required", "/packages/exclude/0 invalid_pattern",
			"/paths/include/0 invalid_pattern", "/versions/range invalid_version_range",
		}
		if got := codes(ps); !slices.Equal(got, want) {
			t.Fatalf("%v\nwant %v", got, want)
		}
		many := make([]string, 257)
		for i := range many {
			many[i] = "p" + strconv.Itoa(i)
		}
		if got := codes(validateSyntax(ExposureRuleInput{Name: "ok", Repository: "r", Paths: PatternLists{Include: many}}, "/rule")); !slices.Equal(got, []string{"/rule/paths/include invalid_pattern"}) {
			t.Fatalf("a list of 257 patterns: %v", got)
		}
	})
	t.Run("R-EP-014 R-EP-018 the dimensions a format supports", func(t *testing.T) {
		in := ExposureRuleInput{
			Name: "r", Repository: "generic-local", Packages: PatternLists{Include: []string{"x"}},
			Versions: VersionLists{Include: []string{"1.*"}}, Prereleases: true, DistTags: PatternLists{Include: []string{"latest"}},
		}
		want := []string{"/packages dimension_not_supported", "/versions dimension_not_supported", "/prereleases dimension_not_supported", "/distTags dimension_not_supported"}
		if got := codes(s.validateForFormat(in, "generic", "")); !slices.Equal(got, want) {
			t.Fatalf("generic: %v", got)
		}
		if got := codes(s.validateForFormat(ExposureRuleInput{Name: "r"}, "maven", "")); !slices.Equal(got, []string{"/repository format_not_exposable"}) {
			t.Fatalf("a format without exposure support: %v", got)
		}
		if got := codes(s.validateForFormat(ExposureRuleInput{Versions: VersionLists{Range: ptr(">=2.0.0 <")}}, "npm", "")); !slices.Equal(got, []string{"/versions/range invalid_version_range"}) {
			t.Fatalf("an incomplete range: %v", got)
		}
		if got := s.validateForFormat(in, "npm", ""); len(got) != 0 {
			t.Fatalf("npm-like accepts every dimension: %v", codes(got))
		}
	})
	t.Run("R-EP-012 warnings: path patterns on a package format, a rule on an endpoint in mode all", func(t *testing.T) {
		if got := s.warnings("customers", "npm", ExposureRuleInput{Paths: PatternLists{Include: []string{"x/**"}}}); !slices.Equal(got, []string{WarnPathPatternsOnPackageFormat}) {
			t.Fatalf("%v", got)
		}
		if got := s.warnings("partners", "generic", ExposureRuleInput{}); !slices.Equal(got, []string{WarnEndpointNotInRulesMode}) {
			t.Fatalf("%v", got)
		}
	})
	t.Run("R-EP-020 the journal entry of an update carries IDs and both states; a deletion its IDs", func(t *testing.T) {
		r := ExposureRule{ID: "id-1", Endpoint: "customers", RepositoryID: 7, ExposureRuleInput: ExposureRuleInput{Name: "releases", Repository: "generic-local", Enabled: true}}
		before, after := stateOfRule(r.ExposureRuleInput), stateOfRule(ExposureRuleInput{Enabled: false})
		up := exposureEvent(t.Context(), EventExposureUpdated, aliceSub, r, before, after, []string{"enabled"}, 9, "")
		if !strings.Contains(string(up.Journal), `"ruleId":"id-1"`) || !strings.Contains(string(up.Journal), `"repositoryId":7`) ||
			!strings.Contains(string(up.Journal), `"after":{"enabled":false`) {
			t.Fatalf("%s", up.Journal)
		}
		del := exposureEvent(t.Context(), EventExposureDeleted, aliceSub, r, before, nil, nil, 9, "api")
		if !strings.Contains(string(del.Journal), `"name":"releases"`) || strings.Contains(string(del.Journal), "before") {
			t.Fatalf("%s", del.Journal)
		}
		if created := exposureEvent(t.Context(), EventExposureCreated, aliceSub, r, nil, after, nil, 9, ""); created.Journal != nil {
			t.Fatal("a create carries a journal entry")
		}
		if got := changedMembers(ExposureRuleInput{Enabled: true}, ExposureRuleInput{Enabled: false, Description: "x"}); !slices.Equal(got, []string{"description", "enabled"}) {
			t.Fatalf("%v", got)
		}
	})
}
