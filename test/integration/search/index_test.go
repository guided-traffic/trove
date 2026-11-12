//go:build integration

package search

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_API_064_ThePackageStatementsUseTheirIndexes: statement A of the suggestions reads packages_name_lower and statement B
// packages_name_words (migration 00076), with enable_seqscan off as R-API-064 states it, on a table of several thousand names of
// which few match. The plans are those the server runs: a prepared statement under plan_cache_mode = force_custom_plan, which
// SearchCustomPlans sets for the transaction of the package statements. The generated column holds the words of the lower-cased
// name.
func TestR_API_064_ThePackageStatementsUseTheirIndexes(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Name: "searchindex"})
	st.Repository("npm-a", "local", "npm", `{}`)
	st.Repository("npm-b", "local", "npm", `{}`)
	st.Exec(`INSERT INTO packages (repo_id, name, display_name)
	         SELECT r.id, 'pkg-' || r.key || '-' || n, 'pkg-' || r.key || '-' || n
	           FROM repositories r, generate_series(1, 3000) n WHERE r.key IN ('npm-a', 'npm-b')`)
	st.Exec(`INSERT INTO packages (repo_id, name, display_name)
	         SELECT r.id, n, n FROM repositories r, unnest(ARRAY['@acme/ui-kit', '@acme/kit-ui', 'microsoft.extensions.logging']) n
	          WHERE r.key = 'npm-a'`)
	st.Exec(`INSERT INTO package_versions (package_id, version, display_version) SELECT id, '1.0.0', '1.0.0' FROM packages`)
	st.Exec(`ANALYZE packages`)
	st.Exec(`ANALYZE package_versions`)

	t.Run("R-API-064 the generated column holds the words of the lower-cased name", func(t *testing.T) {
		if got := st.Text(`SELECT name_words::text FROM packages WHERE name = 'microsoft.extensions.logging'`); got != `'extensions':2 'logging':3 'microsoft':1` {
			t.Fatalf("name_words = %s", got)
		}
		if got := st.Text(`SELECT name_words::text FROM packages WHERE name = '@acme/ui-kit'`); got != `'acme':1 'kit':3 'ui':2` {
			t.Fatalf("name_words = %s", got)
		}
	})

	// explain prepares the statement, as the server's connection does, and explains one run of it with values under a plan cache
	// mode.
	explain := func(t *testing.T, mode, statement, values string) string {
		t.Helper()
		st.Exec(`SET enable_seqscan = off`)
		st.Exec(`SET plan_cache_mode = ` + mode)
		st.Exec(`PREPARE search_explain AS ` + statement)
		defer func() {
			st.Exec(`DEALLOCATE search_explain`)
			st.Exec(`RESET plan_cache_mode`)
			st.Exec(`RESET enable_seqscan`)
		}()
		var plan []string
		st.Query(`EXPLAIN (COSTS OFF) EXECUTE search_explain(`+values+`)`, func(r pgx.Rows) error {
			var line string
			if err := r.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
			return nil
		})
		return strings.Join(plan, "\n")
	}
	repos := st.Text(`SELECT array_agg(id ORDER BY id)::text FROM repositories WHERE key IN ('npm-a', 'npm-b')`)

	for _, c := range []struct {
		name, statement, index, values string
	}{
		{"statement A", "SearchPackagesByPrefix", "packages_name_lower", `'` + repos + `', '@acme/ui', '@acme/uj', '{}', 201`},
		{"statement B", "SearchPackagesByWords", "packages_name_words", `'` + repos + `', '''kit'':*', '{}', '{}', 201`},
	} {
		text := statementText(t, c.statement)
		t.Run("R-API-064 "+c.name+" reads "+c.index, func(t *testing.T) {
			if plan := explain(t, "force_custom_plan", text, c.values); !strings.Contains(plan, c.index) {
				t.Fatalf("the plan does not use %s:\n%s", c.index, plan)
			}
		})
	}
}
