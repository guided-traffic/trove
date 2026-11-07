//go:build integration

// Package querybudget tests the database budget of a download (R-ARCH-027) against the integration stack: how many
// PostgreSQL statements a replica sends to serve a stored file, counted from the database's log, per route and per format. The
// replicas are started through the composition root, so the real chain, the formats, the item store and the remote engine run.
package querybudget

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
	"github.com/guided-traffic/trove/test/nugetstub"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// The repositories of the installation.
const (
	rNPM      = "npm-local"
	rNPMRem   = "npm-remote"
	rNPMVirt  = "npm-virtual"
	rNuGet    = "nuget-local"
	rNuGetRem = "nuget-remote"
	rNuGetVrt = "nuget-virtual"
)

// env is one installation with a local, a remote and a virtual repository of each of npm and NuGet, a stub upstream of each
// and a reader.
type env struct {
	t         *testing.T
	st        *testenv.Stack
	npmStub   *npmstub.Server
	nugetStub *nugetstub.Server
	npmURL    string
	nugetURL  string
	tokAdmin  string
	tokReader string
	// pkg and ver are a package and version of the npm upstream's generated corpus.
	pkg, ver string
}

func newEnv(t *testing.T) *env { return newEnvWith(t, false) }

// newEnvWith is newEnv with Valkey for the replicas, which collapses concurrent fills across them.
func newEnvWith(t *testing.T, valkey bool) *env {
	t.Helper()
	dir := t.TempDir()

	npmSrv, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	npmCert, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	npmRun, err := npmSrv.Start("127.0.0.1:0", npmCert.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = npmRun.Close() })
	npmCA := filepath.Join(dir, "npm-ca.pem")
	if err := os.WriteFile(npmCA, npmCert.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	nugetSrv, err := nugetstub.New(nugetstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	nugetCert, err := nugetstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	nuget := httptest.NewUnstartedServer(nugetSrv)
	nuget.TLS = nugetCert.ServerConfig()
	nuget.StartTLS()
	t.Cleanup(nuget.Close)
	nugetCA := filepath.Join(dir, "nuget-ca.pem")
	if err := os.WriteFile(nugetCA, nugetCert.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	st := testenv.NewStack(t, testenv.StackOptions{Name: "querybudget", Valkey: valkey, Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(npmCA) + "," + strconv.Quote(nugetCA) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
	}})
	e := &env{t: t, st: st, npmStub: npmSrv, nugetStub: nugetSrv, npmURL: npmRun.URL + "/", nugetURL: nuget.URL + "/v3/index.json"}
	for _, p := range corpus.Default().Packages {
		if !strings.HasPrefix(p.Name, "@") && len(p.Versions) > 0 && len(p.Versions) < 30 {
			e.pkg, e.ver = p.Name, p.Versions[0].Version
			break
		}
	}
	if e.pkg == "" {
		t.Fatal("the upstream's corpus has no plain package")
	}
	rita, reader := st.User("rita"), st.User("reader")
	st.Bind("admin", rita)
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokReader = st.Token(reader, scope.Default())

	st.Repository(rNPM, "local", "npm", `{}`)
	st.Repository(rNPMRem, "remote", "npm", e.remoteConfig(e.npmURL, nil))
	st.Repository(rNuGet, "local", "nuget", `{}`)
	content := []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}
	st.Repository(rNuGetRem, "remote", "nuget", e.remoteConfig(e.nugetURL, content))
	virtual := func(key, format string, members ...string) {
		st.Repository(key, "virtual", format, `{}`)
		for i, m := range members {
			st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
				SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, key, i, m)
		}
	}
	virtual(rNPMVirt, "npm", rNPM, rNPMRem)
	virtual(rNuGetVrt, "nuget", rNuGet, rNuGetRem)
	all := []string{rNPM, rNPMRem, rNPMVirt, rNuGet, rNuGetRem, rNuGetVrt}
	st.Policy("readers", []string{"read"}, all, reader)
	st.Policy("writers", []string{"read", "deploy", "delete"}, []string{rNPM, rNuGet}, rita)
	st.Policy("fillers", []string{"read"}, all, rita)
	return e
}

// remoteConfig is the canonical document of a remote repository that points to a test upstream.
func (e *env) remoteConfig(url string, extraHosts []any) string {
	if extraHosts == nil {
		extraHosts = []any{}
	}
	remote := map[string]any{
		"url": url, "extraHosts": extraHosts, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 3600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}
	b, err := json.Marshal(map[string]any{"remote": remote})
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) do(r *testenv.Replica, path string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", path, e.tokReader, nil)
}

func npmTarballPath(repo, name, version string) string {
	return "/api/npm/it/" + repo + "/" + name + "/-/" + name + "-" + version + ".tgz"
}

func nupkgPath(repo, id, version string) string {
	id = strings.ToLower(id)
	return "/api/nuget/it/" + repo + "/v3/flatcontainer/" + id + "/" + version + "/" + id + "." + version + ".nupkg"
}

// tarball builds a package tarball.
func tarball(t testing.TB, name, version string) []byte {
	t.Helper()
	pj, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range [][2]string{{"package/package.json", string(pj)}, {"package/README.md", "# " + name + "\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f[1]))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// publish stores name@version in a local npm repository.
func (e *env) publish(r *testenv.Replica, repo, name, version string) {
	e.t.Helper()
	tgz := tarball(e.t, name, version)
	s512, s1 := sha512.Sum512(tgz), sha1.Sum(tgz) //nolint:gosec // see the import
	file := name + "-" + version + ".tgz"
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version, "_id": name + "@" + version,
			"dist": map[string]any{
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:]),
				"tarball": "http://x/" + name + "/-/" + file,
			},
		}},
		"access": nil,
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	if a := e.st.Do(r, "PUT", "/api/npm/it/"+repo+"/"+name, e.tokAdmin, doc); a.Status != http.StatusCreated {
		e.t.Fatalf("publish %s@%s to %s: %d %s", name, version, repo, a.Status, a.Body)
	}
}

// nupkg builds a NuGet package.
func nupkg(t testing.TB, id, version string) []byte {
	t.Helper()
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>%s</id><version>%s</version><authors>Acme</authors><description>Widgets</description></metadata></package>`, id, version)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{id + ".nuspec", nuspec}, {"lib/net8.0/" + id + ".dll", "MZ" + version}} {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(f[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// push stores a package in a local NuGet repository.
func (e *env) push(r *testenv.Replica, repo, id, version string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="package"; filename="package"`)
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = part.Write(nupkg(e.t, id, version))
	_ = mw.Close()
	if a := e.st.Do(r, "PUT", "/api/nuget/it/"+repo+"/v3/package", e.tokAdmin, buf.Bytes(), "Content-Type", mw.FormDataContentType()); a.Status != http.StatusCreated {
		e.t.Fatalf("push %s %s to %s: %d %s", id, version, repo, a.Status, a.Body)
	}
}

// itemPath is the stored path of the first live item of a repository that ends in a suffix.
func (e *env) itemPath(repo, suffix string) string {
	e.t.Helper()
	p := e.st.Text(`SELECT COALESCE((SELECT i.path FROM items i JOIN repositories r ON r.id = i.repo_id
		WHERE r.key = $1 AND i.path LIKE '%' || $2 AND i.trashed_at IS NULL ORDER BY i.id LIMIT 1), '')`, repo, suffix)
	if p == "" {
		e.t.Fatalf("%s holds no item that ends in %s", repo, suffix)
	}
	return p
}
