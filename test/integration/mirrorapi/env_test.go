//go:build integration

// Package mirrorapi tests the status and the run operation of a remote repository's mirror over the management API of a
// started replica (R-REPO-056, R-REPO-057): the real authentication stage, the real decision point, the real repository
// service, the real job queue and the real audit writer. The engine (work package C) is not part of it: the rows of
// mirror_state and the jobs it would write are inserted straight into the database, as the vulnapi suite does for the
// evaluation.
//
// The mirrors are npm remotes (npm's remote adapter implements the lister, R-REPO-050); their repositories are created,
// patched and read through the management API like any other.
package mirrorapi

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// mirrorFormat is the format the suite creates its mirrors of.
const mirrorFormat = "npm"

// env is one installation with these callers and repositories:
//
//	rita   an administrator
//	olga   an auditor: repositories:read-config, not write-config
//	ravi   a repository administrator: repositories:read-config and repositories:write-config
//	mona   manage on npm-mirror and nothing else
//	rex    read on npm-mirror and nothing else
//	dave   no grant at all
//	pam    Owner of the project web, which holds web/web-mirror
//	lena   Maintainer of the project lab, which holds lab/lab-mirror
//
// npm-mirror is a remote in mode mirror, npm-pull one in mode pullThrough, npm-paused a paused mirror, npm-local a local
// repository and npm-all a virtual one.
type env struct {
	t  *testing.T
	st *testenv.Stack
	r  *testenv.Replica

	rita, olga, ravi, mona, rex, dave, pam, lena testenv.Person
	tok                                          map[string]string
}

const upstream = "https://registry.example.org/"

func remoteConfig(mode string, mirror string) string {
	if mirror == "" {
		mirror = "{}"
	}
	return `{"remote":{"url":"` + upstream + `","mode":"` + mode + `","mirror":` + mirror + `}}`
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "mirrorapi"})
	e := &env{t: t, st: st, tok: map[string]string{}}
	e.rita, e.olga, e.ravi, e.mona, e.rex, e.dave = st.User("rita"), st.User("olga"), st.User("ravi"), st.User("mona"), st.User("rex"), st.User("dave")
	e.pam, e.lena = st.User("pam"), st.User("lena")
	st.Bind("admin", e.rita)
	st.Bind("auditor", e.olga)
	st.Bind("repository-admin", e.ravi)

	st.Repository("npm-mirror", "remote", mirrorFormat, remoteConfig("mirror", ""))
	st.Repository("npm-pull", "remote", mirrorFormat, remoteConfig("pullThrough", ""))
	st.Repository("npm-paused", "remote", mirrorFormat, remoteConfig("mirror", `{"paused":true}`))
	st.Repository("npm-bounded", "remote", mirrorFormat, remoteConfig("mirror", `{"maxBytes":107374182400}`))
	st.Repository("npm-state", "remote", mirrorFormat, remoteConfig("mirror", ""))
	st.Repository("web/web-mirror", "remote", mirrorFormat, remoteConfig("mirror", ""))
	st.Repository("lab/lab-mirror", "remote", mirrorFormat, remoteConfig("mirror", ""))
	st.Repository("npm-local", "local", "npm", `{}`)
	st.Repository("npm-all", "virtual", "npm", "")
	// The roles of the projects (Q-AUTHZ-207): an Owner reads a repository's configuration and holds every action on its
	// project's repositories, a Maintainer holds every action on them (R-AUTHZ-026).
	st.Member("web", e.pam, "owner")
	st.Member("lab", e.lena, "maintainer")
	st.Policy("managers", []string{"manage"}, []string{"npm-mirror"}, e.mona)
	st.Policy("readers", []string{"read"}, []string{"npm-mirror"}, e.rex)

	e.tok["rita"] = st.Token(e.rita, scope.Scope{Inherit: true, Admin: true})
	e.tok["olga"] = st.Token(e.olga, scope.Scope{Inherit: true, System: []string{"repositories:read-config"}})
	e.tok["ravi"] = st.Token(e.ravi, scope.Scope{Inherit: true, System: []string{"repositories:read-config", "repositories:write-config"}})
	for name, p := range map[string]testenv.Person{"mona": e.mona, "rex": e.rex, "dave": e.dave} {
		e.tok[name] = st.Token(p, scope.Default())
	}
	// An Owner administers its project through a session or a token with admin true (R-AUTHZ-026); a Maintainer needs none.
	e.tok["pam"] = st.Token(e.pam, scope.Scope{Inherit: true, Admin: true})
	e.tok["lena"] = st.Token(e.lena, scope.Default())
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web1", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"}})
	return e
}

func (e *env) do(who, method, path string, body any, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, method, path, e.tok[who], body, hdr...)
}

func (e *env) person(who string) testenv.Person {
	return map[string]testenv.Person{"rita": e.rita, "olga": e.olga, "ravi": e.ravi, "mona": e.mona, "rex": e.rex, "dave": e.dave, "pam": e.pam, "lena": e.lena}[who]
}

func (e *env) repoID(key string) string {
	e.t.Helper()
	project, k := splitKey(key)
	return e.st.Text("SELECT r.id::text FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2", project, k)
}

// splitKey reads "<project>/<key>" or a bare key of the default project.
func splitKey(key string) (project, k string) {
	if p, rest, ok := strings.Cut(key, "/"); ok {
		return p, rest
	}
	return testenv.DefaultProject, key
}

// qualified is the qualified name audit events carry.
func qualified(key string) string {
	p, k := splitKey(key)
	return p + "/" + k
}

func mirrorPath(key string) string {
	p, k := splitKey(key)
	return "/api/v1/projects/" + p + "/repositories/" + k + "/mirror"
}
func runPath(key string) string                      { return mirrorPath(key) + "/run" }
func (e *env) status(who, key string) testenv.Answer { return e.do(who, "GET", mirrorPath(key), nil) }

func (e *env) wantStatus(a testenv.Answer, status int, code string) {
	e.t.Helper()
	if a.Status != status || (code != "" && a.Code(e.t) != code) {
		e.t.Fatalf("answered %d %s, want %d %s", a.Status, a.Body, status, code)
	}
}
