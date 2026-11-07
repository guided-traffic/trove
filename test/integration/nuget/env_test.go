//go:build integration

package nuget

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// feedBase is the response base URL of the default endpoint of a test replica (server.base_url
// of the stack: plain HTTP).
const feedBase = "http://" + testenv.DefaultHost

// env is one installation with an administrator, a reader and a person without any grant.
type env struct {
	t  *testing.T
	st *testenv.Stack

	tokAdmin, tokReader, tokNobody string
}

// newEnv creates the installation. The people and their grants are seeded before the
// replica starts: its grant index is built from what exists then. The repositories a test
// asks about are created through the management API, because the service is what is tested.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nuget"})
	e := &env{t: t, st: st}
	rita, reader, nobody := st.User("rita"), st.User("reader"), st.User("nobody")
	st.Bind("admin", rita)
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokReader = st.Token(reader, scope.Default())
	e.tokNobody = st.Token(nobody, scope.Default())
	st.Repository("nuget-local", "local", "nuget", "{}")
	st.Repository("gen-local", "local", "generic", "{}")
	st.Policy("nuget-readers", []string{"read"}, []string{"nuget-local", "gen-local"}, reader)
	return e
}

// replica starts a web replica.
func (e *env) replica(name string, env ...string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Env: env})
}

// get sends a GET of a path below the repository's registry URL.
func (e *env) get(r *testenv.Replica, repo, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", "/api/nuget/it/"+repo+"/"+path, token, nil, hdr...)
}

// line checks that an answer is a NuGet error body, one line of text/plain in the form
// "<code>: <message>" (R-NUGET-002), and returns the code.
func line(t *testing.T, a testenv.Answer) string {
	t.Helper()
	if ct := a.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("answer %d has Content-Type %q, want text/plain; charset=utf-8: %s", a.Status, ct, a.Body)
	}
	body := string(a.Body)
	if strings.ContainsAny(body, "\r\n") {
		t.Fatalf("answer %d is not one line: %q", a.Status, body)
	}
	code, msg, ok := strings.Cut(body, ": ")
	if !ok || code == "" || msg == "" || strings.ContainsAny(code, " ") {
		t.Fatalf("answer %d is not \"<code>: <message>\": %q", a.Status, body)
	}
	return code
}

// problemFields maps the pointer of each field error of a problem document to its code.
func problemFields(t *testing.T, a testenv.Answer) map[string]string {
	t.Helper()
	var p struct {
		Errors []struct {
			Pointer string `json:"pointer"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(a.Body, &p); err != nil {
		t.Fatalf("%d is no problem document: %v\n%s", a.Status, err, a.Body)
	}
	out := map[string]string{}
	for _, f := range p.Errors {
		out[f.Pointer] = f.Code
	}
	return out
}
