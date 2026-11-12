package v1

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_API_062_RepositoriesAndProjectsAreRankedByTheirNames: the ranks of R-API-062 with the rows of its acceptance criteria.
func TestR_API_062_RepositoriesAndProjectsAreRankedByTheirNames(t *testing.T) {
	t.Run("R-API-062 q=npm orders web/npm, then mobile/npm-local and web/npm-local, then web/my-npm", func(t *testing.T) {
		var facts []authz.RepositoryFacts
		for _, n := range []string{"web/my-npm", "web/npm-local", "mobile/npm-local", "web/npm", "web/docs"} {
			name, err := qname.ParseRepo(n)
			if err != nil {
				t.Fatal(err)
			}
			facts = append(facts, authz.RepositoryFacts{Name: name, Class: repository.Local, Format: "npm"})
		}
		all := func(names []qname.Repo) ([]qname.Repo, error) { return names, nil }
		got, err := repositorySuggestions(suggestOf(t, "npm", 10), facts, all)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, i := range got.Items {
			order = append(order, i.Project+"/"+i.Key)
		}
		if want := []string{"web/npm", "mobile/npm-local", "web/npm-local", "web/my-npm"}; !slices.Equal(order, want) {
			t.Fatalf("order %v, want %v", order, want)
		}
	})
	repo := func(s string) qname.Repo {
		n, err := qname.ParseRepo(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		text, name string
		want       int
	}{
		{"npm", "web/npm", 0},
		{"web/npm", "web/npm", 0},
		{"npm", "web/npm-local", 1},
		{"web/npm", "web/npm-local", 2},
		{"npm", "web/my-npm", 3},
		{"web npm", "web/my-npm", 3},
		{"npm", "web/docs", -1},
		{"web docs", "web/npm", -1},
	} {
		t.Run("R-API-062 the repository "+c.name+" ranks "+strconv.Itoa(c.want)+" for "+c.text, func(t *testing.T) {
			if got := repositoryRank(c.text, suggestTerms(c.text), repo(c.name)); got != c.want {
				t.Fatalf("rank %d, want %d", got, c.want)
			}
		})
	}
	for _, c := range []struct {
		text, key, display string
		want               int
	}{
		{"web", "web", "Web platform", 0},
		{"web", "web-tools", "Tools", 1},
		{"plat", "web", "Web platform", 2},
		{"web plat", "web", "Web platform", 2},
		{"form", "web", "Web platform", 3},
		{"zz", "web", "Web platform", -1},
	} {
		t.Run("R-API-062 the project "+c.key+" ("+c.display+") for "+c.text, func(t *testing.T) {
			if got := projectRank(c.text, suggestTerms(c.text), c.key, c.display); got != c.want {
				t.Fatalf("rank %d, want %d", got, c.want)
			}
		})
	}
}

// TestR_API_062_PackageTextsAreWordsAndBounds: the words of a text, its tsquery and its prefix bounds (R-API-062, R-API-064): no
// character of a request reaches the tsquery as an operator, and the bounds hold exactly the strings that start with the text.
func TestR_API_062_PackageTextsAreWordsAndBounds(t *testing.T) {
	for _, c := range []struct{ text, words, query string }{
		{"@acme/ui-kit", "acme ui kit", "'acme':* & 'ui':* & 'kit':*"},
		{"a:*|!b", "a b", "'a':* & 'b':*"},
		{"microsoft.extensions.logging", "microsoft extensions logging", "'microsoft':* & 'extensions':* & 'logging':*"},
		{"'x' & y2", "x y2", "'x':* & 'y2':*"},
		{"@/-", "", ""},
	} {
		t.Run("R-API-064 the tsquery of "+c.text, func(t *testing.T) {
			words := suggestWords(c.text)
			if strings.Join(words, " ") != c.words || suggestTSQuery(words) != c.query {
				t.Fatalf("words %q query %q, want %q %q", words, suggestTSQuery(words), c.words, c.query)
			}
		})
	}
	for _, c := range []struct {
		text, high string
		ok         bool
	}{
		{"@acme/ui", "@acme/uj", true},
		{"ab%_\\", "ab%_]", true},
		{"a퟿", "a", true},
		{"a\U0010FFFF", "b", true},
		{"\U0010FFFF\U0010FFFF", "", false},
	} {
		t.Run("R-API-064 the prefix bounds of "+c.text, func(t *testing.T) {
			low, high, ok := prefixBounds(c.text)
			if low != c.text || high != c.high || ok != c.ok || (ok && !utf8.ValidString(high)) {
				t.Fatalf("[%q, %q) %v, want [%q, %q) %v", low, high, ok, c.text, c.high, c.ok)
			}
		})
	}
	t.Run("R-API-064 in byte order a name lies in the bounds exactly when it starts with the text", func(t *testing.T) {
		names := []string{"@acme/ui", "@acme/ui-kit", "@acme/uj", "@acme/u", "@acme/ui\U0010FFFF", "@acme/uh", "@acme/uié", "@acme/uiz"}
		low, high, _ := prefixBounds("@acme/ui")
		for _, n := range names {
			if in, starts := n >= low && n < high, strings.HasPrefix(n, "@acme/ui"); in != starts {
				t.Errorf("%q: in the bounds %v, starts with the text %v", n, in, starts)
			}
		}
	})
}
