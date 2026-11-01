//go:build integration

package nuget

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// wenv is the installation of the write tests: a developer who may deploy and delete, a peer who
// may deploy, a reader, and a repository of each deleteMode. The grants are seeded before the
// replica starts, because its grant index is built from what exists then.
type wenv struct {
	t  *testing.T
	st *testenv.Stack

	tokAdmin, tokDev, tokPeer, tokReader, tokNobody string
	devID                                           string
}

func newWriteEnv(t *testing.T) *wenv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetw"})
	e := &wenv{t: t, st: st}
	rita, dev, peer, reader, nobody := st.User("rita"), st.User("dev"), st.User("peer"), st.User("reader"), st.User("nobody")
	st.Bind("admin", rita)
	e.devID = dev.ID
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokDev = st.Token(dev, scope.Default())
	e.tokPeer = st.Token(peer, scope.Default())
	e.tokReader = st.Token(reader, scope.Default())
	e.tokNobody = st.Token(nobody, scope.Default())
	st.Repository("nuget-local", "local", "nuget", "{}")
	st.Repository("nuget-hard", "local", "nuget", `{"nuget":{"deleteMode":"delete"}}`)
	st.Policy("devs", []string{"read", "deploy", "delete"}, []string{"nuget-local", "nuget-hard"}, dev)
	st.Policy("peers", []string{"read", "deploy"}, []string{"nuget-local", "nuget-hard"}, peer)
	st.Policy("readers", []string{"read"}, []string{"nuget-local", "nuget-hard"}, reader)
	return e
}

func (e *wenv) replica(name string, env ...string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Env: env})
}

// pkgSpec describes a package to build.
type pkgSpec struct {
	id, version, description string
	symbols                  bool
	// title, tags and deps put the members the documents and the search read into the .nuspec.
	title, tags string
	deps        bool
}

// nupkg builds a package archive. The description makes two builds of one version differ.
func nupkg(t testing.TB, s pkgSpec) []byte {
	t.Helper()
	if s.description == "" {
		s.description = "Widgets"
	}
	types := ""
	if s.symbols {
		types = `<packageTypes><packageType name="SymbolsPackage"/></packageTypes>`
	}
	extra := types
	if s.title != "" {
		extra += "<title>" + s.title + "</title>"
	}
	if s.tags != "" {
		extra += "<tags>" + s.tags + "</tags>"
	}
	if s.deps {
		extra += `<dependencies><group targetFramework="net8.0"><dependency id="Contoso.Util" version="[1.0.0, 2.0.0)"/></group></dependencies>`
	}
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>%s</id><version>%s</version><authors>Acme</authors><description>%s</description>%s</metadata></package>`,
		s.id, s.version, s.description, extra)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{s.id + ".nuspec": nuspec, "lib/net8.0/" + s.id + ".dll": "MZ" + s.version + s.description} {
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

// form wraps a package the way the NuGet clients send it.
func form(t testing.TB, pkg []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="package"; filename="package"`)
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(pkg)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// push sends PUT v3/package. The API-key header of NuGet clients is not read by the
// authentication stage of this base, so the token goes as a Bearer token.
func (e *wenv) push(r *testenv.Replica, repo, token string, pkg []byte) testenv.Answer {
	e.t.Helper()
	body, ct := form(e.t, pkg)
	return e.st.Do(r, "PUT", "/api/nuget/"+repo+"/v3/package", token, body, "Content-Type", ct)
}

func (e *wenv) mustPush(r *testenv.Replica, repo, token string, pkg []byte) {
	e.t.Helper()
	if ans := e.push(r, repo, token, pkg); ans.Status != 201 {
		e.t.Fatalf("push: %d %s", ans.Status, ans.Body)
	}
}

func (e *wenv) send(r *testenv.Replica, method, repo, path, token string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, method, "/api/nuget/"+repo+"/v3/package/"+path, token, nil)
}

// code is the code of a native error body.
func code(t testing.TB, a testenv.Answer) string {
	t.Helper()
	c, _, _ := strings.Cut(string(a.Body), ": ")
	return c
}

func (e *wenv) versions(repo, id string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = $2 AND pv.trashed_at IS NULL`, repo, id)
}

func (e *wenv) rev(repo, id string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT COALESCE((SELECT p.rev_seq FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2), 0)`, repo, id)
}

func (e *wenv) listed(repo, id, version string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE((SELECT nv.listed::text FROM nuget_version nv JOIN repositories r ON r.id = nv.repository_id
		JOIN package_versions pv ON pv.id = nv.version_id WHERE r.key = $1 AND nv.lower_id = $2 AND pv.version = $3), 'none')`, repo, id, version)
}

func (e *wenv) events(typ, repo, path string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1 AND repository = $2 AND path = $3`, typ, repo, path)
}

func (e *wenv) tombstone(repo, id, version string) (kind, sha512hex string) {
	e.t.Helper()
	kind = e.st.Text(`SELECT COALESCE((SELECT t.kind FROM nuget_tombstone t JOIN repositories r ON r.id = t.repository_id
		WHERE r.key = $1 AND t.lower_id = $2 AND t.lower_version = $3), '')`, repo, id, version)
	sha512hex = e.st.Text(`SELECT COALESCE((SELECT encode(t.sha512, 'hex') FROM nuget_tombstone t JOIN repositories r ON r.id = t.repository_id
		WHERE r.key = $1 AND t.lower_id = $2 AND t.lower_version = $3), '')`, repo, id, version)
	return kind, sha512hex
}

func (e *wenv) files(repo, id, version string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.kind = 'file' AND i.path LIKE $2`,
		repo, id+"/"+version+"/%")
}

func sha512hex(b []byte) string { s := sha512.Sum512(b); return fmt.Sprintf("%x", s[:]) }
