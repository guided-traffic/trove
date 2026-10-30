package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/npmstub"
)

// seen is what a test upstream recorded.
type seen struct {
	mu   sync.Mutex
	reqs []seenRequest
}

type seenRequest struct {
	host, path, auth string
}

func (s *seen) add(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, seenRequest{host: r.Host, path: r.URL.Path, auth: r.Header.Get("Authorization")})
}

func (s *seen) all() []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seenRequest(nil), s.reqs...)
}

func (s *seen) last(t *testing.T) seenRequest {
	t.Helper()
	all := s.all()
	if len(all) == 0 {
		t.Fatal("the upstream got no request")
	}
	return all[len(all)-1]
}

// stubRegistry is a TLS upstream that answers the probe resource of the npmlike
// format and records every request.
func stubRegistry(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.add(r)
		switch r.URL.Path {
		case "/-/ping", "/":
			_, _ = io.WriteString(w, "{}")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// factoryTrusting returns an outbound factory that trusts the certificates of
// the servers and allows loopback, as the integration environment does.
func factoryTrusting(t *testing.T, servers ...*httptest.Server) *outbound.Factory {
	t.Helper()
	cfg := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
	for _, k := range config.MustDescribe().Keys {
		if k.Path == "proxy.outbound.address_policy.deny_cidrs" {
			if err := json.Unmarshal([]byte(k.Default), &cfg.AddressPolicy.DenyCIDRs); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8", "::1/128"}
	dir := t.TempDir()
	for i, srv := range servers {
		p := filepath.Join(dir, "ca"+strconv.Itoa(i)+".pem")
		b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.TLS.CAFiles = append(cfg.TLS.CAFiles, p)
	}
	return outbound.New(cfg)
}

func keyRing(t *testing.T, active string, ids ...string) *secrets.KeyRing {
	t.Helper()
	var files []string
	for _, id := range ids {
		p := filepath.Join(t.TempDir(), id+".key")
		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(len(id)*7 + i)
		}
		if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	r, err := secrets.Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// store is the stored secrets of the test repositories.
type store struct {
	mu   sync.Mutex
	rows map[int64][]repository.StoredSecret
	n    atomic.Int64
}

func (s *store) Secrets(_ context.Context, id int64) ([]repository.StoredSecret, error) {
	s.n.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]repository.StoredSecret(nil), s.rows[id]...), nil
}

func (s *store) set(id int64, rows ...repository.StoredSecret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[int64][]repository.StoredSecret{}
	}
	s.rows[id] = rows
}

func testCatalog() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npmlike", Classes: []catalog.Class{catalog.Local, catalog.Remote}, Layout: "npmlike", ProtocolBasePath: "/api/npmlike/{key}/",
		PatternSubject: catalog.SubjectPath, ProbePath: "-/ping",
	}})
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "indexfmt", Classes: []catalog.Class{catalog.Remote}, Layout: "indexfmt", ProtocolBasePath: "/api/indexfmt/{key}/",
		PatternSubject: catalog.SubjectPath,
	}})
	return r
}

type fixture struct {
	t       *testing.T
	clients *Clients
	store   *store
	ring    *secrets.KeyRing
	caches  *cache.Registry
	dir     string
	env     map[string]string
	envMu   sync.Mutex
}

type fixtureOpts struct {
	servers    []*httptest.Server
	proxy      config.Proxy
	interfaces func() ([]netip.Addr, error)
	now        func() time.Time
}

func newFixture(t *testing.T, o fixtureOpts) *fixture {
	t.Helper()
	f := &fixture{t: t, store: &store{}, ring: keyRing(t, "2026-10", "2026-10"), dir: t.TempDir(), env: map[string]string{}}
	var err error
	f.caches, err = cache.NewRegistry(nil, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if o.proxy.MaxRedirects == 0 {
		o.proxy.MaxRedirects = 5
	}
	f.clients, err = New(Options{
		Factory: factoryTrusting(t, o.servers...), Store: f.store, Ring: f.ring, Caches: f.caches, Catalog: testCatalog(),
		Proxy: o.proxy, SecretsDir: f.dir, SafetyTTL: time.Minute, Interfaces: o.interfaces, Now: o.now,
		LookupEnv: func(k string) (string, bool) {
			f.envMu.Lock()
			defer f.envMu.Unlock()
			v, ok := f.env[k]
			return v, ok
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) setEnv(k, v string) {
	f.envMu.Lock()
	defer f.envMu.Unlock()
	f.env[k] = v
}

func (f *fixture) writeSecretFile(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// seal stores a sealed password row for the repository.
func (f *fixture) seal(id int64, value string) {
	f.t.Helper()
	sealed, err := repository.SealSecret(f.ring, id, "/remote/password", observability.Secret(value))
	if err != nil {
		f.t.Fatal(err)
	}
	f.store.set(id, repository.StoredSecret{Field: "/remote/password", Sealed: sealed})
}

func settings(rawURL, mode string) repository.RemoteSettings {
	s := repository.RemoteSettings{
		URL: rawURL, ExtraHosts: []repository.ExtraHost{}, AuthMode: mode, Headers: []repository.Header{},
		OutboundProxy: repository.OutboundProxy{Mode: repository.ProxyDefault}, ConnectTimeoutMs: 5000, ReadTimeoutMs: 5000,
		CacheFillAction: repository.FillRead, Availability: repository.Availability{Mode: repository.AvailabilityOnline},
	}
	if mode != repository.AuthNone {
		s.Password = &repository.SecretField{Set: true}
	}
	return s
}

func repo(id int64, key string, version int64) repository.Repository {
	return repository.Repository{ID: strconv.FormatInt(id, 10), Key: key, Class: repository.Remote, Format: "npmlike", Version: version}
}

func get(t *testing.T, c *Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.HTTP().Do(req)
}

// getErr sends a GET and returns only the error, closing any response.
func getErr(t *testing.T, c *Client, rawURL string) error {
	t.Helper()
	resp, err := get(t, c, rawURL)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

func mustGet(t *testing.T, c *Client, rawURL string) {
	t.Helper()
	resp, err := get(t, c, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestCredentials(t *testing.T) {
	srv, got := stubRegistry(t)
	f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}})

	t.Run("R-REPO-033 basic sends Authorization: Basic with the username and the unsealed password on every request", func(t *testing.T) {
		f.seal(11, "p4ss")
		s := settings(srv.URL+"/", repository.AuthBasic)
		s.Username = "ci-bot"
		cl, err := f.clients.For(t.Context(), repo(11, "npm-partner", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		mustGet(t, cl, srv.URL+"/-/ping")
		mustGet(t, cl, srv.URL+"/left-pad")
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ci-bot:p4ss"))
		for _, r := range got.all() {
			if r.auth != want {
				t.Errorf("request %s carried %q, want %q", r.path, r.auth, want)
			}
		}
		if len(got.all()) < 2 {
			t.Fatalf("requests = %+v", got.all())
		}
	})

	t.Run("R-REPO-033 bearer sends Authorization: Bearer with the token and ignores the username", func(t *testing.T) {
		f.seal(12, "tok-123")
		s := settings(srv.URL+"/", repository.AuthBearer)
		s.Username = "ignored"
		cl, err := f.clients.For(t.Context(), repo(12, "npm-bearer", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer tok-123" {
			t.Fatalf("Authorization = %q", r.auth)
		}
	})

	t.Run("R-REPO-033 none sends no credentials and reads no secret", func(t *testing.T) {
		before := f.store.n.Load()
		cl, err := f.clients.For(t.Context(), repo(13, "npm-public", 1), settings(srv.URL+"/", repository.AuthNone))
		if err != nil {
			t.Fatal(err)
		}
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "" {
			t.Fatalf("Authorization = %q", r.auth)
		}
		if f.store.n.Load() != before {
			t.Fatal("a repository without credentials read the secrets table")
		}
	})

	t.Run("R-REPO-033 a request never carries an Authorization header of its maker", func(t *testing.T) {
		cl, err := f.clients.For(t.Context(), repo(14, "npm-public2", 1), settings(srv.URL+"/", repository.AuthNone))
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/-/ping", nil)
		req.Header.Set("Authorization", "Bearer from-the-caller")
		resp, err := cl.HTTP().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if r := got.last(t); r.auth != "" {
			t.Fatalf("the caller's Authorization reached the upstream: %q", r.auth)
		}
	})
}

func TestSecretRefs(t *testing.T) {
	srv, got := stubRegistry(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}, now: func() time.Time { return time.Unix(0, clock.Load()) }})
	advance := func(d time.Duration) { clock.Add(int64(d)) }
	ref := func(r repository.SecretRef) {
		f.store.set(21, repository.StoredSecret{Field: "/remote/password", Ref: &r})
	}
	s := settings(srv.URL+"/", repository.AuthBearer)

	t.Run("R-REPO-033 a file reference is read under secrets_dir and a trailing line break is dropped", func(t *testing.T) {
		f.writeSecretFile("npm-token", "file-token\n")
		ref(repository.SecretRef{File: "npm-token"})
		cl, err := f.clients.For(t.Context(), repo(21, "npm-ref", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer file-token" {
			t.Fatalf("Authorization = %q", r.auth)
		}
	})

	t.Run("R-REPO-033 a changed file takes effect within the refresh interval, without a new client", func(t *testing.T) {
		cl, err := f.clients.For(t.Context(), repo(21, "npm-ref", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		f.writeSecretFile("npm-token", "rotated-token\n")
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer file-token" {
			t.Fatalf("the file was read again before the interval: %q", r.auth)
		}
		advance(RefreshInterval + time.Second)
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer rotated-token" {
			t.Fatalf("Authorization = %q", r.auth)
		}
		if again, _ := f.clients.For(t.Context(), repo(21, "npm-ref", 1), s); again != cl {
			t.Fatal("a rotated file built a new client")
		}
	})

	t.Run("R-REPO-033 a file that vanishes fails the request with upstream_secret_unavailable and sends nothing", func(t *testing.T) {
		cl, err := f.clients.For(t.Context(), repo(21, "npm-ref", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(f.dir, "npm-token")); err != nil {
			t.Fatal(err)
		}
		advance(RefreshInterval + time.Second)
		before := len(got.all())
		err = getErr(t, cl, srv.URL+"/-/ping")
		e, ok := errs.As(err)
		if !ok || e.Code != errs.CodeUpstreamSecretUnavailable || e.Status() != 502 {
			t.Fatalf("err = %v", err)
		}
		if len(got.all()) != before {
			t.Fatal("a request went out without its credential")
		}
		if strings.Contains(e.Detail, f.dir) || strings.Contains(e.Detail, "npm-token") {
			t.Fatalf("the detail names the file: %s", e.Detail)
		}
		f.writeSecretFile("npm-token", "back\n")
		advance(RetryInterval + time.Second)
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer back" {
			t.Fatalf("Authorization = %q", r.auth)
		}
	})

	t.Run("R-REPO-033 a TROVE_CAC_ variable is read; any other name is refused even in a stored row", func(t *testing.T) {
		f.setEnv("TROVE_CAC_NPM_TOKEN", "env-token")
		f.setEnv("TROVE_DATABASE_PASSWORD", "db-secret")
		ref(repository.SecretRef{Env: "TROVE_CAC_NPM_TOKEN"})
		cl, err := f.clients.For(t.Context(), repo(21, "npm-env", 2), s)
		if err != nil {
			t.Fatal(err)
		}
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer env-token" {
			t.Fatalf("Authorization = %q", r.auth)
		}
		ref(repository.SecretRef{Env: "TROVE_DATABASE_PASSWORD"})
		_, err = f.clients.For(t.Context(), repo(21, "npm-env", 3), s)
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamSecretUnavailable {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-REPO-033 a file name that leaves secrets_dir is refused even in a stored row", func(t *testing.T) {
		outside := filepath.Join(filepath.Dir(f.dir), "outside-secret")
		if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i, name := range []string{"../outside-secret", filepath.Join(f.dir, "npm-token"), "a/b"} {
			ref(repository.SecretRef{File: name})
			_, err := f.clients.For(t.Context(), repo(21, "npm-path", int64(10+i)), s)
			if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamSecretUnavailable {
				t.Errorf("%q: err = %v", name, err)
			}
		}
	})
}

func TestSealedSecrets(t *testing.T) {
	srv, got := stubRegistry(t)
	f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}})
	s := settings(srv.URL+"/", repository.AuthBearer)

	t.Run("R-REPO-033 a sealed value copied to the row of another repository fails with upstream_secret_unavailable", func(t *testing.T) {
		f.seal(31, "partner-token")
		rows, _ := f.store.Secrets(t.Context(), 31)
		f.store.set(32, rows...) // the value of repository 31, in the row of repository 32
		_, err := f.clients.For(t.Context(), repo(32, "npm-other", 1), s)
		e, ok := errs.As(err)
		if !ok || e.Code != errs.CodeUpstreamSecretUnavailable || e.Status() != 502 {
			t.Fatalf("err = %v", err)
		}
		// The original keeps working: other repositories are unaffected.
		cl, err := f.clients.For(t.Context(), repo(31, "npm-partner", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		mustGet(t, cl, srv.URL+"/-/ping")
		if r := got.last(t); r.auth != "Bearer partner-token" {
			t.Fatalf("Authorization = %q", r.auth)
		}
	})

	t.Run("R-REPO-033 a sealed value moved to another field does not open", func(t *testing.T) {
		sealed, err := repository.SealSecret(f.ring, 33, "/remote/password", "x")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.OpenSecret(f.ring, 33, "/remote/headers/X-Api-Key", sealed); err == nil {
			t.Fatal("opened under another field")
		}
	})

	t.Run("R-REPO-033 a missing row, an unknown key and a damaged value are upstream_secret_unavailable", func(t *testing.T) {
		f.store.set(34)
		f.store.set(35, repository.StoredSecret{Field: "/remote/password", Sealed: "v1:retired-key:AAAA"})
		f.store.set(36, repository.StoredSecret{Field: "/remote/password", Sealed: "not sealed"})
		for id := int64(34); id <= 36; id++ {
			_, err := f.clients.For(t.Context(), repo(id, "npm-broken-"+strconv.FormatInt(id, 10), 1), s)
			if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamSecretUnavailable {
				t.Errorf("id %d: err = %v", id, err)
			}
		}
	})

	t.Run("R-REPO-033 the refusal is remembered briefly: a flood of requests costs one read", func(t *testing.T) {
		f.store.set(37)
		before := f.store.n.Load()
		for range 20 {
			_, _ = f.clients.For(t.Context(), repo(37, "npm-flood", 1), s)
		}
		if n := f.store.n.Load() - before; n != 1 {
			t.Fatalf("reads = %d", n)
		}
	})

	t.Run("R-REPO-033 a rotation keeps the old key opening old values and a re-sealed value under the new key", func(t *testing.T) {
		old := keyRing(t, "k1", "k1")
		sealed, err := repository.SealSecret(old, 41, "/remote/password", "tok")
		if err != nil {
			t.Fatal(err)
		}
		rotated := keyRing(t, "k2", "k1", "k2")
		v, err := repository.OpenSecret(rotated, 41, "/remote/password", sealed)
		if err != nil || v.Reveal() != "tok" {
			t.Fatalf("the old value does not open under the rotated ring: %v", err)
		}
		again, changed, err := rotated.Reseal(repository.RepositorySecrets, secrets.Row("41", "/remote/password"), sealed)
		if err != nil || !changed {
			t.Fatalf("reseal: %v %v", changed, err)
		}
		if id, _ := secrets.KeyIDOf(again); id != "k2" {
			t.Fatalf("resealed under %q", id)
		}
	})
}

func TestHopPolicy(t *testing.T) {
	other, atOther := stubRegistry(t)
	origin, atOrigin := func() (*httptest.Server, *seen) {
		got := &seen{}
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got.add(r)
			if r.URL.Path == "/redirect" {
				http.Redirect(w, r, other.URL+"/tarball", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, "{}")
		}))
		t.Cleanup(s.Close)
		return s, got
	}()
	f := newFixture(t, fixtureOpts{servers: []*httptest.Server{origin, other}})
	f.seal(51, "tok")

	build := func(s repository.RemoteSettings, version int64) *Client {
		cl, err := f.clients.For(t.Context(), repo(51, "npm-redirect", version), s)
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}

	t.Run("R-REPO-039 R-REPO-033 a redirect to another origin carries no credential", func(t *testing.T) {
		cl := build(settings(origin.URL+"/", repository.AuthBearer), 1)
		mustGet(t, cl, origin.URL+"/redirect")
		if r := atOrigin.last(t); r.auth != "Bearer tok" {
			t.Fatalf("the origin got %q", r.auth)
		}
		if r := atOther.last(t); r.path != "/tarball" || r.auth != "" {
			t.Fatalf("the other origin got %+v", r)
		}
	})

	t.Run("R-REPO-039 forwardAuthOnRedirect sends the credential along the chain", func(t *testing.T) {
		s := settings(origin.URL+"/", repository.AuthBearer)
		s.ForwardAuthOnRedirect = true
		cl := build(s, 2)
		mustGet(t, cl, origin.URL+"/redirect")
		if r := atOther.last(t); r.auth != "Bearer tok" {
			t.Fatalf("the other origin got %q", r.auth)
		}
	})

	t.Run("R-REPO-033 an extraHosts entry with sendCredentials receives it on the redirect", func(t *testing.T) {
		u, _ := url.Parse(other.URL)
		s := settings(origin.URL+"/", repository.AuthBearer)
		s.ExtraHosts = []repository.ExtraHost{{Pattern: u.Hostname(), SendCredentials: true}}
		cl := build(s, 3)
		mustGet(t, cl, origin.URL+"/redirect")
		if r := atOther.last(t); r.auth != "Bearer tok" {
			t.Fatalf("the extra host got %q", r.auth)
		}
	})

	t.Run("R-REPO-033 an extraHosts entry without sendCredentials does not", func(t *testing.T) {
		u, _ := url.Parse(other.URL)
		s := settings(origin.URL+"/", repository.AuthBearer)
		s.ExtraHosts = []repository.ExtraHost{{Pattern: u.Hostname()}}
		cl := build(s, 4)
		mustGet(t, cl, origin.URL+"/redirect")
		if r := atOther.last(t); r.auth != "" {
			t.Fatalf("the extra host got %q", r.auth)
		}
	})
}

func TestAllowed(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	s := settings("https://registry.example.org/", repository.AuthNone)
	s.ExtraHosts = []repository.ExtraHost{{Pattern: "*.cdn.example.org"}, {Pattern: "tarballs.example.net", SendCredentials: true}}
	cl, err := f.clients.For(t.Context(), repo(61, "npm-hosts", 1), s)
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]bool{
		"https://registry.example.org/left-pad/-/left-pad-1.0.0.tgz": true,
		"https://REGISTRY.example.org:443/x":                         true,
		"https://registry.example.org:8443/x":                        false,
		"https://eu.cdn.example.org/x":                               true,
		"https://cdn.example.org/x":                                  false,
		"https://tarballs.example.net/x":                             true,
		"https://evil.example.com/x":                                 false,
		"https://registry.example.org.evil.example.com/x":            false,
		"https://169.254.169.254/latest":                             false,
	} {
		u, _ := url.Parse(raw)
		err := cl.Allowed(u)
		if want != (err == nil) {
			t.Errorf("R-REPO-043 Allowed(%s) = %v, want allowed %v", raw, err, want)
			continue
		}
		if !want {
			if e, ok := errs.As(err); !ok || e.Code != errs.CodeForeignHostDenied || e.Status() != 502 {
				t.Errorf("Allowed(%s) = %v", raw, err)
			}
		}
	}

	t.Run("R-REPO-039 R-REPO-043 Q-ARCH-224 an https upstream's document cannot send a fetch to http, on its own host or an extra one", func(t *testing.T) {
		for _, raw := range []string{"http://registry.example.org/x", "http://registry.example.org:443/x", "http://tarballs.example.net/x", "http://eu.cdn.example.org/x"} {
			u, _ := url.Parse(raw)
			if e, ok := errs.As(cl.Allowed(u)); !ok || e.Code != errs.CodeInsecureRedirect || e.Status() != 502 {
				t.Errorf("Allowed(%s) = %v", raw, e)
			}
		}
	})
	t.Run("R-REPO-043 an http upstream's document may move to https on the same host", func(t *testing.T) {
		plain, err := f.clients.For(t.Context(), repo(62, "npm-plain", 1), settings("http://registry.example.org/", repository.AuthNone))
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{"http://registry.example.org/x", "https://registry.example.org/x"} {
			u, _ := url.Parse(raw)
			if err := plain.Allowed(u); err != nil {
				t.Errorf("Allowed(%s) = %v", raw, err)
			}
		}
	})
}

func TestClientCache(t *testing.T) {
	srv, _ := stubRegistry(t)
	f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}})
	s := settings(srv.URL+"/", repository.AuthNone)

	t.Run("R-REPO-049 the client is kept per repository and configuration version", func(t *testing.T) {
		a, err := f.clients.For(t.Context(), repo(71, "npm-cache", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := f.clients.For(t.Context(), repo(71, "npm-cache", 1), s); b != a {
			t.Fatal("the same version built a second client")
		}
		c, _ := f.clients.For(t.Context(), repo(71, "npm-cache", 2), s)
		if c == a {
			t.Fatal("a new config_version reused the old client")
		}
		if d, _ := f.clients.For(t.Context(), repo(72, "npm-cache2", 1), s); d == a {
			t.Fatal("two repositories share a client")
		}
	})

	t.Run("R-REPO-049 a repo_config change of the repository evicts its clients, not the others", func(t *testing.T) {
		a, _ := f.clients.For(t.Context(), repo(71, "npm-cache", 2), s)
		other, _ := f.clients.For(t.Context(), repo(72, "npm-cache2", 1), s)
		f.caches.ApplyChange("repo_config", 1, json.RawMessage(`{"id":"71","key":"npm-cache","op":"updated"}`))
		f.clients.mu.Lock()
		_, kept := f.clients.latest[71]
		f.clients.mu.Unlock()
		if kept {
			t.Fatal("the replaced client of the repository is still referenced")
		}
		if b, _ := f.clients.For(t.Context(), repo(71, "npm-cache", 2), s); b == a {
			t.Fatal("the change did not evict the client")
		}
		if o, _ := f.clients.For(t.Context(), repo(72, "npm-cache2", 1), s); o != other {
			t.Fatal("a change of another repository evicted this client")
		}
		f.caches.ApplyChange("repo_config", 1, json.RawMessage(`not json`))
		if o, _ := f.clients.For(t.Context(), repo(72, "npm-cache2", 1), s); o == other {
			t.Fatal("an unreadable payload did not flush the cache")
		}
	})

	t.Run("R-REPO-049 the cache is registered under its name in the area repo_config", func(t *testing.T) {
		found := false
		for _, st := range f.caches.Stats() {
			if st.Name == CacheClients {
				found = true
			}
		}
		if !found {
			t.Fatalf("stats = %+v", f.caches.Stats())
		}
	})
}

func TestSourceNetwork(t *testing.T) {
	srv, _ := stubRegistry(t)
	s := settings(srv.URL+"/", repository.AuthNone)
	p := netip.MustParsePrefix("10.99.0.0/24")
	s.SourceNetwork = &p

	t.Run("R-REPO-024 without an interface address inside the prefix the request fails with source_network_unavailable", func(t *testing.T) {
		f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}, interfaces: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("127.0.0.1")}, nil
		}})
		cl, err := f.clients.For(t.Context(), repo(81, "npm-src", 1), s)
		if err != nil {
			t.Fatal(err)
		}
		err = getErr(t, cl, srv.URL+"/-/ping")
		if !errors.Is(err, outbound.ErrSourceNetworkUnavailable) || Classify(err) != CodeSourceNetwork {
			t.Fatalf("err = %v (%s)", err, Classify(err))
		}
		res := f.clients.TestSaved(t.Context(), repo(81, "npm-src", 1), s)
		if res.Reachable || res.Error == nil || res.Error.Code != "source_network_unavailable" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("R-REPO-024 with an address inside the prefix the connection is bound to it and the test reports it", func(t *testing.T) {
		// The loopback alias is the one local address every host has: the prefix is a
		// /8 around it, so the bind works without a secondary interface (D-17).
		lo := netip.MustParsePrefix("127.0.0.0/8")
		s := s
		s.SourceNetwork = &lo
		f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}, interfaces: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.1.2.3"), netip.MustParseAddr("127.0.0.1")}, nil
		}})
		res := f.clients.TestSaved(t.Context(), repo(82, "npm-src2", 1), s)
		if !res.Reachable || res.SourceAddress == nil || *res.SourceAddress != "127.0.0.1" {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestUpstreamTest(t *testing.T) {
	srv, got := stubRegistry(t)
	f := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}})

	t.Run("R-REPO-024 the test of a reachable upstream reports the status, the certificate and the duration", func(t *testing.T) {
		res := f.clients.TestSaved(t.Context(), repo(91, "npm-up", 1), settings(srv.URL+"/", repository.AuthNone))
		if !res.Reachable || res.HTTPStatus == nil || *res.HTTPStatus != 200 || res.Authenticated == nil || !*res.Authenticated || res.Error != nil {
			t.Fatalf("result = %+v", res)
		}
		if res.TLS == nil || res.TLS.PeerSubject == "" || res.TLS.NotAfter.Before(time.Now()) || res.SourceAddress != nil || res.OutboundProxy != nil {
			t.Fatalf("tls = %+v, source = %v", res.TLS, res.SourceAddress)
		}
		if r := got.last(t); r.path != "/-/ping" {
			t.Fatalf("the probe requested %q: the format's probe path is not used", r.path)
		}
	})

	t.Run("R-REPO-024 a format without a probe path probes the URL itself", func(t *testing.T) {
		s := settings(srv.URL+"/", repository.AuthNone)
		rep := repo(92, "idx-up", 1)
		rep.Format = "indexfmt"
		res := f.clients.TestSaved(t.Context(), rep, s)
		if !res.Reachable {
			t.Fatalf("result = %+v", res)
		}
		if r := got.last(t); r.path != "/" {
			t.Fatalf("the probe requested %q", r.path)
		}
	})

	t.Run("R-REPO-024 the probe resource is below the path of the URL", func(t *testing.T) {
		for in, want := range map[string]string{
			"https://r.example/":         "https://r.example/-/ping",
			"https://r.example/npm":      "https://r.example/npm/-/ping",
			"https://r.example/npm/?x=1": "https://r.example/npm/-/ping?x=1",
		} {
			u, _ := url.Parse(in)
			if got := probeURL(u, "-/ping").String(); got != want {
				t.Errorf("probeURL(%s) = %s, want %s", in, got, want)
			}
		}
	})

	t.Run("R-REPO-033 the test reports 401 as not authenticated, with the credential the fetches use", func(t *testing.T) {
		locked := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer good" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, "{}")
		}))
		t.Cleanup(locked.Close)
		f := newFixture(t, fixtureOpts{servers: []*httptest.Server{locked}})
		f.seal(93, "bad")
		s := settings(locked.URL+"/", repository.AuthBearer)
		res := f.clients.TestSaved(t.Context(), repo(93, "npm-locked", 1), s)
		if !res.Reachable || *res.HTTPStatus != 401 || *res.Authenticated {
			t.Fatalf("result = %+v", res)
		}
		f.seal(93, "good")
		res = f.clients.TestSaved(t.Context(), repo(93, "npm-locked", 2), s)
		if !res.Reachable || *res.HTTPStatus != 200 || !*res.Authenticated {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("R-REPO-024 an unknown host reaches nothing and the error is a DNS error", func(t *testing.T) {
		res := f.clients.TestSaved(t.Context(), repo(94, "npm-nx", 1), settings("https://registry.does-not-exist.invalid/", repository.AuthNone))
		if res.Reachable || res.HTTPStatus != nil || res.Error == nil || res.Error.Code != "dns_error" {
			t.Fatalf("result = %+v", res)
		}
		if strings.Contains(res.Error.Message, "https://") {
			t.Fatalf("the message repeats the URL: %s", res.Error.Message)
		}
	})

	t.Run("R-REPO-038 http://169.254.169.254/ is egress_denied and nothing connects", func(t *testing.T) {
		res := f.clients.TestSaved(t.Context(), repo(95, "npm-imds", 1), settings("http://169.254.169.254/latest/", repository.AuthNone))
		if res.Reachable || res.Error == nil || res.Error.Code != "egress_denied" || !strings.Contains(res.Error.Message, "169.254.169.254") {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("R-REPO-038 loopback is denied by the default policy", func(t *testing.T) {
		cfg := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
		for _, k := range config.MustDescribe().Keys {
			if k.Path == "proxy.outbound.address_policy.deny_cidrs" {
				_ = json.Unmarshal([]byte(k.Default), &cfg.AddressPolicy.DenyCIDRs)
			}
		}
		reg, _ := cache.NewRegistry(nil, prometheus.NewRegistry())
		c, err := New(Options{Factory: outbound.New(cfg), Store: &store{}, Ring: f.ring, Caches: reg, Catalog: testCatalog(), SafetyTTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		res := c.TestSaved(t.Context(), repo(96, "npm-lo", 1), settings(srv.URL+"/", repository.AuthNone))
		if res.Error == nil || res.Error.Code != "egress_denied" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("R-REPO-035 an untrusted certificate is tls_untrusted", func(t *testing.T) {
		ca, err := npmstub.NewSelfSigned("127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		stranger := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		stranger.TLS = ca.ServerConfig() // a CA that proxy.outbound.tls does not trust
		stranger.StartTLS()
		t.Cleanup(stranger.Close)
		res := f.clients.TestSaved(t.Context(), repo(97, "npm-tls", 1), settings(stranger.URL+"/", repository.AuthNone))
		if res.Reachable || res.Error == nil || res.Error.Code != "tls_untrusted" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("R-REPO-024 a refused connection and a timeout are named", func(t *testing.T) {
		dead := httptest.NewUnstartedServer(nil)
		addr := dead.Listener.Addr().String()
		_ = dead.Listener.Close()
		res := f.clients.TestSaved(t.Context(), repo(98, "npm-dead", 1), settings("https://"+addr+"/", repository.AuthNone))
		if res.Error == nil || res.Error.Code != "connection_refused" {
			t.Fatalf("refused: %+v", res)
		}
		slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) }))
		t.Cleanup(slow.Close)
		f2 := newFixture(t, fixtureOpts{servers: []*httptest.Server{slow}})
		s := settings(slow.URL+"/", repository.AuthNone)
		s.ReadTimeoutMs = 1000
		res = f2.clients.TestSaved(t.Context(), repo(99, "npm-slow", 1), s)
		if res.Error == nil || res.Error.Code != "timeout" {
			t.Fatalf("slow: %+v", res)
		}
	})

	t.Run("R-REPO-029 the test touches no circuit state: it has no store for it", func(t *testing.T) {
		// The clients hold no circuit and no database handle besides the secret reader;
		// the engine counts failures from its own fetches, never from a test.
		res := f.clients.TestSaved(t.Context(), repo(100, "npm-down", 1), settings("https://registry.does-not-exist.invalid/", repository.AuthNone))
		if res.Error == nil {
			t.Fatal("expected a failure to count")
		}
	})

	t.Run("R-REPO-030 R-REPO-031 an offline repository and the global switch send nothing", func(t *testing.T) {
		before := len(got.all())
		s := settings(srv.URL+"/", repository.AuthNone)
		s.Availability.Mode = repository.AvailabilityOffline
		res := f.clients.TestSaved(t.Context(), repo(101, "npm-off", 1), s)
		if res.Reachable || res.Error == nil || res.Error.Code != "offline" {
			t.Fatalf("offline: %+v", res)
		}
		g := newFixture(t, fixtureOpts{servers: []*httptest.Server{srv}, proxy: config.Proxy{GlobalOffline: true}})
		res = g.clients.TestSaved(t.Context(), repo(102, "npm-global", 1), settings(srv.URL+"/", repository.AuthNone))
		if res.Error == nil || res.Error.Code != "offline" || len(got.all()) != before {
			t.Fatalf("global: %+v (%d requests)", res, len(got.all())-before)
		}
	})

	t.Run("R-REPO-033 an unsaved test uses the inline secret once and stores nothing", func(t *testing.T) {
		pw := observability.Secret("inline-token")
		s := settings(srv.URL+"/", repository.AuthBearer)
		before := f.store.n.Load()
		res := f.clients.TestUnsaved(t.Context(), repository.UnsavedProbe{Format: "npmlike", Settings: s, Password: &pw})
		if !res.Reachable {
			t.Fatalf("result = %+v", res)
		}
		if r := got.last(t); r.auth != "Bearer inline-token" {
			t.Fatalf("Authorization = %q", r.auth)
		}
		if f.store.n.Load() != before {
			t.Fatal("the unsaved test read the secrets table")
		}
		if b, _ := json.Marshal(res); strings.Contains(string(b), "inline-token") {
			t.Fatalf("the result carries the secret: %s", b)
		}
	})

	t.Run("R-REPO-033 an unsaved test resolves a reference like a saved one", func(t *testing.T) {
		f.writeSecretFile("probe-token", "from-file\n")
		s := settings(srv.URL+"/", repository.AuthBearer)
		s.Password = &repository.SecretField{Ref: &repository.SecretRef{File: "probe-token"}}
		res := f.clients.TestUnsaved(t.Context(), repository.UnsavedProbe{Format: "npmlike", Settings: s})
		if !res.Reachable {
			t.Fatalf("result = %+v", res)
		}
		if r := got.last(t); r.auth != "Bearer from-file" {
			t.Fatalf("Authorization = %q", r.auth)
		}
		f.setEnv("HOME", "x")
		s.Password = &repository.SecretField{Ref: &repository.SecretRef{File: "missing"}}
		res = f.clients.TestUnsaved(t.Context(), repository.UnsavedProbe{Format: "npmlike", Settings: s})
		if res.Error == nil || res.Error.Code != "upstream_secret_unavailable" {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestClassify(t *testing.T) {
	cases := map[string]error{
		"egress_denied":               &outbound.EgressDeniedError{Addr: netip.MustParseAddr("127.0.0.1"), Rule: "127.0.0.0/8"},
		"source_network_unavailable":  outbound.ErrSourceNetworkUnavailable,
		"too_many_redirects":          outbound.ErrTooManyRedirects,
		"insecure_redirect":           outbound.ErrInsecureRedirect,
		"upstream_secret_unavailable": secretUnavailable("npm-x", errors.New("boom")),
		"timeout":                     context.DeadlineExceeded,
		"request_failed":              io.ErrUnexpectedEOF,
	}
	for want, err := range cases {
		t.Run("R-REPO-038 "+want, func(t *testing.T) {
			if got := Classify(&url.Error{Op: "Get", URL: "https://x/", Err: err}); got != want {
				t.Fatalf("Classify = %q", got)
			}
		})
	}
	if Classify(nil) != "" {
		t.Fatal("a nil error has a code")
	}
}
