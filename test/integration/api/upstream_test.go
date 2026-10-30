//go:build integration

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The upstream-test operations over HTTP (R-REPO-024), against a migrated
// database and a TLS test upstream, with every answer validated against the
// OpenAPI document.

type apiNotifier struct{}

func (apiNotifier) Notify(meta.Tx, string, int, any) error { return nil }

type upstreamStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string
}

func (u *upstreamStub) last(t *testing.T) string {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.got) == 0 {
		t.Fatal("the upstream got no request")
	}
	return u.got[len(u.got)-1]
}

func newUpstreamStub(t *testing.T) *upstreamStub {
	t.Helper()
	u := &upstreamStub{}
	u.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.got = append(u.got, r.Header.Get("Authorization"))
		u.mu.Unlock()
		if r.URL.Path != "/-/ping" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func upstreamCatalog() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npmlike", Classes: []catalog.Class{catalog.Local, catalog.Remote}, Layout: "npmlike", ProtocolBasePath: "/api/npmlike/{key}/",
		PatternSubject: catalog.SubjectPath, ProbePath: "-/ping",
	}})
	return r
}

func TestUpstreamTestOperations(t *testing.T) {
	val := apitest.New(t, v1.EmbeddedYAML())
	e := newEnv(t)
	stub := newUpstreamStub(t)

	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 3)
	}
	keyFile := filepath.Join(dir, "2026-10.key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.Load(secretsConfig("2026-10", keyFile))
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: stub.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	out := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, CAFiles: []string{caFile}, MinVersion: "1.2"}}
	for _, k := range config.MustDescribe().Keys {
		if k.Path == "proxy.outbound.address_policy.deny_cidrs" {
			if err := json.Unmarshal([]byte(k.Default), &out.AddressPolicy.DenyCIDRs); err != nil {
				t.Fatal(err)
			}
		}
	}
	out.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8"}

	var svc *repository.Service
	n := e.newNode("web-1", nodeOpts{limits: relaxed(), val: val, repos: func(n *node) v1.RepositoryServices {
		caches, err := cache.NewRegistry(nil, prometheus.NewRegistry())
		if err != nil {
			t.Fatal(err)
		}
		cat := upstreamCatalog()
		clients, err := upstream.New(upstream.Options{
			Factory: outbound.New(out), Store: repository.NewSecretStore(n.db), Ring: ring, Caches: caches, Catalog: cat,
			Proxy: config.Proxy{MaxRedirects: 5}, SecretsDir: dir, SafetyTTL: time.Minute, Log: quiet,
		})
		if err != nil {
			t.Fatal(err)
		}
		svc, err = repository.NewService(repository.Options{
			Runner: n.db, Catalog: cat, Notifier: apiNotifier{}, Recorder: audit.Nop{}, Log: quiet,
			Remote: repository.RemoteDeps{Ring: ring, Prober: clients},
		})
		if err != nil {
			t.Fatal(err)
		}
		return v1.RepositoryServices{Service: svc}
	}})
	c := newWebCluster(n)
	admin := []string{"X-Test-Principal", uuidV4(), "X-Test-Caps", "repositories:write-config"}
	reader := []string{"X-Test-Principal", uuidV4(), "X-Test-Caps", "repositories:read-config,settings:read"}
	caller := repository.Caller{}
	create := func(body string) {
		t.Helper()
		if _, err := svc.Create(context.Background(), caller, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	unsaved := func(remote string) string { return fmt.Sprintf(`{"format":"npmlike","remote":%s}`, remote) }

	t.Run("R-REPO-024 POST /upstream-tests probes an unsaved section and the answer matches the contract", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/upstream-tests", unsaved(fmt.Sprintf(`{"url":%q,"authMode":"bearer","password":{"value":"probe-token"}}`, stub.srv.URL)), admin...)
		if a.status != 200 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		res := a.json(t)
		if res["reachable"] != true || res["httpStatus"] != float64(200) || res["authenticated"] != true || res["error"] != nil {
			t.Fatalf("result = %v", res)
		}
		if tls, _ := res["tls"].(map[string]any); tls == nil || tls["peerSubject"] == "" {
			t.Fatalf("tls = %v", res["tls"])
		}
		if got := stub.last(t); got != "Bearer probe-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if strings.Contains(string(a.body), "probe-token") {
			t.Fatalf("the answer carries the secret: %s", a.body)
		}
		if n := e.scalar(`SELECT count(*) FROM repository_secrets`); n != 0 {
			t.Fatalf("the unsaved test stored %d secrets", n)
		}
	})

	t.Run("R-REPO-038 R-REPO-024 an upstream at the metadata address is egress_denied", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/upstream-tests", unsaved(`{"url":"http://169.254.169.254/latest/"}`), admin...)
		res := a.json(t)
		if a.status != 200 || res["reachable"] != false {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if e, _ := res["error"].(map[string]any); e == nil || e["code"] != "egress_denied" {
			t.Fatalf("error = %v", res["error"])
		}
	})

	t.Run("R-REPO-024 an unknown host is a DNS error, in a 200", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/upstream-tests", unsaved(`{"url":"https://registry.does-not-exist.invalid/"}`), admin...)
		res := a.json(t)
		if e, _ := res["error"].(map[string]any); a.status != 200 || res["reachable"] != false || e == nil || e["code"] != "dns_error" {
			t.Fatalf("%d %s", a.status, a.body)
		}
	})

	t.Run("R-REPO-008 R-API-009 an invalid section answers 422 with every violation, and 400 for a misspelt member", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/upstream-tests", unsaved(`{"url":"https://u:p@x.example/","connectTimeoutMs":50}`), admin...)
		if a.status != 422 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if n := len(a.json(t)["errors"].([]any)); n != 2 {
			t.Fatalf("errors = %d: %s", n, a.body)
		}
		if strings.Contains(string(a.body), "u:p@") {
			t.Fatalf("the problem repeats the credential: %s", a.body)
		}
		if a := send(t, c, "POST", "/api/v1/upstream-tests", unsaved(`{"url":"https://x.example/","connectTimeout":50}`), admin...); a.status != 400 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if a := send(t, c, "POST", "/api/v1/upstream-tests", `{"format":"npmlike","remote":{}}`, append(admin, "Content-Type", "text/plain")...); a.status != 415 {
			t.Fatalf("%d %s", a.status, a.body)
		}
	})

	t.Run("R-REPO-024 both operations need repositories:write-config", func(t *testing.T) {
		for _, path := range []string{"/api/v1/upstream-tests", "/api/v1/repositories/npm-up/upstream-test"} {
			if a := send(t, c, "POST", path, unsaved(`{}`)); a.status != 401 {
				t.Errorf("%s anonymous: %d", path, a.status)
			}
			if a := send(t, c, "POST", path, unsaved(`{}`), reader...); a.status != 403 {
				t.Errorf("%s without the capability: %d", path, a.status)
			}
		}
	})

	t.Run("R-REPO-024 POST /repositories/{key}/upstream-test probes the saved configuration", func(t *testing.T) {
		create(fmt.Sprintf(`{"key":"npm-up","class":"remote","format":"npmlike","remote":{"url":%q,"authMode":"basic","username":"ci","password":{"value":"p4ss"}}}`, stub.srv.URL))
		a := send(t, c, "POST", "/api/v1/repositories/npm-up/upstream-test", nil, admin...)
		if a.status != 200 || a.json(t)["reachable"] != true {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if got, want := stub.last(t), "Basic "+base64.StdEncoding.EncodeToString([]byte("ci:p4ss")); got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}

		create(`{"key":"npm-nx","class":"remote","format":"npmlike","remote":{"url":"https://registry.does-not-exist.invalid/"}}`)
		a = send(t, c, "POST", "/api/v1/repositories/npm-nx/upstream-test", nil, admin...)
		if e, _ := a.json(t)["error"].(map[string]any); a.status != 200 || e == nil || e["code"] != "dns_error" {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if n := e.scalar(`SELECT count(*) FROM remote_upstream_state`); n != 0 {
			t.Fatalf("the test wrote %d circuit rows", n)
		}
	})

	t.Run("R-REPO-033 a sealed value copied to another repository is upstream_secret_unavailable in the result", func(t *testing.T) {
		create(fmt.Sprintf(`{"key":"npm-copy","class":"remote","format":"npmlike","remote":{"url":%q,"authMode":"bearer","password":{"value":"other"}}}`, stub.srv.URL))
		e.exec(`UPDATE repository_secrets SET sealed = (SELECT s.sealed FROM repository_secrets s JOIN repositories r ON r.id = s.repo_id WHERE r.key = 'npm-up')
		         WHERE repo_id = (SELECT id FROM repositories WHERE key = 'npm-copy')`)
		a := send(t, c, "POST", "/api/v1/repositories/npm-copy/upstream-test", nil, admin...)
		if e, _ := a.json(t)["error"].(map[string]any); a.status != 200 || e == nil || e["code"] != "upstream_secret_unavailable" {
			t.Fatalf("%d %s", a.status, a.body)
		}
	})

	t.Run("R-REPO-024 a missing repository is 404 and a local one is 400", func(t *testing.T) {
		create(`{"key":"npm-local","class":"local","format":"npmlike"}`)
		if a := send(t, c, "POST", "/api/v1/repositories/npm-local/upstream-test", nil, admin...); a.status != 400 || a.code(t) != "not_applicable" {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if a := send(t, c, "POST", "/api/v1/repositories/nope-nope/upstream-test", nil, admin...); a.status != 404 {
			t.Fatalf("%d %s", a.status, a.body)
		}
	})
}
