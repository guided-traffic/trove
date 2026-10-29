package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// packageRoutes stands in for the package formats that do not exist yet: one
// generic route of the packages family.
func packageRoutes(r *httpserver.Router) {
	r.HandleFunc("GET /api/npm/{repoKey}/{rest...}", func(w http.ResponseWriter, req *http.Request) {
		info := httpserver.InfoFrom(req.Context())
		_ = json.NewEncoder(w).Encode(map[string]string{
			"endpoint": httpserver.EndpointFrom(req.Context()).Name(), "base": info.BaseURL, "root": httpserver.RootRepository(req.Context()),
		})
	})
	holdRoutes(r)
}

func startEndpoints(t *testing.T, role string, extra map[string]string) *harness {
	t.Helper()
	env := baseEnv(t, extra)
	loaded := loadCfg(t, role, env)
	logs := &syncBuffer{}
	a, err := New(Options{Loaded: loaded, Role: role, Environ: env, LogWriter: logs, Extra: packageRoutes, SkipInfrastructure: true})
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
	h.ops = "http://" + a.OpsAddr().String()
	t.Cleanup(func() {
		cancel()
		_ = a.Shutdown(context.WithoutCancel(ctx))
	})
	return h
}

// endpointsJSON configures the endpoint customers. The routing tests send no
// credential, so the endpoint admits anonymous requests; a further endpoint
// refuses them by default (R-EP-010 item 4), which
// TestEndpointRefusesAnonymousByDefault covers.
func endpointsJSON(listen string, more string) string {
	return fmt.Sprintf(`{"customers":{"listen":%q,"base_url":"https://packages.example.com","hosts":["pkgs.example.org"],"wildcard_host":"*.registry.example.com","host_roots":{"pkgs.example.org":"docker-hub"},"auth":{"anonymous":true}%s}}`, listen, more)
}

func TestEndpointRefusesAnonymousByDefault(t *testing.T) {
	h := startEndpoints(t, "web", map[string]string{
		"TROVE_SERVER_ENDPOINTS": fmt.Sprintf(`{"customers":{"listen":%q,"base_url":"https://packages.example.com"}}`, freePort(t)),
	})
	cust := h.app.EndpointAddr("customers")
	t.Run("R-EP-010 R-EP-005 a further endpoint answers every request without a credential with the same 401", func(t *testing.T) {
		var want string
		for _, p := range []string{"/does-not-exist", "/ui/", "/auth/oidc/corp/start", "/api/v1/system/ping", "/api/npm/npm-customers/"} {
			code, body := hostGet(t, cust, "packages.example.com", p)
			var pr map[string]any
			_ = json.Unmarshal([]byte(body), &pr)
			if code != 401 || pr["code"] != "credentials_required" {
				t.Errorf("%s: %d %s", p, code, body)
			}
			delete(pr, "instance")
			delete(pr, "requestId")
			b, _ := json.Marshal(pr)
			if want == "" {
				want = string(b)
			} else if string(b) != want {
				t.Errorf("%s: body %s differs from the unknown path's %s", p, b, want)
			}
		}
	})
}

// hostGet sends a request to addr with the given Host.
func hostGet(t *testing.T, addr, host, path string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	req.Host = host
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatalf("GET %s%s: %v", addr, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func requestRecords(logs *syncBuffer) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["log"] == "request" {
			out = append(out, rec)
		}
	}
	return out
}

func TestEndpointsOnOneReplica(t *testing.T) {
	custAddr := freePort(t)
	h := startEndpoints(t, "web", map[string]string{"TROVE_SERVER_ENDPOINTS": endpointsJSON(custAddr, "")})
	def := h.app.EndpointAddr("default")
	cust := h.app.EndpointAddr("customers")
	if def == "" || cust != custAddr {
		t.Fatalf("addresses: default %q customers %q", def, cust)
	}

	t.Run("R-EP-002 a replica opens the listener of every endpoint of its pool", func(t *testing.T) {
		if got := h.app.EndpointNames(); len(got) != 2 || got[0] != "default" || got[1] != "customers" {
			t.Fatalf("endpoints = %v", got)
		}
	})

	t.Run("R-EP-003 a request on the second listener with the host of the default endpoint answers 421", func(t *testing.T) {
		code, body := hostGet(t, cust, "trove.example.com", "/api/npm/npm-all/")
		var p map[string]any
		_ = json.Unmarshal([]byte(body), &p)
		if code != 421 || p["code"] != "misdirected_request" {
			t.Fatalf("%d %s", code, body)
		}
		if code, _ := hostGet(t, cust, "packages.example.com", "/api/npm/npm-all/"); code != 200 {
			t.Fatalf("own host: %d", code)
		}
	})

	t.Run("R-EP-002 R-EP-009 the endpoint in the request log and the metrics comes from the listener, not from Host or a forwarded host", func(t *testing.T) {
		hostGet(t, def, "packages.example.com", "/api/v1/system/ping", "X-Forwarded-Host", "packages.example.com")
		hostGet(t, cust, "trove.example.com", "/api/npm/x/", "X-Forwarded-Host", "trove.example.com")
		hostGet(t, cust, "pkgs.example.org", "/api/npm/x/")
		deadline := time.Now().Add(2 * time.Second)
		var onDefault, mis, own map[string]any
		for time.Now().Before(deadline) && (onDefault == nil || mis == nil || own == nil) {
			for _, r := range requestRecords(h.logs) {
				switch {
				case r["path"] == "/api/v1/system/ping" && r["endpoint"] == "default":
					onDefault = r
				case r["path"] == "/api/npm/x/" && r["status"] == float64(421):
					mis = r
				case r["path"] == "/api/npm/x/" && r["status"] == float64(200):
					own = r
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		if onDefault == nil || onDefault["host"] != "packages.example.com" {
			t.Errorf("default record = %v", onDefault)
		}
		if mis == nil || mis["endpoint"] != "customers" || mis["host"] != "trove.example.com" {
			t.Errorf("misdirected record = %v", mis)
		}
		if own == nil || own["endpoint"] != "customers" || own["host"] != "pkgs.example.org" {
			t.Errorf("own-host record = %v", own)
		}
		_, _, metrics := get(t, h.ops+"/metrics")
		for _, want := range []string{
			`trove_http_requests_total{code="200",endpoint="default",handler="GET /api/v1/system/ping",listener="client",method="GET"}`,
			`trove_http_requests_total{code="200",endpoint="customers",handler="GET /api/npm/{repoKey}/{rest...}",listener="client",method="GET"}`,
			`trove_endpoint_misdirected_total{endpoint="customers"} 2`,
			`trove_endpoint_misdirected_total{endpoint="default"} 0`,
			`trove_endpoint_listener_up{endpoint="customers"} 1`,
			`trove_endpoint_listener_up{endpoint="default"} 1`,
		} {
			if !strings.Contains(metrics, want) {
				t.Errorf("metrics lack %s\n%s", want, grepLines(metrics, "trove_http_requests_total"))
			}
		}
		if strings.Contains(metrics, `listener="ops",endpoint`) || strings.Contains(metrics, `endpoint="customers",handler="GET /readyz"`) {
			t.Error("an ops series carries an endpoint")
		}
	})

	t.Run("R-EP-003 a forwarded host from a peer outside the trusted proxies is not read", func(t *testing.T) {
		code, _ := hostGet(t, cust, "trove.example.com", "/api/npm/x/", "X-Forwarded-Host", "packages.example.com", "X-Forwarded-Proto", "https")
		if code != 421 {
			t.Fatalf("%d", code)
		}
	})

	t.Run("R-EP-005 the second endpoint serves packages only: ui, api and the management API answer 404, /v2/ stays reserved", func(t *testing.T) {
		for _, p := range []string{"/ui/", "/", "/auth/oidc/corp/start", "/api/v1/system/ping", "/api/v1/system/info", "/api/v1/openapi.json", "/files/generic-local/x", "/v2/", "/.well-known/terraform.json", "/does-not-exist"} {
			code, body := hostGet(t, cust, "packages.example.com", p)
			var pr map[string]any
			_ = json.Unmarshal([]byte(body), &pr)
			if code != 404 || pr["code"] != "not_found" {
				t.Errorf("%s: %d %s", p, code, body)
			}
		}
		if code, _ := hostGet(t, cust, "packages.example.com", "/api/npm/npm-customers/"); code != 200 {
			t.Errorf("package route: %d", code)
		}
		// the default endpoint keeps every family
		if code, _ := hostGet(t, def, "trove.example.com", "/api/v1/system/info"); code != 200 && code != 401 && code != 403 {
			t.Errorf("default api: %d", code)
		}
		if code, _ := hostGet(t, def, "trove.example.com", "/api/v1/system/ping"); code != 200 {
			t.Errorf("default ping: %d", code)
		}
	})

	t.Run("R-EP-006 a configured host-root binding is accepted and reserved: no route uses it", func(t *testing.T) {
		code, body := hostGet(t, cust, "pkgs.example.org", "/api/npm/x/")
		var got map[string]string
		_ = json.Unmarshal([]byte(body), &got)
		if code != 200 || got["root"] != "docker-hub" || got["base"] != "https://pkgs.example.org" {
			t.Fatalf("bound host: %d %s", code, body)
		}
		for _, p := range []string{"/v2/", "/.well-known/terraform.json"} {
			if code, _ := hostGet(t, cust, "pkgs.example.org", p); code != 404 {
				t.Errorf("%s on the bound host: %d", p, code)
			}
		}
		_, body = hostGet(t, cust, "packages.example.com", "/api/npm/x/")
		_ = json.Unmarshal([]byte(body), &got)
		if got["root"] != "" {
			t.Errorf("unbound host has a binding: %v", got)
		}
	})

	t.Run("R-EP-003 the default endpoint answers 421 once hosts are configured for it", func(t *testing.T) {
		h := startEndpoints(t, "web", map[string]string{
			"TROVE_SERVER_ENDPOINTS": fmt.Sprintf(`{"default":{"hosts":["trove.example.com"]},"customers":{"listen":%q,"base_url":"https://packages.example.com","auth":{"anonymous":true}}}`, freePort(t)),
		})
		def := h.app.EndpointAddr("default")
		if code, _ := hostGet(t, def, "packages.example.com", "/api/v1/system/ping"); code != 421 {
			t.Errorf("foreign host on :8080: %d", code)
		}
		if code, _ := hostGet(t, def, "trove.example.com", "/api/v1/system/ping"); code != 200 {
			t.Errorf("own host on :8080: %d", code)
		}
	})
}

func TestEndpointProcesses(t *testing.T) {
	custAddr := freePort(t)
	pooled := func(extra map[string]string) map[string]string {
		m := map[string]string{"TROVE_SERVER_ENDPOINTS": endpointsJSON(custAddr, `,"pool":"ep-customers"`)}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	listening := func(addr string) bool {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil
	}

	t.Run("R-EP-002 a process of the endpoint's pool opens that endpoint's listener and not :8080", func(t *testing.T) {
		defAddr := freePort(t)
		h := startEndpoints(t, "web", pooled(map[string]string{"TROVE_POOL_NAME": "ep-customers", "TROVE_SERVER_LISTEN": defAddr}))
		if got := h.app.EndpointNames(); len(got) != 1 || got[0] != "customers" {
			t.Fatalf("endpoints = %v", got)
		}
		if !listening(custAddr) || listening(defAddr) {
			t.Fatalf("customers listening %v, default listening %v", listening(custAddr), listening(defAddr))
		}
		if code, _ := hostGet(t, custAddr, "packages.example.com", "/api/npm/x/"); code != 200 {
			t.Fatalf("pool process: %d", code)
		}
	})

	t.Run("R-EP-002 the main web Deployment opens :8080 and not the pooled endpoint", func(t *testing.T) {
		custAddr = freePort(t)
		defAddr := freePort(t)
		h := startEndpoints(t, "web", pooled(map[string]string{"TROVE_SERVER_LISTEN": defAddr}))
		if got := h.app.EndpointNames(); len(got) != 1 || got[0] != "default" {
			t.Fatalf("endpoints = %v", got)
		}
		if listening(custAddr) || !listening(defAddr) {
			t.Fatalf("customers listening %v, default listening %v", listening(custAddr), listening(defAddr))
		}
	})

	t.Run("R-EP-002 a process in a pool that no endpoint names serves the endpoints without a pool", func(t *testing.T) {
		custAddr = freePort(t)
		h := startEndpoints(t, "web", pooled(map[string]string{"TROVE_POOL_NAME": "path-pool"}))
		if got := h.app.EndpointNames(); len(got) != 1 || got[0] != "default" {
			t.Fatalf("endpoints = %v", got)
		}
	})

	t.Run("R-EP-002 the development role all opens every listener, a worker none", func(t *testing.T) {
		custAddr = freePort(t)
		h := startEndpoints(t, "all", pooled(nil))
		if got := h.app.EndpointNames(); len(got) != 2 || !listening(custAddr) {
			t.Fatalf("all: %v", got)
		}
		custAddr = freePort(t)
		startEndpoints(t, "worker", pooled(nil))
		if listening(custAddr) {
			t.Fatal("a worker opened an endpoint listener")
		}
	})

	t.Run("R-EP-002 a listener that cannot bind stops startup and names the endpoint", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		env := baseEnv(t, map[string]string{"TROVE_SERVER_ENDPOINTS": endpointsJSON(busy.Addr().String(), "")})
		a, err := New(Options{Loaded: loadCfg(t, "web", env), Role: "web", Environ: env, LogWriter: io.Discard, SkipInfrastructure: true})
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
			if !strings.Contains(err.Error(), "endpoint customers") {
				t.Fatalf("error does not name the endpoint: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("start-up did not fail")
		}
	})
}

func TestEndpointListenerTLS(t *testing.T) {
	ca := tlstest.NewCA(t, "endpoint app ca")
	dir := t.TempDir()
	exact, exactKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"packages.example.com"}, Serial: big.NewInt(1001)}).Files(t, dir, "exact")
	wild, wildKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"*.packages.example.com"}, Serial: big.NewInt(1002)}).Files(t, dir, "wild")
	def, defKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"trove.example.com", "localhost"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}, Serial: big.NewInt(1003)}).Files(t, dir, "default")
	custAddr := freePort(t)
	h := startEndpoints(t, "web", map[string]string{
		"TROVE_SERVER_TLS_CERTIFICATES":    certificatesEnv(def, defKey),
		"TROVE_SERVER_TLS_RELOAD_INTERVAL": "1s",
		"TROVE_SERVER_ENDPOINTS": fmt.Sprintf(`{"customers":{"listen":%q,"base_url":"https://packages.example.com","tls":{"certificates":[{"cert_file":%q,"key_file":%q},{"cert_file":%q,"key_file":%q}]}}}`,
			custAddr, exact, exactKey, wild, wildKey),
	})
	def8080 := h.app.EndpointAddr("default")

	served := func(addr, sni string) (int64, int, error) {
		c := &tls.Config{RootCAs: ca.Pool(), ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // the test reads which certificate arrives
		conn, err := tls.Dial("tcp", addr, c)
		if err != nil {
			return 0, 0, err
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), 0, nil
	}

	t.Run("R-EP-004 SNI picks the endpoint's certificate incl. a wildcard, the same names on the default listener get server.tls", func(t *testing.T) {
		for sni, want := range map[string]int64{"packages.example.com": 1001, "a.packages.example.com": 1002, "other.example": 1001, "": 1001} {
			if got, _, err := served(custAddr, sni); err != nil || got != want {
				t.Errorf("customers SNI %q: %d %v, want %d", sni, got, err, want)
			}
		}
		for _, sni := range []string{"packages.example.com", "a.packages.example.com", "trove.example.com"} {
			if got, _, err := served(def8080, sni); err != nil || got != 1003 {
				t.Errorf("default SNI %q: %d %v", sni, got, err)
			}
		}
	})

	t.Run("R-EP-004 the endpoint's TLS series name the listener and the pair", func(t *testing.T) {
		_, m, _, err := httpsGet(t, ca, "https://"+h.app.OpsAddr().String()+"/metrics")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{exact, wild} {
			want := fmt.Sprintf(`trove_tls_certificate_expiry_timestamp_seconds{kind="listener",listener="client:customers",name=%q}`, f)
			if !strings.Contains(m, want) {
				t.Errorf("metrics lack %s\n%s", want, grepLines(m, "trove_tls_certificate"))
			}
		}
	})

	t.Run("R-EP-004 a rotation of the second listener's pair is served within the reload interval, a broken one keeps the old pair and is counted", func(t *testing.T) {
		ca.Issue(t, tlstest.Options{DNSNames: []string{"*.packages.example.com"}, Serial: big.NewInt(1004)}).Write(t, wild, wildKey)
		deadline := time.Now().Add(4 * time.Second)
		for {
			if got, _, err := served(custAddr, "a.packages.example.com"); err == nil && got == 1004 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("rotated pair not served")
			}
			time.Sleep(100 * time.Millisecond)
		}
		tlstest.WriteFile(t, wildKey, ca.Issue(t, tlstest.Options{}).KeyPEM)
		time.Sleep(2200 * time.Millisecond)
		if got, _, err := served(custAddr, "a.packages.example.com"); err != nil || got != 1004 {
			t.Errorf("after the broken rotation: %d %v", got, err)
		}
		_, m, _, err := httpsGet(t, ca, "https://"+h.app.OpsAddr().String()+"/metrics")
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf(`trove_tls_reload_errors_total{kind="listener",listener="client:customers",name=%q} 1`, wild)
		if !strings.Contains(m, want) {
			t.Errorf("the refused rotation is not counted\n%s", grepLines(m, "trove_tls_reload"))
		}
		if got, _, _ := served(def8080, "trove.example.com"); got != 1003 {
			t.Errorf("default listener changed: %d", got)
		}
	})
}
