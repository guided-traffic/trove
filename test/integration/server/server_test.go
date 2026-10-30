//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func jsonLines(logs string) []map[string]any {
	var out []map[string]any
	for _, l := range strings.Split(logs, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestReplicas(t *testing.T) {
	in := newInstall(t)

	t.Run("R-ARCH-026 trove migrate up creates the schema on a fresh database", func(t *testing.T) {
		code, stdout, stderr := in.run(t, "migrate", "up")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
		}
	})

	all := in.start(t, "all", "trove-all-it")
	web := in.start(t, "web", "trove-web-it")
	worker := in.start(t, "worker", "trove-worker-it")

	t.Run("R-OPS-001 every role answers /readyz 200 and /healthz 200", func(t *testing.T) {
		for _, r := range []*replica{all, web, worker} {
			if code, body := opsGet(r.ops, "/readyz"); code != 200 || !strings.Contains(body, `"ready"`) {
				t.Errorf("%s readyz %d %s", r.name, code, body)
			}
			if code, _ := opsGet(r.ops, "/healthz"); code != 200 {
				t.Errorf("%s healthz %d", r.name, code)
			}
		}
		_, body := opsGet(web.ops, "/readyz?verbose=1")
		for _, check := range []string{"startup", "database", "schema", "valkey", "outbound", "draining"} {
			if !strings.Contains(body, `"`+check+`"`) {
				t.Errorf("verbose readyz lacks check %q: %s", check, body)
			}
		}
	})

	t.Run("R-ARCH-001 a worker has no client socket, web and all have one", func(t *testing.T) {
		if c, err := net.DialTimeout("tcp", worker.client, 300*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("worker listens on the client address")
		}
		for _, r := range []*replica{web, all} {
			if c, err := net.DialTimeout("tcp", r.client, time.Second); err != nil {
				t.Errorf("%s: %v", r.name, err)
			} else {
				c.Close()
			}
		}
	})

	t.Run("R-OPS-002 ping answers OK and system/info carries the installation facts", func(t *testing.T) {
		if code, _, body := web.get(t, "/api/v1/system/ping"); code != 200 || body != "OK" {
			t.Fatalf("ping %d %q", code, body)
		}
		code, _, body := web.get(t, "/api/v1/system/info")
		if code != 200 {
			t.Fatalf("info %d %s", code, body)
		}
		var info struct {
			InstallationID *string `json:"installationId"`
			Incarnation    *string `json:"incarnation"`
			AnsweredBy     struct{ Replica, Role string }
			BaseURL        string `json:"baseUrl"`
			Database       *struct {
				ServerVersion string `json:"serverVersion"`
				SchemaVersion int    `json:"schemaVersion"`
				Pools         map[string]struct{ Max int }
			}
			Valkey struct {
				Mode     string
				Keyspace *string
			}
			ObjectStore struct{ Bucket string }
		}
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatal(err)
		}
		if info.InstallationID == nil || info.Incarnation == nil || *info.InstallationID == "" {
			t.Errorf("installation facts not filled: %s", body)
		}
		if info.Database == nil || !strings.HasPrefix(info.Database.ServerVersion, "18") || info.Database.SchemaVersion < 1 || info.Database.Pools["request"].Max < 1 {
			t.Errorf("database block: %s", body)
		}
		if info.Valkey.Keyspace == nil || !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(*info.Valkey.Keyspace) || info.Valkey.Mode != "sentinel" {
			t.Errorf("valkey block: %s", body)
		}
		if info.AnsweredBy.Replica != "trove-web-it" || info.AnsweredBy.Role != "web" || info.BaseURL != "http://trove.test" || info.ObjectStore.Bucket == "" {
			t.Errorf("info = %s", body)
		}
		if strings.Contains(strings.ToLower(body), "password") || strings.Contains(body, "26381") {
			t.Errorf("info leaks a secret or a Sentinel address: %s", body)
		}
	})

	t.Run("R-OPS-006 /metrics exposes trove_ metrics on the ops listener only", func(t *testing.T) {
		web.get(t, "/api/v1/system/ping")
		code, body := opsGet(web.ops, "/metrics")
		if code != 200 || !strings.Contains(body, "trove_http_requests_total") || !strings.Contains(body, "trove_valkey_available 1") || !strings.Contains(body, `trove_build_info{`) {
			t.Fatalf("metrics %d", code)
		}
		// The authentication stage runs before the router (R-ARCH-005): without a
		// credential and with anonymous access off the client listener answers 401,
		// with one it answers 404. Either way it serves no metrics.
		if code, _, body := web.get(t, "/metrics"); (code != 404 && code != 401) || strings.Contains(body, "trove_") {
			t.Fatalf("client /metrics = %d: %.200s", code, body)
		}
	})

	t.Run("R-OPS-018 and R-OPS-002 X-Request-Id echoes the trace ID of an incoming traceparent", func(t *testing.T) {
		const tid = "0af7651916cd43dd8448eb211c80319c"
		code, hdr, _ := web.get(t, "/api/v1/system/ping", "traceparent", "00-"+tid+"-b7ad6b7169203331-01")
		if code != 200 || hdr.Get("X-Request-Id") != tid {
			t.Fatalf("%d X-Request-Id %q", code, hdr.Get("X-Request-Id"))
		}
	})

	t.Run("R-OPS-010 the request log is JSON and carries trace ID, client address and handler", func(t *testing.T) {
		const tid = "1af7651916cd43dd8448eb211c80319d"
		web.get(t, "/api/v1/system/ping", "traceparent", "00-"+tid+"-b7ad6b7169203331-01", "X-Forwarded-For", "198.51.100.77")
		var rec map[string]any
		for end := time.Now().Add(3 * time.Second); rec == nil && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			for _, m := range jsonLines(web.logs.String()) {
				if m["trace_id"] == tid {
					rec = m
				}
			}
		}
		if rec == nil {
			t.Fatalf("no request record\n%s", web.logs.String())
		}
		if rec["remote_addr"] != "198.51.100.77" || rec["handler"] != "GET /api/v1/system/ping" || rec["listener"] != "client" || rec["status"] != float64(200) {
			t.Errorf("record = %v", rec)
		}
		for _, k := range []string{"peer_addr", "method", "path", "duration_ms", "user_agent", "principal", "request_bytes", "response_bytes"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("record lacks %q", k)
			}
		}
	})

	t.Run("R-ARCH-031 Valkey connections are named trove:<instance>:<class>", func(t *testing.T) {
		admin := valkeyAdmin(t)
		out, err := admin.Do(context.Background(), admin.B().ClientList().Build()).ToString()
		if err != nil {
			t.Fatal(err)
		}
		for _, inst := range []string{"trove-web-it", "trove-worker-it", "trove-all-it"} {
			if !strings.Contains(out, "name=trove:"+inst+":") {
				t.Errorf("no Valkey connection named trove:%s:…", inst)
			}
		}
	})

	t.Run("R-ARCH-019 and R-ARCH-001 PostgreSQL sessions are named; a web replica holds no lease session", func(t *testing.T) {
		names := map[string]int{}
		rows, err := in.conn.Query(context.Background(), `SELECT application_name FROM pg_stat_activity WHERE datname = current_database() AND application_name LIKE 'trove:%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names[n]++
		}
		for _, inst := range []string{"trove-web-it", "trove-worker-it", "trove-all-it"} {
			if names["trove:"+inst+":main"] == 0 {
				t.Errorf("no session named trove:%s:main in %v", inst, names)
			}
		}
		if names["trove:trove-web-it:lease"] != 0 {
			t.Errorf("a web replica holds a lease session: %v", names)
		}
	})

	t.Run("R-OPS-003 the replica registry lists the three replicas with their roles and nodes of state ready", func(t *testing.T) {
		var items []replicaItem
		waitFor(t, "three ready replicas", 20*time.Second, func() bool {
			items = listReplicas(t, web)
			ready := 0
			for _, it := range items {
				if it.State == "ready" && !it.Lost {
					ready++
				}
			}
			return ready == 3
		})
		roles := map[string]string{}
		for _, it := range items {
			if len(it.Roles) != 1 {
				t.Errorf("%s roles = %v", it.Name, it.Roles)
				continue
			}
			roles[it.Name] = it.Roles[0]
		}
		want := map[string]string{"trove-web-it": "web", "trove-worker-it": "worker", "trove-all-it": "all"}
		for n, r := range want {
			if roles[n] != r {
				t.Errorf("roles = %v, want %s: %s", roles, n, r)
			}
		}
		for _, it := range items {
			if it.Checks["database"].Status != "pass" || it.Checks["invalidation"].Status != "pass" {
				t.Errorf("%s checks = %v", it.Name, it.Checks)
			}
		}
	})

	t.Run("R-ARCH-001 and E0.6 exactly one worker-capable replica holds the scheduler lease, the web replica none", func(t *testing.T) {
		var holders []string
		waitFor(t, "a scheduler lease holder", 20*time.Second, func() bool {
			holders = nil
			for _, it := range listReplicas(t, web) {
				for _, l := range it.Leases {
					if l.Name == "scheduler" {
						holders = append(holders, it.Name)
					}
				}
			}
			return len(holders) >= 1
		})
		if len(holders) != 1 || holders[0] == "trove-web-it" {
			t.Fatalf("scheduler holders = %v", holders)
		}
		for _, it := range listReplicas(t, web) {
			if it.Name == "trove-web-it" && len(it.Leases) != 0 {
				t.Errorf("the web replica lists leases %v", it.Leases)
			}
		}
	})

	t.Run("R-ARCH-001 the web replica's instance UID never appears in job.locked_by, and the scheduled gc job runs on a worker-capable replica", func(t *testing.T) {
		var webUID string
		for _, it := range listReplicas(t, web) {
			if it.Name == "trove-web-it" {
				webUID = it.InstanceUID
			}
		}
		if webUID == "" {
			t.Fatal("no instance UID for the web replica")
		}
		// The first scheduler tick enqueues the latest slot of every task (R-ARCH-016).
		var done int64
		waitFor(t, "a succeeded gc job", 40*time.Second, func() bool {
			if n := scalarInt(t, in, `SELECT count(*) FROM job WHERE locked_by = $1::uuid`, webUID); n != 0 {
				t.Fatalf("the web replica holds %d jobs", n)
			}
			done = scalarInt(t, in, `SELECT count(*) FROM job WHERE kind = 'gc' AND state = 'succeeded'`)
			return done > 0
		})
		claimed := func(r *replica) float64 { return metricValue(t, r, `trove_jobs_claimed_total{kind="gc"}`) }
		if got := claimed(worker) + claimed(all); got < 1 {
			t.Errorf("worker and all claimed %v gc jobs", got)
		}
		if got := claimed(web); got != 0 {
			t.Errorf("the web replica claimed %v gc jobs", got)
		}
		if n := scalarInt(t, in, `SELECT count(*) FROM job WHERE task IS NOT NULL`); n < 1 {
			t.Errorf("no job came from a scheduled task")
		}
	})

	t.Run("R-OPS-003 a SIGKILLed replica is listed lost within system.heartbeat_stale_after", func(t *testing.T) {
		victim := in.start(t, "web", "trove-kill-it")
		waitFor(t, "the victim in the registry", 10*time.Second, func() bool {
			for _, it := range listReplicas(t, web) {
				if it.Name == "trove-kill-it" && it.State == "ready" {
					return true
				}
			}
			return false
		})
		killed := time.Now()
		_ = victim.cmd.Process.Kill()
		<-victim.exited
		waitFor(t, "the victim listed lost", 3*time.Second+6*time.Second, func() bool {
			for _, it := range listReplicas(t, web) {
				if it.Name == "trove-kill-it" {
					return it.Lost
				}
			}
			return false
		})
		fmt.Printf("lost after %v (stale_after 3s)\n", time.Since(killed).Round(100*time.Millisecond))
	})

	t.Run("R-ARCH-031 the example ACL rule produces no denial for these replicas", func(t *testing.T) {
		admin := valkeyAdmin(t)
		entries, err := admin.Do(context.Background(), admin.B().Arbitrary("ACL", "LOG").Build()).ToArray()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			m, err := e.AsMap()
			if err != nil {
				t.Fatal(err)
			}
			ci := m["client-info"]
			info, _ := ci.ToString()
			if strings.Contains(info, "name=trove:trove-") && strings.Contains(info, "-it:") {
				ob, rs := m["object"], m["reason"]
				obj, _ := ob.ToString()
				reason, _ := rs.ToString()
				t.Errorf("ACL denial for %s: %s %s", info, reason, obj)
			}
		}
	})

	t.Run("R-ARCH-024 SIGTERM under load: no failed request before the drain delay ends, readiness flips first, the exit is clean", func(t *testing.T) {
		// A load loop against a path that is always 200 while the listener is open.
		type result struct {
			at   time.Time
			code int
		}
		var (
			mu      sync.Mutex
			results []result
			stop    = make(chan struct{})
			wg      sync.WaitGroup
		)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					code := -1
					if resp, err := noKeepAlive.Get("http://" + web.client + "/ui/en/"); err == nil {
						code = resp.StatusCode
						resp.Body.Close()
					}
					mu.Lock()
					results = append(results, result{time.Now(), code})
					mu.Unlock()
					time.Sleep(10 * time.Millisecond)
				}
			}()
		}
		time.Sleep(300 * time.Millisecond)

		signalled := time.Now()
		done := make(chan error, 1)
		go func() { done <- web.term(t) }()

		var flipped time.Time
		for end := time.Now().Add(1200 * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if code, _ := opsGet(web.ops, "/readyz"); code == 503 {
				flipped = time.Now()
				break
			}
		}
		if flipped.IsZero() {
			t.Error("readyz did not turn 503 during the drain delay")
		}
		if err := <-done; err != nil {
			t.Fatalf("exit: %v\n%s", err, web.logs.String())
		}
		// Keep the load running past the exit: a replica that closes its
		// listener and exits within one load round would otherwise leave no
		// refused request in the results.
		time.Sleep(300 * time.Millisecond)
		close(stop)
		wg.Wait()

		mu.Lock()
		defer mu.Unlock()
		// The drain delay is 1.5 s: until then every request must have succeeded.
		var firstFail time.Time
		for _, r := range results {
			if r.code != 200 {
				if firstFail.IsZero() || r.at.Before(firstFail) {
					firstFail = r.at
				}
				if r.at.Before(signalled.Add(1400 * time.Millisecond)) {
					t.Errorf("request failed %v after SIGTERM with status %d, before the drain delay ended", r.at.Sub(signalled), r.code)
				}
			}
		}
		if firstFail.IsZero() {
			t.Error("the listener never closed")
		}
		if !strings.Contains(web.logs.String(), `"msg":"stopped"`) {
			t.Errorf("no clean stop in the log\n%s", web.logs.String())
		}
		fmt.Printf("load: %d requests, first failure %v after SIGTERM, readyz flipped %v after SIGTERM\n",
			len(results), firstFail.Sub(signalled).Round(time.Millisecond), flipped.Sub(signalled).Round(time.Millisecond))
	})

	t.Run("R-ARCH-024 after the exit the web replica's database sessions and Valkey connections are gone", func(t *testing.T) {
		time.Sleep(500 * time.Millisecond)
		var n int
		if err := in.conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:trove-web-it:%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d sessions of the stopped replica remain", n)
		}
		admin := valkeyAdmin(t)
		out, _ := admin.Do(context.Background(), admin.B().ClientList().Build()).ToString()
		if strings.Contains(out, "name=trove:trove-web-it:") {
			t.Error("Valkey connections of the stopped replica remain")
		}
	})

	t.Run("R-OPS-003 a replica that stopped cleanly deleted its registry row", func(t *testing.T) {
		for _, it := range listReplicas(t, all) {
			if it.Name == "trove-web-it" {
				t.Errorf("the stopped web replica is still listed: %+v", it)
			}
		}
	})

	t.Run("R-ARCH-024 the worker and all replicas stop cleanly on SIGTERM", func(t *testing.T) {
		for _, r := range []*replica{worker, all} {
			if err := r.term(t); err != nil {
				t.Errorf("%s: %v\n%s", r.name, err, r.logs.String())
			}
		}
	})
}

func TestStartWithoutSchema(t *testing.T) {
	t.Run("R-OPS-001 a replica on an unmigrated database is alive and not ready, and keeps retrying", func(t *testing.T) {
		in := newInstall(t)
		r := &replica{}
		_ = r
		rep := &replica{name: "trove-wait-it", role: "web", client: freeAddr(t), ops: freeAddr(t), logs: &syncBuffer{}, exited: make(chan error, 1)}
		cmd := startRaw(t, in, rep)
		_ = cmd
		for end := time.Now().Add(4 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			if code, _ := opsGet(rep.ops, "/healthz"); code == 200 {
				break
			}
		}
		time.Sleep(1500 * time.Millisecond)
		if code, _ := opsGet(rep.ops, "/healthz"); code != 200 {
			t.Fatalf("healthz = %d", code)
		}
		if code, _ := opsGet(rep.ops, "/readyz"); code != 503 {
			t.Fatalf("readyz = %d on an unmigrated database", code)
		}
		if c, err := net.DialTimeout("tcp", rep.client, 300*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("the client listener opened before the schema was ready")
		}
		// Migrate underneath it: the replica becomes ready by itself.
		if code, out, errOut := in.run(t, "migrate", "up"); code != 0 {
			t.Fatalf("migrate %d %s %s", code, out, errOut)
		}
		var ready bool
		for end := time.Now().Add(40 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
			if code, _ := opsGet(rep.ops, "/readyz"); code == 200 {
				ready = true
				break
			}
		}
		if !ready {
			t.Fatalf("not ready after the migration\n%s", rep.logs.String())
		}
	})
}

var _ = http.StatusOK

type replicaItem struct {
	Name        string `json:"name"`
	InstanceUID string `json:"instanceUid"`
	Roles       []string
	State       string
	Lost        bool
	Leases      []struct{ Name string }
	Checks      map[string]struct{ Status string }
}

// listReplicas reads GET /api/v1/system/replicas from a client-serving replica.
func listReplicas(t *testing.T, r *replica) []replicaItem {
	t.Helper()
	code, _, body := r.get(t, "/api/v1/system/replicas")
	if code != 200 {
		t.Fatalf("replicas %d %s", code, body)
	}
	var list struct{ Items []replicaItem }
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("%s did not happen within %v", what, within)
}

func scalarInt(t *testing.T, in *install, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := in.conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// metricValue returns the value of the series that starts with the given text
// (name and labels), summed over matching lines; 0 when the series is absent.
func metricValue(t *testing.T, r *replica, series string) float64 {
	t.Helper()
	_, body := opsGet(r.ops, "/metrics")
	var sum float64
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(line, series+" "); ok {
			var v float64
			fmt.Sscan(rest, &v)
			sum += v
		}
	}
	return sum
}
