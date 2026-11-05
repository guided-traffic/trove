//go:build integration

package mirror

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// The status and the run operation with the real engine (E6b.5; R-REPO-056, R-REPO-057). The permissions, the refusals and the
// states of a seeded row are the suite test/integration/mirrorapi; this one runs a pass and reads what the API and the
// observability show of it.

func jobRef(t *testing.T, doc map[string]any) (id string) {
	t.Helper()
	job, _ := doc["job"].(map[string]any)
	id, _ = job["id"].(string)
	if id == "" {
		t.Fatalf("no job reference in %v", doc)
	}
	return id
}

func TestR_REPO_056_AStatusFollowsARunningPassAndEveryReplicaAnswersTheSame(t *testing.T) {
	const packages = 20
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, mirror: settings{"concurrency": 2}})
	r.publishMany("@acme", packages)
	r.setStub(npmstub.Mode{DelayMS: map[string]int{npmstub.KindTarball: 300}})
	run, a, b := r.runner("runner"), r.web("web-a"), r.web("web-b")
	status := "/api/v1/repositories/" + repoKey + "/mirror"
	before := r.api(a, http.MethodGet, status, nil).JSON(t)

	t.Run("R-REPO-056 a status before any pass is scheduled", func(t *testing.T) {
		if before["state"] != "scheduled" || before["pass"] != nil || before["lastCompletePass"] != nil {
			t.Errorf("status %v", before)
		}
	})

	first := r.api(a, http.MethodPost, status+"/run", nil)
	second := r.api(b, http.MethodPost, status+"/run", nil)

	t.Run("R-REPO-056 two runs in a row answer 202 and 409 mirror_pass_running with the same job", func(t *testing.T) {
		if first.Status != http.StatusAccepted {
			t.Fatalf("first run: %d %s", first.Status, first.Body)
		}
		if second.Status != http.StatusConflict || second.Code(t) != "mirror_pass_running" {
			t.Fatalf("second run: %d %s", second.Status, second.Body)
		}
		if got, want := jobRef(t, second.JSON(t)), jobRef(t, first.JSON(t)); got != want {
			t.Errorf("the 409 names the job %s, the 202 named %s", got, want)
		}
	})
	jobID := jobRef(t, first.JSON(t))

	// During the pass: running, with counts that grow, from every replica.
	var done []float64
	var sawJob string
	deadline := time.Now().Add(passWithin)
	for time.Now().Before(deadline) {
		doc := r.api(a, http.MethodGet, status, nil).JSON(t)
		if doc["state"] == "running" {
			pass, _ := doc["pass"].(map[string]any)
			job, _ := pass["job"].(map[string]any)
			sawJob, _ = job["id"].(string)
			d, _ := pass["packagesDone"].(float64)
			if len(done) == 0 || done[len(done)-1] != d {
				done = append(done, d)
			}
			other := r.api(b, http.MethodGet, status, nil).JSON(t)
			if other["state"] == "running" && sawJob != jobID {
				t.Fatalf("the pass is run by the job %s, the run named %s", sawJob, jobID)
			}
		}
		if doc["lastCompletePass"] != nil && doc["state"] == "scheduled" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	t.Run("R-REPO-056 during the pass the status is running with counts that grow", func(t *testing.T) {
		if len(done) < 3 {
			t.Fatalf("packagesDone was seen as %v: the status did not follow the pass", done)
		}
		for i := 1; i < len(done); i++ {
			if done[i] < done[i-1] {
				t.Errorf("packagesDone went down: %v", done)
			}
		}
		if sawJob != jobID {
			t.Errorf("pass.job is %q, want the job of the run %s", sawJob, jobID)
		}
	})

	t.Run("R-REPO-056 after the pass lastCompletePass is complete, pass is null and every replica answers the same", func(t *testing.T) {
		var bodies []map[string]any
		for _, answer := range []string{string(r.api(a, http.MethodGet, status, nil).Body), string(r.api(b, http.MethodGet, status, nil).Body)} {
			var m map[string]any
			if err := json.Unmarshal([]byte(answer), &m); err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, m)
		}
		doc := bodies[0]
		last, _ := doc["lastCompletePass"].(map[string]any)
		if last["outcome"] != "complete" || doc["pass"] != nil || doc["state"] != "scheduled" || last["packages"] != float64(packages) || last["itemsStored"] != float64(packages) {
			t.Errorf("status %v", doc)
		}
		if a, b := fmt.Sprint(bodies[0]), fmt.Sprint(bodies[1]); a != b {
			t.Errorf("the replicas answer differently:\n%s\n%s", a, b)
		}
	})

	t.Run("R-REPO-057 the items counter grew by the stored tarballs, and the audit store holds the populate events and one mirror.pass_completed", func(t *testing.T) {
		if got := r.metric(run, "trove_mirror_items_total", "result", "stored"); got != packages {
			t.Errorf("trove_mirror_items_total{result=stored} = %v, want %d", got, packages)
		}
		if got := r.metric(run, "trove_mirror_passes_total", "kind", "full", "outcome", "complete"); got != 1 {
			t.Errorf("trove_mirror_passes_total{full,complete} = %v, want 1", got)
		}
		if got := r.metric(run, "trove_mirror_bytes_total"); got <= 0 {
			t.Errorf("trove_mirror_bytes_total = %v", got)
		}
		populate := `SELECT count(*) FROM audit_event WHERE action = 'authz.write.allowed' AND repository = $1 AND actor_type = 'system'
			AND actor_name = 'mirror' AND reason = 'mirror' AND details->>'reason' = 'mirror' AND `
		r.waitAudit("mirror.pass_completed", 1)
		if n := r.number(populate+`path LIKE '%.tgz'`, repoKey); n != packages {
			t.Errorf("%d populate events of tarballs by the system with the reason mirror, want %d", n, packages)
		}
		// Every document a pass stores writes the populate event as well (R-REPO-057, Q-REPO-145).
		if n := r.number(populate+`path NOT LIKE '%.tgz'`, repoKey); n != packages {
			t.Errorf("%d populate events of documents, want %d", n, packages)
		}
		if n := r.waitAudit("mirror.pass_completed", 1); n != 1 {
			t.Errorf("%d mirror.pass_completed events, want 1", n)
		}
		if n := r.number(`SELECT count(*) FROM audit_event WHERE action = 'mirror.pass_requested' AND repository = $1`, repoKey); n != 1 {
			t.Errorf("%d mirror.pass_requested events, want 1 (the second run was refused)", n)
		}
	})
}
