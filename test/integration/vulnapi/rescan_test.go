//go:build integration

package vulnapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_011_RescanAnswers202ThenTheRunningJobWith409: the first request enqueues the exclusive vuln-rescan job of the
// repository (the kind is registered by the evaluation), the second one finds it queued and answers 409 with the same job
// (R-VULN-011's criterion); a caller without manage on the repository is refused (Q-VULN-27), a repository that is not scanned
// (virtual, or of a format without the adapter) is 422 and an unknown key is refused like a repository the caller may not manage.
func TestR_VULN_011_RescanAnswers202ThenTheRunningJobWith409(t *testing.T) {
	e := newEnv(t)
	path := "/api/v1/projects/it/repositories/npm-local/vulnerabilities/rescan"

	first := e.st.Do(e.r, "POST", path, e.tokRita, nil)
	if first.Status != http.StatusAccepted {
		t.Fatalf("first rescan: %d %s", first.Status, first.Body)
	}
	var ans struct {
		Job struct{ ID, Kind, State string }
	}
	if err := json.Unmarshal(first.Body, &ans); err != nil || ans.Job.ID == "" || ans.Job.Kind != "vuln-rescan" {
		t.Fatalf("the answer names no vuln-rescan job: %v %s", err, first.Body)
	}
	job := ans.Job
	if got := e.st.Text(`SELECT kind || ' ' || dedup_key FROM job WHERE id = $1::uuid`, job.ID); got != "vuln-rescan vuln-rescan:"+e.st.Text(`SELECT id::text FROM repositories WHERE key = 'npm-local'`) {
		t.Errorf("job %s is %q", job.ID, got)
	}

	second := e.st.Do(e.r, "POST", path, e.tokRita, nil)
	if second.Status != http.StatusConflict || second.Code(t) != "conflict" {
		t.Fatalf("second rescan: %d %s", second.Status, second.Body)
	}
	running, _ := second.JSON(t)["job"].(map[string]any)
	if running == nil || running["id"] != job.ID {
		t.Errorf("the 409 names job %v, want %s", running, job.ID)
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'vuln-rescan'`); n != 1 {
		t.Errorf("%d vuln-rescan jobs, want 1", n)
	}

	if a := e.st.Do(e.r, "POST", path, e.tokAlice, nil); a.Status != http.StatusForbidden {
		t.Errorf("a reader: %d %s", a.Status, a.Body)
	}
	if a := e.st.Do(e.r, "POST", "/api/v1/projects/it/repositories/npm-all/vulnerabilities/rescan", e.tokRita, nil); a.Status != http.StatusUnprocessableEntity {
		t.Errorf("a virtual repository: %d %s", a.Status, a.Body)
	}
	// The authorization step runs before the lookup, so an unknown key is as refused as a repository the caller may not manage.
	if a := e.st.Do(e.r, "POST", "/api/v1/projects/it/repositories/nope/vulnerabilities/rescan", e.tokRita, nil); a.Status != http.StatusForbidden {
		t.Errorf("an unknown repository: %d %s", a.Status, a.Body)
	}
}

// TestR_VULN_001_TheEvaluationStatesOfAVersion: the derived state of a stored version with the real adapters and the real
// claim index: evaluated, pending_data (the data was never imported completely), internal (the name is claimed by a local
// repository whose matchPublicAdvisories is auto) and not_covered (a format without the adapter is not part of the report,
// scanning off is).
func TestR_VULN_001_TheEvaluationStatesOfAVersion(t *testing.T) {
	e := newEnv(t)
	e.addVersion("npm-local", "@acme/ui", "1.0.0")
	e.addVersion("npm-local", "open-pkg", "1.0.0")
	e.addVersion("npm-off", "open-pkg", "1.0.0")
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, '@acme/*', 'manual', 'members' FROM repositories WHERE key = 'npm-local'`)

	states := func(name, version string) map[string]string {
		t.Helper()
		a := e.get(e.r, "/api/v1/packages/"+packageID("npm", name)+"/versions/"+version+"/vulnerabilities", e.tokRita)
		if a.Status != http.StatusOK {
			t.Fatalf("%s@%s: %d %s", name, version, a.Status, a.Body)
		}
		var v struct {
			Repositories []struct{ Repository, Evaluation string }
		}
		if err := json.Unmarshal(a.Body, &v); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range v.Repositories {
			out[r.Repository] = r.Evaluation
		}
		return out
	}
	expect := func(what, name string, want map[string]string) {
		t.Helper()
		testenv.WaitFor(t, what, 20*time.Second, func() bool {
			got := states(name, "1.0.0")
			for repo, st := range want {
				if got[repo] != st {
					return false
				}
			}
			return true
		})
	}

	// No import yet: the data was never complete.
	expect("pending_data before the first import", "open-pkg", map[string]string{"npm-local": "pending_data", "npm-off": "not_covered"})
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at) VALUES ('osv', 'npm', 'complete', now())`)
	expect("evaluated after the import", "open-pkg", map[string]string{"npm-local": "evaluated", "npm-off": "not_covered"})
	expect("internal for a claimed name", "@acme/ui", map[string]string{"npm-local": "internal"})
}
