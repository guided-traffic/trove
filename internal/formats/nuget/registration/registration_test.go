package registration

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

var update = flag.Bool("update", false, "rewrite the golden documents in testdata")

const feed = Feed("https://trove.example.com/api/nuget/nuget-local/v3/")

var published = time.Date(2026, 10, 28, 9, 14, 3, 987654000, time.UTC)

// entryOf builds the row of a version the way the store hands it over: the forms of the
// version computed by the version rules, the metadata a JSON object.
func entryOf(t testing.TB, v string, listed bool, metadata string) rows.RegistrationVersion {
	t.Helper()
	p := version.MustParse(v)
	return rows.RegistrationVersion{
		VersionEntry: rows.VersionEntry{
			VersionID: 1, LowerVersion: p.URLForm(), FullVersion: p.FullNormalized(), DeclaredID: "Acme.Widgets",
			Listed: listed, IsPrerelease: p.IsPrerelease(), IsSemver2: p.IsSemver2(), PublishedAt: published,
		},
		PackageTypes: []string{"dependency"},
		Metadata:     []byte(metadata),
	}
}

const fullMetadata = `{
  "title": "Widgets", "summary": "Widgets in short.", "description": "Widget primitives <fast> & small.",
  "language": "en-US", "authors": "Acme Platform Team", "tags": ["widgets", "ui"],
  "projectUrl": "https://acme.example/widgets", "iconUrl": "https://acme.example/icon.png",
  "licenseUrl": "https://licenses.nuget.org/MIT", "license": {"type": "expression", "version": "1.0.0", "value": "MIT"},
  "requireLicenseAcceptance": false, "minClientVersion": "4.1.0", "releaseNotes": "not shown", "copyright": "not shown",
  "dependencyGroups": [
    {"targetFramework": "net8.0", "dependencies": [
      {"id": "Acme.Core", "version": "[2.0.0, )"},
      {"id": "Unrelated", "version": ""},
      {"id": "Ünicode.Dep", "version": "1.0.0"}]},
    {"targetFramework": "netstandard2.0"},
    {"dependencies": [{"id": "Flat.Dep", "version": "1.0.0"}]}
  ]
}`

func fixture(t testing.TB) []rows.RegistrationVersion {
	return []rows.RegistrationVersion{
		entryOf(t, "1.2.3", true, fullMetadata),
		entryOf(t, "1.3.0-beta", false, `{"description": "beta"}`),
		entryOf(t, "1.4.0-ci.5+sha.abc", true, `{"description": "ci build", "authors": "CI"}`),
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil { //nolint:gosec // test data
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Errorf("%s differs from the golden document (run go test -update to see the change):\n got: %s\nwant: %s", name, got, want)
	}
}

func doc(h Hive) Doc {
	return Doc{Feed: feed, Hive: h, LowerID: "acme.widgets", Paging: Paging{PageSize: 64, InlineBelow: 128}}
}

// TestR_NUGET_009_GoldenDocuments pins the bytes of every registration document of the
// fixture package: the index with its inlined pages in the SemVer 2 and in the SemVer 1
// hive, a page fetched on its own, and a leaf.
func TestR_NUGET_009_GoldenDocuments(t *testing.T) {
	all := fixture(t)
	t.Run("index of the semver2 hive", func(t *testing.T) {
		b, err := doc(SemVer2).Index(Visible(all, SemVer2, nil))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "index-semver2.json", b)
	})
	t.Run("index of the semver1 hive leaves out the semver2 version", func(t *testing.T) {
		b, err := doc(Gz).Index(Visible(all, Gz, nil))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "index-semver1.json", b)
	})
	t.Run("page", func(t *testing.T) {
		b, found, err := doc(SemVer2).Page(Visible(all, SemVer2, nil), "1.3.0-beta", "1.4.0-ci.5")
		if err != nil || !found {
			t.Fatalf("found %v, err %v", found, err)
		}
		golden(t, "page.json", b)
	})
	t.Run("leaf", func(t *testing.T) {
		b, found, err := doc(Plain).Leaf(Visible(all, Plain, nil), "1.3.0-beta")
		if err != nil || !found {
			t.Fatalf("found %v, err %v", found, err)
		}
		golden(t, "leaf.json", b)
	})
}

// TestR_NUGET_009_CatalogEntryFields: the members of a catalog entry follow the mapping of
// R-NUGET-009 - listed is always there, absent values are omitted, the license expression
// comes from an expression license, the dependency's registration is the same hive of the
// same repository and is left out for an ID the local rule does not admit, and deprecation
// is absent unless it is set.
func TestR_NUGET_009_CatalogEntryFields(t *testing.T) {
	var index struct {
		Items []struct {
			Items []struct {
				CatalogEntry map[string]json.RawMessage `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	render := func(d Doc, vs []rows.RegistrationVersion) map[string]map[string]json.RawMessage {
		t.Helper()
		b, err := d.Index(Visible(vs, SemVer2, nil))
		if err != nil {
			t.Fatal(err)
		}
		index.Items = nil
		if err := json.Unmarshal(b, &index); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]json.RawMessage{}
		for _, p := range index.Items {
			for _, it := range p.Items {
				var v string
				if err := json.Unmarshal(it.CatalogEntry["version"], &v); err != nil {
					t.Fatal(err)
				}
				out[v] = it.CatalogEntry
			}
		}
		return out
	}
	entries := render(doc(SemVer2), fixture(t))

	full := entries["1.2.3"]
	for k, want := range map[string]string{
		"id": `"Acme.Widgets"`, "version": `"1.2.3"`, "listed": `true`, "published": `"2026-10-28T09:14:03Z"`,
		"authors": `"Acme Platform Team"`, "description": `"Widget primitives <fast> & small."`, "title": `"Widgets"`,
		"summary": `"Widgets in short."`, "language": `"en-US"`, "minClientVersion": `"4.1.0"`, "requireLicenseAcceptance": `false`,
		"projectUrl": `"https://acme.example/widgets"`, "iconUrl": `"https://acme.example/icon.png"`,
		"licenseUrl": `"https://licenses.nuget.org/MIT"`, "licenseExpression": `"MIT"`,
		"@id":            `"https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.widgets/1.2.3.json"`,
		"packageContent": `"https://trove.example.com/api/nuget/nuget-local/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"`,
		"tags":           `["widgets","ui"]`,
	} {
		if string(full[k]) != want {
			t.Errorf("catalogEntry[%q] = %s, want %s", k, full[k], want)
		}
	}
	for _, k := range []string{"releaseNotes", "copyright", "readmeUrl", "deprecation", "vulnerabilities"} {
		if _, ok := full[k]; ok {
			t.Errorf("catalogEntry has %q, which a local package does not carry", k)
		}
	}
	beta := entries["1.3.0-beta"]
	if string(beta["listed"]) != "false" {
		t.Errorf("an unlisted version has listed = %s, want false", beta["listed"])
	}
	for _, k := range []string{"authors", "title", "summary", "licenseExpression", "dependencyGroups", "requireLicenseAcceptance", "minClientVersion"} {
		if _, ok := beta[k]; ok {
			t.Errorf("an absent value is emitted: %q", k)
		}
	}

	groups := full["dependencyGroups"]
	const wantGroups = `[{"targetFramework":"net8.0","dependencies":[` +
		`{"id":"Acme.Core","range":"[2.0.0, )","registration":"https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.core/index.json"},` +
		`{"id":"Unrelated","registration":"https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/unrelated/index.json"},` +
		`{"id":"Ünicode.Dep","range":"1.0.0"}]},` +
		`{"targetFramework":"netstandard2.0"},` +
		`{"dependencies":[{"id":"Flat.Dep","range":"1.0.0","registration":"https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/flat.dep/index.json"}]}]`
	if string(groups) != wantGroups {
		t.Errorf("dependencyGroups:\n got: %s\nwant: %s", groups, wantGroups)
	}

	// Pass-through members (a remote's) appear verbatim when set.
	d := doc(SemVer2)
	d.Extras = map[string]Passthrough{"1.2.3": {Deprecation: json.RawMessage(`{"message":"use Acme.Gadgets","reasons":["Legacy"]}`)}}
	withDeprecation := render(d, fixture(t))
	if dep := withDeprecation["1.2.3"]["deprecation"]; string(dep) != `{"message":"use Acme.Gadgets","reasons":["Legacy"]}` {
		t.Errorf("deprecation = %s", dep)
	}
	if _, ok := withDeprecation["1.3.0-beta"]["deprecation"]; ok {
		t.Error("deprecation of another version appears")
	}
}

// TestR_NUGET_009_Hives: the SemVer 2.0.0-specific version is in registration-semver2/ and
// in neither SemVer 1 hive (R-NUGET-004); an unlisted version is in all of them.
func TestR_NUGET_009_Hives(t *testing.T) {
	for _, tc := range []struct {
		hive Hive
		want []string
		gzip bool
	}{
		{Plain, []string{"1.2.3", "1.3.0-beta"}, false},
		{Gz, []string{"1.2.3", "1.3.0-beta"}, true},
		{SemVer2, []string{"1.2.3", "1.3.0-beta", "1.4.0-ci.5"}, true},
	} {
		t.Run(string(tc.hive), func(t *testing.T) {
			var got []string
			for _, v := range Visible(fixture(t), tc.hive, nil) {
				got = append(got, v.LowerVersion)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("versions = %v, want %v", got, tc.want)
			}
			if tc.hive.Gzipped() != tc.gzip {
				t.Errorf("Gzipped = %v", tc.hive.Gzipped())
			}
		})
	}
	if _, ok := ParseHive("registration-semver3"); ok {
		t.Error("an unknown hive is accepted")
	}
	// The exposure filter composes with the hive.
	kept := Visible(fixture(t), SemVer2, func(v string) bool { return v != "1.2.3" })
	if len(kept) != 2 || kept[0].LowerVersion != "1.3.0-beta" {
		t.Errorf("filtered versions = %v", kept)
	}
}

func versions(t testing.TB, n int) []rows.RegistrationVersion {
	out := make([]rows.RegistrationVersion, n)
	for i := range out {
		out[i] = entryOf(t, fmt.Sprintf("1.0.%d", i), true, `{"description":"d"}`)
	}
	return out
}

type indexShape struct {
	Count int `json:"count"`
	Items []struct {
		ID     string            `json:"@id"`
		Count  int               `json:"count"`
		Lower  string            `json:"lower"`
		Upper  string            `json:"upper"`
		Parent string            `json:"parent"`
		Items  []json.RawMessage `json:"items"`
	} `json:"items"`
}

// TestR_NUGET_009_PagingAtTheThresholds: a package of 130 versions has three pages listed
// without items, 100 versions two inlined pages, 10 versions one; the thresholds of the
// defaults are 65 (two pages), 127 (still inlined) and 128 (listed by URL).
func TestR_NUGET_009_PagingAtTheThresholds(t *testing.T) {
	for _, tc := range []struct {
		n       int
		pages   []int // leaves per page
		inlined bool
	}{
		{1, []int{1}, true},
		{10, []int{10}, true},
		{64, []int{64}, true},
		{65, []int{64, 1}, true},
		{100, []int{64, 36}, true},
		{127, []int{64, 63}, true},
		{128, []int{64, 64}, false},
		{129, []int{64, 64, 1}, false},
		{130, []int{64, 64, 2}, false},
	} {
		t.Run(fmt.Sprintf("%d versions", tc.n), func(t *testing.T) {
			vs := versions(t, tc.n)
			b, err := doc(SemVer2).Index(vs)
			if err != nil {
				t.Fatal(err)
			}
			var got indexShape
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if got.Count != len(tc.pages) || len(got.Items) != len(tc.pages) {
				t.Fatalf("count %d with %d pages, want %d", got.Count, len(got.Items), len(tc.pages))
			}
			next := 0
			for i, p := range got.Items {
				lower, upper := fmt.Sprintf("1.0.%d", next), fmt.Sprintf("1.0.%d", next+tc.pages[i]-1)
				next += tc.pages[i]
				if p.Count != tc.pages[i] || p.Lower != lower || p.Upper != upper {
					t.Errorf("page %d = count %d [%s, %s], want %d [%s, %s]", i, p.Count, p.Lower, p.Upper, tc.pages[i], lower, upper)
				}
				indexURL := string(feed) + "registration-semver2/acme.widgets/index.json"
				if tc.inlined {
					if len(p.Items) != tc.pages[i] || p.Parent != indexURL || p.ID != indexURL+"#page/"+lower+"/"+upper {
						t.Errorf("inlined page %d: %d items, parent %q, @id %q", i, len(p.Items), p.Parent, p.ID)
					}
				} else if len(p.Items) != 0 || p.Parent != "" || p.ID != string(feed)+"registration-semver2/acme.widgets/page/"+lower+"/"+upper+".json" {
					t.Errorf("listed page %d: %d items, parent %q, @id %q", i, len(p.Items), p.Parent, p.ID)
				}
			}
			if next != tc.n {
				t.Errorf("the pages cover %d versions, want %d", next, tc.n)
			}
		})
	}
}

// TestR_NUGET_009_PagesCoverEveryVersionOnce: the pages of a package of 130 versions,
// fetched from their own URLs, hold each version once and in order (R-NUGET-030).
func TestR_NUGET_009_PagesCoverEveryVersionOnce(t *testing.T) {
	vs := versions(t, 130)
	d := doc(SemVer2)
	b, err := d.Index(vs)
	if err != nil {
		t.Fatal(err)
	}
	var idx indexShape
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, p := range idx.Items {
		pb, found, err := d.Page(vs, p.Lower, p.Upper)
		if err != nil || !found {
			t.Fatalf("page %s-%s: found %v, err %v", p.Lower, p.Upper, found, err)
		}
		var page struct {
			ID    string `json:"@id"`
			Count int    `json:"count"`
			Items []struct {
				CatalogEntry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"`
			} `json:"items"`
		}
		if err := json.Unmarshal(pb, &page); err != nil {
			t.Fatal(err)
		}
		if page.ID != p.ID || page.Count != p.Count || len(page.Items) != p.Count {
			t.Errorf("page %s-%s: @id %q count %d items %d; the index says %q %d", p.Lower, p.Upper, page.ID, page.Count, len(page.Items), p.ID, p.Count)
		}
		for _, it := range page.Items {
			seen = append(seen, it.CatalogEntry.Version)
		}
	}
	if len(seen) != 130 {
		t.Fatalf("the pages hold %d leaves, want 130", len(seen))
	}
	for i, v := range seen {
		if want := fmt.Sprintf("1.0.%d", i); v != want {
			t.Fatalf("leaf %d is %q, want %q", i, v, want)
		}
	}
}

// TestR_NUGET_009_PageBoundsAreAnsweredAtRequestTime: a page URL of an older index stays
// valid - it holds the visible leaves inside its bounds now, however many that are; bounds
// that are no range name no page; a range without a version is a page without leaves.
func TestR_NUGET_009_PageBoundsAreAnsweredAtRequestTime(t *testing.T) {
	vs := versions(t, 10)
	d := doc(SemVer2)
	for _, tc := range []struct {
		name, lower, upper string
		found              bool
		count              int
	}{
		{"exact", "1.0.2", "1.0.5", true, 4},
		{"wider than the versions", "0.0.1", "9.0.0", true, 10},
		{"single", "1.0.7", "1.0.7", true, 1},
		{"nothing inside", "1.1.0", "1.2.0", true, 0},
		{"spelled with a zero revision", "1.0.2.0", "1.0.3.0", true, 2},
		{"reversed", "1.0.5", "1.0.2", false, 0},
		{"not a version", "x", "1.0.2", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, found, err := d.Page(vs, tc.lower, tc.upper)
			if err != nil || found != tc.found {
				t.Fatalf("found %v, err %v", found, err)
			}
			if !found {
				return
			}
			var page struct {
				Count int               `json:"count"`
				Items []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(b, &page); err != nil {
				t.Fatal(err)
			}
			if page.Count != tc.count || page.Items == nil || len(page.Items) != tc.count {
				t.Errorf("count %d, items %v (nil %v), want %d", page.Count, len(page.Items), page.Items == nil, tc.count)
			}
		})
	}
}

// TestR_NUGET_009_LeafOfAVersionTheHiveDoesNotList: the leaf of a version outside the
// visible versions is not found.
func TestR_NUGET_009_LeafOfAVersionTheHiveDoesNotList(t *testing.T) {
	vs := Visible(fixture(t), Plain, nil)
	if _, found, err := doc(Plain).Leaf(vs, "1.4.0-ci.5"); err != nil || found {
		t.Errorf("found %v, err %v for a SemVer 2 version in a SemVer 1 hive", found, err)
	}
}

// TestR_NUGET_030_RendersAreByteIdentical: the same rows render to the same bytes however
// often and in whatever order, with no dependence on a clock or on map order.
func TestR_NUGET_030_RendersAreByteIdentical(t *testing.T) {
	first, err := doc(SemVer2).Index(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		again, err := doc(SemVer2).Index(fixture(t))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("render %d differs:\n%s\n%s", i, first, again)
		}
	}
}

// TestR_NUGET_009_PagingDefaults: a zero paging is the default one.
func TestR_NUGET_009_PagingDefaults(t *testing.T) {
	d := doc(SemVer2)
	d.Paging = Paging{}
	b, err := d.Index(versions(t, 130))
	if err != nil {
		t.Fatal(err)
	}
	var got indexShape
	if err := json.Unmarshal(b, &got); err != nil || got.Count != 3 {
		t.Errorf("count %d, err %v", got.Count, err)
	}
}

// TestR_NUGET_009_IndexOfNothingIsNoDocument: an ID without a visible version has no index.
func TestR_NUGET_009_IndexOfNothingIsNoDocument(t *testing.T) {
	if _, err := doc(Plain).Index(nil); err == nil {
		t.Error("an index of no version was rendered")
	}
}

// TestR_NUGET_009_ParseMetadata: the stored object is read with its documented keys; tags
// as one string are split; an unknown key is ignored; a value that is no object fails.
func TestR_NUGET_009_ParseMetadata(t *testing.T) {
	m, err := ParseMetadata([]byte(`{"tags":"a  b\tc","unknown":{"x":1},"license":{"type":"file","value":"LICENSE.txt"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(m.Tags) != "[a b c]" || m.License == nil || m.License.Type != "file" {
		t.Errorf("metadata = %+v", m)
	}
	if m, err := ParseMetadata(nil); err != nil || m.Description != "" {
		t.Errorf("empty metadata: %+v, %v", m, err)
	}
	if _, err := ParseMetadata([]byte(`[1]`)); err == nil {
		t.Error("an array is read as metadata")
	}
	if _, err := ParseMetadata([]byte(`{"tags":5}`)); err == nil {
		t.Error("tags of a number are read")
	}
	// A file license is no license expression.
	file := entryOf(t, "1.0.0", true, `{"license":{"type":"file","value":"LICENSE.txt"}}`)
	b, err := doc(Plain).Index([]rows.RegistrationVersion{file})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("licenseExpression")) {
		t.Errorf("a file license is shown as an expression: %s", b)
	}
	if _, err := doc(Plain).Index([]rows.RegistrationVersion{entryOf(t, "1.0.0", true, `{"tags":5}`)}); err == nil {
		t.Error("a row with a damaged metadata object was rendered")
	}
}

// TestR_NUGET_009_UrlsEscapeTheirSegments: an ID outside the ASCII rule (cached from an
// upstream) is escaped once in the URLs.
func TestR_NUGET_009_UrlsEscapeTheirSegments(t *testing.T) {
	if got, want := feed.Index(Plain, "ünï"), string(feed)+"registration/%C3%BCn%C3%AF/index.json"; got != want {
		t.Errorf("Index = %q, want %q", got, want)
	}
	if got, want := feed.Nupkg("a.b", "1.0.0-rc.1"), string(feed)+"flatcontainer/a.b/1.0.0-rc.1/a.b.1.0.0-rc.1.nupkg"; got != want {
		t.Errorf("Nupkg = %q, want %q", got, want)
	}
}
