//go:build integration

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/config"
)

// relaxed turns the rate limits off for tests that send many requests.
func relaxed() *config.APIRateLimits {
	return &config.APIRateLimits{Enabled: false}
}

// answer is one HTTP answer.
type answer struct {
	status int
	header http.Header
	body   []byte
}

func (a answer) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", a.status, err, a.body)
	}
	return m
}

func (a answer) code(t testing.TB) string {
	t.Helper()
	c, _ := a.json(t)["code"].(string)
	return c
}

// send sends one request through the cluster's next replica.
func send(t testing.TB, c *webCluster, method, path string, body any, hdr ...string) answer {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, "http://cluster"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return answer{status: res.StatusCode, header: res.Header, body: b}
}

// asAdmin and asUser are the caller headers of the test authenticator.
func asAdmin(id string) []string {
	return []string{"X-Test-Principal", id, "X-Test-Caps", "settings:read,settings:write"}
}

func asUser(id string) []string { return []string{"X-Test-Principal", id} }

func newWidgetTable(e *env) { e.exec(widgetSchema) }

// TestConventionsOnTheTestResource runs the contract tests of E1.6 — cursor
// pagination, ETag with If-Match, merge patch and idempotency keys — on the
// widget resource, with every request answered by another of three replicas.
// Pointing the suite at the repository resource is a matter of filling
// apitest.Resource.
func TestConventionsOnTheTestResource(t *testing.T) {
	e := newEnv(t)
	newWidgetTable(e)
	nodes := []*node{e.newNode("web-1", nodeOpts{limits: relaxed()}), e.newNode("web-2", nodeOpts{limits: relaxed()}), e.newNode("web-3", nodeOpts{limits: relaxed()})}
	c := newWebCluster(nodes...)
	for k, v := range map[string]string{"X-Test-Principal": uuidV4()} {
		c.hdr[k] = v
	}
	apitest.RunContract(t, apitest.Resource{
		Name: "widgets", Collection: "http://cluster/api/v1/widgets", Do: c.do,
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

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// TestIdempotentCreate checks the cross-replica and concurrency rules of
// R-API-014 and R-API-051 on the widget resource.
func TestIdempotentCreate(t *testing.T) {
	e := newEnv(t)
	newWidgetTable(e)
	e.cfg.Pool.MaxConns = 24
	nodes := []*node{e.newNode("web-1", nodeOpts{limits: relaxed()}), e.newNode("web-2", nodeOpts{limits: relaxed()}), e.newNode("web-3", nodeOpts{limits: relaxed()})}
	caller := asUser(uuidV4())

	t.Run("R-API-051 a key replayed through another replica returns the stored outcome", func(t *testing.T) {
		first := send(t, newWebCluster(nodes[0]), "POST", "/api/v1/widgets", map[string]any{"name": "replay-a"}, append(caller, "Idempotency-Key", "k-replay")...)
		second := send(t, newWebCluster(nodes[1]), "POST", "/api/v1/widgets", map[string]any{"name": "replay-a"}, append(caller, "Idempotency-Key", "k-replay")...)
		if first.status != 201 || second.status != 201 || second.header.Get("Idempotency-Replayed") != "true" ||
			string(first.body) != string(second.body) || first.header.Get("X-Trove-Replica") == second.header.Get("X-Trove-Replica") {
			t.Fatalf("%d/%d replayed=%q replicas %q/%q\n%s\n%s", first.status, second.status, second.header.Get("Idempotency-Replayed"),
				first.header.Get("X-Trove-Replica"), second.header.Get("X-Trove-Replica"), first.body, second.body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'replay-a'"); n != 1 {
			t.Fatalf("%d widgets, want 1", n)
		}
	})

	t.Run("R-API-014 the scope is the principal and the key", func(t *testing.T) {
		other := asUser(uuidV4())
		a := send(t, newWebCluster(nodes[0]), "POST", "/api/v1/widgets", map[string]any{"name": "scope-a"}, append(caller, "Idempotency-Key", "k-scope")...)
		b := send(t, newWebCluster(nodes[1]), "POST", "/api/v1/widgets", map[string]any{"name": "scope-b"}, append(other, "Idempotency-Key", "k-scope")...)
		if a.status != 201 || b.status != 201 || b.header.Get("Idempotency-Replayed") != "" {
			t.Fatalf("%d %d replayed=%q", a.status, b.status, b.header.Get("Idempotency-Replayed"))
		}
	})

	t.Run("R-API-014 a stored 4xx outcome is replayed, a different method or route or body is 422", func(t *testing.T) {
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-409")
		send(t, newWebCluster(nodes[0]), "POST", "/api/v1/widgets", map[string]any{"name": "exists"})
		first := send(t, newWebCluster(nodes[0]), "POST", "/api/v1/widgets", map[string]any{"name": "exists"}, h...)
		send(t, newWebCluster(nodes[0]), "DELETE", "/api/v1/widgets/exists", nil)
		again := send(t, newWebCluster(nodes[2]), "POST", "/api/v1/widgets", map[string]any{"name": "exists"}, h...)
		if first.status != 409 || again.status != 409 || again.header.Get("Idempotency-Replayed") != "true" || again.code(t) != "already_exists" {
			t.Fatalf("%d %d replayed=%q\n%s", first.status, again.status, again.header.Get("Idempotency-Replayed"), again.body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'exists'"); n != 0 {
			t.Fatal("the replay of a refusal created the widget")
		}
		if reused := send(t, newWebCluster(nodes[1]), "POST", "/api/v1/widgets", map[string]any{"name": "different"}, h...); reused.status != 422 || reused.code(t) != "idempotency_key_reused" {
			t.Fatalf("%d %s", reused.status, reused.body)
		}
	})

	t.Run("R-API-014 a failed first request leaves nothing behind: the retry runs again", func(t *testing.T) {
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-rollback")
		nodes[0].widgets.failNext = true
		if a := send(t, newWebCluster(nodes[0]), "POST", "/api/v1/widgets", map[string]any{"name": "rolled-back"}, h...); a.status != 500 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'rolled-back'"); n != 0 {
			t.Fatal("a failed create left the widget")
		}
		if n := e.scalar("SELECT count(*) FROM trove.api_idempotency_keys WHERE idem_key = 'k-rollback'"); n != 0 {
			t.Fatal("a 5xx stored its outcome")
		}
		if b := send(t, newWebCluster(nodes[1]), "POST", "/api/v1/widgets", map[string]any{"name": "rolled-back"}, h...); b.status != 201 || b.header.Get("Idempotency-Replayed") != "" {
			t.Fatalf("%d %s", b.status, b.body)
		}
	})

	t.Run("R-API-014 concurrent duplicates answer 409 idempotency_in_progress within 1.5 s, and no connection waits on a lock longer", func(t *testing.T) {
		nodes[0].widgets.hold, nodes[0].widgets.entered = make(chan struct{}), make(chan struct{}, 1)
		h := append(append([]string{}, caller...), "Idempotency-Key", "k-hold")
		body := map[string]any{"name": "hold-1"}
		firstDone := make(chan answer, 1)
		go func() { firstDone <- send(t, newWebCluster(nodes[0]), "POST", "/api/v1/widgets", body, h...) }()
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
		cl := newWebCluster(nodes...)
		for i := range duplicates {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				a := send(t, &webCluster{nodes: []*node{cl.nodes[i%3]}, hdr: map[string]string{}}, "POST", "/api/v1/widgets", body, h...)
				results[i] = result{a, time.Since(start)}
			}()
		}
		// Sample the lock waits while the duplicates wait.
		var longest int64
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for end := time.Now().Add(1800 * time.Millisecond); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
				var n int64
				_ = e.admin.QueryRow(context.Background(), `SELECT coalesce(max(extract(epoch FROM now() - state_change) * 1000), 0)::bigint
					FROM pg_stat_activity WHERE datname = $1 AND wait_event_type = 'Lock' AND state = 'active'`, e.db).Scan(&n)
				longest = max(longest, n)
			}
		}()
		wg.Wait()
		<-sampled
		for i, r := range results {
			if r.a.status != 409 || r.a.code(t) != "idempotency_in_progress" || r.a.header.Get("Retry-After") != "1" {
				t.Fatalf("duplicate %d: %d %s retry-after %q", i, r.a.status, r.a.body, r.a.header.Get("Retry-After"))
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
		if first.status != 201 {
			t.Fatalf("the first request: %d %s", first.status, first.body)
		}
		if retry := send(t, newWebCluster(nodes[2]), "POST", "/api/v1/widgets", body, h...); retry.status != 201 || retry.header.Get("Idempotency-Replayed") != "true" || string(retry.body) != string(first.body) {
			t.Fatalf("retry after the commit: %d replayed=%q", retry.status, retry.header.Get("Idempotency-Replayed"))
		}
		if n := e.scalar("SELECT count(*) FROM trove.widgets WHERE name = 'hold-1'"); n != 1 {
			t.Fatalf("%d widgets, want 1", n)
		}
	})

	t.Run("R-API-014 the cleanup task deletes expired keys", func(t *testing.T) {
		e.exec("INSERT INTO trove.api_idempotency_keys (principal_id, idem_key, method, route, request_sha256, response_status, expires_at) VALUES ($1, 'old', 'POST', 'x', decode(repeat('00', 32), 'hex'), 201, now() - interval '1 hour')", uuidV4())
		worker := e.newNode("worker-1", nodeOpts{role: "worker"})
		_ = worker
		admin := asAdmin(uuidV4())
		a := send(t, newWebCluster(nodes[0]), "POST", "/api/v1/system/tasks/idempotency-cleanup/run", nil, admin...)
		if a.status != 202 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		waitFor(t, "the cleanup", 20*time.Second, func() bool {
			return e.scalar("SELECT count(*) FROM trove.api_idempotency_keys WHERE idem_key = 'old'") == 0
		})
	})
}

func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}
