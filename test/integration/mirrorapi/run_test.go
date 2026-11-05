//go:build integration

package mirrorapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

type jobRef struct{ ID, Kind, State, Href string }

func (e *env) jobOf(a testenv.Answer, member string) jobRef {
	e.t.Helper()
	m, _ := a.JSON(e.t)[member].(map[string]any)
	if m == nil {
		e.t.Fatalf("no %s in the answer", member)
	}
	b, _ := json.Marshal(m)
	var j jobRef
	_ = json.Unmarshal(b, &j)
	return j
}

// TestR_REPO_056_RunAnswers202ThenMirrorPassRunningWithTheSameJob is the criterion of R-REPO-056: two runs in a row answer 202
// and 409 mirror_pass_running with the same job. The request is recorded in the three columns of mirror_state, the job is
// the exclusive mirror-pass of the repository on the queue mirror, and the audit event of R-REPO-057 is written once.
func TestR_REPO_056_RunAnswers202ThenMirrorPassRunningWithTheSameJob(t *testing.T) {
	e := newEnv(t)
	id := e.repoID("npm-mirror")

	first := e.do("rita", "POST", runPath("npm-mirror"), nil)
	e.wantStatus(first, http.StatusAccepted, "")
	job := e.jobOf(first, "job")
	if job.Kind != "mirror-pass" || job.State != "queued" || job.Href != "/api/v1/system/jobs/"+job.ID {
		t.Fatalf("job = %+v", job)
	}
	if loc := first.Header.Get("Location"); !strings.HasSuffix(loc, job.Href) {
		t.Errorf("Location = %q", loc)
	}
	if got := e.st.Text(`SELECT kind || ' ' || queue || ' ' || dedup_key || ' ' || dedup_mode FROM job WHERE id = $1::uuid`, job.ID); got != "mirror-pass mirror mirror-pass:"+id+" exclusive" {
		t.Errorf("the job row is %q", got)
	}
	var args struct {
		Repository   string
		RepositoryID int64
		Full         bool
	}
	if err := json.Unmarshal([]byte(e.st.Text(`SELECT args::text FROM job WHERE id = $1::uuid`, job.ID)), &args); err != nil || args.Repository != "npm-mirror" || args.Full || id != jsonNumber(args.RepositoryID) {
		t.Errorf("args = %+v %v", args, err)
	}
	if got := e.st.Text(`SELECT target_kind || ' ' || target_key || ' ' || created_by::text FROM job WHERE id = $1::uuid`, job.ID); got != "repository npm-mirror "+e.rita.ID {
		t.Errorf("the job's target and creator are %q", got)
	}

	// R-REPO-056: the request is recorded, and nothing of the engine's is written.
	row := e.st.Text(`SELECT pass_requested_by::text || ' ' || pass_requested_full::text FROM mirror_state WHERE repo_id = $1::bigint`, id)
	if row != e.rita.ID+" false" {
		t.Errorf("request row = %q", row)
	}
	if n := e.st.Number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1::bigint AND pass_requested_at IS NOT NULL AND next_pass_at IS NULL
		AND pass_started_at IS NULL AND pass_job_id IS NULL AND pass_holder IS NULL AND consecutive_failures = 0 AND feed_cursor IS NULL
		AND last_pass IS NULL AND rate_limited_until IS NULL AND bound_reached IS NULL`, id); n != 1 {
		t.Errorf("the run wrote a column of the engine's")
	}
	requestedAt := e.st.Text(`SELECT pass_requested_at::text FROM mirror_state WHERE repo_id = $1::bigint`, id)

	// R-REPO-057: the audit event, written in the transaction of the request.
	if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'mirror.pass_requested' AND repository = 'npm-mirror'`); n != 1 {
		t.Fatalf("%d mirror.pass_requested events, want 1", n)
	}
	if got := e.st.Text(`SELECT actor_id::text || ' ' || (details->>'job') || ' ' || (details->>'full') FROM audit_event WHERE action = 'mirror.pass_requested'`); got != e.rita.ID+" "+job.ID+" false" {
		t.Errorf("event = %q", got)
	}

	second := e.do("rita", "POST", runPath("npm-mirror"), nil)
	e.wantStatus(second, http.StatusConflict, "mirror_pass_running")
	if again := e.jobOf(second, "job"); again.ID != job.ID || again.Href != job.Href {
		t.Errorf("the 409 names %+v, want %+v", again, job)
	}
	if !strings.Contains(second.JSON(t)["detail"].(string), job.ID) {
		t.Errorf("the detail does not name the job: %s", second.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`); n != 1 {
		t.Errorf("%d mirror-pass jobs, want 1", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'mirror.pass_requested'`); n != 1 {
		t.Errorf("the refused run wrote an event: %d", n)
	}
	if got := e.st.Text(`SELECT pass_requested_at::text FROM mirror_state WHERE repo_id = $1::bigint`, id); got != requestedAt {
		t.Errorf("the refused run moved the request: %s -> %s", requestedAt, got)
	}

	// R-OPS-019: the passes are listed as jobs.
	list := e.do("rita", "GET", "/api/v1/system/jobs?kind=mirror-pass", nil)
	e.wantStatus(list, http.StatusOK, "")
	found := false
	for _, it := range list.JSON(t)["items"].([]any) {
		found = found || it.(map[string]any)["id"] == job.ID
	}
	if !found {
		t.Errorf("the job is not in the list: %s", list.Body)
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestR_REPO_056_RunWritesOnlyTheRequestColumns: a row the engine has written keeps every value of its own across a run; a
// request not yet taken keeps a full flag already asked for, and a request taken starts over (Q-REPO-163).
func TestR_REPO_056_RunWritesOnlyTheRequestColumns(t *testing.T) {
	e := newEnv(t)
	id := e.repoID("npm-state")
	e.st.Exec(`INSERT INTO mirror_state (repo_id, next_pass_at, feed_cursor, feed_method, last_full_pass_at, last_pass, consecutive_failures,
			last_error_at, last_error_code, last_error, rate_limit_waits, rate_limit_wait_seconds, bound_reached, bound_reached_at)
		VALUES ($1::bigint, now() + interval '1 hour', '{"seq": 42}', 'feed', now() - interval '1 day', '{"outcome":"complete"}', 2,
			now() - interval '5 minutes', 'discovery_failed', 'discovery at registry.example.org failed', 12, 340, 'bytes', now() - interval '1 hour')`, id)
	snapshot := `SELECT row_to_json(s)::jsonb - 'pass_requested_at' - 'pass_requested_by' - 'pass_requested_full' - 'updated_at' FROM mirror_state s WHERE repo_id = $1::bigint`
	before := e.st.Text(snapshot, id)

	e.wantStatus(e.do("rita", "POST", runPath("npm-state"), map[string]any{"full": true}), http.StatusAccepted, "")
	if got := e.st.Text(snapshot, id); got != before {
		t.Errorf("the run changed a column of the engine's:\n%s\n%s", before, got)
	}
	if got := e.st.Text(`SELECT pass_requested_full::text FROM mirror_state WHERE repo_id = $1::bigint`, id); got != "true" {
		t.Errorf("pass_requested_full = %s", got)
	}
	var args struct{ Full bool }
	_ = json.Unmarshal([]byte(e.st.Text(`SELECT args::text FROM job WHERE kind = 'mirror-pass' AND dedup_key = 'mirror-pass:'||$1`, id)), &args)
	if !args.Full {
		t.Error("the job does not ask for a full pass")
	}
	if got := e.st.Text(`SELECT details->>'full' FROM audit_event WHERE action = 'mirror.pass_requested' AND repository = 'npm-state'`); got != "true" {
		t.Errorf("the event says full %s", got)
	}

	// The pass ended and the engine has not taken the request: a plain request keeps the full flag.
	e.st.Exec(`UPDATE job SET state = 'succeeded', finished_at = now() WHERE kind = 'mirror-pass'`)
	e.wantStatus(e.do("rita", "POST", runPath("npm-state"), nil), http.StatusAccepted, "")
	if got := e.st.Text(`SELECT pass_requested_full::text FROM mirror_state WHERE repo_id = $1::bigint`, id); got != "true" {
		t.Errorf("a plain request took a full one back: %s", got)
	}

	// The engine takes the request (clears pass_requested_at) and the next plain request is plain.
	e.st.Exec(`UPDATE job SET state = 'succeeded', finished_at = now() WHERE kind = 'mirror-pass'`)
	e.st.Exec(`UPDATE mirror_state SET pass_requested_at = NULL, pass_requested_by = NULL, pass_requested_full = false WHERE repo_id = $1::bigint`, id)
	e.wantStatus(e.do("rita", "POST", runPath("npm-state"), nil), http.StatusAccepted, "")
	if got := e.st.Text(`SELECT pass_requested_full::text FROM mirror_state WHERE repo_id = $1::bigint`, id); got != "false" {
		t.Errorf("pass_requested_full = %s", got)
	}
	if got := e.st.Text(snapshot, id); got != before {
		t.Errorf("the run changed a column of the engine's:\n%s\n%s", before, got)
	}
}

// TestR_REPO_056_RunRefusals: a remote in mode pullThrough answers 409 mirror_off, a paused one 409 mirror_paused, a local and
// a virtual repository 400 not_applicable, an unknown one 404; none of them enqueues a job, records a request or writes an
// event.
func TestR_REPO_056_RunRefusals(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		key    string
		status int
		code   string
	}{
		{"npm-pull", http.StatusConflict, "mirror_off"},
		{"npm-paused", http.StatusConflict, "mirror_paused"},
		{"npm-local", http.StatusBadRequest, "not_applicable"},
		{"npm-all", http.StatusBadRequest, "not_applicable"},
		{"no-such-repo", http.StatusNotFound, "repository_not_found"},
	} {
		a := e.do("rita", "POST", runPath(c.key), nil)
		if a.Status != c.status || a.Code(t) != c.code {
			t.Errorf("%s: %d %s", c.key, a.Status, a.Body)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`) + e.st.Number(`SELECT count(*) FROM mirror_state`) +
		e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'mirror.pass_requested'`); n != 0 {
		t.Errorf("a refused run left %d rows", n)
	}
	// R-API-003: a body that is not the request.
	for _, body := range []string{`{"full":"yes"}`, `{"everything":true}`} {
		if a := e.do("rita", "POST", runPath("npm-mirror"), body); a.Status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, a.Status, a.Body)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`); n != 0 {
		t.Errorf("a refused body enqueued %d jobs", n)
	}
}

// TestR_REPO_056_PausingThroughThePatchStopsTheRun: pause and resume are the member remote.mirror.paused, changed with the
// repository's PATCH and its If-Match (Q-REPO-141); the status follows and the run is refused while paused.
func TestR_REPO_056_PausingThroughThePatchStopsTheRun(t *testing.T) {
	e := newEnv(t)
	// The mirror is created through the API, so that its stored section is the canonical one that a PATCH merges into.
	create := e.do("rita", "POST", "/api/v1/repositories", map[string]any{
		"key": "npm-patch", "class": "remote", "format": mirrorFormat, "remote": map[string]any{"url": upstream, "mode": "mirror"},
	})
	e.wantStatus(create, http.StatusCreated, "")
	patch := func(paused bool) {
		t.Helper()
		got := e.do("rita", "GET", "/api/v1/repositories/npm-patch", nil)
		e.wantStatus(got, http.StatusOK, "")
		a := e.do("rita", "PATCH", "/api/v1/repositories/npm-patch", map[string]any{"remote": map[string]any{"mirror": map[string]any{"paused": paused}}},
			"Content-Type", "application/merge-patch+json", "If-Match", got.Header.Get("ETag"))
		e.wantStatus(a, http.StatusOK, "")
	}
	state := func() string { return e.status("rita", "npm-patch").JSON(t)["state"].(string) }

	if got := state(); got != "scheduled" {
		t.Fatalf("state = %s", got)
	}
	patch(true)
	if got := state(); got != "paused" {
		t.Errorf("state after the pause = %s", got)
	}
	e.wantStatus(e.do("rita", "POST", runPath("npm-patch"), nil), http.StatusConflict, "mirror_paused")
	patch(false)
	if got := state(); got != "scheduled" {
		t.Errorf("state after the resume = %s", got)
	}
	e.wantStatus(e.do("rita", "POST", runPath("npm-patch"), nil), http.StatusAccepted, "")

	// Turning the mode back to pullThrough: off, and a run is refused (Q-REPO-148 keeps the rest).
	got := e.do("rita", "GET", "/api/v1/repositories/npm-patch", nil)
	e.wantStatus(e.do("rita", "PATCH", "/api/v1/repositories/npm-patch", map[string]any{"remote": map[string]any{"mode": "pullThrough"}},
		"Content-Type", "application/merge-patch+json", "If-Match", got.Header.Get("ETag")), http.StatusOK, "")
	if got := state(); got != "off" {
		t.Errorf("state after the mode change = %s", got)
	}
	e.st.Exec(`UPDATE job SET state = 'succeeded', finished_at = now() WHERE kind = 'mirror-pass'`)
	e.wantStatus(e.do("rita", "POST", runPath("npm-patch"), nil), http.StatusConflict, "mirror_off")
}
