//go:build integration

package search

import (
	"slices"
	"strings"
	"testing"
)

// listed names the repositories of one page of GET /api/v1/repositories.
func (f *fixture) listed(t *testing.T, token, query string) []string {
	t.Helper()
	a := f.st.Do(f.r, "GET", "/api/v1/repositories?"+query, token, nil)
	if a.Status != 200 {
		t.Fatalf("%s: %d %s", query, a.Status, a.Body)
	}
	var out []string
	for _, i := range a.JSON(t)["items"].([]any) {
		m := i.(map[string]any)
		out = append(out, m["project"].(string)+"/"+m["key"].(string))
	}
	return out
}

// TestR_API_063_TheRepositoryListAnswersNamedRepositories: the frequently visited repositories of the search field are confirmed
// with one request: `name` keeps the named repositories the caller may see, and a name that does not exist and one the caller
// may not see are both absent (R-API-063); `q` matches the qualified name, so the results page holds every repository the
// suggestions name (R-API-062, R-REPO-012).
func TestR_API_063_TheRepositoryListAnswersNamedRepositories(t *testing.T) {
	f := newFixture(t)
	t.Run("R-API-063 Frank names npm-public, npm-private and nope and gets npm-public alone", func(t *testing.T) {
		if got := f.listed(t, f.tokFrank, "name=web/npm-public&name=web/npm-private&name=web/nope"); !slices.Equal(got, []string{"web/npm-public"}) {
			t.Fatalf("%v", got)
		}
		if got := f.listed(t, f.tokErin, "name=web/npm-public,web/npm-private,web/nope"); !slices.Equal(got, []string{"web/npm-private", "web/npm-public"}) {
			t.Fatalf("Erin: %v", got)
		}
	})
	t.Run("R-API-063 51 names or a malformed name answer 400", func(t *testing.T) {
		for _, q := range []string{"name=" + strings.TrimSuffix(strings.Repeat("web/npm,", 51), ","), "name=npm-public"} {
			if a := f.st.Do(f.r, "GET", "/api/v1/repositories?"+q, f.tokFrank, nil); a.Status != 400 || a.Code(t) != "malformed_request" {
				t.Fatalf("%.30s: %d %s", q, a.Status, a.Body)
			}
		}
		if a := f.st.Do(f.r, "GET", "/api/v1/projects/web/repositories?name=web/npm-public", f.tokFrank, nil); a.Status != 400 {
			t.Fatalf("the list of one project takes name: %d %s", a.Status, a.Body)
		}
	})
	t.Run("R-API-062 q=web/npm lists web/npm-local, and every repository the suggestions name for it", func(t *testing.T) {
		got := f.listed(t, f.tokAda, "q=web/npm&limit=50")
		if !slices.Contains(got, "web/npm-local") {
			t.Fatalf("%v", got)
		}
		for _, s := range f.suggest(f.tokAda, "web/npm", "&kind=repository&limit=10").repos() {
			if !slices.Contains(got, s) {
				t.Errorf("the suggestion %s is not on the list of q=web/npm: %v", s, got)
			}
		}
	})
}
