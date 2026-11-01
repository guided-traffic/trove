package search

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

func term(field, value string, phrase bool) rows.SearchTerm {
	return rows.SearchTerm{Field: field, Value: value, Phrase: phrase}
}

// TestR_NUGET_010_ParseSplitsTheQueryIntoTerms: words, quoted phrases, field prefixes in any
// case, the prefix that is no field, a field with a phrase, an unterminated quote, folding.
func TestR_NUGET_010_ParseSplitsTheQueryIntoTerms(t *testing.T) {
	cases := []struct {
		name, q string
		want    []rows.SearchTerm
	}{
		{"empty", "", nil},
		{"only white space", " \t\n ", nil},
		{"one word", "acme", []rows.SearchTerm{term("", "acme", false)}},
		{"words fold to lower case", "Acme  WIDGETS\tNewtonsoft.Json", []rows.SearchTerm{term("", "acme", false), term("", "widgets", false), term("", "newtonsoft.json", false)}},
		{"phrase", `"big widget" acme`, []rows.SearchTerm{term("", "big widget", true), term("", "acme", false)}},
		{"phrase collapses white space", `"  big   widget "`, []rows.SearchTerm{term("", "big widget", true)}},
		{"empty phrase is dropped", `"" acme " "`, []rows.SearchTerm{term("", "acme", false)}},
		{"unterminated phrase runs to the end", `acme "big widget`, []rows.SearchTerm{term("", "acme", false), term("", "big widget", true)}},
		{"a quote inside a word ends the word", `acme"big widget"`, []rows.SearchTerm{term("", "acme", false), term("", "big widget", true)}},
		{"id", "id:Acme", []rows.SearchTerm{term("id", "acme", false)}},
		{"packageid", "packageId:Acme.Widgets", []rows.SearchTerm{term("packageid", "acme.widgets", false)}},
		{"every field", "title:a tags:b author:c description:d summary:e owner:f id:g packageid:h", []rows.SearchTerm{
			term("title", "a", false), term("tags", "b", false), term("author", "c", false), term("description", "d", false),
			term("summary", "e", false), term("owner", "f", false), term("id", "g", false), term("packageid", "h", false),
		}},
		{"field with a phrase", `title:"Big Widget" acme`, []rows.SearchTerm{term("title", "big widget", true), term("", "acme", false)}},
		{"unknown prefix is free text", "foo:bar http://x", []rows.SearchTerm{term("", "foo:bar", false), term("", "http://x", false)}},
		{"field without a value is dropped", "title: acme", []rows.SearchTerm{term("", "acme", false)}},
		{"colon first is free text", ":acme", []rows.SearchTerm{term("", ":acme", false)}},
		{"only the first colon counts", "title:a:b", []rows.SearchTerm{term("title", "a:b", false)}},
		{"version is the URL form", "version:1.0.0.0 version:1.0.0-Beta.1+build", []rows.SearchTerm{term("version", "1.0.0", false), term("version", "1.0.0-beta.1", false)}},
		{"a version that is none matches nothing", "version:abc", []rows.SearchTerm{term("version", "", false)}},
		{"non-ASCII bytes are no separators", "büro x \u0085y", []rows.SearchTerm{term("", "büro x", false), term("", "\u0085y", false)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, more := Parse(c.q)
			if more {
				t.Error("more")
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Parse(%q) = %+v, want %+v", c.q, got, c.want)
			}
		})
	}
}

// TestR_NUGET_010_ParseBoundsTheNumberOfTerms: the terms beyond the bound are reported, not
// silently dropped.
func TestR_NUGET_010_ParseBoundsTheNumberOfTerms(t *testing.T) {
	got, more := Parse(strings.Repeat("a ", MaxTerms))
	if len(got) != MaxTerms || more {
		t.Errorf("%d terms, more %v", len(got), more)
	}
	got, more = Parse(strings.Repeat("a ", MaxTerms+1))
	if len(got) != MaxTerms || !more {
		t.Errorf("%d terms, more %v", len(got), more)
	}
}

func values(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

var limits = Limits{DefaultTake: 20, MaxTake: 1000, MaxSkip: 3000}

// TestR_NUGET_010_ParamsDefaultsAndBounds: absent parameters, empty ones, the largest values
// and the clamp.
func TestR_NUGET_010_ParamsDefaultsAndBounds(t *testing.T) {
	cases := []struct {
		name       string
		v          url.Values
		skip, take int
	}{
		{"none", values(), 0, 20},
		{"empty values", values("skip", "", "take", ""), 0, 20},
		{"explicit", values("skip", "5", "take", "7"), 5, 7},
		{"largest skip", values("skip", "3000"), 3000, 20},
		{"take zero", values("take", "0"), 0, 0},
		{"take is clamped", values("take", "1001"), 0, 1000},
		{"take far above an int is clamped", values("take", "99999999999999999999999"), 0, 1000},
		{"first value of a repeated parameter", values("take", "3", "take", "9"), 0, 3},
		{"other parameters are ignored", values("testData", "1", "foo", "bar"), 0, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := ParseParams(c.v, limits)
			if err != nil {
				t.Fatal(err)
			}
			if p.Skip != c.skip || p.Take != c.take {
				t.Errorf("skip %d take %d, want %d %d", p.Skip, p.Take, c.skip, c.take)
			}
		})
	}
}

// TestR_NUGET_010_ParamsRefuseWhatIsOutOfBounds: skip above max_skip, negative or not an
// integer, take negative or not an integer, a query too long or with too many terms.
func TestR_NUGET_010_ParamsRefuseWhatIsOutOfBounds(t *testing.T) {
	for name, v := range map[string]url.Values{
		"skip above max":        values("skip", "3001"),
		"skip negative":         values("skip", "-1"),
		"skip signed":           values("skip", "+1"),
		"skip fraction":         values("skip", "1.0"),
		"skip text":             values("skip", "x"),
		"skip far above":        values("skip", "99999999999999999999999"),
		"take negative":         values("take", "-5"),
		"take text":             values("take", "all"),
		"take hex":              values("take", "0x10"),
		"q too long":            values("q", strings.Repeat("a", MaxQueryBytes+1)),
		"q with too many terms": values("q", strings.Repeat("a ", MaxTerms+1)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseParams(v, limits)
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.Invalid || e.Code != errs.CodeInvalid {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// TestR_NUGET_004_SemVerLevelIsParsedAsAVersion: 2.0.0 and above include the SemVer 2.0.0
// versions; absent, unparseable and lower values do not (R-NUGET-004), and prerelease is
// true only for "true" in any case (R-NUGET-010).
func TestR_NUGET_004_SemVerLevelIsParsedAsAVersion(t *testing.T) {
	for level, want := range map[string]bool{
		"": false, "1.0.0": false, "1": false, "2": true, "2.0": true, "2.0.0": true, "2.0.0.0": true, "2.1.0": true,
		"3.0.0": true, "10.0.0": true, "junk": false, "2.0.0-beta": false, "2.0.0+meta": true, "-2": false, "1.9.9": false,
	} {
		p, err := ParseParams(values("semVerLevel", level), limits)
		if err != nil {
			t.Fatal(err)
		}
		if p.SemVer2 != want {
			t.Errorf("semVerLevel=%q: %v, want %v", level, p.SemVer2, want)
		}
	}
	for v, want := range map[string]bool{"true": true, "TRUE": true, "True": true, "false": false, "1": false, "yes": false, "": false} {
		p, _ := ParseParams(values("prerelease", v), limits)
		if p.Prerelease != want {
			t.Errorf("prerelease=%q: %v, want %v", v, p.Prerelease, want)
		}
	}
}

// TestR_NUGET_010_PackageTypeIsLowerCasedAndValidated.
func TestR_NUGET_010_PackageTypeIsLowerCasedAndValidated(t *testing.T) {
	for in, want := range map[string]struct {
		typ     string
		noMatch bool
	}{
		"DotnetTool":                             {"dotnettool", false},
		"Template":                               {"template", false},
		"":                                       {"", false},
		"has space":                              {"", true},
		"tab\there":                              {"", true},
		"nul\x00":                                {"", true},
		strings.Repeat("a", MaxPackageTypeBytes): {strings.Repeat("a", MaxPackageTypeBytes), false},
		strings.Repeat("a", MaxPackageTypeBytes+1): {"", true},
	} {
		p, err := ParseParams(values("packageType", in), limits)
		if err != nil {
			t.Fatal(err)
		}
		if p.PackageType != want.typ || p.NoMatch != want.noMatch {
			t.Errorf("packageType=%.20q: %q noMatch %v, want %q %v", in, p.PackageType, p.NoMatch, want.typ, want.noMatch)
		}
	}
}

// TestR_NUGET_010_IDTokensSplitAtSeparatorsAndCaseChanges.
func TestR_NUGET_010_IDTokensSplitAtSeparatorsAndCaseChanges(t *testing.T) {
	for in, want := range map[string]string{
		"Acme.Widgets":          "Acme Widgets",
		"acme.widgets":          "acme widgets",
		"Newtonsoft.Json":       "Newtonsoft Json",
		"NewtonsoftJson":        "Newtonsoft Json",
		"Acme-Core_Utils":       "Acme Core Utils",
		"System.Text.Json":      "System Text Json",
		"ABCDef":                "ABCDef",
		"Net6Core":              "Net6 Core",
		"a..b--c__d":            "a b c d",
		".Leading":              "Leading",
		"Trailing.":             "Trailing",
		"Microsoft.AspNetCore":  "Microsoft Asp Net Core",
		"xUnit":                 "x Unit",
		"":                      "",
		"Ünïcode.Ärger":         "Ünïcode Ärger",
		"Acme.Widgets.Extra.V2": "Acme Widgets Extra V2",
	} {
		if got := IDTokens(in); got != want {
			t.Errorf("IDTokens(%q) = %q, want %q", in, got, want)
		}
	}
}

func hit(display string, versions ...string) rows.SearchHit {
	h := rows.SearchHit{LowerID: strings.ToLower(display), DisplayName: display}
	for _, v := range versions {
		lower, _, _ := strings.Cut(strings.ToLower(v), "+") // the URL form has no build metadata
		h.Listed = append(h.Listed, rows.VersionEntry{LowerVersion: lower, FullVersion: v, Listed: true})
	}
	h.Latest = rows.RegistrationVersion{VersionEntry: h.Listed[len(h.Listed)-1], PackageTypes: []string{"dependency"}}
	return h
}

// TestR_NUGET_010_RenderBuildsTheResultDocument: members, order of versions, hive links, the
// metadata of the latest version in the shapes the ingestion may store.
func TestR_NUGET_010_RenderBuildsTheResultDocument(t *testing.T) {
	h := hit("Acme.Widgets", "1.0.0", "1.1.0-ci.5+sha.abc")
	h.Latest.Metadata = []byte(`{"title":"T","summary":"S","description":"D","authors":"A, B","tags":["x","y"],"iconUrl":"I","licenseUrl":"L","projectUrl":"P",` +
		`"packageTypes":[{"name":"DotnetTool","version":""}]}`)
	b, err := Render("https://t/api/nuget/k/v3/", true, 7, []rows.SearchHit{h})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		TotalHits int
		Data      []map[string]any
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.TotalHits != 7 || len(d.Data) != 1 {
		t.Fatalf("%s", b)
	}
	r := d.Data[0]
	index := "https://t/api/nuget/k/v3/registration-semver2/acme.widgets/index.json"
	for k, want := range map[string]any{
		"@id": index, "@type": "Package", "registration": index, "id": "Acme.Widgets", "version": "1.1.0-ci.5+sha.abc",
		"title": "T", "summary": "S", "description": "D", "authors": "A, B", "iconUrl": "I", "licenseUrl": "L", "projectUrl": "P",
		"totalDownloads": float64(0),
	} {
		if r[k] != want {
			t.Errorf("%s = %v, want %v", k, r[k], want)
		}
	}
	if tags, _ := json.Marshal(r["tags"]); string(tags) != `["x","y"]` {
		t.Errorf("tags = %s", tags)
	}
	if ty, _ := json.Marshal(r["packageTypes"]); string(ty) != `[{"name":"DotnetTool"}]` {
		t.Errorf("packageTypes = %s", ty)
	}
	vs, _ := json.Marshal(r["versions"])
	want := `[{"@id":"https://t/api/nuget/k/v3/registration-semver2/acme.widgets/1.0.0.json","downloads":0,"version":"1.0.0"},` +
		`{"@id":"https://t/api/nuget/k/v3/registration-semver2/acme.widgets/1.1.0-ci.5.json","downloads":0,"version":"1.1.0-ci.5+sha.abc"}]`
	if string(vs) != want {
		t.Errorf("versions = %s\nwant       %s", vs, want)
	}
	for _, k := range []string{"owners", "verified", "deprecation", "vulnerabilities"} {
		if _, ok := r[k]; ok {
			t.Errorf("%s is present", k)
		}
	}
}

// TestR_NUGET_010_RenderIsTolerantOfStoredMetadata: tags as a text, authors as a list, no
// metadata at all, a hit without versions and an empty result keep the arrays arrays.
func TestR_NUGET_010_RenderIsTolerantOfStoredMetadata(t *testing.T) {
	h := hit("A", "1.0.0")
	h.Latest.Metadata = []byte(`{"tags":"net json","authors":["A","B"]}`)
	h.Latest.PackageTypes = nil
	b, err := Render("f/", false, 1, []rows.SearchHit{h})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"tags":["net","json"]`, `"authors":"A, B"`, `"packageTypes":[{"name":"Dependency"}]`, `"@id":"f/registration/a/index.json"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	h.Latest.Metadata = nil
	if b, err = Render("f/", false, 1, []rows.SearchHit{h}); err != nil || !strings.Contains(string(b), `"tags":[]`) {
		t.Errorf("%v: %s", err, b)
	}
	if b, _ = Render("f/", false, 0, nil); string(b) != `{"totalHits":0,"data":[]}` {
		t.Errorf("empty result = %s", b)
	}
	h.Latest.Metadata = []byte(`{`)
	if _, err = Render("f/", false, 1, []rows.SearchHit{h}); err == nil {
		t.Error("broken metadata rendered")
	}
}
