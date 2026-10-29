package httpserver

import (
	"crypto/tls"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

func TestListenerTLSReview2(t *testing.T) {
	t.Run("R-ARCH-035 SA8 client certificates without a CA file are refused at construction", func(t *testing.T) {
		dir := t.TempDir()
		ca := tlstest.NewCA(t, "ca")
		cert, key := ca.Issue(t, tlstest.Options{}).Files(t, dir, "srv")
		for _, mode := range []string{config.ClientAuthRequire, config.ClientAuthVerifyIfGiven} {
			if _, err := NewListenerTLS("client", config.ServerTLS{Certificates: pairOf(cert, key), ClientAuth: mode}); err == nil {
				t.Errorf("client_auth %s without client_ca_file was accepted", mode)
			}
		}
	})

	t.Run("R-ARCH-035 H-68 a listener with a temporary certificate answers until its pair loads, then serves the pair", func(t *testing.T) {
		dir := t.TempDir()
		ca := tlstest.NewCA(t, "ca")
		certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
		l, err := NewListenerTLS("ops", config.ServerTLS{Certificates: pairOf(certFile, keyFile), ReloadInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		l.UseTemporaryCertificate("pod-7", nil, nil)
		_, addr := start(t, ServerConfig{Handler: nil, TLS: l})
		peer := func(c *tls.Config) (*tls.Certificate, error) {
			conn, err := tls.Dial("tcp", addr, c)
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			return &tls.Certificate{Leaf: conn.ConnectionState().PeerCertificates[0]}, nil
		}
		got, err := peer(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a probe that does not verify
		if err != nil {
			t.Fatalf("an unverified handshake was refused: %v", err)
		}
		leaf := got.Leaf
		if leaf.Subject.CommonName != "pod-7" || time.Until(leaf.NotAfter) > 61*time.Minute || leaf.PublicKeyAlgorithm.String() != "ECDSA" {
			t.Errorf("temporary certificate = CN %q, NotAfter %s, key %s", leaf.Subject.CommonName, leaf.NotAfter, leaf.PublicKeyAlgorithm)
		}
		if !l.Temporary() {
			t.Error("Temporary() is false while the temporary certificate serves")
		}
		if _, err := peer(&tls.Config{RootCAs: ca.Pool()}); err == nil {
			t.Error("the temporary certificate verified against the configured CA")
		}
		if entries, _ := filepath.Glob(filepath.Join(dir, "*")); len(entries) != 0 {
			t.Errorf("the temporary certificate touched the disk: %v", entries)
		}

		ca.Issue(t, tlstest.Options{Serial: big.NewInt(4242)}).Write(t, certFile, keyFile)
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := peer(&tls.Config{RootCAs: ca.Pool(), ServerName: "localhost"})
			if err == nil && got.Leaf.SerialNumber.Int64() == 4242 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the configured pair is not served: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if l.Temporary() {
			t.Error("Temporary() is still true after the pair loaded")
		}
	})

	t.Run("R-ARCH-035 H-68 a listener without the temporary certificate refuses handshakes until its pair loads", func(t *testing.T) {
		dir := t.TempDir()
		l, err := NewListenerTLS("client", config.ServerTLS{Certificates: pairOf(filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")), ReloadInterval: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		_, addr := start(t, ServerConfig{TLS: l})
		if _, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}); err == nil { //nolint:gosec // a probe
			t.Fatal("a handshake succeeded without any certificate")
		}
	})
}

func TestHostOnly(t *testing.T) {
	t.Run("R-EP-003 SA4 a host with a non-ASCII or control byte is no host", func(t *testing.T) {
		for _, v := range []string{"\u212a.example.com", "é.example.com", "a\x00b.example.com", "a\tb.example.com", "a\x7f.example.com", "bücher.example", "ａ.example.com"} {
			if got := hostOnly(v); got != "" {
				t.Errorf("hostOnly(%q) = %q, want empty", v, got)
			}
		}
		for in, want := range map[string]string{"Packages.Example.COM": "packages.example.com", "packages.example.com:8443": "packages.example.com", "packages.example.com.": "packages.example.com", "[::1]:80": "::1"} {
			if got := hostOnly(in); got != want {
				t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

func TestSurfaceFamilies(t *testing.T) {
	t.Run("R-EP-005 SA9 a packages pattern that can match the API or the exchange fails the route-table rule", func(t *testing.T) {
		for _, p := range []string{
			"/api/{format}/{repoKey}/{rest...}", "GET /api/{format}/{repo}/", "/api/{rest...}",
			"/{x}/-/npm/v1/oidc/{rest...}", "POST /-/{a}/v1/oidc/{rest...}",
		} {
			if v := CheckRouteTable([]string{p}, nil); len(v) == 0 {
				t.Errorf("%q passed", p)
			}
		}
	})

	t.Run("R-EP-005 SA9 the planned package, file and API routes pass", func(t *testing.T) {
		ps := []string{
			"/api/npm/{repoKey}/{rest...}", "/api/pypi/{repoKey}/simple/{name}/", "/files/{repoKey}/{path...}",
			"GET /api/v1/system/ping", "POST /api/v1/auth/oidc/token", "POST /-/npm/v1/oidc/token/exchange/package/{name...}",
			"/api/maven/{repoKey}/{path...}",
		}
		if v := CheckRouteTable(ps, nil); len(v) != 0 {
			t.Fatalf("violations: %v", v)
		}
	})
}

func TestFallbackFamilies(t *testing.T) {
	t.Run("R-EP-005 SA9 a path of a family outside the surface answers the same 404 as an unknown path, never 405", func(t *testing.T) {
		ep, err := NewEndpoint(config.ResolvedEndpoint{Name: "customers", CheckHost: false, Surface: []string{config.SurfacePackages}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := NewEndpointRouter(ep)
		// A route of the served family that, for the method, shadows the API path.
		r.HandleFunc("POST /api/{format}/{repoKey}/{rest...}", func(http.ResponseWriter, *http.Request) {})
		h := Chain{Router: r}.Handler()
		get := func(method, path string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			return w
		}
		unknown := get("GET", "/nothing/here")
		for _, path := range []string{"/api/v1/system/ping", "/api/v1/a/b/c", "/files/x/y"} {
			w := get("GET", path)
			if w.Code != 404 || w.Header().Get("Allow") != "" {
				t.Errorf("%s: %d Allow=%q", path, w.Code, w.Header().Get("Allow"))
			}
			if p, u := problemOf(t, w), problemOf(t, unknown); p.Code != u.Code || p.Detail != u.Detail || p.Title != u.Title {
				t.Errorf("%s: %+v differs from the unknown path %+v", path, p, u)
			}
		}
		if w := get("GET", "/api/npm/repo/x"); w.Code != 405 || w.Header().Get("Allow") == "" {
			t.Errorf("a served family keeps its 405: %d", w.Code)
		}
	})
}
