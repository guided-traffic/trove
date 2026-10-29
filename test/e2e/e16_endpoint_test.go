//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// The second endpoint of the dev profile (deploy/dev/values-dev.yaml, endpoints.customers): its own
// listener :8443, its own host packages.trove.localhost and wildcard, its own certificates, only the
// package routes, its own Ingress on the same HAProxy Ingress (R-EP-001 to R-EP-005, R-EP-008, E0.16).

const (
	endpointHost     = "packages.trove.localhost"
	endpointWildcard = "a.packages.trove.localhost" // under *.packages.trove.localhost
	endpointService  = "trove-ep-customers"
)

// edgeClient reaches the edge on the host's published port whatever the host name in the URL is, and
// verifies the certificate against the dev CA for exactly that name (no resolver involved).
func edgeClient(t testing.TB) (*http.Client, string) {
	t.Helper()
	u, err := url.Parse(cfg.edgeURL)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.caFile == "" {
		t.Fatal("TROVE_E2E_CA_FILE is not set: the endpoint test needs the dev CA")
	}
	pem, err := os.ReadFile(cfg.caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	addr := net.JoinHostPort("127.0.0.1", u.Port())
	return &http.Client{
		Timeout: 10 * time.Second,
		// A 404 is the answer under test: never follow a redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, u.Port()
}

// edgeLeaf returns the leaf certificate the edge presents for an SNI name (verified against the dev CA).
func edgeLeaf(t testing.TB, port, sni string) *x509.Certificate {
	t.Helper()
	pem, err := os.ReadFile(cfg.caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort("127.0.0.1", port),
		&tls.Config{ServerName: sni, RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("TLS handshake with the edge for %s: %v", sni, err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

func TestE16Endpoint(t *testing.T) {
	t.Run("R-EP-004 R-HA-063 E0.16 the edge serves the endpoint's certificate for its host and for a host under its wildcard (SNI)", func(t *testing.T) {
		_, port := edgeClient(t)
		want := secretSerial(t, "trove-customers-edge-tls")
		for _, host := range []string{endpointHost, endpointWildcard} {
			leaf := edgeLeaf(t, port, host)
			got := strings.ToUpper(leaf.SerialNumber.Text(16))
			t.Logf("SNI %s: serial %s, names %v", host, got, leaf.DNSNames)
			if got != want {
				t.Errorf("SNI %s got serial %s, want the customers edge certificate %s", host, got, want)
			}
		}
		if other := strings.ToUpper(edgeLeaf(t, port, "localhost").SerialNumber.Text(16)); other != secretSerial(t, "trove-edge-tls") {
			t.Errorf("SNI localhost got serial %s, want the default endpoint's edge certificate", other)
		}
	})

	t.Run("R-EP-004 R-EP-005 E0.16 through the endpoint's Ingress /ui/, /api/v1/system/* and /files/ answer the 404 of an unknown path", func(t *testing.T) {
		c, port := edgeClient(t)
		for _, host := range []string{endpointHost, endpointWildcard} {
			for _, path := range []string{"/ui/", "/", "/api/v1/system/ping", "/api/v1/system/replicas", "/files/x", "/api/v1/nothing"} {
				req, _ := http.NewRequest(http.MethodGet, "https://"+net.JoinHostPort(host, port)+path, nil)
				resp, err := c.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", host, path, err)
				}
				resp.Body.Close()
				if resp.StatusCode != 404 {
					t.Errorf("GET https://%s%s = %d, want 404", host, path, resp.StatusCode)
				}
			}
		}
		// The default endpoint is unchanged next to it.
		req, _ := http.NewRequest(http.MethodGet, "https://"+net.JoinHostPort("localhost", port)+"/api/v1/system/ping", nil)
		resp, err := c.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("default endpoint ping through the edge: %v %v", resp, err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	})

	t.Run("R-EP-002 R-EP-003 R-EP-004 R-EP-005 E0.16 on the endpoint's listener a Host of the default endpoint answers 421, also with a forwarded host from an untrusted peer, and its pairs are chosen by SNI", func(t *testing.T) {
		// HAProxy Ingress routes by Host, so a request with the default endpoint's Host never reaches the
		// endpoint through its Ingress. The misdirected request is made where the endpoint is reachable
		// with any Host: its Service, from a pod that is no trusted proxy.
		svc := fmt.Sprintf("%s.%s.svc:8443", endpointService, cfg.namespace)
		script := fmt.Sprintf(`req() { printf "GET $3 HTTP/1.1\r\nHost: $1\r\n${2}Connection: close\r\n\r\n" | timeout 8 openssl s_client -quiet -connect %[1]s -servername %[2]s 2>/dev/null | head -1; }
sni() { echo | timeout 8 openssl s_client -connect %[1]s -servername "$1" 2>/dev/null | openssl x509 -noout -serial 2>/dev/null | cut -d= -f2; }
echo "A $(req localhost '' /api/v1/system/ping)"
echo "B $(req localhost 'X-Forwarded-Host: %[2]s\r\n' /ui/)"
echo "C $(req 10.1.2.3 '' /)"
echo "D $(req %[2]s '' /ui/)"
echo "E $(req %[3]s '' /api/v1/system/ping)"
echo "S1 $(sni %[2]s)"
echo "S2 $(sni %[3]s)"
`, svc, endpointHost, endpointWildcard)
		out := runProbePod(t, "e2e-endpoint-probe", script)
		t.Logf("from namespace default to https://%s:\n%s", svc, strings.TrimSpace(out))
		got := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			k, v, _ := strings.Cut(l, " ")
			got[k] = strings.TrimSpace(v)
		}
		for k, status := range map[string]string{"A": " 421", "B": " 421", "C": " 421", "D": " 404", "E": " 404"} {
			if !strings.Contains(got[k], status) {
				t.Errorf("probe %s: %q, want status%s", k, got[k], status)
			}
		}
		if want := secretSerial(t, "trove-customers-tls"); strings.TrimLeft(got["S1"], "0") != want {
			t.Errorf("SNI %s on the endpoint listener: serial %s, want the exact pair %s", endpointHost, got["S1"], want)
		}
		if want := secretSerial(t, "trove-customers-wildcard-tls"); strings.TrimLeft(got["S2"], "0") != want {
			t.Errorf("SNI %s on the endpoint listener: serial %s, want the wildcard pair %s", endpointWildcard, got["S2"], want)
		}
	})

	t.Run("R-EP-008 E0.16 trove_http_requests_total carries both endpoint values and the endpoint listener is up", func(t *testing.T) {
		seen := map[string]bool{}
		for _, p := range livePods(t, selWeb) {
			code, body := ops(p.Name, "/metrics")
			if code != 200 {
				t.Fatalf("%s :8081/metrics = %d", p.Name, code)
			}
			for _, l := range strings.Split(body, "\n") {
				if strings.HasPrefix(l, "trove_http_requests_total{") {
					for _, e := range []string{"default", "customers"} {
						if strings.Contains(l, `endpoint="`+e+`"`) {
							seen[e] = true
						}
					}
				}
				if strings.HasPrefix(l, `trove_endpoint_listener_up{endpoint="customers"}`) && !strings.HasSuffix(l, " 1") {
					t.Errorf("%s: %s", p.Name, l)
				}
			}
		}
		if !seen["default"] || !seen["customers"] {
			t.Errorf("trove_http_requests_total endpoint values seen: %v, want default and customers", seen)
		}
	})
}
