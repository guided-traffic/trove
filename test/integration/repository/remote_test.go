//go:build integration

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
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

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/listq"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The remote repository settings against PostgreSQL: the canonical document, the
// sealed secrets, the upstream clients built from the stored rows and the upstream
// test (R-REPO-024, R-REPO-033, R-REPO-038). The format is a test format that
// proposes what the npm format proposes, because the npm format is not built yet.

func remoteTestCatalog() *catalog.Registry {
	ttl, neg := 7200, 1800
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npmlike", Classes: []catalog.Class{catalog.Local, catalog.Remote}, Layout: "npmlike", ProtocolBasePath: "/api/npmlike/{key}/",
		PatternSubject: catalog.SubjectPath, DefaultUpstreamURL: "https://registry.example.org/", DefaultExtraHosts: []string{"*.cdn.example.org"},
		DefaultMetadataTTLSeconds: &ttl, DefaultNotFoundTTLSeconds: &neg, ProbePath: "-/ping",
	}})
	return r
}

// stubUpstream is a TLS registry that answers the probe resource and records the
// credentials it saw.
type stubUpstream struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string // Authorization header of each request
}

func newStubUpstream(t *testing.T) *stubUpstream {
	t.Helper()
	s := &stubUpstream{}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.got = append(s.got, r.Header.Get("Authorization"))
		s.mu.Unlock()
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubUpstream) auths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func (s *stubUpstream) last(t *testing.T) string {
	t.Helper()
	a := s.auths()
	if len(a) == 0 {
		t.Fatal("the upstream got no request")
	}
	return a[len(a)-1]
}

// remoteReplica is a replica whose repository service seals secrets and probes
// upstreams, like the composition root wires it.
type remoteReplica struct {
	*replica
	svc     *repository.Service // shadows the replica's service: it knows the test format and seals secrets
	clients *upstream.Clients
	ring    *secrets.KeyRing
	dir     string
}

func writeKeyring(t *testing.T, active string, ids ...string) *secrets.KeyRing {
	t.Helper()
	var files []string
	for _, id := range ids {
		p := filepath.Join(t.TempDir(), id+".key")
		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i) + id[i%len(id)]
		}
		if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func (e *env) newRemoteReplica(name string, ring *secrets.KeyRing, trusted ...*httptest.Server) *remoteReplica {
	t := e.t
	t.Helper()
	rep := e.newReplica(name, replicaOpts{})
	cat := remoteTestCatalog()
	out := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
	for _, k := range config.MustDescribe().Keys {
		if k.Path == "proxy.outbound.address_policy.deny_cidrs" {
			if err := json.Unmarshal([]byte(k.Default), &out.AddressPolicy.DenyCIDRs); err != nil {
				t.Fatal(err)
			}
		}
	}
	out.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8", "::1/128"}
	dir := t.TempDir()
	for i, srv := range trusted {
		p := filepath.Join(dir, fmt.Sprintf("ca%d.pem", i))
		if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		out.TLS.CAFiles = append(out.TLS.CAFiles, p)
	}
	clients, err := upstream.New(upstream.Options{
		Factory: outbound.New(out), Store: repository.NewSecretStore(rep.db), Ring: ring, Caches: rep.caches, Catalog: cat,
		Proxy: config.Proxy{MaxRedirects: 5}, SecretsDir: dir, SafetyTTL: time.Minute, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := repository.NewService(repository.Options{
		Runner: rep.db, Catalog: cat, Recorder: rep.rec, Notifier: rep.changes, Log: quiet,
		Remote: repository.RemoteDeps{Ring: ring, Prober: clients},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &remoteReplica{replica: rep, svc: svc, clients: clients, ring: ring, dir: dir}
}

// create writes a remote repository of the test format.
func (r *remoteReplica) create(t *testing.T, key, remote string) repository.Stored {
	t.Helper()
	st, err := r.svc.Create(context.Background(), caller, []byte(fmt.Sprintf(`{"key":%q,"class":"remote","format":"npmlike","remote":%s}`, key, remote)))
	if err != nil {
		t.Fatalf("create %s: %v", key, err)
	}
	return st
}

func (r *remoteReplica) repoID(t *testing.T, e *env, key string) int64 {
	t.Helper()
	return e.scalar(`SELECT id FROM repositories WHERE key = $1`, key)
}

func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("not an *errs.Error: %v", err)
	}
	out := map[string]string{}
	for _, f := range e.Fields {
		out[f.Pointer] = f.Code
	}
	return out
}

func TestRemoteSettings(t *testing.T) {
	e := newEnv(t)
	stub := newStubUpstream(t)
	r := e.newRemoteReplica("remote", writeKeyring(t, "2026-10", "2026-10"), stub.srv)
	ctx := context.Background()

	t.Run("R-REPO-024 a remote created with only url reads back every member with its default", func(t *testing.T) {
		st := r.create(t, "npm-defaults", `{"url":"https://registry.example.com"}`)
		got, err := r.svc.Get(ctx, "npm-defaults")
		if err != nil {
			t.Fatal(err)
		}
		var remote map[string]any
		if err := json.Unmarshal(got.Document.Remote, &remote); err != nil {
			t.Fatal(err)
		}
		want := `{"url":"https://registry.example.com/","extraHosts":[],"authMode":"none","username":"","password":null,"clientCertificate":null,` +
			`"headers":[],"forwardAuthOnRedirect":false,"outboundProxy":{"mode":"default"},"connectTimeoutMs":10000,"readTimeoutMs":15000,` +
			`"sourceNetwork":null,"cacheFillAction":"read","cache":{"storeArtifacts":true,"metadataTtlSeconds":7200,"metadataRefreshTimeoutSeconds":60,` +
			`"notFoundTtlSeconds":1800,"maxStaleSeconds":0,"unusedRetentionHours":0},"integrity":{"checksumPolicy":"verify-if-available",` +
			`"blockUnexpectedContentTypes":true,"allowedContentTypes":[]},"availability":{"mode":"online","failureThreshold":5,"circuitOpenSeconds":300},` +
			`"limits":{"maxArtifactBytes":null,"maxMetadataBytes":null,"maxConcurrentFetches":64}}`
		if string(got.Document.Remote) != want {
			t.Fatalf("remote =\n%s\nwant\n%s", got.Document.Remote, want)
		}
		if st.Repository.Version != 1 || len(st.Warnings) != 0 {
			t.Fatalf("version = %d, warnings = %+v", st.Repository.Version, st.Warnings)
		}
		if e.scalar(`SELECT count(*) FROM repository_secrets WHERE repo_id = $1`, r.repoID(t, e, "npm-defaults")) != 0 {
			t.Fatal("a repository without a secret has a secret row")
		}
	})

	t.Run("R-REPO-024 the snapshot of another replica parses the settings", func(t *testing.T) {
		b := e.newRemoteReplica("remote-b", r.ring, stub.srv)
		repo, err := b.reader.ByKey(ctx, "npm-defaults")
		if err != nil {
			t.Fatal(err)
		}
		s, ok := repo.Remote()
		if !ok || s.URL != "https://registry.example.com/" || s.Cache.MetadataTTLSeconds != 7200 || s.Limits.MaxConcurrentFetches != 64 {
			t.Fatalf("settings = %+v (%v)", s, ok)
		}
	})

	t.Run("R-REPO-024 an omitted url takes the upstream and the extra hosts of the format", func(t *testing.T) {
		st := r.create(t, "npm-format-default", `{}`)
		repo, err := r.reader.ByKey(ctx, st.Document.Key)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := repo.Remote()
		if s.URL != "https://registry.example.org/" || len(s.ExtraHosts) != 1 || s.ExtraHosts[0].Pattern != "*.cdn.example.org" {
			t.Fatalf("settings = %+v", s)
		}
	})

	t.Run("R-REPO-024 R-REPO-008 a value outside its range answers 422 out_of_range and writes nothing", func(t *testing.T) {
		before := e.scalar(`SELECT count(*) FROM repositories`)
		_, err := r.svc.Create(ctx, caller, []byte(`{"key":"npm-bad","class":"remote","format":"npmlike","remote":{"url":"https://r.example/","connectTimeoutMs":50}}`))
		wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldCodes(t, err); got["/remote/connectTimeoutMs"] != "out_of_range" {
			t.Fatalf("fields = %v", got)
		}
		_, err = r.svc.Patch(ctx, caller, "npm-defaults", repository.MatchAny(), mergePatch(`{"remote":{"cache":{"metadataTtlSeconds":99999999}}}`))
		wantCode(t, err, 422, errs.CodeValidation)
		if e.scalar(`SELECT count(*) FROM repositories`) != before {
			t.Fatal("a refused create wrote a row")
		}
	})

	t.Run("R-REPO-008 R-REPO-024 the warnings of a save are returned and do not block it", func(t *testing.T) {
		st := r.create(t, "npm-warn", `{"url":"http://registry.internal.example:8080/","sourceNetwork":"10.99.0.11/32"}`)
		got := map[string]string{}
		for _, w := range st.Warnings {
			got[w.Code] = w.Pointer
		}
		if got["plain_http_upstream"] != "/remote/url" || got["source_network_single_address"] != "/remote/sourceNetwork" {
			t.Fatalf("warnings = %+v", st.Warnings)
		}
		// An update that keeps the settings reports them again: they describe the document.
		st, err := r.svc.Patch(ctx, caller, "npm-warn", repository.MatchAny(), mergePatch(`{"description":"x"}`))
		if err != nil || len(st.Warnings) != 2 {
			t.Fatalf("warnings = %+v, %v", st.Warnings, err)
		}
	})

	t.Run("R-REPO-008 R-REPO-024 a url with user info answers 422 invalid_upstream_url", func(t *testing.T) {
		_, err := r.svc.Create(ctx, caller, []byte(`{"key":"npm-userinfo","class":"remote","format":"npmlike","remote":{"url":"https://user:pw@registry.example.com/"}}`))
		wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldCodes(t, err); got["/remote/url"] != "invalid_upstream_url" {
			t.Fatalf("fields = %v", got)
		}
	})

	t.Run("R-REPO-008 a misspelt member of the remote section answers 400 malformed_request", func(t *testing.T) {
		_, err := r.svc.Create(ctx, caller, []byte(`{"key":"npm-typo","class":"remote","format":"npmlike","remote":{"url":"https://r.example/","connectTimeout":5}}`))
		wantCode(t, err, 400, errs.CodeInvalid)
	})

	t.Run("R-REPO-008 the P1 members are refused until they are built", func(t *testing.T) {
		_, err := r.svc.Create(ctx, caller, []byte(`{"key":"npm-p1","class":"remote","format":"npmlike","remote":{"url":"https://r.example/","headers":[{"name":"X-Api-Key","value":{"value":"k"},"sensitive":true}],"clientCertificate":"mtls"}}`))
		wantCode(t, err, 422, errs.CodeValidation)
		got := fieldCodes(t, err)
		if got["/remote/headers"] != errs.CodeNotApplicable || got["/remote/clientCertificate"] != errs.CodeNotApplicable {
			t.Fatalf("fields = %v", got)
		}
	})
}

func TestRemoteSecrets(t *testing.T) {
	e := newEnv(t)
	stub := newStubUpstream(t)
	ring := writeKeyring(t, "2026-10", "2026-10")
	r := e.newRemoteReplica("secrets", ring, stub.srv)
	ctx := context.Background()
	const token = "npm_s3cr3t-partner-token-0123456789"

	t.Run("R-REPO-033 the read shows {\"set\": true} and the value is nowhere in the database", func(t *testing.T) {
		r.create(t, "npm-partner", fmt.Sprintf(`{"url":%q,"authMode":"bearer","password":{"value":%q}}`, stub.srv.URL, token))
		got, err := r.svc.Get(ctx, "npm-partner")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got.Document.Remote), `"password":{"set":true}`) || strings.Contains(string(got.Document.Remote), token) {
			t.Fatalf("remote = %s", got.Document.Remote)
		}
		id := r.repoID(t, e, "npm-partner")
		var sealed string
		if err := e.admin.QueryRow(ctx, `SELECT sealed FROM repository_secrets WHERE repo_id = $1 AND field = '/remote/password' AND secret_ref IS NULL`, id).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(sealed, "v1:2026-10:") || strings.Contains(sealed, token) {
			t.Fatalf("sealed = %q", sealed)
		}

		// What pg_dump would write: every row of every table of the schema, as text.
		rows, err := e.admin.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'trove' AND table_type = 'BASE TABLE'`)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, n)
		}
		rows.Close()
		for _, tbl := range tables {
			var dump string
			if err := e.admin.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(string_agg(x::text, E'\n'), '') FROM trove.%q x`, tbl)).Scan(&dump); err != nil {
				t.Fatalf("dump %s: %v", tbl, err)
			}
			if strings.Contains(dump, token) || strings.Contains(dump, base64.StdEncoding.EncodeToString([]byte(token))) {
				t.Fatalf("the table %s holds the plaintext of the token", tbl)
			}
		}
		for _, ev := range r.rec.events {
			if b, _ := json.Marshal(ev); strings.Contains(string(b), token) {
				t.Fatalf("the audit event %s holds the plaintext: %s", ev.Type, b)
			}
		}
	})

	t.Run("R-REPO-033 a bearer token arrives at the upstream on every request, built from the stored row", func(t *testing.T) {
		repo, err := r.reader.ByKey(ctx, "npm-partner")
		if err != nil {
			t.Fatal(err)
		}
		s, _ := repo.Remote()
		cl, err := r.clients.For(ctx, repo, s)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, stub.srv.URL+"/-/ping", nil)
			resp, err := cl.HTTP().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if got := stub.last(t); got != "Bearer "+token {
				t.Fatalf("Authorization = %q", got)
			}
		}
	})

	t.Run("R-REPO-033 basic sends Authorization: Basic and no Bearer header", func(t *testing.T) {
		r.create(t, "npm-basic", fmt.Sprintf(`{"url":%q,"authMode":"basic","username":"ci","password":{"value":"hunter2"}}`, stub.srv.URL))
		res, err := r.svc.UpstreamTest(ctx, "npm-basic")
		if err != nil || !res.Reachable {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if got, want := stub.last(t), "Basic "+base64.StdEncoding.EncodeToString([]byte("ci:hunter2")); got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
	})

	t.Run("R-REPO-033 a document that was read can be sent back: the secret is kept, not blanked or leaked", func(t *testing.T) {
		got, _ := r.svc.Get(ctx, "npm-partner")
		body, err := json.Marshal(got.Document)
		if err != nil {
			t.Fatal(err)
		}
		st, err := r.svc.Replace(ctx, caller, "npm-partner", repository.MatchVersion(got.Repository.Version), body)
		if err != nil {
			t.Fatal(err)
		}
		if st.Repository.Version != got.Repository.Version {
			t.Fatalf("sending back what was read moved the version to %d", st.Repository.Version)
		}
		if _, err := r.svc.UpstreamTest(ctx, "npm-partner"); err != nil || stub.last(t) != "Bearer "+token {
			t.Fatalf("the secret did not survive: %q %v", stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 a patch of another setting keeps the secret", func(t *testing.T) {
		if _, err := r.svc.Patch(ctx, caller, "npm-partner", repository.MatchAny(), mergePatch(`{"remote":{"connectTimeoutMs":4000}}`)); err != nil {
			t.Fatal(err)
		}
		if res, err := r.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable || stub.last(t) != "Bearer "+token {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 a new value replaces the old one on the next request, and moves the version", func(t *testing.T) {
		before, _ := r.svc.Get(ctx, "npm-partner")
		st, err := r.svc.Patch(ctx, caller, "npm-partner", repository.MatchAny(), mergePatch(`{"remote":{"password":{"value":"rotated-token"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if st.Repository.Version != before.Repository.Version+1 {
			t.Fatalf("version %d -> %d", before.Repository.Version, st.Repository.Version)
		}
		if got := r.rec.last(); got.Type != repository.EventUpdated || !strings.Contains(string(got.Details), `"remote"`) || strings.Contains(string(got.Details), "rotated-token") {
			t.Fatalf("event = %s %s", got.Type, got.Details)
		}
		if res, err := r.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable || stub.last(t) != "Bearer rotated-token" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 the new value reaches another replica with the new version", func(t *testing.T) {
		b := e.newRemoteReplica("secrets-b", ring, stub.srv)
		if res, err := b.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable || stub.last(t) != "Bearer rotated-token" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
		if _, err := r.svc.Patch(ctx, caller, "npm-partner", repository.MatchAny(), mergePatch(`{"remote":{"password":{"value":"rotated-again"}}}`)); err != nil {
			t.Fatal(err)
		}
		if res, err := b.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable || stub.last(t) != "Bearer rotated-again" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 a copied sealed value fails the other repository with 502 upstream_secret_unavailable", func(t *testing.T) {
		r.create(t, "npm-other", fmt.Sprintf(`{"url":%q,"authMode":"bearer","password":{"value":"other-token"}}`, stub.srv.URL))
		partnerID, otherID := r.repoID(t, e, "npm-partner"), r.repoID(t, e, "npm-other")
		e.exec(`UPDATE repository_secrets SET sealed = (SELECT sealed FROM repository_secrets WHERE repo_id = $1 AND field = '/remote/password')
		         WHERE repo_id = $2 AND field = '/remote/password'`, partnerID, otherID)
		repo, err := r.reader.ByKey(ctx, "npm-other")
		if err != nil {
			t.Fatal(err)
		}
		s, _ := repo.Remote()
		_, err = r.clients.For(ctx, repo, s)
		ee := wantCode(t, err, 502, errs.CodeUpstreamSecretUnavailable)
		if strings.Contains(ee.Detail, "rotated") || strings.Contains(ee.Detail, "other-token") {
			t.Fatalf("detail = %q", ee.Detail)
		}
		res, err := r.svc.UpstreamTest(ctx, "npm-other")
		if err != nil || res.Reachable || res.Error == nil || res.Error.Code != "upstream_secret_unavailable" {
			t.Fatalf("test result = %+v, %v", res, err)
		}
		// The other repository is unaffected.
		if res, err := r.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("R-REPO-033 a reference to a trove variable is refused with 422 invalid_secret", func(t *testing.T) {
		for _, ref := range []string{`{"env":"TROVE_DATABASE_PASSWORD"}`, `{"file":"../db-password"}`, `{"file":"/etc/trove/database-password"}`} {
			_, err := r.svc.Create(ctx, caller, []byte(fmt.Sprintf(`{"key":"npm-ref","class":"remote","format":"npmlike","remote":{"url":%q,"authMode":"bearer","password":{"secretRef":%s}}}`, stub.srv.URL, ref)))
			wantCode(t, err, 422, errs.CodeValidation)
			if got := fieldCodes(t, err); got["/remote/password/secretRef"] != "invalid_secret" {
				t.Fatalf("%s: fields = %v", ref, got)
			}
		}
	})

	t.Run("R-REPO-033 a file reference is stored as the reference and read from secrets_dir by the replica that uses it", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(r.dir, "partner-token"), []byte("file-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		st := r.create(t, "npm-file", fmt.Sprintf(`{"url":%q,"authMode":"bearer","password":{"secretRef":{"file":"partner-token"}}}`, stub.srv.URL))
		if !strings.Contains(string(st.Document.Remote), `"password":{"secretRef":{"file":"partner-token"}}`) {
			t.Fatalf("remote = %s", st.Document.Remote)
		}
		if e.scalar(`SELECT count(*) FROM repository_secrets WHERE repo_id = $1 AND sealed IS NULL AND secret_ref = '{"file":"partner-token"}'::jsonb`, r.repoID(t, e, "npm-file")) != 1 {
			t.Fatal("the reference is not stored")
		}
		if res, err := r.svc.UpstreamTest(ctx, "npm-file"); err != nil || !res.Reachable || stub.last(t) != "Bearer file-token" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 the marker of a value that does not exist is 422 invalid_secret", func(t *testing.T) {
		_, err := r.svc.Create(ctx, caller, []byte(fmt.Sprintf(`{"key":"npm-marker","class":"remote","format":"npmlike","remote":{"url":%q,"authMode":"bearer","password":{"set":true}}}`, stub.srv.URL)))
		wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldCodes(t, err); got["/remote/password"] != "invalid_secret" {
			t.Fatalf("fields = %v", got)
		}
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'npm-marker'`) != 0 {
			t.Fatal("a refused create left the repository behind")
		}
	})

	t.Run("R-REPO-033 removing the password removes its row", func(t *testing.T) {
		id := r.repoID(t, e, "npm-basic")
		if _, err := r.svc.Patch(ctx, caller, "npm-basic", repository.MatchAny(), mergePatch(`{"remote":{"authMode":"none","password":null}}`)); err != nil {
			t.Fatal(err)
		}
		if e.scalar(`SELECT count(*) FROM repository_secrets WHERE repo_id = $1`, id) != 0 {
			t.Fatal("the row stayed")
		}
		if res, err := r.svc.UpstreamTest(ctx, "npm-basic"); err != nil || !res.Reachable || stub.last(t) != "" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 a failed update leaves the old secret", func(t *testing.T) {
		_, err := r.svc.Patch(ctx, caller, "npm-partner", repository.MatchAny(), mergePatch(`{"remote":{"password":{"value":"never-stored"},"connectTimeoutMs":1}}`))
		wantCode(t, err, 422, errs.CodeValidation)
		if res, err := r.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable || stub.last(t) != "Bearer rotated-again" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})

	t.Run("R-REPO-033 a rotation reseals the rows under the new key and the repositories keep working", func(t *testing.T) {
		st, err := ring.StatusWithCounts(ctx, listq.SealedCounter{DB: r.db.Main()})
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, k := range st.Keys {
			if k.KeyID == "2026-10" && k.SealedValues != nil {
				n = *k.SealedValues
			}
		}
		if n != 2 { // npm-partner and npm-other
			t.Fatalf("status = %+v", st)
		}
		// Step 2 of the rotation: the new key is active, the old one still opens.
		rotated := writeKeyring(t, "2027-04", "2026-10", "2027-04")
		res, err := repository.ResealSecrets(ctx, r.db, rotated, 1)
		if err != nil {
			t.Fatal(err)
		}
		// npm-other holds the value of npm-partner (copied above): it does not open, so it
		// is reported and left alone, and the walk goes on.
		if res.Resealed != 1 || res.Failed != 1 || res.Current != 0 {
			t.Fatalf("reseal = %+v", res)
		}
		if e.scalar(`SELECT count(*) FROM repository_secrets WHERE sealed LIKE 'v1:2027-04:%'`) != 1 {
			t.Fatal("the value is not under the active key")
		}
		again, err := repository.ResealSecrets(ctx, r.db, rotated, 100)
		if err != nil || again.Resealed != 0 || again.Current != 1 {
			t.Fatalf("a second run is not idempotent: %+v %v", again, err)
		}
		// Step 4: the old key leaves the ring; a replica with only the new key serves it.
		onlyNew := e.newRemoteReplica("secrets-c", writeKeyring(t, "2027-04", "2027-04"), stub.srv)
		if res, err := onlyNew.svc.UpstreamTest(ctx, "npm-partner"); err != nil || !res.Reachable || stub.last(t) != "Bearer rotated-again" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
	})
}

func TestUpstreamChangeHook(t *testing.T) {
	e := newEnv(t)
	stub := newStubUpstream(t)
	r := e.newRemoteReplica("hook", writeKeyring(t, "2026-10", "2026-10"), stub.srv)
	ctx := context.Background()
	var calls []string
	var failNext bool
	r.svc.Bind(repository.RemoteDeps{Ring: r.ring, OnUpstreamChanged: func(_ context.Context, _ meta.Tx, _ repository.Caller, repo repository.Repository) error {
		calls = append(calls, repo.Key)
		if failNext {
			return errors.New("invalidation store unavailable")
		}
		return nil
	}})
	r.create(t, "npm-hook", fmt.Sprintf(`{"url":%q,"authMode":"bearer","password":{"value":"t1"}}`, stub.srv.URL))
	if len(calls) != 0 {
		t.Fatalf("a create called the hook: %v", calls)
	}
	patch := func(p string) {
		t.Helper()
		if _, err := r.svc.Patch(ctx, caller, "npm-hook", repository.MatchAny(), mergePatch(p)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, patch string
		want        int
	}{
		{"a description", `{"description":"x"}`, 0},
		{"a timeout", `{"remote":{"connectTimeoutMs":4000}}`, 0},
		{"the cache periods", `{"remote":{"cache":{"metadataTtlSeconds":10}}}`, 0},
		{"the redirect rule", `{"remote":{"forwardAuthOnRedirect":true}}`, 0},
		{"the url", `{"remote":{"url":"https://other.example.com/"}}`, 1},
		{"the extra hosts", `{"remote":{"extraHosts":[{"pattern":"cdn.example.com"}]}}`, 1},
		{"the user name", `{"remote":{"username":"bot"}}`, 1},
		{"the auth mode", `{"remote":{"authMode":"basic"}}`, 1},
		{"the password", `{"remote":{"password":{"value":"t2"}}}`, 1},
	} {
		t.Run("R-REPO-011 R-REPO-045 a change of "+c.name+" calls the invalidation hook "+fmt.Sprint(c.want)+" times", func(t *testing.T) {
			before := len(calls)
			patch(c.patch)
			if got := len(calls) - before; got != c.want {
				t.Fatalf("calls = %d", got)
			}
		})
	}
	t.Run("R-REPO-011 R-REPO-045 an update that changes nothing does not call it", func(t *testing.T) {
		got, _ := r.svc.Get(ctx, "npm-hook")
		body, _ := json.Marshal(got.Document)
		before := len(calls)
		if _, err := r.svc.Replace(ctx, caller, "npm-hook", repository.MatchAny(), body); err != nil {
			t.Fatal(err)
		}
		if len(calls) != before {
			t.Fatal("called")
		}
	})
	t.Run("R-REPO-045 a hook that fails fails the update with it", func(t *testing.T) {
		failNext = true
		defer func() { failNext = false }()
		before, _ := r.svc.Get(ctx, "npm-hook")
		_, err := r.svc.Patch(ctx, caller, "npm-hook", repository.MatchAny(), mergePatch(`{"remote":{"url":"https://third.example.com/"}}`))
		if err == nil {
			t.Fatal("the update succeeded")
		}
		after, _ := r.svc.Get(ctx, "npm-hook")
		if after.Repository.Version != before.Repository.Version {
			t.Fatal("the update was not rolled back")
		}
	})
}

func TestUpstreamTestAgainstThePrivateUpstream(t *testing.T) {
	e := newEnv(t)
	stub := newStubUpstream(t)
	r := e.newRemoteReplica("probe", writeKeyring(t, "2026-10", "2026-10"), stub.srv)
	ctx := context.Background()
	circuit := func() int64 { return e.scalar(`SELECT count(*) FROM remote_upstream_state`) }
	upstreamEpoch := func() int64 { return e.scalar(`SELECT epoch FROM cluster_epoch WHERE area = 'upstream_state'`) }

	t.Run("R-REPO-024 a reachable upstream is reachable, with its certificate", func(t *testing.T) {
		r.create(t, "npm-up", fmt.Sprintf(`{"url":%q}`, stub.srv.URL))
		res, err := r.svc.UpstreamTest(ctx, "npm-up")
		if err != nil {
			t.Fatal(err)
		}
		if !res.Reachable || res.HTTPStatus == nil || *res.HTTPStatus != 200 || res.Error != nil || res.TLS == nil || res.TLS.PeerSubject == "" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("R-REPO-024 an unknown host is unreachable with a DNS error and leaves the circuit alone", func(t *testing.T) {
		r.create(t, "npm-nx", `{"url":"https://registry.does-not-exist.invalid/"}`)
		beforeRows, beforeEpoch := circuit(), upstreamEpoch()
		for range 10 { // more than availability.failureThreshold
			res, err := r.svc.UpstreamTest(ctx, "npm-nx")
			if err != nil {
				t.Fatal(err)
			}
			if res.Reachable || res.Error == nil || res.Error.Code != "dns_error" {
				t.Fatalf("result = %+v", res)
			}
		}
		if circuit() != beforeRows || upstreamEpoch() != beforeEpoch {
			t.Fatal("the upstream test touched the circuit state")
		}
	})

	t.Run("R-REPO-038 R-REPO-024 http://169.254.169.254/ is saved and the test reports egress_denied", func(t *testing.T) {
		r.create(t, "npm-imds", `{"url":"http://169.254.169.254/latest/"}`)
		res, err := r.svc.UpstreamTest(ctx, "npm-imds")
		if err != nil {
			t.Fatal(err)
		}
		if res.Reachable || res.Error == nil || res.Error.Code != "egress_denied" {
			t.Fatalf("result = %+v", res)
		}
		if circuit() != 0 {
			t.Fatal("an egress refusal reached the circuit")
		}
	})

	t.Run("R-REPO-024 the test of an unsaved section validates, probes and stores nothing", func(t *testing.T) {
		before := e.scalar(`SELECT count(*) FROM repositories`)
		res, err := r.svc.UpstreamTestUnsaved(ctx, []byte(fmt.Sprintf(`{"format":"npmlike","remote":{"url":%q,"authMode":"bearer","password":{"value":"probe-only"}}}`, stub.srv.URL)))
		if err != nil || !res.Reachable || stub.last(t) != "Bearer probe-only" {
			t.Fatalf("%+v %q %v", res, stub.last(t), err)
		}
		if e.scalar(`SELECT count(*) FROM repositories`) != before || e.scalar(`SELECT count(*) FROM repository_secrets WHERE sealed IS NOT NULL AND repo_id NOT IN (SELECT id FROM repositories)`) != 0 {
			t.Fatal("the unsaved test stored something")
		}
		_, err = r.svc.UpstreamTestUnsaved(ctx, []byte(`{"format":"npmlike","remote":{"readTimeoutMs":1}}`))
		wantCode(t, err, 422, errs.CodeValidation)
	})

	t.Run("R-REPO-024 the test of a local repository or a missing one is refused", func(t *testing.T) {
		if _, err := r.svc.Create(ctx, caller, []byte(`{"key":"npm-local","class":"local","format":"npmlike"}`)); err != nil {
			t.Fatal(err)
		}
		_, err := r.svc.UpstreamTest(ctx, "npm-local")
		wantCode(t, err, 400, errs.CodeNotApplicable)
		_, err = r.svc.UpstreamTest(ctx, "nope")
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})

	t.Run("R-REPO-030 an offline repository is not contacted by the test", func(t *testing.T) {
		r.create(t, "npm-off", fmt.Sprintf(`{"url":%q,"availability":{"mode":"offline"}}`, stub.srv.URL))
		before := len(stub.auths())
		res, err := r.svc.UpstreamTest(ctx, "npm-off")
		if err != nil || res.Reachable || res.Error == nil || res.Error.Code != "offline" || len(stub.auths()) != before {
			t.Fatalf("%+v %v (%d requests)", res, err, len(stub.auths())-before)
		}
	})
}
