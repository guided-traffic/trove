//go:build integration

package packages

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestR_API_029_ListFiltersAndSorts: the list across repositories with its filters and
// every documented sort, walked page by page with a cursor of one item.
func TestR_API_029_ListFiltersAndSorts(t *testing.T) {
	e := newEnv(t)
	e.seed()

	t.Run("the packages of both repositories are listed once each, aggregated over the repositories", func(t *testing.T) {
		items := e.all(e.tokE, "/api/v1/packages")
		if got, want := names(items), []string{"@team-a/ui", "@team-b/core", "left-pad", "secret-tool"}; !slices.Equal(got, want) {
			t.Fatalf("names %v, want %v", got, want)
		}
		ui := pkgByName(items, "@team-a/ui")
		// 1.3.0, 1.4.0 (in both repositories, counted once), 1.5.0 and 2.0.0-beta.1.
		if num(ui["versionCount"]) != 4 || ui["latestVersion"] != "1.5.0" {
			t.Fatalf("versionCount %v latestVersion %v", ui["versionCount"], ui["latestVersion"])
		}
		if !slices.Equal(toStrings(ui["repositories"]), []string{"npm-local", "npm-team"}) {
			t.Fatalf("repositories %v", ui["repositories"])
		}
		if ui["description"] != "Shared UI components" || !slices.Equal(toStrings(ui["keywords"]), []string{"ui", "components"}) {
			t.Fatalf("description %v keywords %v", ui["description"], ui["keywords"])
		}
		if ui["id"] == "" || !strings.HasPrefix(ui["id"].(string), "pkg_") {
			t.Fatalf("id %v", ui["id"])
		}
		created, _ := time.Parse(time.RFC3339Nano, ui["createdAt"].(string))
		updated, _ := time.Parse(time.RFC3339Nano, ui["updatedAt"].(string))
		if !updated.After(created) {
			t.Fatalf("createdAt %v updatedAt %v", created, updated)
		}
	})

	t.Run("the sorts order the list and the cursor walks them without a gap or a repeat", func(t *testing.T) {
		for _, sort := range []string{"name", "-name", "createdAt", "-createdAt", "updatedAt", "-updatedAt"} {
			whole := names(e.all(e.tokE, "/api/v1/packages?sort="+sort))
			paged := names(e.all(e.tokE, "/api/v1/packages?limit=1&sort="+sort))
			if !slices.Equal(whole, paged) || len(whole) != 4 {
				t.Errorf("sort=%s: whole %v, one at a time %v", sort, whole, paged)
			}
			switch sort {
			case "name":
				if !slices.IsSorted(whole) {
					t.Errorf("sort=name: %v", whole)
				}
			case "-name":
				if !slices.IsSorted(reversed(whole)) {
					t.Errorf("sort=-name: %v", whole)
				}
			case "createdAt":
				// left-pad was published after @team-a/ui, before @team-b/core and secret-tool.
				if !slices.Equal(whole, []string{"@team-a/ui", "left-pad", "@team-b/core", "secret-tool"}) {
					t.Errorf("sort=createdAt: %v", whole)
				}
			case "-createdAt":
				if !slices.Equal(whole, []string{"secret-tool", "@team-b/core", "left-pad", "@team-a/ui"}) {
					t.Errorf("sort=-createdAt: %v", whole)
				}
			case "updatedAt":
				// @team-a/ui got its last version (1.5.0) after left-pad's only one.
				if !slices.Equal(whole, []string{"left-pad", "@team-a/ui", "@team-b/core", "secret-tool"}) {
					t.Errorf("sort=updatedAt: %v", whole)
				}
			}
		}
	})

	t.Run("a sort or a filter the document does not list answers 400", func(t *testing.T) {
		for _, q := range []string{"sort=downloads", "sort=-version", "colour=red", "createdFrom=yesterday", "cursor=%25%25"} {
			a := e.get(e.tokE, "/api/v1/packages?"+q)
			if a.Status != 400 {
				t.Errorf("%s: %d %s", q, a.Status, a.Body)
			}
		}
	})

	t.Run("a cursor of another sort or filter is refused", func(t *testing.T) {
		p := e.page(e.tokE, "/api/v1/packages?limit=1&sort=name")
		if p.NextCursor == nil {
			t.Fatal("no next page")
		}
		a := e.get(e.tokE, "/api/v1/packages?limit=1&sort=createdAt&cursor="+url.QueryEscape(*p.NextCursor))
		if a.Status != 400 || a.Code(t) != "invalid_cursor" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	filter := func(q string) []string { return names(e.all(e.tokE, "/api/v1/packages?"+q)) }

	t.Run("format and repository narrow the list; the counts follow the repositories asked for", func(t *testing.T) {
		if got := filter("format=generic"); len(got) != 0 {
			t.Errorf("format=generic: %v", got)
		}
		if got := filter("format=npm&format=generic"); len(got) != 4 {
			t.Errorf("two formats: %v", got)
		}
		if got := filter("repository=npm-local"); !slices.Equal(got, []string{"@team-a/ui", "left-pad"}) {
			t.Errorf("repository=npm-local: %v", got)
		}
		if got := filter("repository=npm-local&repository=npm-team"); len(got) != 4 {
			t.Errorf("both repositories: %v", got)
		}
		if got := filter("repository=nope"); len(got) != 0 {
			t.Errorf("an unknown repository: %v", got)
		}
		ui := pkgByName(e.all(e.tokE, "/api/v1/packages?repository=npm-local"), "@team-a/ui")
		if num(ui["versionCount"]) != 3 || ui["latestVersion"] != "1.4.0" || !slices.Equal(toStrings(ui["repositories"]), []string{"npm-local"}) {
			t.Errorf("in npm-local: %v", ui)
		}
	})

	t.Run("q matches the name and the description case-insensitively", func(t *testing.T) {
		if got := filter("q=TEAM-A"); !slices.Equal(got, []string{"@team-a/ui"}) {
			t.Errorf("q=TEAM-A: %v", got)
		}
		if got := filter("q=pads%20strings"); !slices.Equal(got, []string{"left-pad"}) {
			t.Errorf("q on the description: %v", got)
		}
		if got := filter("q=NEEDLE-WORD"); !slices.Equal(got, []string{"@team-b/core"}) {
			t.Errorf("q on another description: %v", got)
		}
		if got := filter("q=%25"); len(got) != 0 {
			t.Errorf("a percent sign is no wildcard: %v", got)
		}
	})

	t.Run("namePrefix, keyword and the creation period", func(t *testing.T) {
		if got := filter("namePrefix=%40team"); !slices.Equal(got, []string{"@team-a/ui", "@team-b/core"}) {
			t.Errorf("namePrefix: %v", got)
		}
		if got := filter("keyword=components"); !slices.Equal(got, []string{"@team-a/ui"}) {
			t.Errorf("keyword: %v", got)
		}
		if got := filter("keyword=compon"); len(got) != 0 {
			t.Errorf("a keyword matches exactly: %v", got)
		}
		all := e.all(e.tokE, "/api/v1/packages?sort=createdAt")
		mid := all[2]["createdAt"].(string) // @team-b/core
		if got := filter("createdFrom=" + url.QueryEscape(mid)); !slices.Equal(got, []string{"@team-b/core", "secret-tool"}) {
			t.Errorf("createdFrom (inclusive): %v", got)
		}
		if got := filter("createdTo=" + url.QueryEscape(mid)); !slices.Equal(got, []string{"@team-a/ui", "left-pad"}) {
			t.Errorf("createdTo (exclusive): %v", got)
		}
	})
}

func reversed(in []string) []string {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
