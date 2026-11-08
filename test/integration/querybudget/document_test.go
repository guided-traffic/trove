//go:build integration

package querybudget

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// firstRequestStatements serves a path once and returns the statements of its window that are not the replica's own, by
// name: the statements of the first request after a write, which renders what no cache holds yet.
func firstRequestStatements(t *testing.T, e *env, r *testenv.Replica, path, token string) map[string]int {
	t.Helper()
	time.Sleep(time.Second) // the write's own statements, and what its commit sets off, land before the window opens
	start := time.Now()
	if a := e.st.Do(r, "GET", path, token, nil); a.Status != http.StatusOK {
		t.Fatalf("%s: %d %.200s", path, a.Status, a.Body)
	}
	time.Sleep(300 * time.Millisecond)
	names := map[string]int{}
	for _, s := range testenv.StatementsSince(t, start.Add(-20*time.Millisecond)) {
		if name := testenv.StatementName(s); !background[name] {
			names[name]++
		}
	}
	return names
}

// leastOf keeps, per statement name, the smallest count of the rounds: a statement another part of the replica sent into
// one window (a credential read whose cache entry expired, a replica count) is not one every round has, so it falls out.
func leastOf(rounds []map[string]int) (map[string]int, int) {
	least := map[string]int{}
	for name := range rounds[0] {
		least[name] = rounds[0][name]
		for _, r := range rounds[1:] {
			least[name] = min(least[name], r[name])
		}
		if least[name] == 0 {
			delete(least, name)
		}
	}
	total := 0
	for _, n := range least {
		total += n
	}
	return least, total
}

// R-ARCH-027, Q-AUTHZ-255: a document of a caller whose own grants hide a version by path costs one statement when it is
// cached, the revision lookup, as every caller's does: the version list of the revision, which the fingerprint of the
// hidden versions needs, is kept in the replica's rendition cache. The first request after a write costs at most one
// statement more than the first request of a caller without path patterns: the version list of the new revision, read with
// the revision in one statement.
func TestR_ARCH_027_ADocumentOfACallerWithPathPatternsCostsOneStatement(t *testing.T) {
	e := newEnv(t)
	e.st.LogStatements(t)
	narrowed := e.st.User("narrowed")
	e.st.Policy("narrowed-read", []string{"read"}, []string{rNPM, rNuGet}, narrowed)
	for _, p := range []string{"budget-doc/-/budget-doc-2.0.0.tgz", "budget.doc/2.0.0/**"} {
		e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', $1 FROM authz_policy WHERE name = 'narrowed-read'`, p)
	}
	tokNarrowed := e.st.Token(narrowed, scope.Default())
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web-doc"})
	for _, v := range []string{"1.0.0", "2.0.0"} {
		e.publish(r, rNPM, "budget-doc", v)
		e.push(r, rNuGet, "Budget.Doc", v)
	}

	docs := []struct {
		name, path string
		write      func(version string)
	}{
		{"the npm package document", "/api/npm/it/" + rNPM + "/budget-doc", func(v string) { e.publish(r, rNPM, "budget-doc", v) }},
		{"the NuGet version list", "/api/nuget/it/" + rNuGet + "/v3/flatcontainer/budget.doc/index.json", func(v string) { e.push(r, rNuGet, "Budget.Doc", v) }},
	}
	for i, d := range docs {
		t.Run(d.name+", cached", func(t *testing.T) {
			if a := e.st.Do(r, "GET", d.path, tokNarrowed, nil); a.Status != http.StatusOK || strings.Contains(string(a.Body), `"2.0.0"`) {
				t.Fatalf("the narrowed caller's document: %d %s, want 200 without 2.0.0", a.Status, a.Body)
			}
			got := perRequestAs(t, e, r, d.path, tokNarrowed)
			total := 0
			for _, v := range got {
				total += v
			}
			t.Logf("statements per request: %d (%s)", total, describe(got))
			if total > 1 {
				t.Errorf("a cached document of a caller with path patterns runs %d statements (%s), the budget of R-ARCH-027 is 1", total, describe(got))
			}
		})
		t.Run(d.name+", the first request after a write", func(t *testing.T) {
			// Four rounds, each writing a new version and serving both callers, the narrowed one first in every other round,
			// so that neither always meets what the write set off; a statement counts with the smallest number of times it ran
			// in a round (leastOf).
			var narrowedRounds, readerRounds []map[string]int
			for round := range 4 {
				d.write("3." + string(rune('0'+i)) + "." + string(rune('0'+round)))
				if round%2 == 0 {
					narrowedRounds = append(narrowedRounds, firstRequestStatements(t, e, r, d.path, tokNarrowed))
					readerRounds = append(readerRounds, firstRequestStatements(t, e, r, d.path, e.tokReader))
				} else {
					readerRounds = append(readerRounds, firstRequestStatements(t, e, r, d.path, e.tokReader))
					narrowedRounds = append(narrowedRounds, firstRequestStatements(t, e, r, d.path, tokNarrowed))
				}
			}
			narrowedLeast, narrowedMin := leastOf(narrowedRounds)
			readerLeast, readerMin := leastOf(readerRounds)
			t.Logf("narrowed caller: %s; caller without patterns: %s", describe(narrowedLeast), describe(readerLeast))
			t.Logf("first request after a write: %d statements for the narrowed caller, %d for a caller without patterns", narrowedMin, readerMin)
			if narrowedMin > readerMin+1 {
				t.Errorf("the first document of a caller with path patterns after a write runs %d statements, %d more than a caller's without "+
					"patterns (%d); the bound is one more (the version list of the revision)", narrowedMin, narrowedMin-readerMin, readerMin)
			}
		})
	}
}
