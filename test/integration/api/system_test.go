//go:build integration

package api

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/secrets"
)

// waitJob polls the job through the cluster until it is in one of the states.
func waitJob(t *testing.T, c *webCluster, id string, hdr []string, states ...string) map[string]any {
	t.Helper()
	var last map[string]any
	waitFor(t, "job "+id+" to reach "+strings.Join(states, "/"), 30*time.Second, func() bool {
		a := send(t, c, "GET", "/api/v1/system/jobs/"+id, nil, hdr...)
		if a.Status != 200 {
			t.Fatalf("GET job: %d %s", a.Status, a.Body)
		}
		last = a.JSON(t)
		for _, s := range states {
			if last["state"] == s {
				return true
			}
		}
		return false
	})
	return last
}

// TestGarbageCollectionTrigger is the criterion of R-HA-002 and R-OPS-020: a run
// triggered through a web pod executes on a worker pod, and web only enqueues.
// Every answer is validated against api/openapi.yaml (E1.6).
func TestGarbageCollectionTrigger(t *testing.T) {
	e := newEnv(t, true)
	val := e.stack.Validator()
	webs := []*node{e.newNode("web-1", nodeOpts{}), e.newNode("web-2", nodeOpts{}), e.newNode("web-3", nodeOpts{})}
	c := e.cluster(webs...)
	adminID := e.admin.ID
	admin := e.admin.header()

	// 1. No worker is live: the trigger enqueues, the job stays queued.
	a := send(t, c, "POST", "/api/v1/system/tasks/gc/run", map[string]any{"dryRun": true}, admin...)
	if a.Status != 202 {
		t.Fatalf("trigger: %d %s", a.Status, a.Body)
	}
	job := a.JSON(t)["job"].(map[string]any)
	id := job["id"].(string)
	if a.Header.Get("Location") != "http://trove.test/api/v1/system/jobs/"+id || job["state"] != "queued" || job["kind"] != "gc" || job["href"] != "/api/v1/system/jobs/"+id {
		t.Fatalf("Location %q job %v", a.Header.Get("Location"), job)
	}
	time.Sleep(1500 * time.Millisecond)
	queued := waitJob(t, c, id, admin, "queued")
	if queued["replica"] != nil || queued["triggeredBy"] != adminID {
		t.Fatalf("%v", queued)
	}

	// 2. R-OPS-020: either task while a run is queued answers 409 with that job (the shared key).
	for _, path := range []string{"/api/v1/system/tasks/gc/run", "/api/v1/system/tasks/gc-full/run"} {
		again := send(t, c, "POST", path, nil, admin...)
		if again.Status != 409 || again.Code(t) != "conflict" {
			t.Fatalf("%s: %d %s", path, again.Status, again.Body)
		}
		if got := again.JSON(t)["job"].(map[string]any)["id"]; got != id {
			t.Fatalf("%s: the 409 names job %v, want %s", path, got, id)
		}
	}

	// 3. A worker starts and executes it: replica is the worker, never a web pod.
	e.newNode("worker-1", nodeOpts{role: "worker"})
	done := waitJob(t, c, id, admin, "succeeded")
	if done["replica"] != "worker-1" {
		t.Fatalf("the run was executed by %v, want worker-1", done["replica"])
	}
	// The run is the real collector's: its result is the collector's summary, and
	// the replica that ran it is the job's own record.
	result := done["result"].(map[string]any)
	if result["dryRun"] != true || result["errors"] != float64(0) {
		t.Fatalf("result = %v", result)
	}
	for _, w := range webs {
		if n := e.scalar("SELECT count(*) FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.ran_by WHERE r.name = $1", w.Name); n != 0 {
			t.Fatalf("%s ran %d jobs; web replicas only enqueue", w.Name, n)
		}
	}

	// 4. The finished run is listed by kind, and a new one may start.
	// (The scheduler of the worker may have added the latest due slot of the task, a run
	// without triggeredBy: the list holds ours among the runs of the kind.)
	list := send(t, c, "GET", "/api/v1/system/jobs?kind=gc", nil, admin...)
	found := false
	for _, it := range list.JSON(t)["items"].([]any) {
		m := it.(map[string]any)
		if m["kind"] != "gc" {
			t.Fatalf("kind=gc lists %v", m["kind"])
		}
		found = found || m["id"] == id
	}
	if list.Status != 200 || !found {
		t.Fatalf("%d %s", list.Status, list.Body)
	}
	waitFor(t, "no gc run to be queued or running", 20*time.Second, func() bool {
		return e.scalar("SELECT count(*) FROM trove.job WHERE dedup_key = 'gc' AND state IN ('queued', 'running', 'cancelling')") == 0
	})
	next := send(t, c, "POST", "/api/v1/system/tasks/gc-full/run", map[string]any{}, admin...)
	if next.Status != 202 {
		t.Fatalf("a run after the first ended: %d %s", next.Status, next.Body)
	}

	// 5. Arguments are checked against the task's type.
	waitFor(t, "the full run to end", 20*time.Second, func() bool {
		return e.scalar("SELECT count(*) FROM trove.job WHERE kind = 'gc-full' AND state = 'succeeded' AND created_by IS NOT NULL") == 1
	})
	for _, c2 := range []struct {
		body any
		want int
	}{{map[string]any{"dryrun": true}, 400}, {map[string]any{"dryRun": "yes"}, 400}, {`[1]`, 400}} {
		if bad := send(t, c, "POST", "/api/v1/system/tasks/gc/run", c2.body, admin...); bad.Status != c2.want || bad.Code(t) != "malformed_request" {
			t.Fatalf("%v: %d %s", c2.body, bad.Status, bad.Body)
		}
	}
	if unknown := send(t, c, "POST", "/api/v1/system/tasks/nope/run", nil, admin...); unknown.Status != 404 {
		t.Fatalf("unknown task: %d", unknown.Status)
	}
	if denied := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil, e.alice.header()...); denied.Status != 403 {
		t.Fatalf("a caller without settings:write: %d", denied.Status)
	}
	if anon := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil); anon.Status != 401 {
		t.Fatalf("anonymous: %d", anon.Status)
	}
	if missing := val.Uncovered("listJobs", "getJob", "runTask"); len(missing) > 0 {
		t.Fatalf("operations not reached: %v", missing)
	}
}

// TestJobAccessAndPaging covers who sees and cancels which job (R-API-015) and
// the cursor walk over the jobs across replicas (R-API-010, R-API-051).
func TestJobAccessAndPaging(t *testing.T) {
	e := newEnv(t, true)
	val := e.stack.Validator()
	webs := []*node{e.newNode("web-1", nodeOpts{}), e.newNode("web-2", nodeOpts{}), e.newNode("web-3", nodeOpts{})}
	c := e.cluster(webs...)
	alice, bob := e.alice.ID, e.bob.ID
	admin, reader := e.admin.header(), e.reader.header()
	asAlice := e.alice.header()

	// Jobs started by three principals; kinds without a registered handler never run here.
	insert := func(createdBy string, kind string) string {
		id := uuidV4()
		e.exec("INSERT INTO trove.job (id, kind, queue, created_by) VALUES ($1, $2, 'maintenance', $3)", id, kind, nullable(createdBy))
		time.Sleep(3 * time.Millisecond) // distinct creation times, though the order does not depend on them
		return id
	}
	var aliceJobs, all []string
	for i := range 3 {
		id := insert(alice, "probe")
		aliceJobs, all = append(aliceJobs, id), append(all, id)
		all = append(all, insert(bob, "probe"))
		if i == 0 {
			all = append(all, insert("", "sweep"))
		}
	}

	t.Run("R-API-015 a caller without settings:read lists and reads only the jobs it started", func(t *testing.T) {
		page := send(t, c, "GET", "/api/v1/system/jobs", nil, asAlice...)
		items := page.JSON(t)["items"].([]any)
		if page.Status != 200 || len(items) != 3 {
			t.Fatalf("%d %s", page.Status, page.Body)
		}
		for _, it := range items {
			if it.(map[string]any)["triggeredBy"] != alice {
				t.Fatalf("alice sees %v", it)
			}
		}
		if own := send(t, c, "GET", "/api/v1/system/jobs/"+aliceJobs[0], nil, asAlice...); own.Status != 200 || own.Header.Get("Retry-After") != "2" {
			t.Fatalf("own job: %d retry-after %q", own.Status, own.Header.Get("Retry-After"))
		}
		bobsJob := all[1]
		if other := send(t, c, "GET", "/api/v1/system/jobs/"+bobsJob, nil, asAlice...); other.Status != 404 {
			t.Fatalf("another caller's job: %d", other.Status)
		}
		if missing := send(t, c, "GET", "/api/v1/system/jobs/"+uuidV4(), nil, asAlice...); missing.Status != 404 {
			t.Fatalf("a missing job: %d", missing.Status)
		}
		if anon := send(t, c, "GET", "/api/v1/system/jobs", nil); anon.Status != 401 {
			t.Fatalf("anonymous: %d", anon.Status)
		}
	})

	t.Run("R-OPS-019 a caller with settings:read sees every job and filters by kind and state", func(t *testing.T) {
		page := send(t, c, "GET", "/api/v1/system/jobs?limit=500", nil, reader...)
		if n := len(page.JSON(t)["items"].([]any)); n != len(all) {
			t.Fatalf("%d jobs, want %d", n, len(all))
		}
		sweep := send(t, c, "GET", "/api/v1/system/jobs?kind=sweep&state=queued", nil, reader...)
		if items := sweep.JSON(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["triggeredBy"] != nil {
			t.Fatalf("%s", sweep.Body)
		}
		if bad := send(t, c, "GET", "/api/v1/system/jobs?state=weird", nil, reader...); bad.Status != 400 || bad.Code(t) != "malformed_request" {
			t.Fatalf("%d %s", bad.Status, bad.Body)
		}
		if bad := send(t, c, "GET", "/api/v1/system/jobs?fromat=x", nil, reader...); bad.Status != 400 {
			t.Fatalf("%d", bad.Status)
		}
	})

	t.Run("R-API-010 R-API-051 walking the jobs with limit=1 across replicas visits each once, in either order", func(t *testing.T) {
		for _, sort := range []string{"-enqueuedAt", "enqueuedAt"} {
			seen := map[string]bool{}
			replicas := map[string]bool{}
			var order []string
			path := "/api/v1/system/jobs?limit=1&sort=" + url.QueryEscape(sort)
			for pages := 0; ; pages++ {
				if pages > len(all)+2 {
					t.Fatal("the walk does not end")
				}
				a := send(t, c, "GET", path, nil, reader...)
				if a.Status != 200 {
					t.Fatalf("%d %s", a.Status, a.Body)
				}
				replicas[a.Header.Get("X-Trove-Replica")] = true
				m := a.JSON(t)
				for _, it := range m["items"].([]any) {
					id := it.(map[string]any)["id"].(string)
					if seen[id] {
						t.Fatalf("%s twice", id)
					}
					seen[id] = true
					order = append(order, it.(map[string]any)["enqueuedAt"].(string))
				}
				if m["nextCursor"] == nil {
					break
				}
				path = "/api/v1/system/jobs?limit=1&sort=" + url.QueryEscape(sort) + "&cursor=" + m["nextCursor"].(string)
			}
			if len(seen) != len(all) || len(replicas) < 2 {
				t.Fatalf("sort %s: %d of %d jobs, %d replicas", sort, len(seen), len(all), len(replicas))
			}
			for i := 1; i < len(order); i++ {
				if (sort[0] == '-') == (order[i] > order[i-1]) && order[i] != order[i-1] {
					t.Fatalf("sort %s: %s before %s", sort, order[i-1], order[i])
				}
			}
		}
		a := send(t, c, "GET", "/api/v1/system/jobs?limit=1", nil, reader...)
		cur := a.JSON(t)["nextCursor"].(string)
		if bad := send(t, c, "GET", "/api/v1/system/jobs?limit=1&sort=enqueuedAt&cursor="+cur, nil, reader...); bad.Status != 400 || bad.Code(t) != "invalid_cursor" {
			t.Fatalf("a cursor for another sort: %d %s", bad.Status, bad.Body)
		}
		if bad := send(t, c, "GET", "/api/v1/system/jobs?limit=1&kind=probe&cursor="+cur, nil, reader...); bad.Status != 400 || bad.Code(t) != "invalid_cursor" {
			t.Fatalf("a cursor for other filters: %d %s", bad.Status, bad.Body)
		}
	})

	t.Run("R-API-015 an administrator or the caller who started the job may cancel it", func(t *testing.T) {
		if other := send(t, c, "POST", "/api/v1/system/jobs/"+all[1]+"/cancel", nil, asAlice...); other.Status != 404 {
			t.Fatalf("alice cancels bob's job: %d", other.Status)
		}
		if seeOnly := send(t, c, "POST", "/api/v1/system/jobs/"+aliceJobs[1]+"/cancel", nil, reader...); seeOnly.Status != 403 {
			t.Fatalf("a reader who did not start the job: %d", seeOnly.Status)
		}
		own := send(t, c, "POST", "/api/v1/system/jobs/"+aliceJobs[0]+"/cancel", nil, asAlice...)
		if own.Status != 202 || own.JSON(t)["state"] != "cancelled" || own.Header.Get("Location") != "http://trove.test/api/v1/system/jobs/"+aliceJobs[0] {
			t.Fatalf("%d %s", own.Status, own.Body)
		}
		if again := send(t, c, "POST", "/api/v1/system/jobs/"+aliceJobs[0]+"/cancel", nil, asAlice...); again.Status != 409 || again.Code(t) != "conflict" {
			t.Fatalf("a finished job: %d %s", again.Status, again.Body)
		}
		if adminCancel := send(t, c, "POST", "/api/v1/system/jobs/"+all[1]+"/cancel", nil, admin...); adminCancel.Status != 202 {
			t.Fatalf("admin: %d %s", adminCancel.Status, adminCancel.Body)
		}
		// A running job turns cancelling.
		running := uuidV4()
		e.exec("INSERT INTO trove.job (id, kind, queue, state, created_by, locked_by, locked_until, attempt) VALUES ($1, 'probe', 'maintenance', 'running', $2, $3, now() + interval '1 hour', 1)", running, alice, uuidV4())
		if r := send(t, c, "POST", "/api/v1/system/jobs/"+running+"/cancel", nil, asAlice...); r.Status != 202 || r.JSON(t)["state"] != "cancelling" {
			t.Fatalf("%d %s", r.Status, r.Body)
		}
		if r := send(t, c, "POST", "/api/v1/system/jobs/"+running+"/cancel", nil, asAlice...); r.Status != 202 || r.JSON(t)["state"] != "cancelling" {
			t.Fatalf("cancelling twice: %d %s", r.Status, r.Body)
		}
	})

	if missing := val.Uncovered("listJobs", "getJob", "cancelJob"); len(missing) > 0 {
		t.Fatalf("operations not reached: %v", missing)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestTaskList checks GET /api/v1/system/tasks (R-OPS-019): schedule from the
// registry, a manual-only task with null schedule, the last run, and paging.
func TestTaskList(t *testing.T) {
	e := newEnv(t, true)
	val := e.stack.Validator()
	webs := []*node{e.newNode("web-1", nodeOpts{}), e.newNode("web-2", nodeOpts{})}
	c := e.cluster(webs...)
	reader := e.reader.header()

	a := send(t, c, "GET", "/api/v1/system/tasks?limit=500", nil, reader...)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	byName := map[string]map[string]any{}
	var names []string
	for _, it := range a.JSON(t)["items"].([]any) {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
		names = append(names, m["name"].(string))
	}
	for _, want := range []string{"gc", "gc-full", "idempotency-cleanup", "secrets-reencrypt"} {
		if byName[want] == nil {
			t.Fatalf("task %s is not listed: %v", want, names)
		}
	}
	if gc := byName["gc"]; gc["schedule"] != "*/30 * * * *" || gc["nextRunAt"] == nil || gc["enabled"] != true || gc["running"] != false || gc["lastRun"] != nil {
		t.Fatalf("gc = %v", gc)
	}
	if m := byName["secrets-reencrypt"]; m["schedule"] != nil || m["nextRunAt"] != nil {
		t.Fatalf("a manual-only task has no schedule: %v", m)
	}
	for i := 1; i < len(names); i++ {
		if names[i] <= names[i-1] {
			t.Fatalf("tasks are ordered by name: %v", names)
		}
	}

	t.Run("R-OPS-019 the last run and the running state appear", func(t *testing.T) {
		e.newNode("worker-1", nodeOpts{role: "worker"})
		admin := e.admin.header()
		run := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil, admin...)
		id := run.JSON(t)["job"].(map[string]any)["id"].(string)
		waitJob(t, c, id, admin, "succeeded")
		gc := func() map[string]any {
			for _, it := range send(t, c, "GET", "/api/v1/system/tasks?limit=500", nil, reader...).JSON(t)["items"].([]any) {
				if it.(map[string]any)["name"] == "gc" {
					return it.(map[string]any)
				}
			}
			return nil
		}()
		last, _ := gc["lastRun"].(map[string]any)
		// The scheduler may run gc again after the triggered run, so the last run is
		// the triggered one or one that started after it finished.
		if last["jobId"] != id {
			ours := send(t, c, "GET", "/api/v1/system/jobs/"+id, nil, admin...).JSON(t)
			fin, _ := ours["finishedAt"].(string)
			started, _ := last["startedAt"].(string)
			tf, err1 := time.Parse(time.RFC3339Nano, fin)
			ts, err2 := time.Parse(time.RFC3339Nano, started)
			if err1 != nil || err2 != nil || ts.Before(tf) {
				t.Fatalf("lastRun = %v, but the triggered run %s finished at %q", last, id, fin)
			}
		}
		if last["outcome"] != "succeeded" || last["replica"] != "worker-1" || last["finishedAt"] == nil {
			t.Fatalf("lastRun = %v", last)
		}
	})

	t.Run("R-API-010 the task list pages with limit=1 across replicas", func(t *testing.T) {
		seen := map[string]bool{}
		path := "/api/v1/system/tasks?limit=1"
		for pages := 0; ; pages++ {
			if pages > len(names)+2 {
				t.Fatal("the walk does not end")
			}
			p := send(t, c, "GET", path, nil, reader...)
			m := p.JSON(t)
			for _, it := range m["items"].([]any) {
				seen[it.(map[string]any)["name"].(string)] = true
			}
			if m["nextCursor"] == nil {
				break
			}
			path = "/api/v1/system/tasks?limit=1&cursor=" + m["nextCursor"].(string)
		}
		if len(seen) != len(names) {
			t.Fatalf("%d of %d tasks", len(seen), len(names))
		}
	})

	t.Run("R-OPS-019 listing tasks needs settings:read", func(t *testing.T) {
		if a := send(t, c, "GET", "/api/v1/system/tasks", nil, e.alice.header()...); a.Status != 403 {
			t.Fatalf("%d", a.Status)
		}
	})
	if missing := val.Uncovered("listTasks"); len(missing) > 0 {
		t.Fatalf("operations not reached: %v", missing)
	}
}

// TestKeyRingStatus is the acceptance of R-API-049 with two replicas whose key
// rings differ: the status says so and the re-encryption refuses to start.
func TestKeyRingStatus(t *testing.T) {
	e := newEnv(t, true)
	val := e.stack.Validator()
	oldKey, newKey := e.keyFile("2026-04", 1), e.keyFile("2026-10", 2)
	// Replica A still runs the old ring; B was started with a new key active that A's ring lacks.
	a := e.newNode("web-a", nodeOpts{env: ringEnv("2026-04", oldKey)})
	b := e.newNode("web-b", nodeOpts{env: ringEnv("2026-10", oldKey, newKey)})
	c := e.cluster(a, b)
	admin := e.admin.header()

	st := send(t, c, "GET", "/api/v1/system/encryption", nil, admin...)
	m := st.JSON(t)
	if st.Status != 200 || m["ringsCompatible"] != false {
		t.Fatalf("%d %s", st.Status, st.Body)
	}
	reps := m["replicas"].([]any)
	if len(reps) != 2 || reps[0].(map[string]any)["name"] != "web-a" || reps[0].(map[string]any)["activeKeyId"] != "2026-04" ||
		reps[1].(map[string]any)["activeKeyId"] != "2026-10" {
		t.Fatalf("replicas = %v", reps)
	}
	if strings := string(st.Body); strings == "" || contains(strings, "AQEBAQEB") {
		t.Fatalf("the answer carries key material: %s", strings)
	}
	// answeredBy names the replica whose ring the top-level values describe.
	seenActive := map[string]string{}
	for range 4 {
		r := send(t, c, "GET", "/api/v1/system/encryption", nil, admin...).JSON(t)
		seenActive[r["answeredBy"].(string)] = r["activeKeyId"].(string)
	}
	if seenActive["web-a"] != "2026-04" || seenActive["web-b"] != "2026-10" {
		t.Fatalf("answeredBy/activeKeyId = %v", seenActive)
	}
	if denied := send(t, c, "GET", "/api/v1/system/encryption", nil, e.alice.header()...); denied.Status != 403 {
		t.Fatalf("%d", denied.Status)
	}

	run := send(t, c, "POST", "/api/v1/system/tasks/secrets-reencrypt/run", nil, admin...)
	if run.Status != 409 || run.Code(t) != "key_ring_incompatible" {
		t.Fatalf("%d %s", run.Status, run.Body)
	}
	if n := e.scalar("SELECT count(*) FROM trove.job WHERE kind = 'secrets-reencrypt'"); n != 0 {
		t.Fatalf("%d jobs enqueued", n)
	}

	// Replica A gets the new ring: the rings are compatible and the run starts.
	if err := a.Stop(); err != nil {
		t.Fatalf("stop web-a: %v", err)
	}
	e.exec("DELETE FROM trove.replica WHERE name = 'web-a'")
	a2 := e.newNode("web-a2", nodeOpts{env: ringEnv("2026-04", oldKey, newKey)})
	c = e.cluster(a2, b)
	waitFor(t, "compatible rings", 15*time.Second, func() bool {
		return send(t, c, "GET", "/api/v1/system/encryption", nil, admin...).JSON(t)["ringsCompatible"] == true
	})
	if ok := send(t, c, "POST", "/api/v1/system/tasks/secrets-reencrypt/run", nil, admin...); ok.Status != 202 {
		t.Fatalf("%d %s", ok.Status, ok.Body)
	}
	_ = val
}

// TestKeyRingCountsAndCanaries covers the per-key counts over a registered
// sealed column and the canary states.
func TestKeyRingCountsAndCanaries(t *testing.T) {
	e := newEnv(t, true)
	col := vaultColumn
	f1, f2 := e.stack.KeyFile("2026-04.key", make([]byte, 32)), e.stack.KeyFile("2026-10.key", append(make([]byte, 31), 1))
	secretsConfig := func(active string, files ...string) config.Secrets {
		return config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active}
	}
	both, err := secrets.Load(secretsConfig("2026-10", f1, f2))
	if err != nil {
		t.Fatal(err)
	}
	only10, err := secrets.Load(secretsConfig("2026-10", f2))
	if err != nil {
		t.Fatal(err)
	}
	// Three values under 2026-10. A replica whose ring lacks a key that a stored
	// value references does not start (R-ARCH-021), so the replica with the
	// narrower ring starts first and the value under 2026-04 comes after it.
	for i, ring := range []*secrets.KeyRing{both, both, only10} {
		sealed, _ := ring.Seal(col, secrets.Row(randHex(3)), []byte("pw"))
		e.exec("INSERT INTO trove.vault_items VALUES ($1, $2)", randHex(4)+itoa(i), sealed)
	}
	n2 := e.newNode("web-2", nodeOpts{env: ringEnv("2026-10", f2)})
	oldRing, _ := secrets.Load(secretsConfig("2026-04", f1))
	old, _ := oldRing.Seal(col, secrets.Row("old"), []byte("pw"))
	e.exec("INSERT INTO trove.vault_items VALUES ('old', $1)", old)
	n := e.newNode("web-1", nodeOpts{env: ringEnv("2026-10", f1, f2)})
	// The start of web-1 seeded the canary of 2026-04; the state under test is the
	// canary of 2026-10 only.
	e.exec("DELETE FROM trove.secret_key_canary WHERE key_id = '2026-04'")
	c10, _ := both.SealCanary("2026-10")

	c := e.cluster(n)
	m := send(t, c, "GET", "/api/v1/system/encryption", nil, e.admin.header()...).JSON(t)
	keys := map[string]map[string]any{}
	for _, k := range m["keys"].([]any) {
		keys[k.(map[string]any)["id"].(string)] = k.(map[string]any)
	}
	if keys["2026-10"]["sealedValues"] != float64(3) || keys["2026-04"]["sealedValues"] != float64(1) {
		t.Fatalf("sealedValues = %v", keys)
	}
	if keys["2026-10"]["canary"] != "ok" || keys["2026-04"]["canary"] != "absent" || keys["2026-10"]["active"] != true {
		t.Fatalf("canary = %v", keys)
	}
	// A canary row that this replica's ring cannot open is failed.
	e.exec("INSERT INTO trove.secret_key_canary (key_id, ciphertext) VALUES ('2026-04', $1)", c10)
	m = send(t, c, "GET", "/api/v1/system/encryption", nil, e.admin.header()...).JSON(t)
	for _, k := range m["keys"].([]any) {
		if k.(map[string]any)["id"] == "2026-04" && k.(map[string]any)["canary"] != "failed" {
			t.Fatalf("canary of 2026-04 = %v", k)
		}
	}
	// A ring that lacks a key that sealed values reference: the key is listed, not loaded.
	m = send(t, e.cluster(n2), "GET", "/api/v1/system/encryption", nil, e.admin.header()...).JSON(t)
	if m["ringsCompatible"] != false {
		t.Fatalf("a ring without a referenced key must not be compatible: %v", m)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestSecretsReencryptRefusesAColumnWithoutAWalk runs the task on a worker. The
// process of this package registers a sealed column of its own (vault_items), which
// has no walk: the job fails permanently and names it, where it would otherwise
// reseal the repository secrets (repository.ResealSecrets, R-ARCH-021 rotation
// step 3, R-OPS-019).
func TestSecretsReencryptRefusesAColumnWithoutAWalk(t *testing.T) {
	e := newEnv(t, true)
	web := e.newNode("web-1", nodeOpts{})
	e.newNode("worker-1", nodeOpts{role: "worker"})
	c := e.cluster(web)
	admin := e.admin.header()

	run := send(t, c, "POST", "/api/v1/system/tasks/secrets-reencrypt/run", nil, admin...)
	if run.Status != 202 {
		t.Fatalf("%d %s", run.Status, run.Body)
	}
	done := waitJob(t, c, run.JSON(t)["job"].(map[string]any)["id"].(string), admin, "succeeded", "failed")
	detail, _ := done["error"].(map[string]any)["detail"].(string)
	if done["state"] != "failed" || !strings.Contains(detail, "vault_items.secret") || done["attempt"] != float64(1) {
		t.Fatalf("job = %v", done)
	}
}
