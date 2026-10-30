package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// syncBuffer is a log sink the test reads while the app writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func keyFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "2026-10.key")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func emptyConfig(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "trove.yaml")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type harness struct {
	app    *App
	cancel context.CancelFunc
	done   chan error
	logs   *syncBuffer
	client string // base URL of the client listener; "" for a worker
	ops    string
}

// env is the minimal valid serve configuration plus overrides.
func baseEnv(t *testing.T, extra map[string]string) []string {
	env := map[string]string{
		"TROVE_SERVER_LISTEN": freePort(t), "TROVE_SERVER_OPS_LISTEN": freePort(t),
		"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "300ms", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "5s",
		"TROVE_HEALTH_CHECK_INTERVAL": "200ms", "TROVE_HEALTH_CHECK_TIMEOUT": "100ms",
		"TROVE_DATABASE_HOST": "db.example", "TROVE_DATABASE_NAME": "trove",
		"TROVE_VALKEY_SENTINEL_ADDRESSES": `["s1:26379"]`, "TROVE_VALKEY_SENTINEL_PRIMARY_NAME": "trove",
		"TROVE_STORAGE_S3_BUCKET": "blobs", "TROVE_SECRETS_ENCRYPTION_KEY_FILES": fmt.Sprintf(`[%q]`, keyFile(t)),
		"TROVE_SECRETS_ACTIVE_KEY_ID": "2026-10", "TROVE_SERVER_BASE_URL": "https://trove.example.com",
	}
	for k, v := range extra {
		env[k] = v
	}
	env["TROVE_CONFIG_FILE"] = emptyConfig(t)
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func loadCfg(t *testing.T, role string, env []string) *config.Loaded {
	t.Helper()
	l, err := config.Load(config.Options{Environ: env, Flags: map[string]string{"process.role": role}, Command: config.CommandServe})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func holdRoutes(r *httpserver.Router) {
	r.HandleFunc("GET /test/hold", func(w http.ResponseWriter, req *http.Request) {
		d, _ := time.ParseDuration(req.URL.Query().Get("for"))
		_, _ = w.Write([]byte("started\n"))
		w.(http.Flusher).Flush()
		select {
		case <-time.After(d):
		case <-req.Context().Done():
			return
		}
		_, _ = w.Write([]byte("finished\n"))
	})
	r.HandleFunc("GET /test/stream", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", "1000000000")
		for {
			if _, err := w.Write(make([]byte, 1024)); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func start(t *testing.T, role string, extra map[string]string) *harness {
	t.Helper()
	env := baseEnv(t, extra)
	loaded := loadCfg(t, role, env)
	logs := &syncBuffer{}
	a, err := New(Options{Loaded: loaded, Role: role, Environ: env, LogWriter: logs, Extra: holdRoutes, SkipInfrastructure: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{app: a, cancel: cancel, logs: logs, done: make(chan error, 1)}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.Started():
	case err := <-a.fatal:
		t.Fatalf("start-up failed: %v\n%s", err, logs.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("not started\n%s", logs.String())
	}
	if a.ClientAddr() != nil {
		h.client = "http://" + a.ClientAddr().String()
	}
	h.ops = "http://" + a.OpsAddr().String()
	t.Cleanup(func() {
		cancel()
		_ = a.Shutdown(context.WithoutCancel(ctx))
	})
	return h
}

func get(t *testing.T, url string, hdr ...string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func TestRoles(t *testing.T) {
	t.Run("R-ARCH-001 a web replica binds the client and the ops listener, R-OPS-002 ping answers OK", func(t *testing.T) {
		h := start(t, "web", nil)
		if code, _, body := get(t, h.client+"/api/v1/system/ping"); code != 200 || body != "OK" {
			t.Fatalf("ping %d %q", code, body)
		}
		if code, _, _ := get(t, h.ops+"/readyz"); code != 200 {
			t.Fatalf("readyz %d", code)
		}
	})

	t.Run("R-ARCH-001 a worker replica opens no client socket", func(t *testing.T) {
		client := freePort(t)
		h := start(t, "worker", map[string]string{"TROVE_SERVER_LISTEN": client})
		if h.app.ClientAddr() != nil {
			t.Fatal("worker has a client listener")
		}
		if c, err := net.DialTimeout("tcp", client, 500*time.Millisecond); err == nil {
			c.Close()
			t.Fatalf("something listens on the client address %s", client)
		}
		if code, _, _ := get(t, h.ops+"/readyz"); code != 200 {
			t.Fatalf("worker readyz %d", code)
		}
		if code, _, _ := get(t, h.ops+"/healthz"); code != 200 {
			t.Fatalf("worker healthz %d", code)
		}
	})

	t.Run("R-ARCH-001 role all serves both and logs a warning that names the role", func(t *testing.T) {
		h := start(t, "all", nil)
		if h.client == "" {
			t.Fatal("no client listener")
		}
		if !strings.Contains(h.logs.String(), "process.role is all") {
			t.Fatalf("no warning in %s", h.logs.String())
		}
	})

	t.Run("R-ARCH-001 the subsystem table", func(t *testing.T) {
		web, _ := SubsystemsFor("web")
		worker, _ := SubsystemsFor("worker")
		if web.ClientListener != true || web.LeasePool || web.JobLoops || !web.Registry {
			t.Errorf("web = %+v", web)
		}
		if worker.ClientListener || !worker.LeasePool || !worker.JobLoops || !worker.OpsListener {
			t.Errorf("worker = %+v", worker)
		}
		if _, err := SubsystemsFor("backup"); err == nil {
			t.Error("backup is not a serving role")
		}
	})
}

func TestListeners(t *testing.T) {
	h := start(t, "web", nil)
	t.Run("R-ARCH-005 /healthz is a 404 on the client listener and 200 on the ops listener", func(t *testing.T) {
		if code, _, _ := get(t, h.client+"/healthz"); code != 404 {
			t.Fatalf("client /healthz = %d", code)
		}
		if code, _, _ := get(t, h.ops+"/healthz"); code != 200 {
			t.Fatalf("ops /healthz = %d", code)
		}
		if code, _, _ := get(t, h.ops+"/api/v1/system/ping"); code != 404 {
			t.Fatalf("ops ping = %d", code)
		}
	})
	t.Run("R-ARCH-005 GET / redirects to /ui/ and the UI is mounted", func(t *testing.T) {
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(h.client + "/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 302 || resp.Header.Get("Location") != "/ui/" {
			t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		resp, err = (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(h.client + "/ui/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/ui/en/") {
			t.Fatalf("/ui/ = %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		if code, _, body := get(t, h.client+"/ui/en/"); code != 200 || !strings.Contains(body, "<") {
			t.Fatalf("/ui/en/ = %d", code)
		}
	})
	t.Run("R-HA-041 /v2/ answers 404 on the running server and the real route table passes the test", func(t *testing.T) {
		for _, p := range []string{"/v2/", "/v2/docker-all/manifests/latest"} {
			code, hdr, _ := get(t, h.client+p)
			if code != 404 || hdr.Get("Content-Type") != "application/problem+json" {
				t.Errorf("%s: %d %s", p, code, hdr.Get("Content-Type"))
			}
		}
		if v := httpserver.CheckRouteTable(h.app.router.Patterns(), nil); len(v) != 0 {
			t.Fatalf("route table violations: %v", v)
		}
	})
	t.Run("R-HA-041 a plug-in that registers GET /v2/x stops start-up", func(t *testing.T) {
		env := baseEnv(t, nil)
		a, err := New(Options{
			Loaded: loadCfg(t, "web", env), Role: "web", Environ: env, LogWriter: io.Discard, SkipInfrastructure: true,
			Extra: func(r *httpserver.Router) { r.HandleFunc("GET /v2/x", func(http.ResponseWriter, *http.Request) {}) },
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := a.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer a.Shutdown(context.Background())
		select {
		case err := <-a.fatal:
			if !strings.Contains(err.Error(), "GET /v2/x") {
				t.Fatalf("error does not name the route: %v", err)
			}
		case <-time.After(30 * time.Second): // a guard against a start-up that never fails, not a time budget: a loaded CI runner took more than 5 s
			t.Fatal("start-up did not fail")
		}
	})
	t.Run("R-API-002 a route missing from the document stops start-up and names it", func(t *testing.T) {
		env := baseEnv(t, nil)
		err := RunWith(t.Context(), Options{
			Loaded: loadCfg(t, "web", env), Role: "web", Environ: env, LogWriter: io.Discard, SkipInfrastructure: true,
			Extra: func(r *httpserver.Router) {
				r.HandleFunc("GET /api/v1/ghost", func(http.ResponseWriter, *http.Request) {})
			},
		})
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/ghost") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestEndToEndHeaders(t *testing.T) {
	// The trace context of a caller counts only from tracing.trusted_peers, never from the
	// trusted proxies (S6); the test client is a trusted peer here.
	h := start(t, "web", map[string]string{"TROVE_SERVER_TRUSTED_PROXIES": `["127.0.0.0/8"]`, "TROVE_TRACING_TRUSTED_PEERS": `["127.0.0.0/8"]`})
	t.Run("E0.10 a request with traceparent keeps its trace ID in X-Request-Id and the request log", func(t *testing.T) {
		const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
		code, hdr, _ := get(t, h.client+"/api/v1/system/ping", "traceparent", "00-"+tid+"-00f067aa0ba902b7-01", "X-Forwarded-For", "198.51.100.4")
		if code != 200 || hdr.Get("X-Request-Id") != tid {
			t.Fatalf("%d X-Request-Id %q", code, hdr.Get("X-Request-Id"))
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, line := range strings.Split(h.logs.String(), "\n") {
				var rec map[string]any
				if json.Unmarshal([]byte(line), &rec) != nil || rec["trace_id"] != tid {
					continue
				}
				if rec["remote_addr"] != "198.51.100.4" || rec["handler"] != "GET /api/v1/system/ping" || rec["listener"] != "client" {
					t.Fatalf("request record = %v", rec)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no request record with the trace ID in\n%s", h.logs.String())
	})
	t.Run("R-OPS-002 a presented but invalid credential on ping answers 401", func(t *testing.T) {
		code, hdr, _ := get(t, h.client+"/api/v1/system/ping", "Authorization", "Bearer trv_pat_revoked")
		if code != 401 || hdr.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%d %s", code, hdr.Get("Content-Type"))
		}
	})
	t.Run("R-OPS-002 version needs credentials, info and replicas need system.read", func(t *testing.T) {
		for _, p := range []string{"/api/v1/system/version", "/api/v1/system/info", "/api/v1/system/replicas"} {
			if code, hdr, _ := get(t, h.client+p); code != 401 || hdr.Get("X-Trove-Replica") != "" {
				t.Errorf("%s: %d replica header %q", p, code, hdr.Get("X-Trove-Replica"))
			}
		}
	})
	t.Run("R-API-002 openapi.json is public and equals the embedded document", func(t *testing.T) {
		code, _, body := get(t, h.client+"/api/v1/openapi.json")
		var doc map[string]any
		if code != 200 || json.Unmarshal([]byte(body), &doc) != nil || !strings.HasPrefix(doc["openapi"].(string), "3.1.") {
			t.Fatalf("%d %.100s", code, body)
		}
		if doc["servers"].([]any)[0].(map[string]any)["url"] != "https://trove.example.com" {
			t.Fatalf("servers = %v", doc["servers"])
		}
	})
	t.Run("E0.10 ops /metrics exposes trove_ metrics", func(t *testing.T) {
		get(t, h.client+"/api/v1/system/ping")
		code, _, body := get(t, h.ops+"/metrics")
		if code != 200 || !strings.Contains(body, "trove_http_requests_total") || !strings.Contains(body, "trove_build_info") {
			t.Fatalf("%d %.200s", code, body)
		}
	})
}

func TestBootstrapAccess(t *testing.T) {
	// Anonymous read is refused behind an https base URL (S10); the bootstrap tests run on a local one.
	local := "http://localhost:8080"
	h := start(t, "web", map[string]string{"TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ": "true", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "0s", "TROVE_SERVER_BASE_URL": local})
	t.Run("R-API-038 replicas lists this replica with its role and state", func(t *testing.T) {
		code, _, body := get(t, h.client+"/api/v1/system/replicas")
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal([]byte(body), &list)
		if len(list.Items) != 1 || list.Items[0]["state"] != "ready" || fmt.Sprint(list.Items[0]["roles"]) != "[web]" {
			t.Fatalf("%s", body)
		}
	})
	t.Run("R-API-038 a replica started with role all lists roles [all]", func(t *testing.T) {
		all := start(t, "all", map[string]string{"TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ": "true", "TROVE_SERVER_BASE_URL": local})
		_, _, body := get(t, all.client+"/api/v1/system/replicas")
		if !strings.Contains(body, `"roles":["all"]`) {
			t.Fatalf("%s", body)
		}
	})
	t.Run("R-OPS-004 info answers with the configuration facts and no secret", func(t *testing.T) {
		code, _, body := get(t, h.client+"/api/v1/system/info")
		if code != 200 || !strings.Contains(body, `"bucket":"blobs"`) || strings.Contains(strings.ToLower(body), "password") {
			t.Fatalf("%d %s", code, body)
		}
	})
}

func TestIdentity(t *testing.T) {
	t.Run("R-OPS-003 the instance is the pod name from TROVE_POD_NAME, then POD_NAME", func(t *testing.T) {
		if id := NewIdentity([]string{"TROVE_POD_NAME=trove-web-1", "POD_NAME=other", "HOSTNAME=h"}, "web"); id.Instance != "trove-web-1" {
			t.Fatalf("%+v", id)
		}
		if id := NewIdentity([]string{"POD_NAME=pod-2", "HOSTNAME=h"}, "web"); id.Instance != "pod-2" {
			t.Fatalf("%+v", id)
		}
	})
	t.Run("R-OPS-003 HOSTNAME is the pod name inside Kubernetes", func(t *testing.T) {
		if id := NewIdentity([]string{"KUBERNETES_SERVICE_HOST=10.0.0.1", "HOSTNAME=trove-worker-9"}, "worker"); id.Instance != "trove-worker-9" {
			t.Fatalf("%+v", id)
		}
	})
	t.Run("R-OPS-003 outside Kubernetes the instance is hostname-pid", func(t *testing.T) {
		id := NewIdentity([]string{"HOSTNAME=laptop"}, "all")
		if !strings.Contains(id.Instance, "-") || id.UID == "" || NewIdentity(nil, "all").UID == id.UID {
			t.Fatalf("%+v", id)
		}
	})
	t.Run("R-OPS-003 the node name comes from TROVE_NODE_NAME", func(t *testing.T) {
		if id := NewIdentity([]string{"TROVE_NODE_NAME=n1"}, "web"); id.Node != "n1" {
			t.Fatalf("%+v", id)
		}
	})
}

func TestStartupFailures(t *testing.T) {
	t.Run("R-ARCH-021 a missing key ring stops the replica", func(t *testing.T) {
		env := baseEnv(t, map[string]string{"TROVE_SECRETS_ENCRYPTION_KEY_FILES": `["/nonexistent/2026-10.key"]`})
		err := RunWith(t.Context(), Options{Loaded: loadCfg(t, "web", env), Role: "web", Environ: env, LogWriter: io.Discard, SkipInfrastructure: true})
		if err == nil || !strings.Contains(err.Error(), "key file") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-005 a port that is taken stops start-up", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		env := baseEnv(t, map[string]string{"TROVE_SERVER_OPS_LISTEN": busy.Addr().String()})
		err = RunWith(t.Context(), Options{Loaded: loadCfg(t, "web", env), Role: "web", Environ: env, LogWriter: io.Discard, SkipInfrastructure: true})
		if err == nil || !strings.Contains(err.Error(), busy.Addr().String()) {
			t.Fatalf("err = %v, want one that names the address", err)
		}
	})
}

// lateWriter fails the test if anything writes after seal.
type lateWriter struct {
	mu     sync.Mutex
	sealed bool
	late   int
}

func (w *lateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed {
		w.late++
	}
	return len(p), nil
}

func (w *lateWriter) seal()      { w.mu.Lock(); w.sealed = true; w.mu.Unlock() }
func (w *lateWriter) lates() int { w.mu.Lock(); defer w.mu.Unlock(); return w.late }

func TestNothingOutlivesRun(t *testing.T) {
	t.Run("R-ARCH-024 no goroutine of the app writes to the log after RunWith returned", func(t *testing.T) {
		env := baseEnv(t, map[string]string{"TROVE_SECRETS_ENCRYPTION_KEY_FILES": `["/nonexistent/2026-10.key"]`})
		w := &lateWriter{}
		_ = RunWith(t.Context(), Options{Loaded: loadCfg(t, "web", env), Role: "web", Environ: env, LogWriter: w, SkipInfrastructure: true})
		w.seal()
		time.Sleep(800 * time.Millisecond) // several check intervals (200 ms)
		if n := w.lates(); n != 0 {
			t.Fatalf("%d log writes after RunWith returned", n)
		}
	})
}
