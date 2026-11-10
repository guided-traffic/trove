//go:build integration

// Package ansiblelocal tests the local class of the Ansible Galaxy format against the integration stack: the publish over HTTP in both
// orders of its parts with the counting of rows, the import tasks, immutability, tombstones and their journaled replay, the three
// read operations, the credentials of the Galaxy clients, permissions and ownership, the reindex and the screens of the package
// views (R-ANS-004 to R-ANS-012, R-ANS-028, R-ANS-029).
package ansiblelocal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	project = "it"
	repo    = "ansible-local"
	base    = "/api/ansible/" + project + "/" + repo + "/"
)

// env is one installation with a local Ansible Galaxy repository and three people: alice may read, deploy and delete, bob may read
// and deploy, carol may only read. ops is the administrator.
type env struct {
	t  *testing.T
	st *testenv.Stack
	// r is the first replica, rs all of them; every replica runs every role.
	r  *testenv.Replica
	rs []*testenv.Replica

	alice, bob, carol testenv.Person
	tokA, tokB, tokC  string
	tokAdmin          string
	tokBlind          string
}

type envOpts struct {
	cfg      string
	replicas int
	valkey   bool
	// seed adds principals, policies and rows before the replicas start, because their grant index is built from what exists then.
	seed func(e *env)
}

func newEnv(t *testing.T) *env { return newEnvOpts(t, envOpts{}) }

func newEnvOpts(t *testing.T, o envOpts) *env {
	t.Helper()
	if o.cfg == "" {
		o.cfg = `{"local":{"packageOwnership":"off"}}`
	}
	if o.replicas == 0 {
		o.replicas = 1
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "ansiblelocal", Valkey: o.valkey, Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	e := &env{t: t, st: st}
	e.alice, e.bob, e.carol = st.User("alice"), st.User("bob"), st.User("carol")
	st.Repository(repo, "local", "ansible", o.cfg)
	st.Policy("ansible-writers", []string{"read", "deploy", "delete"}, []string{repo}, e.alice)
	st.Policy("ansible-deployers", []string{"read", "deploy"}, []string{repo}, e.bob)
	st.Policy("ansible-readers", []string{"read"}, []string{repo}, e.carol)
	e.tokA, e.tokB, e.tokC = st.Token(e.alice, scope.Default()), st.Token(e.bob, scope.Default()), st.Token(e.carol, scope.Default())
	admin := st.User("ops")
	st.Bind("admin", admin)
	e.tokAdmin = st.Token(admin, scope.Scope{Inherit: true, Admin: true})
	if o.seed != nil {
		o.seed(e)
	}
	for i := 1; i <= o.replicas; i++ {
		e.rs = append(e.rs, st.Start(t, testenv.ReplicaOptions{Name: fmt.Sprintf("web-%d", i), Role: "all"}))
	}
	e.r = e.rs[0]
	return e
}

func (e *env) repoID() int64 {
	e.t.Helper()
	return e.st.Number(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2`, project, repo)
}

// spec is the archive of the tests: acme.tools at the version, with a README, a module and a runtime file.
func spec(version string) collectiontest.Spec {
	return collectiontest.Spec{
		Namespace: "acme", Name: "tools", Version: version,
		Dependencies: map[string]string{"community.general": ">=1.0.0"},
		Files: map[string]string{
			"README.md":            "# acme.tools " + version + "\n\nTools for the tests.\n",
			"plugins/modules/x.py": "print('x')\n",
			"meta/runtime.yml":     "---\nrequires_ansible: '>=2.14.0'\n",
		},
	}
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// form builds the multipart body of a publish: sha256 first as ansible-galaxy sends it, or file first as the documentation shows.
func form(archive []byte, shaFirst bool, digest string) (body []byte, contentType string) {
	if digest == "" {
		digest = sum(archive)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	file := func() {
		fw, _ := mw.CreateFormFile("file", "acme-tools.tar.gz")
		_, _ = fw.Write(archive)
	}
	if shaFirst {
		_ = mw.WriteField("sha256", digest)
		file()
	} else {
		file()
		_ = mw.WriteField("sha256", digest)
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

// publish sends an archive with the token as a Bearer credential.
func (e *env) publish(token string, archive []byte, shaFirst bool) testenv.Answer {
	e.t.Helper()
	body, ct := form(archive, shaFirst, "")
	return e.st.Do(e.r, "POST", base+"v3/artifacts/collections/", token, body, "Content-Type", ct)
}

// publishWith sends an archive with the given Authorization header value.
func (e *env) publishWith(auth string, archive []byte) testenv.Answer {
	e.t.Helper()
	body, ct := form(archive, true, "")
	return e.st.Do(e.r, "POST", base+"v3/artifacts/collections/", "", body, "Content-Type", ct, "Authorization", auth)
}

func (e *env) get(token, path string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "GET", base+path, token, nil)
}

// mustPublish publishes acme.tools at a version with alice's token and returns the task document.
func (e *env) mustPublish(version string) map[string]any {
	e.t.Helper()
	a := e.publish(e.tokA, collectiontest.Build(spec(version)), true)
	if a.Status != 202 {
		e.t.Fatalf("publish %s: %d %s", version, a.Status, a.Body)
	}
	return e.task(e.tokA, a)
}

// task follows the address a publish answered and returns the task document.
func (e *env) task(token string, publish testenv.Answer) map[string]any {
	e.t.Helper()
	u, _ := publish.JSON(e.t)["task"].(string)
	i := strings.Index(u, "/api/ansible/")
	if i < 0 {
		e.t.Fatalf("the publish answered %s", publish.Body)
	}
	a := e.st.Do(e.r, "GET", u[i:], token, nil)
	if a.Status != 200 {
		e.t.Fatalf("task %s: %d %s", u, a.Status, a.Body)
	}
	return a.JSON(e.t)
}

func (e *env) versionRows() int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM ansible_collection_versions WHERE repo_id = $1`, e.repoID())
}

func (e *env) eventCount(action string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1`, action)
}

// waitFor polls until the condition holds or the time is over.
func waitFor(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, within)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// specBytes builds the archive of a spec.
func specBytes(s collectiontest.Spec) []byte { return collectiontest.Build(s) }
