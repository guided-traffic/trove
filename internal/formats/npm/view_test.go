package npm

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

func TestR_API_029_NPMVersionOrderIsSemVerPrecedence(t *testing.T) {
	a := viewAdapter{}
	ordered := []string{"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i, x := range ordered {
		for j, y := range ordered {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := a.Compare(x, y); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", x, y, got, want)
			}
		}
	}
	if a.Compare("1.0.0+a", "1.0.0+b") == 0 {
		t.Error("two different strings compare equal")
	}
	if a.Compare("not-a-version", "0.0.1") >= 0 || a.Compare("0.0.1", "not-a-version") <= 0 {
		t.Error("a string that is no version sorts after a version")
	}
	for v, want := range map[string]bool{"1.0.0": false, "1.0.0-rc.1": true, "2.0.0-beta.1+build": true, "1.0.0+build": false, "garbage": false} {
		if got := a.Prerelease(v); got != want {
			t.Errorf("Prerelease(%s) = %v, want %v", v, got, want)
		}
	}
}

func detail(manifest string, deprecated string, tags map[string]string) rows.ViewDetail {
	return rows.ViewDetail{VersionID: 7, Manifest: []byte(manifest), Deprecated: deprecated, HasReadme: true, Readme: "# x", ReadmeFilename: "README.md", DistTags: tags}
}

func TestR_API_029_NPMMetadataMembers(t *testing.T) {
	manifest := `{
		"name": "rich", "version": "1.0.0", "description": " A rich package ", "keywords": ["a", "", 3, "b"],
		"license": "MIT", "licenses": [{"type": "Apache-2.0"}, {"type": ""}],
		"dependencies": {"lit": "^3", "bad": 1}, "devDependencies": {"vitest": "^2"}, "peerDependencies": {"react": ">=18"},
		"optionalDependencies": {"fsevents": "^2"}, "engines": {"node": ">=20"},
		"author": {"name": "Ada", "email": "ada@example.com", "url": "https://example.com/ada"},
		"contributors": ["Bob <bob@example.com>", {"name": "Cy"}, {"email": "nameless@example.com"}],
		"homepage": "https://example.com/rich", "repository": {"type": "git", "url": "git+https://example.com/rich.git"},
		"bugs": "https://example.com/rich/issues"
	}`
	data, err := describeManifest(detail(manifest, "use something else", map[string]string{"latest": "1.0.0", "next": "2.0.0-beta.1", "old": "0.1.0"}),
		func(v string) bool { return v != "2.0.0-beta.1" })
	if err != nil {
		t.Fatal(err)
	}
	if data.Description != "A rich package" || !slices.Equal(data.Keywords, []string{"a", "b"}) || !slices.Equal(data.Licenses, []string{"MIT", "Apache-2.0"}) {
		t.Errorf("%+v", data)
	}
	if !data.HasReadme || data.Readme != "# x" || data.ReadmeFile != "README.md" {
		t.Errorf("readme %+v", data)
	}
	md := data.Metadata
	if got := md["dependencies"].(map[string]string); len(got) != 1 || got["lit"] != "^3" {
		t.Errorf("dependencies %v", md["dependencies"])
	}
	for _, k := range []string{"devDependencies", "peerDependencies", "optionalDependencies", "engines"} {
		if _, ok := md[k]; !ok {
			t.Errorf("%s is missing", k)
		}
	}
	if got := md["authors"].([]string); !slices.Equal(got, []string{"Ada <ada@example.com> (https://example.com/ada)", "Bob <bob@example.com>", "Cy"}) {
		t.Errorf("authors %v", got)
	}
	if md["homepage"] != "https://example.com/rich" || md["repositoryUrl"] != "git+https://example.com/rich.git" || md["bugsUrl"] != "https://example.com/rich/issues" {
		t.Errorf("urls %v", md)
	}
	if md["deprecated"] != "use something else" {
		t.Errorf("deprecated %v", md["deprecated"])
	}
	// A dist-tag that names a version the caller may not read is not shown.
	if tags := md["distTags"].(map[string]string); len(tags) != 2 || tags["latest"] != "1.0.0" || tags["old"] != "0.1.0" {
		t.Errorf("a hidden version is named by a tag: %v", tags)
	}
}

func TestR_API_029_NPMDistTagsNameVisibleVersionsOnly(t *testing.T) {
	d := detail(`{"name":"p","version":"1.0.0"}`, "", map[string]string{"latest": "1.0.0", "next": "2.0.0-beta.1"})
	seen := map[string]bool{"1.0.0": true}
	data, err := describeManifest(d, func(v string) bool { return seen[v] })
	if err != nil {
		t.Fatal(err)
	}
	tags := data.Metadata["distTags"].(map[string]string)
	if len(tags) != 1 || tags["latest"] != "1.0.0" {
		t.Fatalf("%v", tags)
	}
	data, _ = describeManifest(d, func(string) bool { return false })
	if _, ok := data.Metadata["distTags"]; ok {
		t.Fatalf("an empty map of tags is present: %v", data.Metadata)
	}
}

func TestR_API_029_NPMMembersThatDoNotApplyAreAbsent(t *testing.T) {
	data, err := describeManifest(detail(`{"name":"p","version":"1.0.0"}`, "", nil), func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Metadata) != 0 || data.Description != "" || len(data.Keywords) != 0 || len(data.Licenses) != 0 {
		t.Fatalf("%+v", data)
	}
	if _, err := describeManifest(detail(`not json`, "", nil), nil); err == nil {
		t.Fatal("a stored manifest that does not parse was accepted")
	}
	// A member of an unexpected type is left out and fails nothing.
	data, err = describeManifest(detail(`{"description": 5, "keywords": "x", "license": {"year": 1}, "dependencies": [], "author": 3, "homepage": []}`, "", nil), nil)
	if err != nil || len(data.Metadata) != 0 || data.Description != "" {
		t.Fatalf("%+v %v", data, err)
	}
}

// fakePorts hands a view port to the adapter.
type fakePorts struct{ p viewPort }

func (f fakePorts) FormatData(formats.ID) any { return f.p }

type fakeViewPort struct {
	ids       []int64
	text, kw  string
	limit     int
	summaries []rows.ViewSummary
	details   []rows.ViewDetail
	withheld  []int64
	readme    bool
}

func (f *fakeViewPort) ViewSummaries(_ context.Context, ids []int64) ([]rows.ViewSummary, error) {
	f.ids = ids
	return f.summaries, nil
}

func (f *fakeViewPort) ViewDetails(_ context.Context, ids []int64, withReadme bool) ([]rows.ViewDetail, error) {
	f.ids, f.readme = ids, withReadme
	return f.details, nil
}

func (f *fakeViewPort) ViewFind(_ context.Context, _ []string, text, keyword string, limit int) ([]int64, error) {
	f.text, f.kw, f.limit = text, keyword, limit
	return []int64{4, 5}, nil
}

func (f *fakeViewPort) ViewWithheld(context.Context, []string) ([]int64, error) {
	return f.withheld, nil
}

func TestR_API_029_NPMAdapterReadsThroughThePort(t *testing.T) {
	port := &fakeViewPort{
		summaries: []rows.ViewSummary{{VersionID: 3, Description: "d", Keywords: []string{"k"}}},
		details:   []rows.ViewDetail{detail(`{"description":"from the manifest"}`, "", nil)},
		withheld:  []int64{9},
	}
	a, ports := viewAdapter{}, fakePorts{port}
	ctx := context.Background()
	vs := []formats.ViewVersion{{ID: 3, Package: "p", Version: "1.0.0"}}

	got, err := a.Describe(ctx, ports, vs, formats.ViewSummary, nil)
	if err != nil || got[3].Description != "d" || !slices.Equal(port.ids, []int64{3}) {
		t.Fatalf("%v %v", got, err)
	}
	got, err = a.Describe(ctx, ports, vs, formats.ViewSummary|formats.ViewDetails|formats.ViewReadme, nil)
	if err != nil || got[7].Description != "from the manifest" || !port.readme || got[7].Readme != "# x" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err = a.Describe(ctx, ports, vs, formats.ViewSummary|formats.ViewDetails, nil); err != nil || port.readme {
		t.Fatalf("a README was read without being asked for (%v)", err)
	}
	ids, err := a.Find(ctx, ports, formats.ViewFind{Repos: []string{"r"}, Keyword: "k"}, 10)
	if err != nil || !slices.Equal(ids, []formats.VersionID{4, 5}) || port.kw != "k" || port.text != "" || port.limit != 11 {
		t.Fatalf("%v %v %+v", ids, err, port)
	}
	w, err := a.Withheld(ctx, ports, []string{"r"})
	if err != nil || !slices.Equal(w, []formats.VersionID{9}) {
		t.Fatalf("%v %v", w, err)
	}
	if _, err := a.Find(ctx, nil, formats.ViewFind{Text: "x"}, 1); err == nil {
		t.Fatal("no data port is no error")
	}
}

func TestR_API_029_NPMHasAViewAdapter(t *testing.T) {
	f := New()
	if f.View() == nil {
		t.Fatal("npm has no package view")
	}
	if err := formats.Validate(f); err != nil {
		t.Fatal(err)
	}
}
