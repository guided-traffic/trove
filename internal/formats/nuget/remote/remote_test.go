package remote

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// nugetOrgIndex is the shape of the service index of nuget.org: the resources spread over several
// hosts, several aliases of one type, and the registration hives in the order the index lists them.
const nugetOrgIndex = `{"version":"3.0.0","resources":[
 {"@id":"https://azuresearch-usnc.nuget.org/query","@type":"SearchQueryService"},
 {"@id":"https://azuresearch-usnc.nuget.org/query","@type":"SearchQueryService/3.5.0"},
 {"@id":"https://azuresearch-usnc.nuget.org/autocomplete","@type":"SearchAutocompleteService/3.5.0"},
 {"@id":"https://api.nuget.org/v3/registration5-semver1/","@type":"RegistrationsBaseUrl"},
 {"@id":"https://api.nuget.org/v3/registration5-gz-semver1/","@type":"RegistrationsBaseUrl/3.4.0"},
 {"@id":"https://api.nuget.org/v3/registration5-gz-semver2/","@type":"RegistrationsBaseUrl/3.6.0"},
 {"@id":"https://api.nuget.org/v3-flatcontainer/","@type":"PackageBaseAddress/3.0.0"},
 {"@id":"https://www.nuget.org/api/v2/package","@type":"PackagePublish/2.0.0"}]}`

func TestR_NUGET_014_DiscoveryPicksTheResourcesInTheOrderOfTheSpec(t *testing.T) {
	d, err := ParseDiscovery([]byte(nugetOrgIndex))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"content": d.Content.String(), "registration": d.Registration.String(), "search": d.Search.String(), "autocomplete": d.Autocomplete.String(),
	}
	want := map[string]string{
		"content": "https://api.nuget.org/v3-flatcontainer/", "registration": "https://api.nuget.org/v3/registration5-gz-semver2/",
		"search": "https://azuresearch-usnc.nuget.org/query/", "autocomplete": "https://azuresearch-usnc.nuget.org/autocomplete/",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if d.RegistrationType != "RegistrationsBaseUrl/3.6.0" {
		t.Errorf("registration type = %q", d.RegistrationType)
	}
}

func TestR_NUGET_014_DiscoveryFallsBackToTheOlderRegistrationTypes(t *testing.T) {
	for name, tc := range map[string]struct{ types, want string }{
		"3.4.0":      {`"RegistrationsBaseUrl/3.4.0"`, "RegistrationsBaseUrl/3.4.0"},
		"unversoned": {`"RegistrationsBaseUrl"`, "RegistrationsBaseUrl"},
		"a list":     {`["Other","RegistrationsBaseUrl/3.4.0","RegistrationsBaseUrl/3.6.0"]`, "RegistrationsBaseUrl/3.6.0"},
	} {
		doc := `{"resources":[{"@id":"https://h.test/c/","@type":"PackageBaseAddress/3.0.0"},{"@id":"https://h.test/r","@type":` + tc.types + `}]}`
		d, err := ParseDiscovery([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.RegistrationType != tc.want || d.Registration.String() != "https://h.test/r/" {
			t.Errorf("%s: %q %s", name, d.RegistrationType, d.Registration)
		}
		if d.Search != nil || d.Autocomplete != nil {
			t.Errorf("%s: a resource the index does not name", name)
		}
	}
}

func TestR_NUGET_014_DiscoveryRefusesAnIndexWithoutTheRequiredResources(t *testing.T) {
	for name, tc := range map[string]struct{ doc, mentions string }{
		"no package content": {`{"resources":[{"@id":"https://h.test/r/","@type":"RegistrationsBaseUrl/3.6.0"}]}`, "PackageBaseAddress/3.0.0"},
		"no registration":    {`{"resources":[{"@id":"https://h.test/c/","@type":"PackageBaseAddress/3.0.0"}]}`, "RegistrationsBaseUrl"},
		"no resources":       {`{"resources":[]}`, "no resources"},
		"not json":           {`<html>nope</html>`, "not a JSON document"},
		"a v2 feed":          {`{"resources":[{"@id":"https://h.test/api/v2","@type":"LegacyGallery"}]}`, "PackageBaseAddress/3.0.0"},
		"ftp address":        {`{"resources":[{"@id":"ftp://h.test/c/","@type":"PackageBaseAddress/3.0.0"},{"@id":"https://h.test/r/","@type":"RegistrationsBaseUrl"}]}`, "cannot be used"},
		"user info":          {`{"resources":[{"@id":"https://u:p@h.test/c/","@type":"PackageBaseAddress/3.0.0"},{"@id":"https://h.test/r/","@type":"RegistrationsBaseUrl"}]}`, "user info"},
		"relative address":   {`{"resources":[{"@id":"/c/","@type":"PackageBaseAddress/3.0.0"},{"@id":"https://h.test/r/","@type":"RegistrationsBaseUrl"}]}`, "absolute"},
	} {
		_, err := ParseDiscovery([]byte(tc.doc))
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.UpstreamFailed || e.Code != errs.CodeUpstreamError || !strings.Contains(e.Detail, tc.mentions) {
			t.Errorf("%s: %v, want a 502 upstream_error mentioning %q", name, err, tc.mentions)
		}
	}
}

func TestR_NUGET_015_JoinEscapesEverySegmentOnce(t *testing.T) {
	base, _ := url.Parse("https://h.test/v3-flat/")
	u, err := Join(base, "Ünïcode.pkg", "1.0.0", "a b.nupkg")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "https://h.test/v3-flat/%C3%9Cn%C3%AFcode.pkg/1.0.0/a%20b.nupkg" {
		t.Errorf("url = %s", u)
	}
	for _, bad := range []string{"", ".", "..", "a\x00b", `a\b`} {
		if _, err := Join(base, "id", bad); err == nil {
			t.Errorf("segment %q was joined", bad)
		}
	}
	// A segment with a slash stays one segment.
	u, err = Join(base, "a/b")
	if err != nil || u.String() != "https://h.test/v3-flat/a%2Fb" {
		t.Errorf("a/b = %v %v", u, err)
	}
}

func TestR_NUGET_015_SearchURLSortsTheParameters(t *testing.T) {
	d, _ := ParseDiscovery([]byte(nugetOrgIndex))
	u, ok := d.SearchURL(url.Values{"take": {"20"}, "q": {"json"}, "prerelease": {"true"}})
	if !ok || u.String() != "https://azuresearch-usnc.nuget.org/query?prerelease=true&q=json&take=20" {
		t.Errorf("url = %v %v", u, ok)
	}
	d.Search = nil
	if _, ok := d.SearchURL(nil); ok {
		t.Error("a search URL for an upstream without a search resource")
	}
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestR_ARCH_028_DecodeInflatesGzipWithinTheBoundAndOnlyThen(t *testing.T) {
	doc := []byte(`{"versions":["1.0.0"]}`)
	got, err := Decode(gz(t, doc), 1<<20)
	if err != nil || !bytes.Equal(got, doc) {
		t.Fatalf("gzip: %q %v", got, err)
	}
	if got, err = Decode(doc, 1<<20); err != nil || !bytes.Equal(got, doc) {
		t.Fatalf("plain: %q %v", got, err)
	}
	// A gzip bomb is bounded by what it inflates to, not by its size.
	bomb := gz(t, bytes.Repeat([]byte("a"), 10<<20))
	if len(bomb) > 100<<10 {
		t.Fatalf("the bomb is %d bytes", len(bomb))
	}
	_, err = Decode(bomb, 1<<20)
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamItemTooLarge {
		t.Errorf("bomb: %v, want upstream_item_too_large", err)
	}
	// A plain document above the bound is refused as well.
	if _, err = Decode(bytes.Repeat([]byte("a"), 100), 50); err == nil {
		t.Error("an oversize plain document was accepted")
	}
	// A truncated stream is an upstream failure.
	if _, err = Decode(gz(t, doc)[:12], 1<<20); err == nil {
		t.Error("a truncated gzip stream was accepted")
	}
}

func TestR_NUGET_015_VersionListReadsTheUpstreamsDocument(t *testing.T) {
	got, err := VersionList(gz(t, []byte(`{"versions":["1.0.0","2.0.0-Beta"]}`)), 1<<20)
	if err != nil || strings.Join(got, ",") != "1.0.0,2.0.0-Beta" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{`{}`, `[]`, `{"versions":"1.0.0"}`, `not json`, `{"versions":[1]}`} {
		_, err := VersionList([]byte(bad), 1<<20)
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeNuGetInvalidUpstreamDocument || e.Kind != errs.UpstreamFailed {
			t.Errorf("%q: %v, want 502 nuget_invalid_upstream_document", bad, err)
		}
	}
}

const sampleLeaf = `{"@id":"https://up.test/reg/acme/1.0.0.json","@type":"Package","commitId":"c","catalogEntry":{
 "@id":"https://up.test/cat/acme.1.0.0.json","@type":"PackageDetails","id":"Acme","version":"1.0.0+build.5","authors":["A","B"],
 "description":"D","iconUrl":"https://icons.test/i.png","licenseExpression":"MIT","licenseUrl":"https://l.test/","listed":false,
 "published":"2020-01-02T03:04:05.123+00:00","requireLicenseAcceptance":false,"tags":["x","y"],"title":"T","readmeUrl":"https://up.test/readme",
 "dependencyGroups":[{"@type":"PackageDependencyGroup","targetFramework":".NETStandard2.0","dependencies":[
   {"@type":"PackageDependency","id":"Dep","range":"[1.0.0-rc.1.2, )","registration":"https://up.test/reg/dep/index.json"}]}],
 "deprecation":{"message":"old","reasons":["Legacy"]},"vulnerabilities":[{"advisoryUrl":"https://github.com/advisories/x","severity":"2"}],
 "packageHash":"abc","packageHashAlgorithm":"SHA512","packageSize":12,"copyright":"(c) A","verbatimVersion":"1.0.0+build.5",
 "repository":"https://github.com/acme/acme","aliases":{"a":["https://x.test/"]},"extra":{"k":[1,2]}},
 "packageContent":"https://up.test/flat/acme/1.0.0/acme.1.0.0.nupkg","registration":"https://up.test/reg/acme/index.json"}`

func leafOf(t *testing.T, doc string) Leaf {
	t.Helper()
	var raw rawLeaf
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatal(err)
	}
	return Leaf{PackageContent: raw.PackageContent, Entry: raw.CatalogEntry}
}

func TestR_NUGET_015_ConvertTakesTheMetadataTheDocumentsRender(t *testing.T) {
	v, ok := Convert(leafOf(t, sampleLeaf), "acme")
	if !ok {
		t.Fatal("the leaf was not converted")
	}
	row := v.Row
	if row.LowerVersion != "1.0.0" || row.FullVersion != "1.0.0+build.5" || row.DeclaredID != "Acme" || row.Listed || !row.IsSemver2 {
		t.Errorf("row = %+v", row.VersionEntry)
	}
	if got := row.PublishedAt.UTC().Format("2006-01-02T15:04:05"); got != "2020-01-02T03:04:05" {
		t.Errorf("published = %s", got)
	}
	var m map[string]any
	if err := json.Unmarshal(row.Metadata, &m); err != nil {
		t.Fatal(err)
	}
	if m["authors"] != "A, B" || m["description"] != "D" || m["title"] != "T" || m["requireLicenseAcceptance"] != false {
		t.Errorf("metadata = %s", row.Metadata)
	}
	if lic, _ := m["license"].(map[string]any); lic["type"] != "expression" || lic["value"] != "MIT" {
		t.Errorf("license = %v", m["license"])
	}
	groups, _ := m["dependencyGroups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", m["dependencyGroups"])
	}
	g := groups[0].(map[string]any)
	dep := g["dependencies"].([]any)[0].(map[string]any)
	if g["targetFramework"] != ".NETStandard2.0" || dep["id"] != "Dep" || dep["version"] != "[1.0.0-rc.1.2, )" {
		t.Errorf("group = %v", g)
	}
	if string(v.Extras.Deprecation) != `{"message":"old","reasons":["Legacy"]}` || !strings.Contains(string(v.Extras.Vulnerabilities), "advisoryUrl") {
		t.Errorf("extras = %s %s", v.Extras.Deprecation, v.Extras.Vulnerabilities)
	}
	// The unknown members that hold no address are kept in key order, the others dropped (Q-NUGET-52).
	var keys []string
	for _, m := range v.Extras.Other {
		keys = append(keys, m.Key)
	}
	if got := strings.Join(keys, ","); got != "@type,copyright,extra,packageHash,packageHashAlgorithm,packageSize,verbatimVersion" {
		t.Errorf("kept members = %s", got)
	}
}

func TestR_NUGET_004_ARangeWithASemVer2BoundMakesTheVersionSemVer2(t *testing.T) {
	leaf := func(version, rng string) Leaf {
		return leafOf(t, `{"catalogEntry":{"id":"A","version":"`+version+`","dependencyGroups":[{"dependencies":[{"id":"B","range":"`+rng+`"}]}]}}`)
	}
	for name, tc := range map[string]struct {
		version, rng string
		want         bool
	}{
		"plain":            {"1.0.0", "[1.0.0, 2.0.0)", false},
		"a dotted bound":   {"1.0.0", "[1.0.0-beta.1, )", true},
		"build metadata":   {"1.0.0", "[1.0.0+m, )", true},
		"a dotted version": {"1.0.0-beta.2", "[1.0.0, )", true},
		"no range":         {"1.0.0", "", false},
		"a broken range":   {"1.0.0", "[[", false},
	} {
		v, ok := Convert(leaf(tc.version, tc.rng), "a")
		if !ok || v.Row.IsSemver2 != tc.want {
			t.Errorf("%s: ok=%v semver2=%v, want %v", name, ok, v.Row.IsSemver2, tc.want)
		}
	}
}

func TestR_NUGET_015_ConvertSkipsALeafWithoutAVersionAndKeepsAbsentValuesAbsent(t *testing.T) {
	if _, ok := Convert(leafOf(t, `{"catalogEntry":{"id":"A","version":"one"}}`), "a"); ok {
		t.Error("a leaf with a version that does not parse was converted")
	}
	if _, ok := Convert(Leaf{}, "a"); ok {
		t.Error("a leaf without a catalog entry was converted")
	}
	v, ok := Convert(leafOf(t, `{"catalogEntry":{"version":"1.0.0"}}`), "acme")
	if !ok || v.Row.DeclaredID != "acme" || !v.Row.Listed || string(v.Row.Metadata) != "{}" {
		t.Errorf("minimal entry: %v %+v %s", ok, v.Row, v.Row.Metadata)
	}
	if v.Extras.Deprecation != nil || v.Extras.Vulnerabilities != nil || len(v.Extras.Other) != 0 {
		t.Errorf("extras of a minimal entry: %+v", v.Extras)
	}
}

func TestR_NUGET_015_MergeListsAVersionOnceAndAscending(t *testing.T) {
	mk := func(v string) Leaf { return leafOf(t, `{"catalogEntry":{"id":"A","version":"`+v+`"}}`) }
	vs := Merge([]Leaf{mk("2.0.0"), mk("1.0.0-beta.2"), mk("1.0.0"), mk("1.0.0.0"), mk("1.0.0-BETA.10"), mk("junk")}, "a")
	var got []string
	for _, v := range vs {
		got = append(got, v.Row.LowerVersion)
	}
	if strings.Join(got, ",") != "1.0.0-beta.2,1.0.0-beta.10,1.0.0,2.0.0" {
		t.Errorf("versions = %v", got)
	}
}

func TestR_NUGET_015_ParseIndexTellsInlinedPagesFromPagesToFetch(t *testing.T) {
	doc := `{"count":2,"items":[
	  {"@id":"https://up.test/reg/a/index.json#page/1.0.0/1.0.0","count":1,"lower":"1.0.0","upper":"1.0.0","items":[` + sampleLeaf + `]},
	  {"@id":"https://up.test/reg/a/page/2.0.0/3.0.0.json","count":9,"lower":"2.0.0","upper":"3.0.0"}]}`
	for name, in := range map[string][]byte{"plain": []byte(doc), "gzip": gz(t, []byte(doc))} {
		idx, err := ParseIndex(in, 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(idx.Pages) != 2 || !idx.Pages[0].Inline || len(idx.Pages[0].Leaves) != 1 || idx.Pages[1].Inline {
			t.Fatalf("%s: pages = %+v", name, idx.Pages)
		}
		if p := idx.Pages[1]; p.ID != "https://up.test/reg/a/page/2.0.0/3.0.0.json" || p.Lower != "2.0.0" || p.Upper != "3.0.0" {
			t.Errorf("%s: page = %+v", name, p)
		}
		if l := idx.Pages[0].Leaves[0]; l.PackageContent != "https://up.test/flat/acme/1.0.0/acme.1.0.0.nupkg" {
			t.Errorf("%s: packageContent = %q", name, l.PackageContent)
		}
	}
	for name, bad := range map[string]string{
		"not json":              `nope`,
		"a page without bounds": `{"items":[{"@id":"https://up.test/p.json"}]}`,
		"leaves of no kind":     `{"items":[{"@id":"x","lower":"1","upper":"2","items":"nope"}]}`,
	} {
		_, err := ParseIndex([]byte(bad), 1<<20)
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeNuGetInvalidUpstreamDocument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestR_NUGET_015_ParsePageNeedsItsLeaves(t *testing.T) {
	ls, err := ParsePage([]byte(`{"@id":"p","lower":"1.0.0","upper":"1.0.0","items":[`+sampleLeaf+`]}`), 1<<20)
	if err != nil || len(ls) != 1 {
		t.Fatalf("%v %v", ls, err)
	}
	if _, err := ParsePage([]byte(`{"@id":"p","lower":"1.0.0","upper":"1.0.0"}`), 1<<20); err == nil {
		t.Error("a page without leaves was accepted")
	}
}

func TestR_NUGET_015_ContentURLAcceptsOnlyAbsoluteWebAddresses(t *testing.T) {
	u, err := ContentURL(" https://h.test/a/b.nupkg#frag ")
	if err != nil || u.String() != "https://h.test/a/b.nupkg" {
		t.Errorf("%v %v", u, err)
	}
	for _, bad := range []string{"", "/a/b", "ftp://h.test/a", "file:///etc/passwd", "https://u@h.test/a", "https:///a", "javascript:alert(1)"} {
		if _, err := ContentURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
