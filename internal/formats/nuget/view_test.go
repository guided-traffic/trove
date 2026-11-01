package nuget

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// ports is the DataPorts of a test: the twin under the format's ID.
type ports struct{ data any }

func (p ports) FormatData(id formats.ID) any {
	if id == ID {
		return p.data
	}
	return nil
}

const sampleMetadata = `{
  "title": "Widgets", "summary": "Widget helpers", "description": "Acme widgets for the Contoso suite",
  "authors": "Ada Lovelace, Grace Hopper", "tags": ["widgets", "contoso"],
  "projectUrl": "https://acme.example/widgets", "licenseUrl": "https://acme.example/license",
  "license": {"type": "expression", "version": "1.0.0", "value": "MIT OR Apache-2.0"},
  "readme": "docs/README.md", "releaseNotes": "Fixed the widgets", "minClientVersion": "4.3.0",
  "requireLicenseAcceptance": true,
  "repository": {"type": "git", "url": "https://git.example/acme/widgets.git"},
  "dependencyGroups": [
    {"targetFramework": "net8.0", "dependencies": [{"id": "Newtonsoft.Json", "range": "[13.0.1, )"}]},
    {"targetFramework": ".NETStandard2.0", "dependencies": [{"id": "Newtonsoft.Json", "version": "12.0.3"}, {"id": "Acme.Core"}]},
    {"dependencies": []}
  ],
  "packageTypes": [{"name": "Dependency"}, {"name": "DotnetTool", "version": "1.0"}]
}`

// seeded is a twin with one repository (id 7, key "nuget-local") holding acme.widgets at the versions
// in the order of their arrival; the version ids are 100, 101, ...
func seeded(t *testing.T, meta string, versions ...string) *nugettest.Store {
	t.Helper()
	st := nugettest.New()
	st.BindRepo("nuget-local", 7)
	for i, v := range versions {
		err := st.InsertVersion(context.Background(), nil, rows.VersionInput{
			RepoID: 7, VersionID: int64(100 + i), LowerID: "acme.widgets", DeclaredID: "Acme.Widgets", OriginalVersion: v, LowerVersion: v, SortKey: []byte(v),
			PublishedAt: time.Date(2026, 3, 1, 0, i, 0, 0, time.UTC), PackageTypes: []string{"dependency"}, Metadata: []byte(meta),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// TestR_API_029_NuGetOrdersVersionsByPrecedence: Compare is the order of NuGet.Versioning, and a total
// one: spellings that NuGet treats as one version still compare unequal, and a string that is no version
// sorts before every version.
func TestR_API_029_NuGetOrdersVersionsByPrecedence(t *testing.T) {
	a := New().View()
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0", "1.10.0.1", "2.0.0"}
	for i := range ordered {
		for j := range ordered {
			if got, want := sign(a.Compare(ordered[i], ordered[j])), sign(i-j); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if c := a.Compare("1.0.0-RC.1", "1.0.0-rc.1"); c == 0 {
		t.Error("two spellings of one version compare equal: the order is not total over the stored strings")
	}
	if c := a.Compare("1.0.0-RC.1", "1.0.0-rc.2"); c >= 0 {
		t.Errorf("the case of a label decided the order: %d", c)
	}
	if a.Compare("not a version", "0.0.1") >= 0 || a.Compare("0.0.1", "not a version") <= 0 || a.Compare("zzz", "zzz") != 0 {
		t.Error("a string that is no version does not sort first")
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// TestR_API_029_NuGetDescribesSummaryAndDetails: the summary is the description (the summary
// element when there is none) and the tags; the details add the licenses, the README flag and the
// metadata members of Q-NUGET-48, and read the README text only when asked.
func TestR_API_029_NuGetDescribesSummaryAndDetails(t *testing.T) {
	st := seeded(t, sampleMetadata, "1.0.0", "1.1.0")
	st.SetReadme(100, "# Widgets")
	vs := []formats.ViewVersion{{ID: 100, Package: "acme.widgets", Version: "1.0.0"}, {ID: 101, Package: "acme.widgets", Version: "1.1.0"}, {ID: 999}}
	ctx, p := context.Background(), ports{st}
	a := New().View()

	sum, err := a.Describe(ctx, p, vs, formats.ViewSummary, nil)
	if err != nil || len(sum) != 2 {
		t.Fatalf("summary: %v %v", sum, err)
	}
	if d := sum[100]; d.Description != "Acme widgets for the Contoso suite" || !slices.Equal(d.Keywords, []string{"widgets", "contoso"}) || d.Metadata != nil || d.Readme != "" {
		t.Errorf("%+v", d)
	}

	st2 := seeded(t, `{"summary": "Only a summary", "tags": "one two"}`, "1.0.0")
	if d, _ := a.Describe(ctx, ports{st2}, vs[:1], formats.ViewSummary, nil); d[100].Description != "Only a summary" || !slices.Equal(d[100].Keywords, []string{"one", "two"}) {
		t.Errorf("the summary element is the fallback description, a tag string is split: %+v", d[100])
	}

	det, err := a.Describe(ctx, p, vs, formats.ViewSummary|formats.ViewDetails, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := det[100]
	if !slices.Equal(d.Licenses, []string{"MIT OR Apache-2.0"}) || !d.HasReadme || d.Readme != "" || d.ReadmeFile != "" {
		t.Errorf("details without the README text: %+v", d)
	}
	md := d.Metadata
	for k, want := range map[string]any{
		"id": "Acme.Widgets", "listed": true, "title": "Widgets", "homepage": "https://acme.example/widgets",
		"repositoryUrl": "https://git.example/acme/widgets.git", "licenseUrl": "https://acme.example/license",
		"minClientVersion": "4.3.0", "releaseNotes": "Fixed the widgets", "requireLicenseAcceptance": true,
	} {
		if md[k] != want {
			t.Errorf("metadata[%q] = %v, want %v", k, md[k], want)
		}
	}
	if a, _ := md["authors"].([]string); !slices.Equal(a, []string{"Ada Lovelace", "Grace Hopper"}) {
		t.Errorf("authors %v", md["authors"])
	}
	if pt, _ := md["packageTypes"].([]string); !slices.Equal(pt, []string{"Dependency", "DotnetTool"}) {
		t.Errorf("package types %v", md["packageTypes"])
	}
	groups, _ := md["dependencyGroups"].([]dependencyGroup)
	if len(groups) != 3 || groups[0].TargetFramework != "net8.0" || groups[0].Dependencies[0] != (dependency{ID: "Newtonsoft.Json", Range: "[13.0.1, )"}) ||
		groups[1].Dependencies[0].Range != "12.0.3" || groups[1].Dependencies[1] != (dependency{ID: "Acme.Core"}) || groups[2].TargetFramework != "" || len(groups[2].Dependencies) != 0 {
		t.Errorf("dependency groups %+v", groups)
	}
	if _, ok := md["deprecated"]; ok {
		t.Error("a member that does not apply is present")
	}

	full, err := a.Describe(ctx, p, vs, formats.ViewSummary|formats.ViewDetails|formats.ViewReadme, nil)
	if err != nil || full[100].Readme != "# Widgets" || full[100].ReadmeFile != "docs/README.md" || full[101].Readme != "" {
		t.Errorf("README: %+v %v", full[100], err)
	}

	if ok, err := st.SetListed(ctx, nil, rows.ListingInput{VersionID: 100, Listed: false}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	again, _ := a.Describe(ctx, p, vs[:1], formats.ViewDetails, nil)
	if again[100].Metadata["listed"] != false {
		t.Errorf("an unlisted version says %v", again[100].Metadata["listed"])
	}
}

// TestR_API_029_NuGetFindsByDescriptionAndTag: Find matches a part of the description
// case-insensitively and a tag exactly, within the repositories asked, and returns one more than the
// limit so that the core can tell a cut list.
func TestR_API_029_NuGetFindsByDescriptionAndTag(t *testing.T) {
	st := seeded(t, sampleMetadata, "1.0.0", "1.1.0", "1.2.0")
	a, ctx, p := New().View(), context.Background(), ports{st}
	ids := func(f formats.ViewFind, limit int) []formats.VersionID {
		got, err := a.Find(ctx, p, f, limit)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ids(formats.ViewFind{Repos: []string{"nuget-local"}, Text: "CONTOSO suite"}, 10); !slices.Equal(got, []formats.VersionID{100, 101, 102}) {
		t.Errorf("text: %v", got)
	}
	if got := ids(formats.ViewFind{Repos: []string{"nuget-local"}, Keyword: "contoso"}, 10); len(got) != 3 {
		t.Errorf("keyword: %v", got)
	}
	if got := ids(formats.ViewFind{Repos: []string{"nuget-local"}, Keyword: "cont"}, 10); len(got) != 0 {
		t.Errorf("a keyword matches a part of a tag: %v", got)
	}
	if got := ids(formats.ViewFind{Repos: []string{"other"}, Text: "widgets"}, 10); len(got) != 0 {
		t.Errorf("another repository: %v", got)
	}
	if got := ids(formats.ViewFind{Repos: []string{"nuget-local"}, Text: "widgets"}, 1); len(got) != 2 {
		t.Errorf("limit 1 asks for 2 rows: %v", got)
	}
}

// TestR_EP_016_NuGetStoredListsWhatTheRepositoryServes: every version, listed or not, in the URL
// form; a hosted repository newest arrival first, a remote one newest published first; within the
// limit; a virtual repository stores nothing; and a package the repository does not hold is empty.
func TestR_EP_016_NuGetStoredListsWhatTheRepositoryServes(t *testing.T) {
	// arrival order is not version order, and the label is mixed case in the stored string
	st := seeded(t, `{}`, "1.0.0", "1.10.0", "1.2.0", "2.0.0-RC.1+build.5", "2.0")
	if ok, err := st.SetListed(context.Background(), nil, rows.ListingInput{VersionID: 101, Listed: false}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	a, ctx, p := New().View(), context.Background(), ports{st}
	repo := func(c formats.Class) formats.RepoRef {
		return formats.RepoRef{ID: 7, Key: "nuget-local", Class: c, Format: ID}
	}
	ask := func(c formats.Class, pkg string, limit int) formats.Stored {
		t.Helper()
		got, err := a.Stored(ctx, p, formats.StoredQuery{Repo: repo(c), Package: pkg, Limit: limit, Tags: true})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := []string{"2.0.0", "2.0.0-rc.1", "1.2.0", "1.10.0", "1.0.0"}
	got := ask(formats.Local, "acme.widgets", 10)
	if !slices.Equal(got.Versions, want) || len(got.Tags) != 0 {
		t.Errorf("local: %+v, want %v and no tags", got, want)
	}
	if got := ask(formats.Local, "acme.widgets", 2).Versions; !slices.Equal(got, want[:2]) {
		t.Errorf("limit: %v", got)
	}
	if got := ask(formats.Remote, "acme.widgets", 10).Versions; !slices.Equal(got, want) {
		t.Errorf("remote, published in arrival order here: %v", got)
	}
	if got := ask(formats.Virtual, "acme.widgets", 10); len(got.Versions) != 0 {
		t.Errorf("virtual: %+v", got)
	}
	if got := ask(formats.Local, "acme.other", 10); len(got.Versions) != 0 {
		t.Errorf("unknown package: %+v", got)
	}
	if got, _ := a.Stored(ctx, p, formats.StoredQuery{Repo: formats.RepoRef{ID: 8, Class: formats.Local}, Package: "acme.widgets", Limit: 10}); len(got.Versions) != 0 {
		t.Errorf("another repository: %+v", got)
	}
}

// TestR_EP_016_NuGetRemoteStoredIsNewestPublishedFirst: a remote repository's order is the publish
// time, not the arrival; the version id breaks a tie.
func TestR_EP_016_NuGetRemoteStoredIsNewestPublishedFirst(t *testing.T) {
	st := nugettest.New()
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC) }
	for i, c := range []struct {
		v string
		t time.Time
	}{{"1.0.0", at(5)}, {"1.1.0", at(9)}, {"1.2.0", at(1)}, {"1.3.0", time.Time{}}} {
		if err := st.InsertVersion(context.Background(), nil, rows.VersionInput{
			RepoID: 3, VersionID: int64(10 + i), LowerID: "x", DeclaredID: "X", OriginalVersion: c.v, LowerVersion: c.v, PublishedAt: c.t, Metadata: []byte(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New().View().Stored(context.Background(), ports{st}, formats.StoredQuery{Repo: formats.RepoRef{ID: 3, Class: formats.Remote}, Package: "x", Limit: 10})
	if err != nil || !slices.Equal(got.Versions, []string{"1.1.0", "1.0.0", "1.2.0", "1.3.0"}) {
		t.Errorf("%v %v", got.Versions, err)
	}
}

// TestR_API_029_NuGetWithholdsNothing: NuGet serves every stored version, so no screen leaves one
// out.
func TestR_API_029_NuGetWithholdsNothing(t *testing.T) {
	st := seeded(t, `{}`, "1.0.0")
	if ids, err := New().View().Withheld(context.Background(), ports{st}, []string{"nuget-local"}); err != nil || len(ids) != 0 {
		t.Errorf("%v %v", ids, err)
	}
}

// TestR_API_029_NuGetViewNeedsItsPort: a data port without the view reads is an internal error,
// not a panic.
func TestR_API_029_NuGetViewNeedsItsPort(t *testing.T) {
	_, err := New().View().Describe(context.Background(), ports{struct{}{}}, nil, formats.ViewSummary, nil)
	if err == nil || !strings.Contains(err.Error(), "no package view") {
		t.Errorf("%v", err)
	}
}

// TestR_NUGET_005_TheMetadataTheWriterStoresIsTheMetadataTheViewReads: the ingestion writes
// nuget_version.metadata with rows.Metadata (the key shape of the table in R-NUGET-005) and the
// view decodes it; a key that one of them spells differently would show up as a missing member.
func TestR_NUGET_005_TheMetadataTheWriterStoresIsTheMetadataTheViewReads(t *testing.T) {
	raw, err := json.Marshal(rows.Metadata{
		Title: "Widgets", Summary: "Small.", Description: "Widget primitives.", Authors: "Ada Lovelace, Grace Hopper",
		Tags: []string{"widgets", "contoso"}, ProjectURL: "https://acme.example/widgets", LicenseURL: "https://acme.example/license",
		License: &rows.License{Type: "expression", Version: "1.0.0", Value: "MIT"}, Readme: "docs/README.md", MinClientVersion: "4.3.0",
		Repository: &rows.Repository{Type: "git", URL: "https://git.example/acme/widgets.git"},
		DependencyGroups: []rows.DependencyGroup{
			{TargetFramework: "net8.0", Dependencies: []rows.Dependency{{ID: "Newtonsoft.Json", Version: "[13.0.1, )"}}},
		},
		PackageTypes: []rows.PackageType{{Name: "Dependency"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := seeded(t, string(raw), "1.0.0")
	vs := []formats.ViewVersion{{ID: 100, Package: "acme.widgets", Version: "1.0.0"}}
	det, err := New().View().Describe(context.Background(), ports{st}, vs, formats.ViewSummary|formats.ViewDetails, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := det[100]
	md := d.Metadata
	if d.Description != "Widget primitives." || !slices.Equal(d.Keywords, []string{"widgets", "contoso"}) || !slices.Equal(d.Licenses, []string{"MIT"}) || !d.HasReadme {
		t.Errorf("summary and details: %+v", d)
	}
	if md["homepage"] != "https://acme.example/widgets" || md["repositoryUrl"] != "https://git.example/acme/widgets.git" || md["minClientVersion"] != "4.3.0" {
		t.Errorf("members: %v", md)
	}
	if a, _ := md["authors"].([]string); !slices.Equal(a, []string{"Ada Lovelace", "Grace Hopper"}) {
		t.Errorf("authors %v", md["authors"])
	}
	groups, _ := json.Marshal(md["dependencyGroups"])
	if string(groups) != `[{"targetFramework":"net8.0","dependencies":[{"id":"Newtonsoft.Json","range":"[13.0.1, )"}]}]` {
		t.Errorf("dependencyGroups = %s", groups)
	}
}
