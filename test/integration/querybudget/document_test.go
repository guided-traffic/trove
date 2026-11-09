//go:build integration

package querybudget

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// firstRequestStatements serves a path once and returns the statements that request ran, by name: the statements of the first
// request after a write, which renders what no cache holds yet. They are the ones the replica's query tracer attributes to the
// request's ID (X-Request-Id), so a statement of the replica's own loops (the readiness check reads system_state twice, the
// registration, the change-log poll) or of the stack's other writers never falls into the count, whatever instant it ran in
// (F-125). The pause before the request only lets what the write set off (the change-log poll that invalidates the caches)
// arrive; it counts nothing.
func firstRequestStatements(t *testing.T, e *env, r *testenv.Replica, rt *testenv.RequestTransactions, path, token string) map[string]int {
	t.Helper()
	time.Sleep(time.Second)
	rt.Count(true)
	defer rt.Count(false)
	a := e.st.Do(r, "GET", path, token, nil)
	if a.Status != http.StatusOK {
		t.Fatalf("%s: %d %.200s", path, a.Status, a.Body)
	}
	id := a.Header.Get("X-Request-Id")
	if id == "" {
		t.Fatalf("%s: the answer carries no X-Request-Id", path)
	}
	return rt.Of(id).Statements()
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
	e.st.LogStatements(t) // the cached subtests count from the database log
	narrowed := e.st.User("narrowed")
	e.st.Policy("narrowed-read", []string{"read"}, []string{rNPM, rNuGet}, narrowed)
	for _, p := range []string{"budget-doc/-/budget-doc-2.0.0.tgz", "budget.doc/2.0.0/**"} {
		e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', $1 FROM authz_policy WHERE name = 'narrowed-read'`, p)
	}
	tokNarrowed := e.st.Token(narrowed, scope.Default())
	rt := testenv.NewRequestTransactions()
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web-doc", Database: []postgres.Option{rt.Option()}})
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
					narrowedRounds = append(narrowedRounds, firstRequestStatements(t, e, r, rt, d.path, tokNarrowed))
					readerRounds = append(readerRounds, firstRequestStatements(t, e, r, rt, d.path, e.tokReader))
				} else {
					readerRounds = append(readerRounds, firstRequestStatements(t, e, r, rt, d.path, e.tokReader))
					narrowedRounds = append(narrowedRounds, firstRequestStatements(t, e, r, rt, d.path, tokNarrowed))
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

// R-ARCH-027: the statements of a request are the ones that ran with its own context, whatever else the database logs in the
// same instants. The replica's readiness check reads system_state (GetSystemState twice) on its own schedule; the first-request
// measurement counted that read when it fell into the window (F-125). A statement another connection sends while the request
// runs, named like one of the replica's, is not the request's.
func TestR_ARCH_027_AStatementOfAnotherConnectionIsNotTheRequests(t *testing.T) {
	e := newEnv(t)
	rt := testenv.NewRequestTransactions()
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web-foreign", Database: []postgres.Option{rt.Option()}})
	e.publish(r, rNPM, "budget-foreign", "1.0.0")
	path := "/api/npm/it/" + rNPM + "/budget-foreign"

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				e.st.Exec("-- name: GetSystemState :one\nSELECT 1")
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()
	got := firstRequestStatements(t, e, r, rt, path, e.tokReader)
	close(stop)
	<-done
	if n := got["GetSystemState"]; n != 0 {
		t.Errorf("the request is charged %d GetSystemState it did not run (%s)", n, describe(got))
	}
	if len(got) == 0 {
		t.Errorf("the request ran no statement the measurement saw; it reads nothing")
	}
}
