//go:build integration

package backup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
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
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"

	_ "github.com/guided-traffic/trove/internal/formatdata/npm"
	_ "github.com/guided-traffic/trove/internal/formatdata/nuget"
)

// The npm and NuGet parts of backups and restores (R-NPM-047, R-NUGET-033, R-BAK-008, R-NPM-025) against the integration
// stack. The restore itself is the end-to-end suite's; these tests produce the state a restore leaves by SQL and call the
// journal handlers and the purgers the way the restore does, in transactions on the installation's database, while a real
// replica serves the protocol routes on top of it.

// The repositories of the installation.
const (
	repoNPMLocal  = "npm-local"
	repoNPMRemote = "npm-remote"
	repoNPMAll    = "npm-all"
	repoNuGet     = "nuget-local"
	repoNuGetOrg  = "nuget-org"
	project       = "it"
)

// fenv is one installation with a local, a remote and a virtual npm repository, a local and a remote NuGet repository,
// the two counting test upstreams, a developer, a reader and an administrator.
type fenv struct {
	t   *testing.T
	st  *testenv.Stack
	npm *npmstub.Server
	nug *nugetstub.Server
	db  *postgres.DB

	tokDev, tokReader, tokAdmin string
}

func newFormatsEnv(t *testing.T) *fenv {
	t.Helper()
	npmStub, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	npmCA, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	npmRun, err := npmStub.Start("127.0.0.1:0", npmCA.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = npmRun.Close() })

	nugStub, err := nugetstub.New(nugetstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	nugCA, err := nugetstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	nugSrv := httptest.NewUnstartedServer(nugStub)
	nugSrv.TLS = nugCA.ServerConfig()
	nugSrv.StartTLS()
	t.Cleanup(nugSrv.Close)

	ca := filepath.Join(t.TempDir(), "stub-ca.pem")
	if err := os.WriteFile(ca, append(append([]byte{}, npmCA.CAPEM...), nugCA.CAPEM...), 0o600); err != nil {
		t.Fatal(err)
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "bakfmt", Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strconv.Quote(ca) + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_PROXY_LOCK_POLL_INTERVAL_MS=50",
		"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off",
	}})
	e := &fenv{t: t, st: st, npm: npmStub, nug: nugStub}

	dev, reader, admin := st.User("dev"), st.User("reader"), st.User("ops")
	st.Bind("admin", admin)
	st.Repository(repoNPMLocal, "local", "npm", `{"local":{"autoClaim":false,"packageOwnership":"off"}}`)
	st.Repository(repoNPMRemote, "remote", "npm", remoteConfig(npmRun.URL+"/", nil))
	st.Repository(repoNPMAll, "virtual", "npm", `{}`)
	for i, m := range []string{repoNPMLocal, repoNPMRemote} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, repoNPMAll, i, m)
	}
	st.Repository(repoNuGet, "local", "nuget", "{}")
	content := []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}
	st.Repository(repoNuGetOrg, "remote", "nuget", remoteConfig(nugSrv.URL+"/v3/index.json", content))
	locals := []string{repoNPMLocal, repoNuGet}
	st.Policy("writers", []string{"read", "deploy", "delete"}, locals, dev)
	st.Policy("readers", []string{"read"}, []string{repoNPMLocal, repoNPMRemote, repoNPMAll, repoNuGet, repoNuGetOrg}, reader, dev)
	e.tokDev = st.Token(dev, scope.Default())
	e.tokReader = st.Token(reader, scope.Default())
	e.tokAdmin = st.Token(admin, scope.Scope{Inherit: true, Admin: true})

	loaded, err := config.Load(config.Options{Environ: st.Environ(), Command: config.CommandAdmin})
	if err != nil {
		t.Fatal(err)
	}
	e.db, err = postgres.Open(context.Background(), loaded.Config.Database, "bakfmt-restore", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.db.Close)
	return e
}

// remoteConfig is the canonical document of a remote repository that points to a test upstream.
func remoteConfig(url string, extraHosts []any) string {
	if extraHosts == nil {
		extraHosts = []any{}
	}
	doc := map[string]any{"remote": map[string]any{
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
	}}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (e *fenv) replica(name string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name})
}

// repoID is the id of a repository of the installation.
func (e *fenv) repoID(key string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT id FROM repositories WHERE key = $1`, key)
}

// replay applies one entry to the restored database the way the replayer does: in a transaction of its own, as the owner,
// through the handler the domain registered.
func (e *fenv) replay(event string, entry any, at time.Time) journal.Result {
	e.t.Helper()
	res, err := e.tryReplay(event, entry, at)
	if err != nil {
		e.t.Fatalf("replay %s: %v", event, err)
	}
	return res
}

func (e *fenv) tryReplay(event string, entry any, at time.Time) (journal.Result, error) {
	h, ok := journal.HandlerFor(event)
	if !ok {
		e.t.Fatalf("no handler for %s", event)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		e.t.Fatal(err)
	}
	h32 := testenv.RandHex(16)
	id := h32[:8] + "-" + h32[8:12] + "-4" + h32[13:16] + "-8" + h32[17:20] + "-" + h32[20:]
	o := journal.Object{V: journal.Version, ID: id, At: at.UTC(), Event: event, Entry: raw}
	if _, err := o.Encode(); err != nil {
		e.t.Fatalf("object: %v", err)
	}
	var res journal.Result
	err = e.db.InTx(context.Background(), func(tx meta.Tx) error {
		var err error
		res, err = h.Apply(context.Background(), tx, journal.Env{Now: time.Now(), RestoredTo: at.Add(-time.Hour)}, o)
		return err
	})
	return res, err
}

// recorder is an audit writer on the installation's database, for the commands that write events.
func (e *fenv) recorder() audit.Recorder {
	e.t.Helper()
	w, err := audit.New(audit.Options{DB: e.db, Replica: "bakfmt-restore", Config: config.Audit{}, Log: testenv.Quiet(), AppLog: testenv.Quiet()})
	if err != nil {
		e.t.Fatal(err)
	}
	return w
}

// ---- npm ----

type npmPkg struct{ Name, Version, Tag string }

func (p npmPkg) tag() string {
	if p.Tag == "" {
		return "latest"
	}
	return p.Tag
}

// tarball builds a package tarball with the layout the publish check wants.
func (p npmPkg) tarball(t testing.TB) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": p.Name, "version": p.Version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, content := range map[string]string{"package.json": string(raw), "README.md": "# " + p.Name + "\n"} {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// integrity is the SRI string of the tarball.
func integrityOf(b []byte) string {
	s := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(s[:])
}

func baseName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func (p npmPkg) file() string { return baseName(p.Name) + "-" + p.Version + ".tgz" }

func npmPath(repo, name string) string { return "/api/npm/" + project + "/" + repo + "/" + name }

// publish sends the publish request of libnpmpublish and requires 201; it returns the integrity of the tarball.
func (e *fenv) publish(r *testenv.Replica, repo string, p npmPkg) string {
	e.t.Helper()
	a, integrity := e.tryPublish(r, repo, p)
	if a.Status != 201 {
		e.t.Fatalf("publish %s@%s: %d %s", p.Name, p.Version, a.Status, a.Body)
	}
	return integrity
}

func (e *fenv) tryPublish(r *testenv.Replica, repo string, p npmPkg) (testenv.Answer, string) {
	e.t.Helper()
	tgz := p.tarball(e.t)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	integrity := integrityOf(tgz)
	manifest := map[string]any{
		"name": p.Name, "version": p.Version, "_id": p.Name + "@" + p.Version,
		"dist": map[string]any{"integrity": integrity, "shasum": hex.EncodeToString(s1[:]), "tarball": "http://x/" + p.Name + "/-/" + p.file()},
	}
	doc := map[string]any{
		"_id": p.Name, "name": p.Name, "dist-tags": map[string]string{p.tag(): p.Version},
		"versions": map[string]any{p.Version: manifest},
		"_attachments": map[string]any{p.Name + "/-/" + p.file(): map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.st.Do(r, "PUT", npmPath(repo, p.Name), e.tokDev, raw), integrity
}

func (e *fenv) npmGet(r *testenv.Replica, repo, path string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", npmPath(repo, path), e.tokReader, nil)
}

// npmDoc reads the versions and the dist-tags a package document lists.
func (e *fenv) npmDoc(r *testenv.Replica, repo, name string) (versions []string, tags map[string]string) {
	e.t.Helper()
	a := e.npmGet(r, repo, name)
	if a.Status != 200 {
		e.t.Fatalf("GET %s/%s: %d %s", repo, name, a.Status, a.Body)
	}
	var d struct {
		Versions map[string]json.RawMessage `json:"versions"`
		Tags     map[string]string          `json:"dist-tags"`
	}
	if err := json.Unmarshal(a.Body, &d); err != nil {
		e.t.Fatal(err)
	}
	for v := range d.Versions {
		versions = append(versions, v)
	}
	return versions, d.Tags
}

// unpublishEntry is the journal entry of npm.unpublish as the producer writes it (R-NPM-047).
func (e *fenv) unpublishEntry(repo, name, version, integrity string) map[string]any {
	return map[string]any{
		"repositoryId": e.repoID(repo), "package": name, "version": version, "integrity": integrity,
		"actorId": e.st.Text(`SELECT id::text FROM principal WHERE name = 'dev'`),
	}
}

// tombstone reads a tombstone: its source, integrity and journal entry, "" for each when there is none.
func (e *fenv) tombstone(repo, name, version string) (source, integrity, entry string) {
	e.t.Helper()
	q := func(col string) string {
		return e.st.Text(`SELECT COALESCE((SELECT t.`+col+`::text FROM npm_tombstones t JOIN repositories r ON r.id = t.repo_id
			WHERE r.key = $1 AND t.name = $2 AND t.version = $3), '')`, repo, name, version)
	}
	return q("source"), q("integrity"), q("journal_entry")
}

func (e *fenv) revSeq(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT COALESCE((SELECT p.rev_seq FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2), 0)`, repo, name)
}

// storedVersions counts the version rows of a package that are not trashed: a withheld version is one of them.
func (e *fenv) storedVersions(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = $2 AND pv.trashed_at IS NULL`, repo, name)
}

// items counts the files of a repository below a path prefix.
func (e *fenv) items(repo, prefix string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`, repo, prefix+"%")
}

// ---- NuGet ----

// nupkg builds a package archive. The description makes two builds of one version differ.
func nupkg(t testing.TB, id, version, description string) []byte {
	t.Helper()
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>%s</id><version>%s</version><authors>Acme</authors><description>%s</description></metadata></package>`,
		id, version, description)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{id + ".nuspec": nuspec, "lib/net8.0/" + id + ".dll": "MZ" + version + description} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha512hex(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

func (e *fenv) pushNuGet(r *testenv.Replica, pkg []byte) testenv.Answer {
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
	_, _ = part.Write(pkg)
	if err := mw.Close(); err != nil {
		e.t.Fatal(err)
	}
	return e.st.Do(r, "PUT", "/api/nuget/"+project+"/"+repoNuGet+"/v3/package", e.tokDev, buf.Bytes(), "Content-Type", mw.FormDataContentType())
}

func (e *fenv) mustPushNuGet(r *testenv.Replica, pkg []byte) {
	e.t.Helper()
	if a := e.pushNuGet(r, pkg); a.Status != 201 {
		e.t.Fatalf("push: %d %s", a.Status, a.Body)
	}
}

func (e *fenv) nugetGet(r *testenv.Replica, repo, path string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, http.MethodGet, "/api/nuget/"+project+"/"+repo+"/"+path, e.tokReader, nil)
}

// nugetEntry is the entry of package.published and package.deleted (R-NUGET-033).
func (e *fenv) nugetEntry(id, version string, sum string) map[string]any {
	return map[string]any{"repository": e.repoID(repoNuGet), "id": id, "version": version, "sha512": sum}
}

func (e *fenv) nugetTombstone(id, version string) (kind, sha string) {
	e.t.Helper()
	kind = e.st.Text(`SELECT COALESCE((SELECT t.kind FROM nuget_tombstone t JOIN repositories r ON r.id = t.repository_id
		WHERE r.key = $1 AND t.lower_id = $2 AND t.lower_version = $3), '')`, repoNuGet, id, version)
	sha = e.st.Text(`SELECT COALESCE((SELECT encode(t.sha512, 'hex') FROM nuget_tombstone t JOIN repositories r ON r.id = t.repository_id
		WHERE r.key = $1 AND t.lower_id = $2 AND t.lower_version = $3), '')`, repoNuGet, id, version)
	return kind, sha
}

func (e *fenv) nugetVersions(id string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = $2 AND pv.trashed_at IS NULL`, repoNuGet, id)
}

func nugetCode(a testenv.Answer) string {
	c, _, _ := strings.Cut(string(a.Body), ": ")
	return c
}
