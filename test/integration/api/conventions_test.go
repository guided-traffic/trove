//go:build integration

package api

import (
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
)

func newWidgetTable(e *env) { e.exec(widgetSchema) }

// TestConventionsOnTheTestResource runs the contract tests of E1.6 — cursor
// pagination, ETag with If-Match, merge patch and idempotency keys — on the
// widget resource, with every request answered by another of three replicas.
// Pointing the suite at the repository resource is a matter of filling
// apitest.Resource.
func TestConventionsOnTheTestResource(t *testing.T) {
	e := newEnv(t, true)
	newWidgetTable(e)
	nodes := []*node{e.newNode("web-1", nodeOpts{widgets: true}), e.newNode("web-2", nodeOpts{widgets: true}), e.newNode("web-3", nodeOpts{widgets: true})}
	c := e.cluster(nodes...)
	c.token = e.alice.token
	apitest.RunContract(t, apitest.Resource{
		Name: "widgets", Collection: "http://cluster/test/widgets", Do: c.do,
		Create: func(n int) map[string]any {
			return map[string]any{"name": "w-" + randHex(4) + "-" + itoa(n), "color": "red", "labels": map[string]string{"team": "a"}}
		},
		Key:   func(m map[string]any) string { return m["name"].(string) },
		Patch: map[string]any{"color": "blue"}, PatchMember: "color", PatchValue: "blue",
		ClearMember: "color", ClearedValue: nil,
		ImmutableMember: "name", ImmutableValue: "other-name",
		OtherQuery: "sort=-name", Put: true,
	})
}

// TestIdempotentCreate checks the cross-replica and concurrency rules of
// R-API-014 and R-API-051 on the widget resource.
func TestIdempotentCreate(t *testing.T) {
	e := newEnv(t, true)
	newWidgetTable(e)
	nodes := []*node{e.newNode("web-1", nodeOpts{widgets: true}), e.newNode("web-2", nodeOpts{widgets: true}), e.newNode("web-3", nodeOpts{widgets: true})}
	caller := e.alice.header()

	t.Run("R-API-051 a key replayed through another replica returns the stored outcome", func(t *testing.T) {
		first := send(t, e.cluster(nodes[0]), "POST", "/test/widgets", map[string]any{"name": "replay-a"}, append(caller, "Idempotency-Key", "k-replay")...)
		second := send(t, e.cluster(nodes[1]), "POST", "/test/widgets", map[string]any{"name": "replay-a"}, append(caller, "Idempotency-Key", "k-replay")...)
		if first.Status != 201 || second.Status != 201 || second.Header.Get("Idempotency-Replayed") != "true" ||
			string(first.Body) != string(second.Body) || first.Header.Get("X-Trove-Replica") == second.Header.Get("X-Trove-Replica") {
			t.Fatalf("%d/%d replayed=%q replicas %q/%q\n%s\n%s", first.Status, second.Status, second.Header.Get("Idempotency-Replayed"),
				first.Header.Get("X-Trove-Replica"), second.Header.Get("X-Trove-Replica"), first.Body, second.Body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'replay-a'"); n != 1 {
			t.Fatalf("%d widgets, want 1", n)
		}
	})

	t.Run("R-API-014 the scope is the principal and the key", func(t *testing.T) {
		other := e.bob.header()
		a := send(t, e.cluster(nodes[0]), "POST", "/test/widgets", map[string]any{"name": "scope-a"}, append(caller, "Idempotency-Key", "k-scope")...)
		b := send(t, e.cluster(nodes[1]), "POST", "/test/widgets", map[string]any{"name": "scope-b"}, append(other, "Idempotency-Key", "k-scope")...)
		if a.Status != 201 || b.Status != 201 || b.Header.Get("Idempotency-Replayed") != "" {
			t.Fatalf("%d %d replayed=%q", a.Status, b.Status, b.Header.Get("Idempotency-Replayed"))
		}
	})

	t.Run("R-API-014 a stored 4xx outcome is replayed, a different method or route or body is 422", func(t *testing.T) {
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-409")
		send(t, e.cluster(nodes[0]), "POST", "/test/widgets", map[string]any{"name": "exists"}, caller...)
		first := send(t, e.cluster(nodes[0]), "POST", "/test/widgets", map[string]any{"name": "exists"}, h...)
		send(t, e.cluster(nodes[0]), "DELETE", "/test/widgets/exists", nil, caller...)
		again := send(t, e.cluster(nodes[2]), "POST", "/test/widgets", map[string]any{"name": "exists"}, h...)
		if first.Status != 409 || again.Status != 409 || again.Header.Get("Idempotency-Replayed") != "true" || again.Code(t) != "already_exists" {
			t.Fatalf("%d %d replayed=%q\n%s", first.Status, again.Status, again.Header.Get("Idempotency-Replayed"), again.Body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'exists'"); n != 0 {
			t.Fatal("the replay of a refusal created the widget")
		}
		if reused := send(t, e.cluster(nodes[1]), "POST", "/test/widgets", map[string]any{"name": "different"}, h...); reused.Status != 422 || reused.Code(t) != "idempotency_key_reused" {
			t.Fatalf("%d %s", reused.Status, reused.Body)
		}
	})

	t.Run("R-API-014 a failed first request leaves nothing behind: the retry runs again", func(t *testing.T) {
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-rollback")
		nodes[0].widgets.failNext = true
		if a := send(t, e.cluster(nodes[0]), "POST", "/test/widgets", map[string]any{"name": "rolled-back"}, h...); a.Status != 500 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'rolled-back'"); n != 0 {
			t.Fatal("a failed create left the widget")
		}
		if n := e.scalar("SELECT count(*) FROM trove.api_idempotency_keys WHERE idem_key = 'k-rollback'"); n != 0 {
			t.Fatal("a 5xx stored its outcome")
		}
		if b := send(t, e.cluster(nodes[1]), "POST", "/test/widgets", map[string]any{"name": "rolled-back"}, h...); b.Status != 201 || b.Header.Get("Idempotency-Replayed") != "" {
			t.Fatalf("%d %s", b.Status, b.Body)
		}
	})

	t.Run("R-API-014 concurrent duplicates answer 409 idempotency_in_progress within 1.5 s, and no connection waits on a lock longer", func(t *testing.T) {
		nodes[0].widgets.hold, nodes[0].widgets.entered = make(chan struct{}), make(chan struct{}, 1)
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-hold")
		body := map[string]any{"name": "hold-1"}
		firstDone := make(chan answer, 1)
		go func() { firstDone <- send(t, e.cluster(nodes[0]), "POST", "/test/widgets", body, h...) }()
		select {
		case <-nodes[0].widgets.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the first request did not start")
		}

		const duplicates = 50
		var wg sync.WaitGroup
		type result struct {
			a answer
			d time.Duration
		}
		results := make([]result, duplicates)
		cl := e.cluster(nodes...)
		for i := range duplicates {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				a := send(t, &webCluster{nodes: []*node{cl.nodes[i%3]}}, "POST", "/test/widgets", body, h...)
				results[i] = result{a, time.Since(start)}
			}()
		}
		// Sample the lock waits while the duplicates wait.
		var longest int64
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for end := time.Now().Add(1800 * time.Millisecond); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
				n, _ := e.stack.TryNumber(`SELECT coalesce(max(extract(epoch FROM now() - state_change) * 1000), 0)::bigint
					FROM pg_stat_activity WHERE datname = $1 AND wait_event_type = 'Lock' AND state = 'active'`, e.stack.Database())
				longest = max(longest, n)
			}
		}()
		wg.Wait()
		<-sampled
		for i, r := range results {
			if r.a.Status != 409 || r.a.Code(t) != "idempotency_in_progress" || r.a.Header.Get("Retry-After") != "1" {
				t.Fatalf("duplicate %d: %d %s retry-after %q", i, r.a.Status, r.a.Body, r.a.Header.Get("Retry-After"))
			}
			if r.d > 1500*time.Millisecond {
				t.Errorf("duplicate %d took %s, want at most 1.5 s", i, r.d)
			}
		}
		if longest > 1500 {
			t.Errorf("a connection waited on a lock for %d ms", longest)
		}
		close(nodes[0].widgets.hold)
		first := <-firstDone
		if first.Status != 201 {
			t.Fatalf("the first request: %d %s", first.Status, first.Body)
		}
		if retry := send(t, e.cluster(nodes[2]), "POST", "/test/widgets", body, h...); retry.Status != 201 || retry.Header.Get("Idempotency-Replayed") != "true" || string(retry.Body) != string(first.Body) {
			t.Fatalf("retry after the commit: %d replayed=%q", retry.Status, retry.Header.Get("Idempotency-Replayed"))
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'hold-1'"); n != 1 {
			t.Fatalf("%d widgets, want 1", n)
		}
	})

	t.Run("R-API-014 a concurrent duplicate of a refused request does not run the operation again", func(t *testing.T) {
		send(t, e.cluster(nodes[0]), "POST", "/test/widgets", map[string]any{"name": "hold-taken"}, caller...)
		nodes[0].widgets.hold, nodes[0].widgets.entered = make(chan struct{}), make(chan struct{}, 1)
		before := nodes[0].widgets.runs.Load() + nodes[1].widgets.runs.Load()
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-refused-dup")
		body := map[string]any{"name": "hold-taken"}
		firstDone := make(chan answer, 1)
		go func() { firstDone <- send(t, e.cluster(nodes[0]), "POST", "/test/widgets", body, h...) }()
		select {
		case <-nodes[0].widgets.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the first request did not start")
		}
		second := make(chan answer, 1)
		go func() { second <- send(t, e.cluster(nodes[1]), "POST", "/test/widgets", body, h...) }()
		time.Sleep(300 * time.Millisecond) // the duplicate waits on the claimed row
		close(nodes[0].widgets.hold)
		first := <-firstDone
		dup := <-second
		if first.Status != 409 || first.Code(t) != "already_exists" {
			t.Fatalf("the first request: %d %s", first.Status, first.Body)
		}
		// The duplicate either timed out on the lock (409 in progress) or found the
		// stored refusal; it never ran the operation.
		if dup.Status != 409 || (dup.Code(t) != "already_exists" && dup.Code(t) != "idempotency_in_progress") {
			t.Fatalf("the duplicate: %d %s", dup.Status, dup.Body)
		}
		if ran := nodes[0].widgets.runs.Load() + nodes[1].widgets.runs.Load() - before; ran != 1 {
			t.Fatalf("the operation ran %d times, want 1", ran)
		}
		if again := send(t, e.cluster(nodes[2]), "POST", "/test/widgets", body, h...); again.Status != 409 || again.Header.Get("Idempotency-Replayed") != "true" {
			t.Fatalf("the replay: %d replayed=%q", again.Status, again.Header.Get("Idempotency-Replayed"))
		}
	})

	t.Run("R-API-014 the cleanup task deletes expired keys", func(t *testing.T) {
		e.exec("INSERT INTO trove.api_idempotency_keys (principal_id, idem_key, method, route, request_sha256, response_status, expires_at) VALUES ($1, 'old', 'POST', 'x', decode(repeat('00', 32), 'hex'), 201, now() - interval '1 hour')", uuidV4())
		e.newNode("worker-1", nodeOpts{role: "worker"})
		a := send(t, e.cluster(nodes[0]), "POST", "/api/v1/system/tasks/idempotency-cleanup/run", nil, e.admin.header()...)
		// The real scheduler of the worker may have queued the task's first run
		// already; the trigger then names that run (409, R-OPS-020), which deletes the
		// key all the same.
		queued := a.Status == 409 && a.JSON(t)["job"].(map[string]any)["kind"] == "idempotency-cleanup"
		if a.Status != 202 && !queued {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		waitFor(t, "the cleanup", 20*time.Second, func() bool {
			return e.scalar("SELECT count(*) FROM trove.api_idempotency_keys WHERE idem_key = 'old'") == 0
		})
	})
}
