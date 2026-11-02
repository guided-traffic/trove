package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// rulesView is the decision of one rule the way the decision point takes it: a package
// include, the version range of the format's adapter and the pre-release switch. The
// decision point is not under test here; what the handlers do with the answer is.
type rulesView struct {
	x        *exposure
	packages map[string]bool
	rng      string
	pre      bool
}

func (v rulesView) Package(name string) bool { return v.packages[name] }

func (v rulesView) Version(name, ver string) bool {
	if !v.packages[name] {
		return false
	}
	if pre, ok := v.x.Prerelease(ver); !ok || (pre && !v.pre) {
		return false
	}
	return v.rng == "" || v.x.InRange(ver, v.rng)
}

func (v rulesView) endpoint(name, fingerprint string) *formats.EndpointFacts {
	return &formats.EndpointFacts{Name: name, Rules: true, ExposureFingerprint: fingerprint, Exposure: v}
}

// TestR_NUGET_034_RangeNotationOfRules: a rule's range is NuGet's interval notation, a bare
// version is a minimum, versions are compared after the normalization of R-NUGET-003, and a
// range the NuGet client's parser refuses is refused when the rule is written.
func TestR_NUGET_034_RangeNotationOfRules(t *testing.T) {
	x := newExposure()
	for _, c := range []struct {
		name, rng, ver string
		want           bool
	}{
		{"a bare version is a minimum, at it", "2.0", "2.0.0", true},
		{"a bare version is a minimum, above it", "2.0", "13.1.0", true},
		{"a bare version is a minimum, below it", "2.0", "1.9.9", false},
		{"a four-part version is normalized", "2.0", "2.0.0.0", true},
		{"a pre-release is below its release", "2.0", "2.0.0-beta.1", false},
		{"a half-open interval includes its minimum", "[2.0,4.0)", "2.0.0", true},
		{"a half-open interval excludes its maximum", "[2.0,4.0)", "4.0.0", false},
		{"a version below the maximum", "[2.0,4.0)", "3.9.9", true},
		{"an exact version", "[2.1.0]", "2.1.0.0", true},
		{"an exact version excludes the next", "[2.1.0]", "2.1.1", false},
		{"an open minimum", "(,1.0]", "0.1.0", true},
		{"an exclusive minimum", "(1.0,)", "1.0.0", false},
		{"the build metadata is not compared", "[1.0.0]", "1.0.0+abc", true},
		{"a text that is no version is outside every range", "1.0", "latest", false},
		{"a range that does not parse admits nothing", "[1.0", "1.0.0", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := x.InRange(c.ver, c.rng); got != c.want {
				t.Errorf("InRange(%q, %q) = %v, want %v", c.ver, c.rng, got, c.want)
			}
		})
	}
	for _, ok := range []string{"2.0", "[2.0,4.0)", "(,1.0]", "[1.0]", " [ 1.0 , 2.0 ) ", "1.0.0-beta.1"} {
		if err := x.ValidateRange(ok); err != nil {
			t.Errorf("ValidateRange(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1.*", "[1.0", "[2.0,1.0]", "^1.0", ">=1.0", "[1.0,2.0,3.0]", "[1.0,1.0)", strings.Repeat("1", 300)} {
		if err := x.ValidateRange(bad); err == nil {
			t.Errorf("ValidateRange(%q) accepted a range the NuGet client's parser refuses", bad)
		}
	}
	if d := x.Dimensions(); !d.Packages || !d.Versions || d.Tags {
		t.Errorf("dimensions = %+v, want packages and versions, no tags", d)
	}
	if pre, ok := x.Prerelease("2.1.0-beta.1"); !pre || !ok {
		t.Errorf("Prerelease(2.1.0-beta.1) = %v, %v", pre, ok)
	}
	if pre, ok := x.Prerelease("2.1.0+build"); pre || !ok {
		t.Errorf("Prerelease(2.1.0+build) = %v, %v", pre, ok)
	}
	if _, ok := x.Prerelease("nonsense"); ok {
		t.Error("Prerelease accepted a text that is no version")
	}
}

// TestR_NUGET_034_TheRangeMemoIsBounded: the parsed ranges the adapter keeps do not grow
// with the number of rules an administrator ever typed.
func TestR_NUGET_034_TheRangeMemoIsBounded(t *testing.T) {
	x := newExposure()
	for i := range 2 * maxRanges {
		x.InRange("1.0.0", fmt.Sprintf("[%d.0,)", i))
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	if len(x.ranges) > maxRanges {
		t.Errorf("the memo holds %d ranges, the bound is %d", len(x.ranges), maxRanges)
	}
}

// acmeSdkView is the rule of the acceptance criteria of R-NUGET-034: Acme.Sdk from 2.0.0, no
// pre-releases.
func acmeSdkView() rulesView {
	return rulesView{x: newExposure(), packages: map[string]bool{"acme.sdk": true}, rng: "[2.0.0, )"}
}

// registeredVersions lists the versions of the inlined leaves of a registration index.
func registeredVersions(t *testing.T, body []byte) []string {
	t.Helper()
	var d struct {
		Items []struct {
			Items []struct {
				Entry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	var out []string
	for _, p := range d.Items {
		for _, l := range p.Items {
			out = append(out, l.Entry.Version)
		}
	}
	return out
}

// TestR_NUGET_034_AcceptanceDocumentsOfTheExampleRule: the criteria of the spec on the
// documents: customers lists 2.0.0 and 2.1.0 in the version list and in every hive, the
// default endpoint lists all four, and the leaves and files of a hidden version answer 404.
func TestR_NUGET_034_AcceptanceDocumentsOfTheExampleRule(t *testing.T) {
	fx := newDocFixture(t)
	for _, v := range []string{"1.9.0", "2.0.0", "2.1.0-beta.1", "2.1.0"} {
		fx.putAll("acme.sdk", registrationVersion("Acme.Sdk", v, true, `{}`))
	}
	customers := acmeSdkView().endpoint("customers", "fp")
	if r := fx.do("GET", "v3/flatcontainer/acme.sdk/index.json", customers); r.Body.String() != `{"versions":["2.0.0","2.1.0"]}` {
		t.Errorf("customers version list = %d %s", r.Code, r.Body)
	}
	if r := fx.get("v3/flatcontainer/acme.sdk/index.json"); r.Body.String() != `{"versions":["1.9.0","2.0.0","2.1.0-beta.1","2.1.0"]}` {
		t.Errorf("default version list = %s", r.Body)
	}
	for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
		idx := fx.do("GET", "v3/"+hive+"/acme.sdk/index.json", customers)
		if idx.Code != 200 {
			t.Fatalf("%s index = %d", hive, idx.Code)
		}
		if got := registeredVersions(t, idx.Body.Bytes()); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Errorf("%s lists %v", hive, got)
		}
		for _, hidden := range []string{"1.9.0", "2.1.0-beta.1"} {
			if r := fx.do("GET", "v3/"+hive+"/acme.sdk/"+hidden+".json", customers); r.Code != http.StatusNotFound {
				t.Errorf("%s leaf of %s = %d", hive, hidden, r.Code)
			}
		}
		if r := fx.do("GET", "v3/"+hive+"/acme.sdk/2.0.0.json", customers); r.Code != http.StatusOK {
			t.Errorf("%s leaf of an exposed version = %d", hive, r.Code)
		}
	}
}

// TestR_NUGET_034_PagingRunsOverTheExposedVersions: count, lower, upper and the inlining of
// the registration index follow the exposed versions, not the stored ones.
func TestR_NUGET_034_PagingRunsOverTheExposedVersions(t *testing.T) {
	fx := newDocFixture(t)
	var vs []rows.RegistrationVersion
	for i := range 200 {
		vs = append(vs, registrationVersion("Acme.Sdk", fmt.Sprintf("1.0.%d", i), true, `{}`))
	}
	fx.putAll("acme.sdk", vs...)
	read := func(ep *formats.EndpointFacts) (count int, pages []map[string]any) {
		r := fx.do("GET", "v3/registration/acme.sdk/index.json", ep)
		var d struct {
			Count int              `json:"count"`
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &d); err != nil || r.Code != 200 {
			t.Fatalf("index = %d %v", r.Code, err)
		}
		return d.Count, d.Items
	}
	// 200 stored versions are fetched page by page (inline below 128); the 100 exposed ones
	// are inlined, in two pages of 64 and 36.
	if n, pages := read(nil); n != 4 || pages[0]["items"] != nil {
		t.Errorf("default: %d pages, first inlined %v", n, pages[0]["items"] != nil)
	}
	view := rulesView{x: newExposure(), packages: map[string]bool{"acme.sdk": true}, rng: "1.0.100"}
	customers := view.endpoint("customers", "fp")
	n, pages := read(customers)
	if n != 2 || pages[0]["lower"] != "1.0.100" || pages[0]["upper"] != "1.0.163" || pages[1]["lower"] != "1.0.164" || pages[1]["upper"] != "1.0.199" ||
		pages[0]["count"] != float64(64) || pages[1]["count"] != float64(36) || pages[0]["items"] == nil {
		t.Errorf("customers pages = %v", pages)
	}
	// A narrower rule changes the validator, so a client holding the earlier document is
	// answered with the new one.
	etag := fx.do("GET", "v3/registration/acme.sdk/index.json", customers).Header().Get("ETag")
	narrower := rulesView{x: newExposure(), packages: map[string]bool{"acme.sdk": true}, rng: "1.0.150"}.endpoint("customers", "fp2")
	r := fx.do("GET", "v3/registration/acme.sdk/index.json", narrower, "If-None-Match", etag)
	if r.Code != http.StatusOK || r.Header().Get("ETag") == etag {
		t.Errorf("after a rule narrowed: %d, ETag %q (was %q)", r.Code, r.Header().Get("ETag"), etag)
	}
}

// TestR_NUGET_034_ServiceIndexOfAnEndpointWithRulesListsNoPublish: the endpoint accepts no
// writes, so its service index names no package publish and no symbol publish resource, and
// its validator differs from the default endpoint's (Q-EP-68).
func TestR_NUGET_034_ServiceIndexOfAnEndpointWithRulesListsNoPublish(t *testing.T) {
	h := newHost(t, formattest.HostOptions{})
	serve := func(ep *formats.EndpointFacts) *formattest.Response {
		rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/index.json", nil)
		if err != nil {
			t.Fatal(err)
		}
		if ep != nil {
			rq.Endpoint = *ep
		}
		return h.ServeRequest(rq, false)
	}
	def, rules := serve(nil), serve(acmeSdkView().endpoint("customers", "fp"))
	if def.Code != 200 || rules.Code != 200 {
		t.Fatalf("status %d and %d", def.Code, rules.Code)
	}
	for _, res := range []string{"PackagePublish/2.0.0", "SymbolPackagePublish/4.9.0"} {
		if !strings.Contains(def.Body.String(), res) {
			t.Errorf("the default endpoint does not list %s: %s", res, def.Body)
		}
		if strings.Contains(rules.Body.String(), res) {
			t.Errorf("the endpoint with rules lists %s: %s", res, rules.Body)
		}
	}
	for _, res := range []string{"PackageBaseAddress/3.0.0", "RegistrationsBaseUrl/3.6.0", "SearchQueryService/3.5.0"} {
		if !strings.Contains(rules.Body.String(), res) {
			t.Errorf("the endpoint with rules lacks the read resource %s: %s", res, rules.Body)
		}
	}
	if def.Header().Get("ETag") == rules.Header().Get("ETag") {
		t.Error("the service index of both endpoints has one validator")
	}
}

// TestR_NUGET_034_SearchOnAnEndpointWithRules: the read scope that holds the rules decides
// each version; version is the latest exposed one under the request's filters, versions are
// the exposed ones and totalHits counts exposed packages only.
func TestR_NUGET_034_SearchOnAnEndpointWithRules(t *testing.T) {
	st := listOf(
		pkg("Acme.Sdk", "1.9.0", "2.0.0", "2.1.0-beta.1", "2.1.0", "3.0.0-rc.1"),
		pkg("Acme.Internal", "1.0.0"),
		pkg("Acme.Old", "1.0.0"),
	)
	view := acmeSdkView()
	view.packages["acme.old"] = true // exposed package without an exposed version
	scope := scopeFake{shape: formats.ReadRows, may: func(p, v string) bool { return view.Version(p, v) }}
	for _, query := range []string{"q=acme&prerelease=true", "q=acme"} {
		r := doSearch(t, searchHost(t, st, scope), query)
		d := decodeSearch(t, r)
		if d.TotalHits != 1 || len(d.Data) != 1 || d.Data[0].Name != "Acme.Sdk" || d.Data[0].Version != "2.1.0" {
			t.Fatalf("%s: %s", query, r.Body)
		}
		var got []string
		for _, v := range d.Data[0].Versions {
			got = append(got, v.Version)
		}
		if !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Errorf("%s: versions %v", query, got)
		}
		for _, hidden := range []string{"1.9.0", "beta", "3.0.0", "Internal", "Acme.Old"} {
			if strings.Contains(r.Body.String(), hidden) {
				t.Errorf("%s: the result names %q", query, hidden)
			}
		}
	}
}

// TestR_NUGET_034_HiddenVersionAndRulesEndpointQuestions: the questions the handlers ask.
func TestR_NUGET_034_HiddenVersionAndRulesEndpointQuestions(t *testing.T) {
	h := newHost(t, formattest.HostOptions{})
	rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/index.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if hiddenVersion(rq, "acme.sdk", "1.0.0") || onRulesEndpoint(rq) {
		t.Error("an endpoint without rules hides a version or counts as one with rules")
	}
	rq.Endpoint = *acmeSdkView().endpoint("customers", "fp")
	for _, c := range []struct {
		id, ver string
		want    bool
	}{{"acme.sdk", "2.0.0", false}, {"acme.sdk", "1.9.0", true}, {"acme.other", "2.0.0", true}} {
		if got := hiddenVersion(rq, c.id, c.ver); got != c.want {
			t.Errorf("hiddenVersion(%s, %s) = %v", c.id, c.ver, got)
		}
	}
	if !onRulesEndpoint(rq) {
		t.Error("an endpoint with rules is not recognized")
	}
	rq.Endpoint = formats.EndpointFacts{Name: "x", Rules: true} // rules and no view: nothing is exposed
	if !hiddenVersion(rq, "acme.sdk", "2.0.0") {
		t.Error("an endpoint with rules and no view exposes a version")
	}
}
