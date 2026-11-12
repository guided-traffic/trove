//go:build integration

package search

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_API_061_ASuggestionNamesOnlyWhatTheCallerMayRead: the acceptance rows of R-API-061 on the worked rows of R-AUTHZ-035
// (fixture): the levels, the external flag, a token's scope, a path-scoped grant, hidden existence and an endpoint's exposure
// rules decide the suggestions as they decide the lists, before the cut.
func TestR_API_061_ASuggestionNamesOnlyWhatTheCallerMayRead(t *testing.T) {
	f := newFixture(t)
	wantRepos := func(t *testing.T, got suggestions, want ...string) {
		t.Helper()
		if !slices.Equal(got.repos(), want) {
			t.Fatalf("repositories %v, want %v", got.repos(), want)
		}
	}

	t.Run("R-API-061 without a credential, q=npm suggests web/npm-public alone and q=button its @web/button", func(t *testing.T) {
		wantRepos(t, f.suggest("", "npm"), "web/npm-public")
		if got := f.suggest("", "button").pkgs(); !slices.Equal(got, []string{"@web/button@web/npm-public"}) {
			t.Fatalf("packages %v", got)
		}
	})
	t.Run("R-API-061 Frank, internal: npm-all, npm-internal and npm-public, never npm-private", func(t *testing.T) {
		wantRepos(t, f.suggest(f.tokFrank, "npm"), "web/npm-all", "web/npm-internal", "web/npm-public")
	})
	t.Run("R-API-061 Erin, external with read on npm-private: npm-private and npm-public, never npm-internal or npm-all", func(t *testing.T) {
		wantRepos(t, f.suggest(f.tokErin, "npm"), "web/npm-private", "web/npm-public")
		if got := f.suggest(f.tokErin, "button").pkgs(); !slices.Equal(got, []string{"@other/button@web/npm-private", "@web/button@web/npm-private"}) {
			t.Fatalf("packages %v", got)
		}
	})
	t.Run("R-API-061 Frank with a token scoped to mobile/*: npm-public alone, which public opens whatever the scope", func(t *testing.T) {
		got := f.suggest(f.tokFrankMobile, "npm")
		wantRepos(t, got, "web/npm-public")
		if p := f.suggest(f.tokFrankMobile, "button").pkgs(); !slices.Equal(p, []string{"@web/button@web/npm-public"}) {
			t.Fatalf("packages %v", p)
		}
	})
	t.Run("R-API-061 a package held by several readable repositories is one entry, in the first of them by name", func(t *testing.T) {
		if got := f.suggest(f.tokFrank, "button").pkgs(); !slices.Equal(got, []string{"@web/button@web/npm-internal"}) {
			t.Fatalf("packages %v", got)
		}
	})
	t.Run("R-API-061 a repository read in part suggests the packages its grant admits only", func(t *testing.T) {
		got := f.suggest(f.tokCarol, "button").pkgs()
		if !slices.Equal(got, []string{"@web/button@web/npm-internal"}) {
			t.Fatalf("packages %v: carol reads @web/* of npm-private only, and npm-internal by its level", got)
		}
	})
	t.Run("R-API-061 a project shows by its key in a repository's and a package's entry, by its display name in its own", func(t *testing.T) {
		got := f.suggest(f.tokFrank, "web", "&kind=project")
		if got.Projects == nil || len(got.Projects.Items) != 1 || got.Projects.Items[0].Key != "web" || got.Projects.Items[0].DisplayName != "web" {
			t.Fatalf("projects %+v", got.Projects)
		}
		if len(f.suggest(f.tokFrank, "mobile", "&kind=project").Projects.Items) != 0 {
			t.Fatal("the private project mobile is suggested to Frank")
		}
	})
	t.Run("R-API-061 twenty packages button-<n> in npm-private change neither Frank's answer to q=button nor its more", func(t *testing.T) {
		before := f.suggest(f.tokFrank, "button", "&limit=1")
		for n := range 20 {
			f.pkg("web/npm-private", fmt.Sprintf("button-%d", n))
		}
		after := f.suggest(f.tokFrank, "button", "&limit=1")
		if !slices.Equal(before.pkgs(), after.pkgs()) || before.Packages.More != after.Packages.More || after.Packages.More {
			t.Fatalf("before %v more=%v, after %v more=%v", before.pkgs(), before.Packages.More, after.pkgs(), after.Packages.More)
		}
		if got := f.suggest(f.tokErin, "button", "&limit=1"); !got.Packages.More {
			t.Fatal("Erin reads npm-private, and its twenty more matches do not set more")
		}
	})
	t.Run("R-API-061 a repository with hideExistence false is suggested no more than any other the caller cannot read", func(t *testing.T) {
		for _, tok := range []string{"", f.tokFrank, f.tokFrankMobile} {
			if got := f.suggest(tok, "private", "&kind=repository"); len(got.repos()) != 0 || got.Repositories.More {
				t.Fatalf("npm-private is suggested: %v", got.repos())
			}
		}
	})
	t.Run("R-API-061 on an endpoint with exposure rules, the rules narrow the suggestions", func(t *testing.T) {
		a := f.st.DoAt(f.r.Addr("customers"), "customers.example.test", "GET", "/api/v1/search/suggestions?q=npm", f.tokBot, nil)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if !strings.Contains(string(a.Body), `"key":"npm-public"`) || strings.Contains(string(a.Body), `"key":"npm-internal"`) ||
			strings.Contains(string(a.Body), `"key":"npm-private"`) {
			t.Fatalf("the endpoint exposes npm-public only: %s", a.Body)
		}
		b := f.st.DoAt(f.r.Addr("customers"), "customers.example.test", "GET", "/api/v1/search/suggestions?q=button&kind=package", f.tokBot, nil)
		if b.Status != 200 || strings.Contains(string(b.Body), `"repository":"npm-private"`) || strings.Contains(string(b.Body), `"repository":"npm-internal"`) ||
			strings.Contains(string(b.Body), `@other/button`) {
			t.Fatalf("a package outside the rules is suggested: %d %s", b.Status, b.Body)
		}
	})
	t.Run("R-API-061 users and groups: the administrator gets them, Frank 403 for every text with the same body", func(t *testing.T) {
		got := f.suggest(f.tokAda, "fr", "&kind=user,group")
		if got.Users == nil || len(got.Users.Items) != 1 || got.Users.Items[0].Username != "frank" || got.Groups == nil || len(got.Groups.Items) != 0 {
			t.Fatalf("users %+v groups %+v", got.Users, got.Groups)
		}
		if g := f.suggest(f.tokAda, "leads", "&kind=group"); len(g.Groups.Items) != 1 || g.Groups.Items[0].Name != "web-leads" || g.Repositories != nil {
			t.Fatalf("groups %+v", g.Groups)
		}
		if u := f.suggest(f.tokAda, "external", "&kind=user"); len(u.Users.Items) != 1 || u.Users.Items[0].Username != "erin" {
			t.Fatalf("a user is matched by its display name: %+v", u.Users)
		}
		a, b := f.get(f.tokFrank, "q=fr&kind=user"), f.get(f.tokFrank, "q=zz&kind=user")
		if a.Status != 403 || b.Status != 403 || a.Problem(t) != b.Problem(t) {
			t.Fatalf("%d %s / %d %s", a.Status, a.Body, b.Status, b.Body)
		}
	})
}

// TestR_API_062_SuggestionsAreRankedByTheirNames: the ranks of R-API-062 with the server's statements: the repositories of an
// administrator for q=npm, and the packages by name prefix and by word prefix.
func TestR_API_062_SuggestionsAreRankedByTheirNames(t *testing.T) {
	f := newFixture(t)
	for _, n := range []string{"@acme/ui-kit", "@acme/kit-ui", "microsoft.extensions.logging"} {
		f.pkg("web/npm-public", n)
	}
	t.Run("R-API-062 q=npm orders web/npm, then the keys that start with npm, then web/my-npm", func(t *testing.T) {
		got := f.suggest(f.tokAda, "npm", "&kind=repository&limit=10")
		want := []string{"web/npm", "mobile/npm-local", "web/npm-all", "web/npm-internal", "web/npm-local", "web/npm-private", "web/npm-public", "web/my-npm"}
		if !slices.Equal(got.repos(), want) || got.Repositories.More {
			t.Fatalf("%v more=%v, want %v", got.repos(), got.Repositories.More, want)
		}
	})
	t.Run("R-API-062 web npm, two terms, matches the qualified name", func(t *testing.T) {
		if got := f.suggest(f.tokFrank, "web public", "&kind=repository").repos(); !slices.Equal(got, []string{"web/npm-public"}) {
			t.Fatalf("%v", got)
		}
	})
	pkgs := func(q string) []string {
		var out []string
		for _, p := range f.suggest(f.tokFrank, q, "&kind=package").Packages.Items {
			out = append(out, p.Name)
		}
		return out
	}
	t.Run("R-API-062 q=@acme/ui suggests @acme/ui-kit before @acme/kit-ui", func(t *testing.T) {
		if got := pkgs("@acme/ui"); !slices.Equal(got, []string{"@acme/ui-kit", "@acme/kit-ui"}) {
			t.Fatalf("%v", got)
		}
	})
	t.Run("R-API-062 q=kit suggests both by words, q=it neither", func(t *testing.T) {
		if got := pkgs("kit"); !slices.Equal(got, []string{"@acme/kit-ui", "@acme/ui-kit"}) {
			t.Fatalf("%v", got)
		}
		if got := pkgs("it"); len(got) != 0 {
			t.Fatalf("an infix of a word is suggested: %v", got)
		}
	})
	t.Run("R-API-062 a word of a dotted name finds it; tsquery syntax in the text is no operator", func(t *testing.T) {
		if got := pkgs("logging"); !slices.Equal(got, []string{"microsoft.extensions.logging"}) {
			t.Fatalf("%v", got)
		}
		for _, q := range []string{"kit:*|!x", "a & b", "'kit'", "kit%", "k_t"} {
			if a := f.get(f.tokFrank, "kind=package&q="+strings.NewReplacer("&", "%26", "%", "%25", "|", "%7C", "'", "%27", " ", "+").Replace(q)); a.Status != 200 {
				t.Fatalf("q=%s: %d %s", q, a.Status, a.Body)
			}
		}
	})
}

// TestR_API_060_TheSuggestionsOperation: the acceptance rows of R-API-060 over HTTP: the parameter errors, the members of the
// kinds asked, the rate-limit class, and an answer no cache stores.
func TestR_API_060_TheSuggestionsOperation(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{"q=n", "q=" + strings.Repeat("a", 101), "q=npm&kind=file", "q=npm&limit=11"} {
		t.Run("R-API-060 "+q[:min(len(q), 20)]+" answers 400", func(t *testing.T) {
			if a := f.get(f.tokFrank, q); a.Status != 400 || a.Code(t) != "malformed_request" {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
		})
	}
	// The general rate limits are off on the stack's replicas; this one has them on.
	limited := f.st.Start(t, testenv.ReplicaOptions{Name: "limited", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=true"}})
	t.Run("R-API-060 kind=repository answers only the member repositories, in the read class, never stored", func(t *testing.T) {
		a := f.st.Do(limited, "GET", "/api/v1/search/suggestions?q=npm&kind=repository", f.tokFrank, nil)
		if a.Status != 200 || !strings.HasPrefix(string(a.Body), `{"repositories":`) || strings.Contains(string(a.Body), `"packages"`) {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// The class, with the stack's budget (the defaults of R-API-016 are the configuration's, q=200).
		if p := a.Header.Get("RateLimit-Policy"); !strings.HasPrefix(p, `"read";q=`) {
			t.Errorf("RateLimit-Policy %q", p)
		}
		if a.Header.Get("Cache-Control") != "no-store" || a.Header.Get("Link") != "" {
			t.Errorf("Cache-Control %q Link %q", a.Header.Get("Cache-Control"), a.Header.Get("Link"))
		}
	})
	t.Run("R-API-060 without a credential the class is anonymous", func(t *testing.T) {
		if p := f.st.Do(limited, "GET", "/api/v1/search/suggestions?q=npm", "", nil).Header.Get("RateLimit-Policy"); !strings.HasPrefix(p, `"anonymous";`) {
			t.Errorf("RateLimit-Policy %q", p)
		}
	})
}
