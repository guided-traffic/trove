//go:build integration

package npm

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// env is one installation with two npm local repositories and three people: alice and
// bob may read, deploy and delete, carol may only read.
type env struct {
	t  *testing.T
	st *testenv.Stack

	alice, bob, carol testenv.Person
	tokA, tokB, tokC  string
}

// newEnv creates the installation. The replicas are started by the test, because the
// settings a test changes (the limits of a publish, the retry window) are per replica.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "npm"})
	e := &env{t: t, st: st}
	e.alice, e.bob, e.carol = st.User("alice"), st.User("bob"), st.User("carol")
	st.Repository("npm-local", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`)
	st.Repository("npm-claims", "local", "npm", `{"local":{"autoClaim":true,"packageOwnership":"enforce"}}`)
	st.Repository("npm-noclaim", "local", "npm", `{"local":{"autoClaim":false,"packageOwnership":"enforce"}}`)
	repos := []string{"npm-local", "npm-claims", "npm-noclaim"}
	st.Policy("npm-writers", []string{"read", "deploy", "delete"}, repos, e.alice, e.bob)
	st.Policy("npm-readers", []string{"read"}, repos, e.carol)
	e.tokA, e.tokB, e.tokC = st.Token(e.alice, scope.Default()), st.Token(e.bob, scope.Default()), st.Token(e.carol, scope.Default())
	return e
}

// replica starts a web replica with the given settings.
func (e *env) replica(name string, env ...string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Env: env})
}

// publish sends a publish request.
func (e *env) publish(r *testenv.Replica, repo, token string, p pkg, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "PUT", pubPath(repo, p.Name), token, body(e.t, p), hdr...)
}

// mustPublish publishes and requires 201.
func (e *env) mustPublish(r *testenv.Replica, repo, token string, p pkg) testenv.Answer {
	e.t.Helper()
	a := e.publish(r, repo, token, p)
	if a.Status != 201 {
		e.t.Fatalf("publish %s@%s: %d %s", p.Name, p.Version, a.Status, a.Body)
	}
	return a
}

// code is the code of an npm error body, which also checks its shape (R-NPM-005).
func code(t *testing.T, a testenv.Answer) string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(a.Body, &b); err != nil {
		t.Fatalf("answer %d is no npm error body: %v\n%s", a.Status, err, a.Body)
	}
	for _, k := range []string{"error", "message", "code"} {
		if b[k] == "" {
			t.Fatalf("answer %d lacks %q: %s", a.Status, k, a.Body)
		}
	}
	if b["error"] != b["message"] {
		t.Fatalf("error and message differ: %s", a.Body)
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q on %d", ct, a.Status)
	}
	return b["code"]
}

// versions counts the live versions of a package.
func (e *env) versions(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = $2 AND pv.trashed_at IS NULL`, repo, name)
}

// revSeq is packages.rev_seq of a package, 0 for none.
func (e *env) revSeq(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT COALESCE((SELECT p.rev_seq FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2), 0)`, repo, name)
}

// tags are the dist-tags of a package.
func (e *env) tags(repo, name string) map[string]string {
	e.t.Helper()
	raw := e.st.Text(`SELECT COALESCE((SELECT n.dist_tags::text FROM npm_packages n JOIN packages p ON p.id = n.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = $2), '{}')`, repo, name)
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

// events counts the audit events of a type for a package, by reason ("" for any).
func (e *env) events(typ, pkgName, reason string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1 AND path = $2 AND ($3 = '' OR reason = $3)`, typ, pkgName, reason)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// sortedProps sorts a comma-separated list of key=value items.
func sortedProps(s string) string {
	parts := strings.Split(s, ",")
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// sumSeries adds the values of the series of a metrics text that start with prefix.
func sumSeries(text, prefix string) float64 {
	var sum float64
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			if _, v, found := strings.Cut(rest, "} "); found {
				f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
				sum += f
			}
		}
	}
	return sum
}
