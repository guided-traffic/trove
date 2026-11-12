//go:build integration

package querybudget

import (
	"maps"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_API_064_ASuggestionCostsAFixedNumberOfStatements: a request of the search field with the five kinds runs the same
// statements, each as often, with 10 and with 10,000 packages that match its text (R-API-064): the repositories are matched
// over the grant index, the package statements read a bounded number of rows, and no statement runs per match. The statements
// of a request are the ones its context ran (RequestTransactions); a statement that a cache of the replica saves in some rounds
// (a credential read) counts with its fewest runs (leastOf).
func TestR_API_064_ASuggestionCostsAFixedNumberOfStatements(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Name: "querybudget"})
	st.Repository("npm-local", "local", "npm", `{}`)
	st.Repository("npm-other", "local", "npm", `{}`)
	ada := st.User("ada", "budget-leads")
	st.Bind("admin", ada)
	tok := st.Token(ada, scope.Scope{Inherit: true, Admin: true})
	seed := func(from, to int) {
		st.Exec(`INSERT INTO packages (repo_id, name, display_name)
		         SELECT r.id, 'budget-' || n, 'budget-' || n FROM repositories r, generate_series($1::int, $2::int) n WHERE r.key = 'npm-local'`, from, to)
		st.Exec(`INSERT INTO package_versions (package_id, version, display_version)
		         SELECT p.id, '1.0.0', '1.0.0' FROM packages p WHERE NOT EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = p.id)`)
	}
	seed(1, 10)
	rt := testenv.NewRequestTransactions()
	r := st.Start(t, testenv.ReplicaOptions{Name: "web-search", Database: []postgres.Option{rt.Option()}})
	const path = "/api/v1/search/suggestions?q=budget&kind=repository,package,project,user,group&limit=5"

	measure := func(t *testing.T) (map[string]int, int) {
		t.Helper()
		if a := st.Do(r, "GET", path, tok, nil); a.Status != http.StatusOK {
			t.Fatalf("warm-up: %d %s", a.Status, a.Body)
		}
		var rounds []map[string]int
		for range 3 {
			rt.Count(true)
			a := st.Do(r, "GET", path, tok, nil)
			rt.Count(false)
			if a.Status != http.StatusOK {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
			if a.JSON(t)["packages"].(map[string]any)["more"] != true {
				t.Fatalf("the packages do not report more: %s", a.Body)
			}
			rounds = append(rounds, rt.Of(a.Header.Get("X-Request-Id")).Statements())
		}
		return leastOf(rounds)
	}
	few, fewTotal := measure(t)
	seed(11, 10_000)
	st.Exec(`ANALYZE packages`)
	many, manyTotal := measure(t)
	t.Logf("10 matches: %d statements (%s); 10,000 matches: %d statements (%s)", fewTotal, describe(few), manyTotal, describe(many))
	if !maps.Equal(few, many) {
		t.Errorf("R-API-064 the statements differ: with 10 matches %s, with 10,000 %s", describe(few), describe(many))
	}
	for _, name := range []string{"SearchPackagesByPrefix", "SearchProjects", "SearchUsers", "SearchGroups"} {
		if many[name] != 1 {
			t.Errorf("R-API-064 %s ran %d times, want once", name, many[name])
		}
	}
}
