//go:build integration

package api

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/secrets"
)

// waitJob polls the job through the cluster until it is in one of the states.
func waitJob(t *testing.T, c *webCluster, id string, hdr []string, states ...string) map[string]any {
	t.Helper()
	var last map[string]any
	waitFor(t, "job "+id+" to reach "+strings.Join(states, "/"), 30*time.Second, func() bool {
		a := send(t, c, "GET", "/api/v1/system/jobs/"+id, nil, hdr...)
		if a.status != 200 {
			t.Fatalf("GET job: %d %s", a.status, a.body)
		}
		last = a.json(t)
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
	e := newEnv(t)
	val := apitest.New(t, v1.EmbeddedYAML())
	webs := []*node{e.newNode("web-1", nodeOpts{limits: relaxed(), val: val}), e.newNode("web-2", nodeOpts{limits: relaxed(), val: val}), e.newNode("web-3", nodeOpts{limits: relaxed(), val: val})}
	c := newWebCluster(webs...)
	adminID := uuidV4()
	admin := asAdmin(adminID)

	// 1. No worker is live: the trigger enqueues, the job stays queued.
	a := send(t, c, "POST", "/api/v1/system/tasks/gc/run", map[string]any{"dryRun": true}, admin...)
	if a.status != 202 {
		t.Fatalf("trigger: %d %s", a.status, a.body)
	}
	job := a.json(t)["job"].(map[string]any)
	id := job["id"].(string)
	if a.header.Get("Location") != "http://trove.test/api/v1/system/jobs/"+id || job["state"] != "queued" || job["kind"] != "gc" || job["href"] != "/api/v1/system/jobs/"+id {
		t.Fatalf("Location %q job %v", a.header.Get("Location"), job)
	}
	time.Sleep(1500 * time.Millisecond)
	queued := waitJob(t, c, id, admin, "queued")
	if queued["replica"] != nil || queued["triggeredBy"] != adminID {
		t.Fatalf("%v", queued)
	}

	// 2. R-OPS-020: either task while a run is queued answers 409 with that job (the shared key).
	for _, path := range []string{"/api/v1/system/tasks/gc/run", "/api/v1/system/tasks/gc-full/run"} {
		again := send(t, c, "POST", path, nil, admin...)
		if again.status != 409 || again.code(t) != "conflict" {
			t.Fatalf("%s: %d %s", path, again.status, again.body)
		}
		if got := again.json(t)["job"].(map[string]any)["id"]; got != id {
			t.Fatalf("%s: the 409 names job %v, want %s", path, got, id)
		}
	}

	// 3. A worker starts and executes it: replica is the worker, never a web pod.
	e.newNode("worker-1", nodeOpts{role: "worker"})
	done := waitJob(t, c, id, admin, "succeeded")
	if done["replica"] != "worker-1" {
		t.Fatalf("the run was executed by %v, want worker-1", done["replica"])
	}
	result := done["result"].(map[string]any)
	if result["ranOn"] != "worker-1" || result["dryRun"] != true {
		t.Fatalf("result = %v", result)
	}
	for _, w := range webs {
		if n := e.scalar("SELECT count(*) FROM trove.job WHERE ran_by = $1::uuid", w.uid); n != 0 {
			t.Fatalf("%s ran %d jobs; web replicas only enqueue", w.name, n)
		}
	}

	// 4. The finished run is listed by kind, and a new one may start.
	// (The scheduler of the worker may have added the latest due slot of the task, a run
	// without triggeredBy: the list holds ours among the runs of the kind.)
	list := send(t, c, "GET", "/api/v1/system/jobs?kind=gc", nil, admin...)
	found := false
	for _, it := range list.json(t)["items"].([]any) {
		m := it.(map[string]any)
		if m["kind"] != "gc" {
			t.Fatalf("kind=gc lists %v", m["kind"])
		}
		found = found || m["id"] == id
	}
	if list.status != 200 || !found {
		t.Fatalf("%d %s", list.status, list.body)
	}
	waitFor(t, "no gc run to be queued or running", 20*time.Second, func() bool {
		return e.scalar("SELECT count(*) FROM trove.job WHERE dedup_key = 'gc' AND state IN ('queued', 'running', 'cancelling')") == 0
	})
	next := send(t, c, "POST", "/api/v1/system/tasks/gc-full/run", map[string]any{}, admin...)
	if next.status != 202 {
		t.Fatalf("a run after the first ended: %d %s", next.status, next.body)
	}

	// 5. Arguments are checked against the task's type.
	waitFor(t, "the full run to end", 20*time.Second, func() bool {
		return e.scalar("SELECT count(*) FROM trove.job WHERE kind = 'gc-full' AND state = 'succeeded' AND created_by IS NOT NULL") == 1
	})
	for _, c2 := range []struct {
		body any
		want int
	}{{map[string]any{"dryrun": true}, 400}, {map[string]any{"dryRun": "yes"}, 400}, {`[1]`, 400}} {
		if bad := send(t, c, "POST", "/api/v1/system/tasks/gc/run", c2.body, admin...); bad.status != c2.want || bad.code(t) != "malformed_request" {
			t.Fatalf("%v: %d %s", c2.body, bad.status, bad.body)
		}
	}
	if unknown := send(t, c, "POST", "/api/v1/system/tasks/nope/run", nil, admin...); unknown.status != 404 {
		t.Fatalf("unknown task: %d", unknown.status)
	}
	if denied := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil, asUser(uuidV4())...); denied.status != 403 {
		t.Fatalf("a caller without settings:write: %d", denied.status)
	}
	if anon := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil); anon.status != 401 {
		t.Fatalf("anonymous: %d", anon.status)
	}
	if missing := val.Uncovered("listJobs", "getJob", "runTask"); len(missing) > 0 {
		t.Fatalf("operations not reached: %v", missing)
	}
}

// TestJobAccessAndPaging covers who sees and cancels which job (R-API-015) and
// the cursor walk over the jobs across replicas (R-API-010, R-API-051).
func TestJobAccessAndPaging(t *testing.T) {
	e := newEnv(t)
	val := apitest.New(t, v1.EmbeddedYAML())
	webs := []*node{e.newNode("web-1", nodeOpts{limits: relaxed(), val: val}), e.newNode("web-2", nodeOpts{limits: relaxed(), val: val}), e.newNode("web-3", nodeOpts{limits: relaxed(), val: val})}
	c := newWebCluster(webs...)
	alice, bob, adminID := uuidV4(), uuidV4(), uuidV4()
	admin, reader := asAdmin(adminID), []string{"X-Test-Principal", uuidV4(), "X-Test-Caps", "settings:read"}

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
		page := send(t, c, "GET", "/api/v1/system/jobs", nil, asUser(alice)...)
		items := page.json(t)["items"].([]any)
		if page.status != 200 || len(items) != 3 {
			t.Fatalf("%d %s", page.status, page.body)
		}
		for _, it := range items {
			if it.(map[string]any)["triggeredBy"] != alice {
				t.Fatalf("alice sees %v", it)
			}
		}
		if own := send(t, c, "GET", "/api/v1/system/jobs/"+aliceJobs[0], nil, asUser(alice)...); own.status != 200 || own.header.Get("Retry-After") != "2" {
			t.Fatalf("own job: %d retry-after %q", own.status, own.header.Get("Retry-After"))
		}
		bobsJob := all[1]
		if other := send(t, c, "GET", "/api/v1/system/jobs/"+bobsJob, nil, asUser(alice)...); other.status != 404 {
			t.Fatalf("another caller's job: %d", other.status)
		}
		if missing := send(t, c, "GET", "/api/v1/system/jobs/"+uuidV4(), nil, asUser(alice)...); missing.status != 404 {
			t.Fatalf("a missing job: %d", missing.status)
		}
		if anon := send(t, c, "GET", "/api/v1/system/jobs", nil); anon.status != 401 {
			t.Fatalf("anonymous: %d", anon.status)
		}
	})

	t.Run("R-OPS-019 a caller with settings:read sees every job and filters by kind and state", func(t *testing.T) {
		page := send(t, c, "GET", "/api/v1/system/jobs?limit=500", nil, reader...)
		if n := len(page.json(t)["items"].([]any)); n != len(all) {
			t.Fatalf("%d jobs, want %d", n, len(all))
		}
		sweep := send(t, c, "GET", "/api/v1/system/jobs?kind=sweep&state=queued", nil, reader...)
		if items := sweep.json(t)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["triggeredBy"] != nil {
			t.Fatalf("%s", sweep.body)
		}
		if bad := send(t, c, "GET", "/api/v1/system/jobs?state=weird", nil, reader...); bad.status != 400 || bad.code(t) != "malformed_request" {
			t.Fatalf("%d %s", bad.status, bad.body)
		}
		if bad := send(t, c, "GET", "/api/v1/system/jobs?fromat=x", nil, reader...); bad.status != 400 {
			t.Fatalf("%d", bad.status)
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
				if a.status != 200 {
					t.Fatalf("%d %s", a.status, a.body)
				}
				replicas[a.header.Get("X-Trove-Replica")] = true
				m := a.json(t)
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
		cur := a.json(t)["nextCursor"].(string)
		if bad := send(t, c, "GET", "/api/v1/system/jobs?limit=1&sort=enqueuedAt&cursor="+cur, nil, reader...); bad.status != 400 || bad.code(t) != "invalid_cursor" {
			t.Fatalf("a cursor for another sort: %d %s", bad.status, bad.body)
		}
		if bad := send(t, c, "GET", "/api/v1/system/jobs?limit=1&kind=probe&cursor="+cur, nil, reader...); bad.status != 400 || bad.code(t) != "invalid_cursor" {
			t.Fatalf("a cursor for other filters: %d %s", bad.status, bad.body)
		}
	})

	t.Run("R-API-015 an administrator or the caller who started the job may cancel it", func(t *testing.T) {
		if other := send(t, c, "POST", "/api/v1/system/jobs/"+all[1]+"/cancel", nil, asUser(alice)...); other.status != 404 {
			t.Fatalf("alice cancels bob's job: %d", other.status)
		}
		if seeOnly := send(t, c, "POST", "/api/v1/system/jobs/"+aliceJobs[1]+"/cancel", nil, reader...); seeOnly.status != 403 {
			t.Fatalf("a reader who did not start the job: %d", seeOnly.status)
		}
		own := send(t, c, "POST", "/api/v1/system/jobs/"+aliceJobs[0]+"/cancel", nil, asUser(alice)...)
		if own.status != 202 || own.json(t)["state"] != "cancelled" || own.header.Get("Location") != "http://trove.test/api/v1/system/jobs/"+aliceJobs[0] {
			t.Fatalf("%d %s", own.status, own.body)
		}
		if again := send(t, c, "POST", "/api/v1/system/jobs/"+aliceJobs[0]+"/cancel", nil, asUser(alice)...); again.status != 409 || again.code(t) != "conflict" {
			t.Fatalf("a finished job: %d %s", again.status, again.body)
		}
		if adminCancel := send(t, c, "POST", "/api/v1/system/jobs/"+all[1]+"/cancel", nil, admin...); adminCancel.status != 202 {
			t.Fatalf("admin: %d %s", adminCancel.status, adminCancel.body)
		}
		// A running job turns cancelling.
		running := uuidV4()
		e.exec("INSERT INTO trove.job (id, kind, queue, state, created_by, locked_by, locked_until, attempt) VALUES ($1, 'probe', 'maintenance', 'running', $2, $3, now() + interval '1 hour', 1)", running, alice, uuidV4())
		if r := send(t, c, "POST", "/api/v1/system/jobs/"+running+"/cancel", nil, asUser(alice)...); r.status != 202 || r.json(t)["state"] != "cancelling" {
			t.Fatalf("%d %s", r.status, r.body)
		}
		if r := send(t, c, "POST", "/api/v1/system/jobs/"+running+"/cancel", nil, asUser(alice)...); r.status != 202 || r.json(t)["state"] != "cancelling" {
			t.Fatalf("cancelling twice: %d %s", r.status, r.body)
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
	e := newEnv(t)
	val := apitest.New(t, v1.EmbeddedYAML())
	kinds := func(r *jobs.Registry, _ *node) {
		r.RegisterKind(v1.KindSecretsReencrypt, jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, Handler: func(_ context.Context, run *jobs.Run) error { return run.SetResult(map[string]int{"resealed": 0}) }})
		r.RegisterTask(v1.KindSecretsReencrypt, jobs.TaskSpec{ManualOnly: true})
	}
	webs := []*node{e.newNode("web-1", nodeOpts{limits: relaxed(), val: val, kinds: kinds}), e.newNode("web-2", nodeOpts{limits: relaxed(), val: val, kinds: kinds})}
	c := newWebCluster(webs...)
	reader := []string{"X-Test-Principal", uuidV4(), "X-Test-Caps", "settings:read"}

	a := send(t, c, "GET", "/api/v1/system/tasks?limit=500", nil, reader...)
	if a.status != 200 {
		t.Fatalf("%d %s", a.status, a.body)
	}
	byName := map[string]map[string]any{}
	var names []string
	for _, it := range a.json(t)["items"].([]any) {
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
		e.newNode("worker-1", nodeOpts{role: "worker", kinds: kinds})
		admin := asAdmin(uuidV4())
		run := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil, admin...)
		id := run.json(t)["job"].(map[string]any)["id"].(string)
		waitJob(t, c, id, admin, "succeeded")
		gc := func() map[string]any {
			for _, it := range send(t, c, "GET", "/api/v1/system/tasks?limit=500", nil, reader...).json(t)["items"].([]any) {
				if it.(map[string]any)["name"] == "gc" {
					return it.(map[string]any)
				}
			}
			return nil
		}()
		last, _ := gc["lastRun"].(map[string]any)
		if last["jobId"] != id || last["outcome"] != "succeeded" || last["replica"] != "worker-1" || last["finishedAt"] == nil {
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
			m := p.json(t)
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
		if a := send(t, c, "GET", "/api/v1/system/tasks", nil, asUser(uuidV4())...); a.status != 403 {
			t.Fatalf("%d", a.status)
		}
	})
	if missing := val.Uncovered("listTasks"); len(missing) > 0 {
		t.Fatalf("operations not reached: %v", missing)
	}
}

// TestKeyRingStatus is the acceptance of R-API-049 with two replicas whose key
// rings differ: the status says so and the re-encryption refuses to start.
func TestKeyRingStatus(t *testing.T) {
	e := newEnv(t)
	val := apitest.New(t, v1.EmbeddedYAML())
	keyFile := func(id string, fill byte) string {
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = fill
		}
		return writeFile(t, id+".key", string(raw))
	}
	oldKey, newKey := keyFile("2026-04", 1), keyFile("2026-10", 2)
	ring := func(active string, files ...string) *secrets.KeyRing {
		r, err := secrets.Load(secretsConfig(active, files...))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	kinds := func(r *jobs.Registry, _ *node) {
		r.RegisterKind(v1.KindSecretsReencrypt, jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, Handler: func(_ context.Context, run *jobs.Run) error { return run.SetResult(map[string]int{"resealed": 0}) }})
		r.RegisterTask(v1.KindSecretsReencrypt, jobs.TaskSpec{ManualOnly: true})
	}
	// Replica A still runs the old ring; B was started with a new key active that A's ring lacks.
	a := e.newNode("web-a", nodeOpts{limits: relaxed(), val: val, kinds: kinds, ring: ring("2026-04", oldKey)})
	b := e.newNode("web-b", nodeOpts{limits: relaxed(), val: val, kinds: kinds, ring: ring("2026-10", oldKey, newKey)})
	c := newWebCluster(a, b)
	admin := asAdmin(uuidV4())

	st := send(t, c, "GET", "/api/v1/system/encryption", nil, admin...)
	m := st.json(t)
	if st.status != 200 || m["ringsCompatible"] != false {
		t.Fatalf("%d %s", st.status, st.body)
	}
	reps := m["replicas"].([]any)
	if len(reps) != 2 || reps[0].(map[string]any)["name"] != "web-a" || reps[0].(map[string]any)["activeKeyId"] != "2026-04" ||
		reps[1].(map[string]any)["activeKeyId"] != "2026-10" {
		t.Fatalf("replicas = %v", reps)
	}
	if strings := string(st.body); strings == "" || contains(strings, "AQEBAQEB") {
		t.Fatalf("the answer carries key material: %s", strings)
	}
	// answeredBy names the replica whose ring the top-level values describe.
	seenActive := map[string]string{}
	for range 4 {
		r := send(t, c, "GET", "/api/v1/system/encryption", nil, admin...).json(t)
		seenActive[r["answeredBy"].(string)] = r["activeKeyId"].(string)
	}
	if seenActive["web-a"] != "2026-04" || seenActive["web-b"] != "2026-10" {
		t.Fatalf("answeredBy/activeKeyId = %v", seenActive)
	}
	if denied := send(t, c, "GET", "/api/v1/system/encryption", nil, asUser(uuidV4())...); denied.status != 403 {
		t.Fatalf("%d", denied.status)
	}

	run := send(t, c, "POST", "/api/v1/system/tasks/secrets-reencrypt/run", nil, admin...)
	if run.status != 409 || run.code(t) != "key_ring_incompatible" {
		t.Fatalf("%d %s", run.status, run.body)
	}
	if n := e.scalar("SELECT count(*) FROM trove.job WHERE kind = 'secrets-reencrypt'"); n != 0 {
		t.Fatalf("%d jobs enqueued", n)
	}

	// Replica A gets the new ring: the rings are compatible and the run starts.
	a.registry.Drain(context.Background())
	_ = a.registry.Stop(context.Background())
	e.exec("DELETE FROM trove.replica WHERE name = 'web-a'")
	a2 := e.newNode("web-a2", nodeOpts{limits: relaxed(), val: val, kinds: kinds, ring: ring("2026-04", oldKey, newKey)})
	_ = a2
	waitFor(t, "compatible rings", 15*time.Second, func() bool {
		return send(t, c, "GET", "/api/v1/system/encryption", nil, admin...).json(t)["ringsCompatible"] == true
	})
	if ok := send(t, c, "POST", "/api/v1/system/tasks/secrets-reencrypt/run", nil, admin...); ok.status != 202 {
		t.Fatalf("%d %s", ok.status, ok.body)
	}
}

// TestKeyRingCountsAndCanaries covers the per-key counts over a registered
// sealed column and the canary states.
func TestKeyRingCountsAndCanaries(t *testing.T) {
	e := newEnv(t)
	col := vaultColumn
	f1, f2 := writeFile(t, "2026-04.key", string(make([]byte, 32))), writeFile(t, "2026-10.key", string(append(make([]byte, 31), 1)))
	both, err := secrets.Load(secretsConfig("2026-10", f1, f2))
	if err != nil {
		t.Fatal(err)
	}
	only10, err := secrets.Load(secretsConfig("2026-10", f2))
	if err != nil {
		t.Fatal(err)
	}
	for i, ring := range []*secrets.KeyRing{both, both, only10} {
		sealed, _ := ring.Seal(col, secrets.Row(randHex(3)), []byte("pw"))
		e.exec("INSERT INTO trove.vault_items VALUES ($1, $2)", randHex(4)+itoa(i), sealed)
	}
	// A value under 2026-04.
	oldRing, _ := secrets.Load(secretsConfig("2026-04", f1))
	old, _ := oldRing.Seal(col, secrets.Row("old"), []byte("pw"))
	e.exec("INSERT INTO trove.vault_items VALUES ('old', $1)", old)
	// The canary of 2026-10 only.
	c10, _ := both.SealCanary("2026-10")
	e.exec("INSERT INTO trove.secret_key_canary (key_id, ciphertext) VALUES ('2026-10', $1)", c10)

	val := apitest.New(t, v1.EmbeddedYAML())
	n := e.newNode("web-1", nodeOpts{limits: relaxed(), val: val, ring: both})
	c := newWebCluster(n)
	m := send(t, c, "GET", "/api/v1/system/encryption", nil, asAdmin(uuidV4())...).json(t)
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
	m = send(t, c, "GET", "/api/v1/system/encryption", nil, asAdmin(uuidV4())...).json(t)
	for _, k := range m["keys"].([]any) {
		if k.(map[string]any)["id"] == "2026-04" && k.(map[string]any)["canary"] != "failed" {
			t.Fatalf("canary of 2026-04 = %v", k)
		}
	}
	// A ring that lacks a key that sealed values reference: the key is listed, not loaded.
	n2 := e.newNode("web-2", nodeOpts{limits: relaxed(), val: val, ring: only10})
	m = send(t, newWebCluster(n2), "GET", "/api/v1/system/encryption", nil, asAdmin(uuidV4())...).json(t)
	if m["ringsCompatible"] != false {
		t.Fatalf("a ring without a referenced key must not be compatible: %v", m)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
