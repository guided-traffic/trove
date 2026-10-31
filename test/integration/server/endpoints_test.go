//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// with returns a copy of the installation with more settings.
func (in *install) with(env ...string) *install {
	c := *in
	c.env = append(append([]string(nil), in.env...), env...)
	return &c
}

// endpointClient talks to one endpoint listener over TLS: it dials addr whatever the URL says,
// sends the server name sni and verifies the certificate against the test CA.
func endpointClient(ca *tlstest.CA, addr, sni string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: ca.Pool(), ServerName: sni, MinVersion: tls.VersionTLS12},
		},
	}
}

func endpointGet(t *testing.T, ca *tlstest.CA, addr, sni, host, path string, hdr ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://"+host+path, nil)
	req.Host = host
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := endpointClient(ca, addr, sni).Do(req)
	if err != nil {
		t.Fatalf("GET %s%s via %s: %v", host, path, addr, err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

// served returns the serial of the certificate the listener presents for a server name.
func served(t *testing.T, ca *tlstest.CA, addr, sni string) int64 {
	t.Helper()
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr,
		&tls.Config{RootCAs: ca.Pool(), ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // reads which certificate arrives, not whether it verifies
	if err != nil {
		t.Fatalf("handshake with %s (SNI %q): %v", addr, sni, err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

func TestEndpoints(t *testing.T) {
	in := newInstall(t)
	if code, out, errOut := in.run(t, "migrate", "up"); code != 0 {
		t.Fatalf("migrate: %d\n%s\n%s", code, out, errOut)
	}

	ca := tlstest.NewCA(t, "endpoint integration ca")
	dir := t.TempDir()
	exact, exactKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"packages.trove.localhost", "pkgs.example.org"}, Serial: big.NewInt(5001)}).Files(t, dir, "exact")
	wild, wildKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"*.registry.example.com"}, Serial: big.NewInt(5002)}).Files(t, dir, "wild")
	// The install enables the bootstrap anonymous read, whose guard accepts only loopback
	// origins; a name below .localhost (RFC 6761) is one.
	custAddr := freeAddr(t)
	endpoints := fmt.Sprintf(`{"customers":{"listen":%q,"base_url":"https://packages.trove.localhost","hosts":["pkgs.example.org"],`+
		`"wildcard_host":"*.registry.example.com","host_roots":{"pkgs.example.org":"docker-hub"},`+
		`"tls":{"certificates":[{"cert_file":%q,"key_file":%q},{"cert_file":%q,"key_file":%q}]}}}`,
		custAddr, exact, exactKey, wild, wildKey)
	web := in.start(t, "web", "trove-ep-it", "TROVE_SERVER_ENDPOINTS="+endpoints, "TROVE_SERVER_TLS_RELOAD_INTERVAL=1s")

	requestRecord := func(match func(map[string]any) bool) map[string]any {
		var rec map[string]any
		for end := time.Now().Add(3 * time.Second); rec == nil && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			for _, m := range jsonLines(web.logs.String()) {
				if m["log"] == "request" && match(m) {
					rec = m
				}
			}
		}
		if rec == nil {
			t.Fatalf("no matching request record\n%s", web.logs.String())
		}
		return rec
	}

	// refused is the answer to a request without a credential: customers admits no
	// anonymous request (R-EP-010 item 4) and anonymous access is off on the default
	// endpoint (R-AUTH-037). It comes after the host check, so it also shows that
	// the check passed where a foreign host would get 421; the routes behind the
	// authentication stage are covered with credentials by the endpoint tests of M1.
	const refused = 401

	t.Run("R-EP-002 one replica opens the default listener and the customers listener, both before it is ready", func(t *testing.T) {
		if code, _, body := web.get(t, "/api/v1/system/ping"); code != 200 || body != "OK" {
			t.Fatalf("default ping: %d %q", code, body)
		}
		if code, body := endpointGet(t, ca, custAddr, "packages.trove.localhost", "packages.trove.localhost", "/does-not-exist"); code != refused {
			t.Fatalf("customers unknown path: %d %s", code, body)
		}
		_, metrics := opsGet(web.ops, "/metrics")
		for _, want := range []string{`trove_endpoint_listener_up{endpoint="default"} 1`, `trove_endpoint_listener_up{endpoint="customers"} 1`} {
			if !strings.Contains(metrics, want) {
				t.Errorf("metrics lack %s", want)
			}
		}
	})

	t.Run("R-EP-003 a request on the second listener with the host of the default endpoint answers 421 and is counted", func(t *testing.T) {
		code, body := endpointGet(t, ca, custAddr, "packages.trove.localhost", "trove.test", "/api/npm/npm-all/")
		var p map[string]any
		_ = json.Unmarshal([]byte(body), &p)
		if code != 421 || p["code"] != "misdirected_request" {
			t.Fatalf("%d %s", code, body)
		}
		for _, host := range []string{"10.0.0.5", "registry.example.com", "a.b.registry.example.com"} {
			if code, _ := endpointGet(t, ca, custAddr, "packages.trove.localhost", host, "/api/npm/npm-all/"); code != 421 {
				t.Errorf("Host %s: %d", host, code)
			}
		}
		// 127.0.0.0/8 is a trusted proxy here: a forwarded host within the set is honoured, one outside it is ignored.
		if code, _ := endpointGet(t, ca, custAddr, "packages.trove.localhost", "evil.example", "/does-not-exist", "X-Forwarded-Host", "packages.trove.localhost"); code != refused {
			t.Errorf("trusted proxy, forwarded host in the set: %d (401 means the host check passed)", code)
		}
		if code, _ := endpointGet(t, ca, custAddr, "packages.trove.localhost", "packages.trove.localhost", "/does-not-exist", "X-Forwarded-Host", "evil.example"); code != refused {
			t.Errorf("trusted proxy, forwarded host outside the set: %d", code)
		}
		if code, _ := endpointGet(t, ca, custAddr, "packages.trove.localhost", "docker-all.registry.example.com", "/does-not-exist"); code != refused {
			t.Errorf("wildcard host: %d", code)
		}
		_, metrics := opsGet(web.ops, "/metrics")
		if !strings.Contains(metrics, `trove_endpoint_misdirected_total{endpoint="customers"} 4`) ||
			!strings.Contains(metrics, `trove_endpoint_forwarded_host_ignored_total{endpoint="customers"} 1`) {
			t.Errorf("host-check counters:\n%s", grepLines(metrics, "trove_endpoint_"))
		}
	})

	t.Run("R-EP-002 R-EP-009 the endpoint of the log and the metrics is the listener's, whatever Host and forwarded host claim", func(t *testing.T) {
		const tid = "2af7651916cd43dd8448eb211c80319e"
		code, _, _ := web.get(t, "/api/v1/system/ping", "traceparent", "00-"+tid+"-b7ad6b7169203331-01", "X-Forwarded-Host", "packages.trove.localhost")
		if code != 200 {
			t.Fatalf("ping: %d", code)
		}
		rec := requestRecord(func(m map[string]any) bool { return m["trace_id"] == tid })
		if rec["endpoint"] != "default" || rec["listener"] != "client" {
			t.Errorf("default record = %v", rec)
		}
		mis := requestRecord(func(m map[string]any) bool {
			return m["endpoint"] == "customers" && m["status"] == float64(421) && m["host"] == "trove.test"
		})
		if mis["listener"] != "client" {
			t.Errorf("421 record = %v", mis)
		}
		own := requestRecord(func(m map[string]any) bool {
			return m["endpoint"] == "customers" && m["status"] == float64(refused) && m["host"] == "docker-all.registry.example.com"
		})
		if own["handler"] != "unmatched" {
			t.Errorf("record = %v", own)
		}
		_, metrics := opsGet(web.ops, "/metrics")
		for _, want := range []string{
			`trove_http_requests_total{code="200",endpoint="default",handler="GET /api/v1/system/ping",listener="client",method="GET"}`,
			// the 421s of R-EP-003 asked for /api/npm/npm-all/, which the npm format's prefix routes on every endpoint
			`trove_http_requests_total{code="421",endpoint="customers",handler="/api/npm/{repoKey}/{rest...}",listener="client",method="GET"}`,
		} {
			if !strings.Contains(metrics, want) {
				t.Errorf("metrics lack %s\n%s", want, grepLines(metrics, "trove_http_requests_total"))
			}
		}
		if strings.Contains(metrics, `listener="ops",endpoint="`) || strings.Contains(metrics, `endpoint="customers",handler="GET /readyz"`) {
			t.Error("an ops-listener series carries an endpoint")
		}
	})

	t.Run("R-EP-005 R-EP-010 the customers endpoint answers a request without a credential with the same 401 on every path, the UI, the API, the management API and the reserved /v2/ and /.well-known/ included", func(t *testing.T) {
		var want string
		for _, p := range []string{"/does-not-exist", "/ui/", "/", "/auth/oidc/corp/start", "/api/v1/system/ping", "/api/v1/system/info", "/api/v1/system/replicas", "/api/v1/openapi.json", "/files/generic-local/x", "/v2/", "/v2/docker-all/manifests/latest", "/.well-known/terraform.json"} {
			code, body := endpointGet(t, ca, custAddr, "packages.trove.localhost", "packages.trove.localhost", p)
			var pr map[string]any
			_ = json.Unmarshal([]byte(body), &pr)
			delete(pr, "instance")
			delete(pr, "requestId")
			b, _ := json.Marshal(pr)
			if code != refused || pr["code"] != "credentials_required" {
				t.Errorf("%s: %d %s", p, code, body)
			}
			if want == "" {
				want = string(b)
			} else if string(b) != want {
				t.Errorf("%s: body %s differs from the unknown path's %s", p, b, want)
			}
		}
		// The default endpoint serves them all.
		for p, wantCode := range map[string]int{"/api/v1/system/ping": 200, "/api/v1/system/info": 200} {
			if code, _, _ := web.get(t, p); code != wantCode {
				t.Errorf("default %s: %d", p, code)
			}
		}
		if code, _, _ := web.get(t, "/", "Accept", "text/html"); code != 302 && code != 200 {
			t.Errorf("default root: %d", code)
		}
		if code, _, _ := web.get(t, "/v2/"); code != refused {
			t.Errorf("default /v2/: %d", code)
		}
	})

	t.Run("R-EP-006 a host-root binding is accepted and reserved: bound-host paths route like any other host", func(t *testing.T) {
		for _, p := range []string{"/v2/", "/.well-known/terraform.json"} {
			if code, _ := endpointGet(t, ca, custAddr, "pkgs.example.org", "pkgs.example.org", p); code != refused {
				t.Errorf("bound host %s: %d", p, code)
			}
		}
		// A binding to a host outside the endpoint stops startup and names the key.
		bad := in.with(`TROVE_SERVER_ENDPOINTS={"customers":{"listen":":0","base_url":"https://packages.trove.localhost","host_roots":{"other.example.com":"docker-hub"}}}`)
		code, _, stderr := bad.run(t, "admin", "config", "validate")
		if code == 0 || !strings.Contains(stderr+"", "server.endpoints.customers.host_roots") {
			t.Errorf("exit %d, stderr %s", code, stderr)
		}
	})

	t.Run("R-EP-004 SNI picks the endpoint's certificate incl. the wildcard, the default listener never serves it", func(t *testing.T) {
		for sni, want := range map[string]int64{
			"packages.trove.localhost": 5001, "docker-all.registry.example.com": 5002, "other.example": 5001, "": 5001,
		} {
			if got := served(t, ca, custAddr, sni); got != want {
				t.Errorf("SNI %q: serial %d, want %d", sni, got, want)
			}
		}
		if c, err := tls.Dial("tcp", web.client, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}); err == nil { //nolint:gosec // the default listener is plain HTTP here
			c.Close()
			t.Error("the default listener, configured without server.tls, completed a TLS handshake")
		}
	})

	t.Run("R-EP-004 rotating the wildcard pair of the second listener is served within the reload interval, a broken rotation keeps the old pair", func(t *testing.T) {
		tlstest.WriteFile(t, wildKey, ca.Issue(t, tlstest.Options{}).KeyPEM) // a key of another certificate
		time.Sleep(2500 * time.Millisecond)
		if got := served(t, ca, custAddr, "docker-all.registry.example.com"); got != 5002 {
			t.Fatalf("after a broken rotation the listener serves %d", got)
		}
		_, metrics := opsGet(web.ops, "/metrics")
		want := fmt.Sprintf(`trove_tls_reload_errors_total{kind="listener",listener="client:customers",name=%q} 1`, wild)
		if !strings.Contains(metrics, want) {
			t.Errorf("the refused rotation is not counted\n%s", grepLines(metrics, "trove_tls_reload"))
		}
		started := time.Now()
		ca.Issue(t, tlstest.Options{DNSNames: []string{"*.registry.example.com"}, Serial: big.NewInt(5003)}).Write(t, wild, wildKey)
		for served(t, ca, custAddr, "docker-all.registry.example.com") != 5003 {
			if time.Since(started) > 3500*time.Millisecond { // reload_interval + 1 s + slack for the handshake
				t.Fatal("the rotated pair is not served within reload_interval + 1 s")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if code, _ := endpointGet(t, ca, custAddr, "docker-all.registry.example.com", "docker-all.registry.example.com", "/does-not-exist"); code != refused {
			t.Errorf("request after the rotation: %d", code)
		}
		if got := served(t, ca, custAddr, "packages.trove.localhost"); got != 5001 {
			t.Errorf("the other pair changed: %d", got)
		}
		if web.cmd.ProcessState != nil {
			t.Error("the process exited during the rotation")
		}
	})

	t.Run("R-EP-002 a worker opens no endpoint listener", func(t *testing.T) {
		wAddr := freeAddr(t)
		ep := fmt.Sprintf(`{"customers":{"listen":%q,"base_url":"https://packages.trove.localhost"}}`, wAddr)
		in.start(t, "worker", "trove-ep-worker-it", "TROVE_SERVER_ENDPOINTS="+ep)
		if c, err := net.DialTimeout("tcp", wAddr, 300*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("a worker listens on an endpoint address")
		}
	})
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
