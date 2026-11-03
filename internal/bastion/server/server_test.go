package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// syncBuffer is a log sink the server and the test may both touch.
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

// harness is a running Bastion replica on loopback.
type harness struct {
	srv  *Server
	logs *syncBuffer
	dir  string
}

func writeTokenFile(t *testing.T, dir string) string {
	t.Helper()
	raw, err := token.Generate(token.TypeBST)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(raw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, file string, env ...string) (*config.BastionLoaded, error) {
	t.Helper()
	return config.LoadBastion(config.Options{
		File: "/etc/trove/trove.yaml", Environ: env, Command: config.CommandServe,
		ReadFile: func(name string) ([]byte, error) {
			if name == "/etc/trove/trove.yaml" {
				return []byte(file), nil
			}
			return nil, os.ErrNotExist
		},
	})
}

// minimal is the configuration of the acceptance of R-BAS-001: the keys of the role, the main URL and the token
// file, listening on loopback with kernel-chosen ports.
func minimal(tokenFile string, extra string) string {
	return fmt.Sprintf(`
server:
  listen: 127.0.0.1:0
  ops_listen: localhost:0
  base_url: https://packages.example.com
%s
bastion:
  cache:
    volume:
      dir: %s
  main:
    url: https://trove.example.com
    token_file: %s
`, extra, filepath.Join(filepath.Dir(tokenFile), "cache"), tokenFile)
}

func start(t *testing.T, cfgYAML string) *harness {
	t.Helper()
	loaded, err := load(t, cfgYAML)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logs := &syncBuffer{}
	srv, err := New(Options{Loaded: loaded, Environ: []string{"TROVE_POD_NAME=trove-bastion-test"}, LogWriter: logs, DrainDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		srv.abort()
		t.Fatalf("Start: %v\n%s", err, logs.String())
	}
	t.Cleanup(func() {
		cancel()
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return &harness{srv: srv, logs: logs, dir: filepath.Dir(loaded.Config.Bastion.Main.TokenFile)}
}

func get(t *testing.T, url string, header ...string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "packages.example.com" // server.base_url of the configurations below
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func waitReady(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !h.srv.Health().Ready() {
		if time.Now().After(deadline) {
			t.Fatalf("not ready:\n%s", h.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestR_BAS_001_TheRoleServesItsTwoListenersAndAnswersWithItsOwnProblems: with the keys of the acceptance the
// role starts, becomes ready, and the client listener answers as the engine does: 404 bastion_not_served as
// problem details to the paths the spec names, 421 to another host, and 503 main_unreachable to a read that
// needs a main installation that is not reachable (R-BAS-001, R-BAS-003, R-BAS-027).
func TestR_BAS_001_TheRoleServesItsTwoListenersAndAnswersWithItsOwnProblems(t *testing.T) {
	h := start(t, minimal(writeTokenFile(t, t.TempDir()), ""))
	waitReady(t, h)
	ops, client := "http://"+h.srv.OpsAddr().String(), "http://"+h.srv.ClientAddr().String()

	for _, p := range []string{"/healthz", "/readyz"} {
		if code, _, body := get(t, ops+p); code != 200 {
			t.Errorf("%s = %d %s", p, code, body)
		}
	}
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/api/v1/system/ping": {404, iface.CodeNotServed}, "/ui/": {404, iface.CodeNotServed}, "/auth/oidc/corp/start": {404, iface.CodeNotServed},
		"/": {404, iface.CodeNotServed}, "/v2/": {404, iface.CodeNotServed}, "/.well-known/x": {404, iface.CodeNotServed},
		"/api/npm/npm-all/lodash": {503, "main_unreachable"}, "/files/raw/x": {503, "main_unreachable"},
	} {
		code, header, body := get(t, client+path)
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal([]byte(body), &p)
		if code != want.status || p.Code != want.code || !strings.HasPrefix(header.Get("Content-Type"), "application/problem+json") {
			t.Errorf("GET %s = %d %s %s", path, code, header.Get("Content-Type"), body)
		}
		if header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s: Cache-Control %q", path, header.Get("Cache-Control"))
		}
	}
	// Another host is not this Bastion's (R-BAS-018 step 1).
	req, _ := http.NewRequest(http.MethodGet, client+"/api/npm/npm-all/lodash", nil)
	req.Host = "other.example.com"
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("another host = %d", resp.StatusCode)
		}
	}
	// A write goes to the probe, which needs the main installation (R-BAS-014).
	req, _ = http.NewRequest(http.MethodPut, client+"/api/npm/npm-all/x", strings.NewReader("{}"))
	req.Host = "packages.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("PUT = %d", resp.StatusCode)
	}
}

// TestR_BAS_001_TheOpsListenerExportsTheBastionMetricsFromTheStart: /metrics holds every metric of the table of
// R-BAS-040 at start, the constants of the configuration, and the interface version of the Bastion.
func TestR_BAS_001_TheOpsListenerExportsTheBastionMetricsFromTheStart(t *testing.T) {
	h := start(t, minimal(writeTokenFile(t, t.TempDir()), ""))
	waitReady(t, h)
	_, _, body := get(t, "http://"+h.srv.OpsAddr().String()+"/metrics")
	for _, want := range []string{
		"trove_bastion_requests_total", "trove_bastion_main_reachable 0", "trove_bastion_answer_period_seconds 300",
		"trove_bastion_grace_period_seconds 900", "trove_bastion_token_warn_before_seconds 2.592e+06",
		`trove_bastion_interface_version{side="bastion"} 1`, `trove_bastion_token_reloads_total{outcome="loaded"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	// A client request leaves its mark in the Bastion's own counters.
	get(t, "http://"+h.srv.ClientAddr().String()+"/api/v1/system/ping")
	_, _, body = get(t, "http://"+h.srv.OpsAddr().String()+"/metrics")
	if !strings.Contains(body, `trove_bastion_refused_total{reason="not_served"} 1`) {
		t.Errorf("not_served not counted:\n%s", grepLines(body, "trove_bastion_refused_total"))
	}
}

// TestR_BAS_006_AMalformedTokenFileStopsStartup: at start the token file is read with R-AUTH-022's syntax and
// checksum; a defect stops startup, names the file and never shows the content.
func TestR_BAS_006_AMalformedTokenFileStopsStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	secret := "trv_bst_" + strings.Repeat("A", 49)
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := load(t, minimal(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(Options{Loaded: loaded, LogWriter: &syncBuffer{}})
	if err == nil || !strings.Contains(err.Error(), "bastion.main.token_file") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error shows the token: %v", err)
	}
	loaded, _ = load(t, minimal(filepath.Join(dir, "missing"), ""))
	if _, err = New(Options{Loaded: loaded, LogWriter: &syncBuffer{}}); err == nil {
		t.Fatal("a missing token file did not stop startup")
	}
	// The same token written in a wrong prefix is refused too.
	if err := os.WriteFile(path, []byte(strings.Replace(secret, "bst", "pat", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = New(Options{Loaded: loaded, LogWriter: &syncBuffer{}}); err == nil {
		t.Fatal("a token of another type did not stop startup")
	}
}

// TestR_BAS_032_ATooShortSigningKeyStopsStartup: the key of the signed entries is at least 32 bytes.
func TestR_BAS_032_ATooShortSigningKeyStopsStartup(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := minimal(writeTokenFile(t, dir), "") + fmt.Sprintf("  signing_key_file: %s\n", keyFile)
	loaded, err := load(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Loaded: loaded, LogWriter: &syncBuffer{}}); err == nil || !strings.Contains(err.Error(), "signing_key_file") {
		t.Fatalf("error = %v", err)
	}
}

// TestR_BAS_044_TLSMaterialIsServedAndReloadedWithoutARestart: the client and the ops listener serve TLS from
// server.tls, and a rotated pair serves the next handshake (R-BAS-044, R-ARCH-035).
func TestR_BAS_044_TLSMaterialIsServedAndReloadedWithoutARestart(t *testing.T) {
	ca := tlstest.NewCA(t, "bastion test ca")
	dir := t.TempDir()
	certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(11)}).Files(t, dir, "tls")
	tlsYAML := fmt.Sprintf("  tls:\n    reload_interval: 1s\n    certificates:\n      - cert_file: %s\n        key_file: %s\n", certFile, keyFile)
	h := start(t, minimal(writeTokenFile(t, dir), tlsYAML))
	waitReady(t, h)

	serial := func(addr string) int64 {
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: ca.Pool(), MinVersion: tls.VersionTLS12},
		}}
		resp, err := c.Get("https://" + addr + "/healthz")
		if err != nil {
			t.Fatalf("https://%s: %v", addr, err)
		}
		defer resp.Body.Close()
		return resp.TLS.PeerCertificates[0].SerialNumber.Int64()
	}
	if s := serial(h.srv.OpsAddr().String()); s != 11 {
		t.Fatalf("ops serial = %d", s)
	}
	if s := serial(h.srv.ClientAddr().String()); s != 11 {
		t.Fatalf("client serial = %d", s)
	}
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + h.srv.ClientAddr().String() + "/"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 404 && resp.Header.Get("X-Request-Id") != "" {
			t.Error("plaintext reached the TLS client listener")
		}
	}
	ca.Issue(t, tlstest.Options{Serial: big.NewInt(12)}).Write(t, certFile, keyFile)
	deadline := time.Now().Add(10 * time.Second)
	for serial(h.srv.ClientAddr().String()) != 12 {
		if time.Now().After(deadline) {
			t.Fatal("the rotated certificate is not served")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s := serial(h.srv.OpsAddr().String()); s != 12 {
		t.Errorf("ops serial after rotation = %d", s)
	}
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
