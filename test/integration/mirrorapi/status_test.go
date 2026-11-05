//go:build integration

package mirrorapi

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

func (e *env) member(a map[string]any, path ...string) any {
	var cur any = a
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// seedJob inserts a mirror-pass job row in a state and returns its id; the engine's jobs are rows of this table.
func (e *env) seedJob(state string) string {
	e.t.Helper()
	return e.st.Text(`INSERT INTO job (id, kind, queue, state, max_attempts, started_at) VALUES (gen_random_uuid(), 'mirror-pass', 'mirror', $1::job_state, 1, now()) RETURNING id::text`, state)
}

// seedState replaces the mirror_state row of a repository by one with the given assignments (the dispatch creates the row).
func (e *env) seedState(key, set string) {
	e.t.Helper()
	e.st.Exec(`DELETE FROM mirror_state WHERE repo_id = (SELECT id FROM repositories WHERE key = $1)`, key)
	e.st.Exec(`INSERT INTO mirror_state (repo_id) SELECT id FROM repositories WHERE key = $1`, key)
	if set != "" {
		e.st.Exec(`UPDATE mirror_state SET `+set+` WHERE repo_id = (SELECT id FROM repositories WHERE key = $1)`, key)
	}
}

// TestR_REPO_056_StatusOfEachState reads the status of a seeded mirror_state row in each of the states (R-REPO-056) over the
// management API of a replica: the same document a second replica would give, because it is read from PostgreSQL.
func TestR_REPO_056_StatusOfEachState(t *testing.T) {
	e := newEnv(t)
	state := func(key string) string {
		t.Helper()
		a := e.status("rita", key)
		e.wantStatus(a, http.StatusOK, "")
		return a.JSON(t)["state"].(string)
	}
	pass := `pass_started_at = now() - interval '10 minutes', pass_kind = 'incremental', pass_method = 'feed', pass_names = 120,
		pass_packages_done = 80, pass_packages_failed = 2, pass_items_stored = 311, pass_bytes_stored = 52428800, pass_discovery_done = true`

	t.Run("R-REPO-056 a remote in mode mirror without a state row answers scheduled with nulls", func(t *testing.T) {
		a := e.status("rita", "npm-bounded").JSON(t)
		if a["mode"] != "mirror" || a["state"] != "scheduled" || a["pass"] != nil || a["lastCompletePass"] != nil || a["nextPassAt"] != nil || a["lastFullPassAt"] != nil {
			t.Fatalf("%v", a)
		}
		// The bound comes from the configuration, the used bytes from the usage counters (zero before the first byte).
		if e.member(a, "bound", "maxBytes") != float64(107374182400) || e.member(a, "bound", "usedBytes") != float64(0) || e.member(a, "bound", "reached") != nil {
			t.Errorf("bound = %v", a["bound"])
		}
		if n := e.st.Number(`SELECT count(*) FROM mirror_state`); n != 0 {
			t.Errorf("a read created a row")
		}
	})
	t.Run("R-REPO-056 off: a remote in mode pullThrough, even with a row", func(t *testing.T) {
		e.seedState("npm-pull", "consecutive_failures = 5")
		if got := state("npm-pull"); got != "off" {
			t.Errorf("state = %s", got)
		}
	})
	t.Run("R-REPO-056 paused", func(t *testing.T) {
		e.seedState("npm-paused", pass)
		if got := state("npm-paused"); got != "paused" {
			t.Errorf("state = %s", got)
		}
	})
	t.Run("R-REPO-056 scheduled: no pass in progress, with the last complete pass and the next one", func(t *testing.T) {
		e.seedState("npm-state", `next_pass_at = '2026-11-04T11:00:00Z', last_full_pass_at = '2026-11-03T13:12:09Z',
			last_pass = '{"kind":"full","method":"feed","startedAt":"2026-11-03T10:00:00Z","finishedAt":"2026-11-03T13:12:09Z","outcome":"complete",
			"packages":1200,"packagesFailed":0,"itemsStored":5021,"bytesStored":9126805504,"discoveryComplete":true,"discoveryNote":null}'::jsonb`)
		a := e.status("rita", "npm-state").JSON(t)
		if a["state"] != "scheduled" || a["pass"] != nil || a["nextPassAt"] != "2026-11-04T11:00:00Z" || a["lastFullPassAt"] != "2026-11-03T13:12:09Z" {
			t.Fatalf("%v", a)
		}
		last := a["lastCompletePass"].(map[string]any)
		if last["outcome"] != "complete" || last["packages"] != float64(1200) || last["bytesStored"] != float64(9126805504) || last["finishedAt"] != "2026-11-03T13:12:09Z" {
			t.Errorf("lastCompletePass = %v", last)
		}
	})
	t.Run("R-REPO-056 running: a pass in progress whose job runs, with the counts, the pending names and the job", func(t *testing.T) {
		job := e.seedJob("running")
		e.seedState("npm-state", pass+`, pass_job_id = '`+job+`'`)
		e.st.Exec(`INSERT INTO mirror_pending (repo_id, name) SELECT id, n FROM repositories, unnest(ARRAY['a','b','c']) AS n WHERE key = 'npm-state'`)
		// A name left after three attempts is no pending name of the pass: the status counts what the engine still takes (Q-OPS-173).
		e.st.Exec(`INSERT INTO mirror_pending (repo_id, name, attempts) SELECT id, 'left', 3 FROM repositories WHERE key = 'npm-state'`)
		e.st.Exec(`INSERT INTO repository_usage (repo_id, logical_bytes) SELECT id, 9200000000 FROM repositories WHERE key = 'npm-state' ON CONFLICT (repo_id) DO UPDATE SET logical_bytes = EXCLUDED.logical_bytes`)
		a := e.status("rita", "npm-state").JSON(t)
		if a["state"] != "running" {
			t.Fatalf("%v", a)
		}
		p := a["pass"].(map[string]any)
		for k, want := range map[string]any{
			"kind": "incremental", "method": "feed", "namesDiscovered": float64(120), "packagesDone": float64(80), "packagesPending": float64(3),
			"packagesFailed": float64(2), "itemsStored": float64(311), "bytesStored": float64(52428800), "discoveryDone": true,
		} {
			if p[k] != want {
				t.Errorf("pass.%s = %v, want %v", k, p[k], want)
			}
		}
		if j := p["job"].(map[string]any); j["id"] != job || j["href"] != "/api/v1/system/jobs/"+job || j["kind"] != "mirror-pass" || j["state"] != "running" {
			t.Errorf("pass.job = %v", j)
		}
		if e.member(a, "bound", "usedBytes") != float64(9200000000) {
			t.Errorf("bound = %v", a["bound"])
		}
	})
	t.Run("R-REPO-056 waiting: a pass in progress and no job that runs it", func(t *testing.T) {
		for _, jobState := range []string{"failed", "queued"} {
			job := e.seedJob(jobState)
			e.seedState("npm-state", pass+`, pass_job_id = '`+job+`'`)
			if got := state("npm-state"); got != "waiting" {
				t.Errorf("job %s: state = %s", jobState, got)
			}
		}
		e.seedState("npm-state", pass+`, pass_job_id = gen_random_uuid()`) // the job row has been removed
		a := e.status("rita", "npm-state").JSON(t)
		if a["state"] != "waiting" || a["pass"].(map[string]any)["job"] != nil {
			t.Errorf("%v", a)
		}
	})
	t.Run("R-REPO-056 failing: three failures in a row, with the stored message", func(t *testing.T) {
		e.seedState("npm-state", `consecutive_failures = 2, last_error_at = now(), last_error_code = 'discovery_failed', last_error = 'discovery at registry.example.org answered 500'`)
		if got := state("npm-state"); got != "scheduled" {
			t.Errorf("two failures: state = %s", got)
		}
		e.seedState("npm-state", `consecutive_failures = 3, last_error_at = '2026-11-04T09:00:00Z', last_error_code = 'discovery_failed', last_error = 'discovery at registry.example.org answered 500'`)
		a := e.status("rita", "npm-state").JSON(t)
		if a["state"] != "failing" || e.member(a, "errors", "consecutiveFailures") != float64(3) || e.member(a, "errors", "lastCode") != "discovery_failed" ||
			e.member(a, "errors", "lastMessage") != "discovery at registry.example.org answered 500" || e.member(a, "errors", "lastAt") != "2026-11-04T09:00:00Z" {
			t.Errorf("%v", a)
		}
	})
	t.Run("R-REPO-056 bound_reached: the bound, when it was reached", func(t *testing.T) {
		e.seedState("npm-bounded", `bound_reached = 'bytes', bound_reached_at = '2026-11-04T10:30:00Z'`)
		a := e.status("rita", "npm-bounded").JSON(t)
		if a["state"] != "bound_reached" || e.member(a, "bound", "reached") != "bytes" || e.member(a, "bound", "at") != "2026-11-04T10:30:00Z" ||
			e.member(a, "bound", "maxBytes") != float64(107374182400) {
			t.Errorf("%v", a)
		}
	})
	t.Run("R-REPO-056 the rate-limit wait of the passes", func(t *testing.T) {
		e.seedState("npm-state", `rate_limit_waits = 12, rate_limit_wait_seconds = 340, last_rate_limit_at = '2026-11-04T10:20:31Z', last_rate_limit_status = 429,
			rate_limited_until = now() + interval '1 hour'`)
		a := e.status("rita", "npm-state").JSON(t)
		rl := a["rateLimit"].(map[string]any)
		until, _ := time.Parse(time.RFC3339, fmt.Sprint(rl["until"]))
		if rl["waits"] != float64(12) || rl["waitSeconds"] != float64(340) || rl["lastAt"] != "2026-11-04T10:20:31Z" || rl["lastStatus"] != float64(429) ||
			time.Until(until) < 55*time.Minute || time.Until(until) > 65*time.Minute {
			t.Errorf("rateLimit = %v", rl)
		}
		// A wait that is over is no rate limit of the status (Q-REPO-187).
		e.seedState("npm-state", `rate_limited_until = '2026-11-04T10:25:00Z'`)
		if rl := e.status("rita", "npm-state").JSON(t)["rateLimit"].(map[string]any); rl["until"] != nil {
			t.Errorf("a wait that is over is shown: %v", rl)
		}
	})
	t.Run("R-REPO-056 a local and a virtual repository are 400 not_applicable, an unknown one 404", func(t *testing.T) {
		e.wantStatus(e.status("rita", "npm-local"), http.StatusBadRequest, "not_applicable")
		e.wantStatus(e.status("rita", "npm-all"), http.StatusBadRequest, "not_applicable")
		e.wantStatus(e.status("rita", "no-such-repo"), http.StatusNotFound, "repository_not_found")
	})
}

// TestR_REPO_056_FormatDescriptorsCarryTheMirrorFact: GET /api/v1/formats says per format whether a remote of it can run in
// mode mirror (R-UI-021), the catalog fact Entry.Mirror: every format answers what its catalog entry says. The suite's own
// format is no registered format (only a catalog entry), so the served list shows the fact of the formats of the build.
func TestR_REPO_056_FormatDescriptorsCarryTheMirrorFact(t *testing.T) {
	e := newEnv(t)
	a := e.do("dave", "GET", "/api/v1/formats", nil)
	e.wantStatus(a, http.StatusOK, "")
	seen := map[string]bool{}
	for _, f := range a.JSON(t)["formats"].([]any) {
		d := f.(map[string]any)
		name := d["name"].(string)
		got, ok := d["mirror"].(bool)
		if !ok {
			t.Errorf("%s has no boolean mirror: %v", name, d["mirror"])
			continue
		}
		entry, _ := catalog.Default().Lookup(name)
		if got != entry.Mirror {
			t.Errorf("%s: mirror %v, the catalog says %v", name, got, entry.Mirror)
		}
		seen[name] = got
	}
	if v, ok := seen["generic"]; !ok || v {
		t.Errorf("generic: %v %v", v, ok)
	}
}
