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
//	pam    administrator of the project web (manageRepositories on), which holds web-mirror
//	lena   administrator of the project lab (manageRepositories off), which holds lab-mirror
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
	st.Repository("web-mirror", "remote", mirrorFormat, remoteConfig("mirror", ""))
	st.Repository("lab-mirror", "remote", mirrorFormat, remoteConfig("mirror", ""))
	st.Repository("npm-local", "local", "npm", `{}`)
	st.Repository("npm-all", "virtual", "npm", "")
	for _, p := range []struct {
		key, name string
		manage    bool
		admin     testenv.Person
		repo      string
	}{{"web", "Web", true, e.pam, "web-mirror"}, {"lab", "Lab", false, e.lena, "lab-mirror"}} {
		st.Exec("INSERT INTO project (id, key, display_name, manage_repositories) VALUES (gen_random_uuid(), $1, $2, $3)", p.key, p.name, p.manage)
		st.Exec("INSERT INTO project_admin (project_id, principal_id) SELECT id, $2::uuid FROM project WHERE key = $1", p.key, p.admin.ID)
		st.Exec("UPDATE repositories SET project_id = (SELECT id FROM project WHERE key = $1) WHERE key = $2", p.key, p.repo)
	}
	st.Policy("managers", []string{"manage"}, []string{"npm-mirror"}, e.mona)
	st.Policy("readers", []string{"read"}, []string{"npm-mirror"}, e.rex)

	e.tok["rita"] = st.Token(e.rita, scope.Scope{Inherit: true, Admin: true})
	e.tok["olga"] = st.Token(e.olga, scope.Scope{Inherit: true, System: []string{"repositories:read-config"}})
	e.tok["ravi"] = st.Token(e.ravi, scope.Scope{Inherit: true, System: []string{"repositories:read-config", "repositories:write-config"}})
	for name, p := range map[string]testenv.Person{"mona": e.mona, "rex": e.rex, "dave": e.dave} {
		e.tok[name] = st.Token(p, scope.Default())
	}
	// The administrator of a project exercises it through a token that carries the owner's system rights.
	for name, p := range map[string]testenv.Person{"pam": e.pam, "lena": e.lena} {
		e.tok[name] = st.Token(p, scope.Scope{Inherit: true, Admin: true})
	}
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
	return e.st.Text("SELECT id::text FROM repositories WHERE key = $1", key)
}

func mirrorPath(key string) string                   { return "/api/v1/repositories/" + key + "/mirror" }
func runPath(key string) string                      { return mirrorPath(key) + "/run" }
func (e *env) status(who, key string) testenv.Answer { return e.do(who, "GET", mirrorPath(key), nil) }

func (e *env) wantStatus(a testenv.Answer, status int, code string) {
	e.t.Helper()
	if a.Status != status || (code != "" && a.Code(e.t) != code) {
		e.t.Fatalf("answered %d %s, want %d %s", a.Status, a.Body, status, code)
	}
}
