//go:build integration

// Package debkeys tests the signing keys of the Debian (APT) repositories against the integration stack (R-DEB-015, R-DEB-017,
// R-DEB-018, R-DEB-032, T-17 WP2): the first key a repository gets in the transaction that creates it, the management API of the keys
// with its permissions, the rotation by a co-signing next key, the sealed private keys, the audit events and journal entries of a key
// change, and the replay of those entries after a restore. The replicas are the production composition root; the replay is run through
// the handlers the domain registered, in a transaction of its own, the way the replayer does.
package debkeys

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const project = testenv.DefaultProject

// env is one installation with a replica and the cast of the scenarios: an administrator, a project maintainer (every repository
// action), a developer and a reader of the project, and a user without any membership.
type env struct {
	t     *testing.T
	st    *testenv.Stack
	r     *testenv.Replica
	db    *postgres.DB
	ring  *secrets.KeyRing
	store *debdata.Store
	// events collects what a store built by the test records outside a request.
	events *captured

	tok map[string]string
	who map[string]testenv.Person
}

// captured records the events a store of the test writes (the audit writer of the replica records those of the requests).
type captured struct{ got []audit.Event }

func (c *captured) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	c.got = append(c.got, ev)
	return nil
}
func (c *captured) Record(context.Context, audit.Event) {}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "debkeys", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	e := &env{t: t, st: st, events: &captured{}, tok: map[string]string{}, who: map[string]testenv.Person{}}
	admin := st.User("ops")
	st.Bind("admin", admin)
	e.add("admin", admin, scope.Scope{Inherit: true, Admin: true})
	for name, role := range map[string]string{"reader": "reader", "developer": "developer", "maintainer": "maintainer"} {
		p := st.User(name)
		st.Member(project, p, role)
		e.add(name, p, scope.Scope{Inherit: true})
	}
	e.add("stranger", st.User("stranger"), scope.Scope{Inherit: true})
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web-1"})

	loaded, err := config.Load(config.Options{Environ: st.Environ(), Command: config.CommandAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if e.ring, err = secrets.Load(loaded.Config.Secrets); err != nil {
		t.Fatal(err)
	}
	if e.db, err = postgres.Open(context.Background(), loaded.Config.Database, "debkeys", meta.RoleWeb, testenv.Quiet()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.db.Close)
	if e.store, err = debdata.New(debdata.Options{DB: e.db, Recorder: e.events, Ring: e.ring}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) add(name string, p testenv.Person, sc scope.Scope) {
	e.who[name] = p
	e.tok[name] = e.st.Token(p, sc)
}

// call sends a request as the person.
func (e *env) call(who, method, path string, body any) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, method, path, e.tok[who], body)
}

func keysPath(repo string, tail ...string) string {
	return "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/signing-keys" + strings.Join(tail, "")
}

// createRepo creates a repository through the management API (so the creation hook runs) as the administrator.
func (e *env) createRepo(key, class, format string) testenv.Answer {
	e.t.Helper()
	return e.call("admin", "POST", "/api/v1/projects/"+project+"/repositories", map[string]any{"key": key, "class": class, "format": format})
}

func (e *env) localRepo(key string) int64 {
	e.t.Helper()
	if a := e.createRepo(key, "local", "deb"); a.Status != 201 {
		e.t.Fatalf("create %s: %d %s", key, a.Status, a.Body)
	}
	return e.repoID(key)
}

func (e *env) repoID(key string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2`, project, key)
}

// keys lists the keys of the repository as the administrator.
func (e *env) keys(repo string) []map[string]any {
	e.t.Helper()
	a := e.call("admin", "GET", keysPath(repo), nil)
	if a.Status != 200 {
		e.t.Fatalf("list %s: %d %s", repo, a.Status, a.Body)
	}
	raw, _ := a.JSON(e.t)["keys"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, k := range raw {
		out = append(out, k.(map[string]any))
	}
	return out
}

// byState returns the key in a state, nil when there is none.
func byState(keys []map[string]any, state string) map[string]any {
	for _, k := range keys {
		if k["state"] == state {
			return k
		}
	}
	return nil
}

func fp(k map[string]any) string {
	s, _ := k["fingerprint"].(string)
	return s
}

// dbState reads a key row straight from the database: the state and whether it still holds a private key.
func (e *env) dbState(fingerprint string) (state string, hasPrivate bool) {
	e.t.Helper()
	state = e.st.Text(`SELECT state FROM deb_signing_keys WHERE fingerprint = $1`, fingerprint)
	hasPrivate = e.st.Number(`SELECT count(*) FROM deb_signing_keys WHERE fingerprint = $1 AND private_key IS NOT NULL`, fingerprint) == 1
	return state, hasPrivate
}

func (e *env) changeSeq(repoID int64, suite string) int64 {
	return e.st.Number(`SELECT change_seq FROM deb_suites WHERE repo_id = $1 AND suite = $2`, repoID, suite)
}

// events counts the audit events of an action of the repository.
func (e *env) eventCount(action, repo string) int64 {
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1 AND repository = $2`, action, project+"/"+repo)
}
