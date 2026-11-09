//go:build integration

// Package deblocal tests the local class of the Debian (APT) format against the integration stack: the upload with its suites and
// components, the coordinates and their change through the management API, the deletion and its journaled event, the withheld file
// and its reinstatement (R-DEB-006, R-DEB-009, R-DEB-013, R-DEB-032).
package deblocal

import (
	"net/url"
	"os"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/test/debstub/debgen"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	project   = "it"
	repo      = "deb-local"
	debMedia  = "application/vnd.debian.binary-package"
	poolHello = "pool/h/hello/hello_2.10-3_amd64.deb"
)

// env is one installation with a local Debian repository and three people: alice may read, deploy and delete, bob may read and
// deploy, carol may only read.
type env struct {
	t  *testing.T
	st *testenv.Stack
	r  *testenv.Replica

	alice, bob, carol testenv.Person
	tokA, tokB, tokC  string
	tokAdmin          string
}

// newEnv is the installation with package ownership off; newOwnedEnv enforces it, so the first upload of a name claims it.
func newEnv(t *testing.T) *env { return newEnvWith(t, "{}") }

func newOwnedEnv(t *testing.T) *env { return newEnvWith(t, `{"local":{"packageOwnership":"enforce"}}`) }

func newEnvWith(t *testing.T, cfg string) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "deblocal", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	e := &env{t: t, st: st}
	e.alice, e.bob, e.carol = st.User("alice"), st.User("bob"), st.User("carol")
	st.Repository(repo, "local", "deb", cfg)
	st.Policy("deb-writers", []string{"read", "deploy", "delete"}, []string{repo}, e.alice)
	st.Policy("deb-deployers", []string{"read", "deploy"}, []string{repo}, e.bob)
	st.Policy("deb-readers", []string{"read"}, []string{repo}, e.carol)
	e.tokA, e.tokB, e.tokC = st.Token(e.alice, scope.Default()), st.Token(e.bob, scope.Default()), st.Token(e.carol, scope.Default())
	admin := st.User("ops")
	st.Bind("admin", admin)
	e.tokAdmin = st.Token(admin, scope.Scope{Inherit: true, Admin: true})
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web-1", Role: "all", Env: []string{"TROVE_DEB_INDEX_WAIT_MAX=2s", "TROVE_DEB_INDEX_DELAY=1s"}})
	e.primaryKey()
	return e
}

// primaryKey gives the repository its first signing key. The stack seeds the repository by SQL, so the creation hook that generates
// the key did not run: a generated next key is promoted, which a repository without a primary allows (R-DEB-017).
func (e *env) primaryKey() {
	e.t.Helper()
	keys := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/signing-keys"
	if a := e.st.Do(e.r, "POST", keys, e.tokAdmin, map[string]any{"generate": true}); a.Status/100 != 2 {
		e.t.Fatalf("create the first key: %d %s", a.Status, a.Body)
	}
	if a := e.st.Do(e.r, "POST", keys+"/promote", e.tokAdmin, nil); a.Status/100 != 2 {
		e.t.Fatalf("promote the first key: %d %s", a.Status, a.Body)
	}
}

func build(t *testing.T, name, version string, mod func(*debgen.Spec)) []byte {
	t.Helper()
	s := debgen.Hello(name, version, "amd64")
	if mod != nil {
		mod(&s)
	}
	d, err := debgen.Build(s)
	if err != nil {
		t.Fatal(err)
	}
	return d.Bytes
}

func uploadPath(file string, dists []string, comps ...string) string {
	q := url.Values{}
	for _, d := range dists {
		q.Add("distribution", d)
	}
	for _, c := range comps {
		q.Add("component", c)
	}
	return "/api/deb/" + project + "/" + repo + "/upload/" + file + "?" + q.Encode()
}

// upload sends a package file.
func (e *env) upload(token, file string, body []byte, dists []string, comps ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "PUT", uploadPath(file, dists, comps...), token, body, "Content-Type", debMedia)
}

func (e *env) mustUpload(token, file string, body []byte, want int, dists []string, comps ...string) map[string]any {
	e.t.Helper()
	a := e.upload(token, file, body, dists, comps...)
	if a.Status != want {
		e.t.Fatalf("upload %s: %d %s, want %d", file, a.Status, a.Body, want)
	}
	return a.JSON(e.t)
}

func (e *env) coordinatesPath() string {
	return "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/coordinates"
}

func (e *env) setCoordinates(token, path string, coords ...[2]string) testenv.Answer {
	e.t.Helper()
	list := make([]map[string]string, 0, len(coords))
	for _, c := range coords {
		list = append(list, map[string]string{"distribution": c[0], "component": c[1]})
	}
	return e.st.Do(e.r, "PUT", e.coordinatesPath(), token, map[string]any{"path": path, "coordinates": list})
}

// coordinates are the stored coordinates of the file at path, "suite/component" sorted.
func (e *env) coordinates(path string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE(string_agg(c.suite || '/' || c.component, ',' ORDER BY c.suite, c.component), '')
		FROM deb_coordinates c JOIN deb_binaries b ON b.item_id = c.item_id JOIN items i ON i.id = b.item_id
		JOIN repositories r ON r.id = b.repo_id WHERE r.key = $1 AND i.path = $2`, repo, path)
}

func (e *env) changeSeq(suite string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT COALESCE((SELECT s.change_seq FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.suite = $2), 0)`, repo, suite)
}

func (e *env) events(typ string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1 AND repository = $2`, typ, project+"/"+repo)
}
