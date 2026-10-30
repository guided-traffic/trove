//go:build integration

package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The transfer suites that test the HTTP surface of the files transfer and the
// repository operations (files_test.go, items_test.go, repository_api_test.go,
// recheck_test.go) run on the in-process harness (test/integration/testenv):
// every replica is the production composition root, a caller is a real user with a
// real token, and the decision point, the chain and the wiring are the ones a
// deployment runs. The suites that need a seam inside the transfer, an object
// store that counts and delays, a recorder that fails, storage hooks, a spool they
// inspect or a generic layer built over a replaced reader (commit_test.go,
// races_test.go, scan_test.go, transfer_test.go), keep the component environment
// of env_test.go: they test the item store and the generic layer, which is
// legitimate for a component suite.

// webOpts describe one harness replica.
type webOpts struct {
	// worker makes the replica run jobs (role all: the client listener and the
	// deletion jobs).
	worker bool
	// env is appended to the replica's environment (the storage limits of a test).
	env []string
}

// webEnv is one installation of the harness with the people of the scenario.
type webEnv struct {
	t     *testing.T
	stack *testenv.Stack

	// anonymous is the switch of R-AUTH-037, changed through the management API as
	// the administrator does.
	anonymous *anonymousSwitch

	mu     sync.Mutex
	tokens map[string]string // bearer name -> token
	reps   []*webReplica
}

func newWebEnv(t *testing.T) *webEnv {
	t.Helper()
	// The break-glass account keeps its reserved name; the administrator of the
	// scenario is a user named admin with the admin role.
	s := testenv.NewStack(t, testenv.StackOptions{Name: "files", Env: []string{"TROVE_AUTH_BREAK_GLASS_USERNAME=bg-admin"}})
	e := &webEnv{t: t, stack: s, tokens: map[string]string{}}
	e.anonymous = &anonymousSwitch{e: e}
	return e
}

func (e *webEnv) exec(sql string, args ...any) { e.t.Helper(); e.stack.Exec(sql, args...) }
func (e *webEnv) text(sql string, args ...any) string {
	e.t.Helper()
	return e.stack.Text(sql, args...)
}

func (e *webEnv) number(sql string, args ...any) int64 {
	e.t.Helper()
	return e.stack.Number(sql, args...)
}

// user creates a user (and the groups it belongs to) and a token under its name.
func (e *webEnv) user(name string, groups ...string) person {
	e.t.Helper()
	u := e.stack.User(name, groups...)
	e.mu.Lock()
	// A token exercises the capabilities its scope names that the owner's roles grant
	// (R-AUTHZ-013): the scope names every capability, so the roles decide.
	var caps []string
	for _, c := range authz.Capabilities() {
		caps = append(caps, string(c))
	}
	e.tokens[name] = e.stack.Token(u, scope.Scope{Inherit: true, System: caps})
	e.mu.Unlock()
	return person{ID: u.ID, Name: u.Name, Kind: principal.KindUser}
}

// breakGlass registers the administrator under the bearer name admin: a user that
// holds the admin role (R-AUTHZ-010) with an admin token.
func (e *webEnv) breakGlass() {
	e.t.Helper()
	u := e.stack.User("admin")
	e.stack.Bind("admin", u)
	e.mu.Lock()
	e.tokens["admin"] = e.stack.Token(u, scope.Scope{Inherit: true, Admin: true})
	e.mu.Unlock()
}

// actor is the subject that writes policies and role bindings: a user row, because
// the writer's id is kept as a reference to the principal table.
func (e *webEnv) actor() authz.Subject {
	e.t.Helper()
	e.mu.Lock()
	_, ok := e.tokens["seed-admin"]
	e.mu.Unlock()
	if !ok {
		e.user("seed-admin")
	}
	id := e.text("SELECT id::text FROM principal WHERE kind = 'user' AND name = 'seed-admin'")
	return authz.Subject{Kind: principal.KindUser, ID: id, Name: "seed-admin"}
}

// grant creates a policy that gives the entries their actions on a repository, with
// optional path patterns.
func (e *webEnv) grant(r *webReplica, name string, repos []string, paths authz.PatternLists, entries map[string][]string) {
	e.t.Helper()
	in := authz.PolicyInput{Name: name, Resources: authz.Resources{Repositories: repos, Paths: paths}}
	for who, actions := range entries {
		in.Entries = append(in.Entries, authz.Entry{Principal: who, Actions: actions})
	}
	if _, _, err := r.policy.Create(context.Background(), e.actor(), in); err != nil {
		e.t.Fatalf("policy %s: %v", name, err)
	}
}

// token is the token of a named user; an unknown name is a token the stage does
// not know, which it answers 401 invalid_token.
func (e *webEnv) token(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tok, ok := e.tokens[name]; ok {
		return tok
	}
	return name
}

// webReplica is one replica of the harness with the helpers of a transfer test.
type webReplica struct {
	t       *testing.T
	e       *webEnv
	name    string
	h       *testenv.Replica
	baseURL string
	policy  *authz.Service
	rec     auditView
	obj     objectStore
}

func (e *webEnv) newReplica(name string, o webOpts) *webReplica {
	t := e.t
	t.Helper()
	role := "web"
	if o.worker {
		role = "all"
	}
	env := append([]string{
		"TROVE_CLUSTER_EPOCH_POLL_INTERVAL=200ms", "TROVE_JOBS_POLL_INTERVAL=200ms",
		"TROVE_STORAGE_DELETE_BATCH_SIZE=2", "TROVE_STORAGE_DELETE_MAX_SYNC_ITEMS=10000",
		"TROVE_AUDIT_RECORD_DOWNLOADS=true",
	}, o.env...)
	h := e.stack.Start(t, testenv.ReplicaOptions{Name: name, Role: role, Env: env})
	r := &webReplica{t: t, e: e, name: name, h: h, baseURL: "http://" + h.Addr("default"), policy: h.App.AuthzService()}
	r.rec = auditView{e: e, r: r}
	r.obj = objectStore{r: r}
	e.mu.Lock()
	e.reps = append(e.reps, r)
	e.mu.Unlock()
	return r
}

// do sends a request to the replica as the named user ("" is anonymous). An
// answer of the management API is checked against the document.
func (r *webReplica) do(user, method, path string, body io.Reader, hdr ...string) response {
	r.t.Helper()
	res, err := r.raw(user, method, path, body, hdr...)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		r.t.Fatalf("%s %s: reading the body: %v", method, path, err)
	}
	if strings.HasPrefix(path, "/api/v1/") || strings.HasPrefix(path, "/files/") {
		check, _ := http.NewRequestWithContext(context.Background(), method, "http://"+testenv.DefaultHost+path, nil)
		if _, verr := r.e.stack.Validator().Check(check, res.StatusCode, res.Header, b); verr != nil {
			r.t.Errorf("response validation: %v\n%s", verr, b)
		}
	}
	return response{status: res.StatusCode, header: res.Header, body: b}
}

// raw sends a request and returns the unread answer.
func (r *webReplica) raw(user, method, path string, body io.Reader, hdr ...string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, r.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+r.e.token(user))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return client.Do(req)
}

func (r *webReplica) put(user, path, content string, hdr ...string) response {
	r.t.Helper()
	return r.do(user, "PUT", path, strings.NewReader(content), hdr...)
}

func (r *webReplica) get(user, path string, hdr ...string) response {
	r.t.Helper()
	return r.do(user, "GET", path, nil, hdr...)
}

func (r *webReplica) postJSON(user, path string, body any, hdr ...string) response {
	r.t.Helper()
	b, _ := json.Marshal(body)
	return r.do(user, "POST", path, bytes.NewReader(b), append([]string{"Content-Type", "application/json"}, hdr...)...)
}

// createRepository creates a repository through the management API as admin.
func (r *webReplica) createRepository(key string, extra map[string]any) {
	r.t.Helper()
	body := map[string]any{"key": key, "class": "local", "format": "generic"}
	for k, v := range extra {
		body[k] = v
	}
	if res := r.postJSON("admin", "/api/v1/repositories", body); res.status != 201 {
		r.t.Fatalf("create %s: %d %s", key, res.status, res.body)
	}
}

// flush writes the buffers of the replica now (a replica does it at shutdown).
func (r *webReplica) flush() {
	r.t.Helper()
	if err := r.h.App.Flush(context.Background()); err != nil {
		r.t.Fatalf("flush: %v", err)
	}
}

// auditEvent is an audit event as the audit table holds it.
type auditEvent struct {
	Path       string
	Repository string
	Actor      struct{ Name string }
	Details    string
}

// auditView reads the audit events a replica recorded. The transactional ones
// are in the table when the request is answered; the asynchronous ones (downloads,
// refusals) arrive within the writer's flush interval, so a test that expects one
// waits for it.
type auditView struct {
	e *webEnv
	r *webReplica
}

func (v auditView) ofType(typ string) []auditEvent {
	v.e.t.Helper()
	var out []auditEvent
	v.e.stack.Query(`SELECT COALESCE(path, ''), COALESCE(repository, ''), COALESCE(actor_name, ''), details::text
		   FROM audit_event WHERE replica = $1 AND action = $2 ORDER BY occurred_at, id`,
		func(rows pgx.Rows) error {
			var ev auditEvent
			if err := rows.Scan(&ev.Path, &ev.Repository, &ev.Actor.Name, &ev.Details); err != nil {
				return err
			}
			var compact bytes.Buffer // the text of a jsonb value has spaces; the events carry none
			if json.Compact(&compact, []byte(ev.Details)) == nil {
				ev.Details = compact.String()
			}
			out = append(out, ev)
			return nil
		}, v.r.name, typ)
	return out
}

// anonymousSwitch is the runtime switch of anonymous access (R-AUTH-037, R-AUTH-040).
type anonymousSwitch struct{ e *webEnv }

// Store sets the switch through the management API and waits until every replica
// of the scenario reports it.
func (s *anonymousSwitch) Store(on bool) {
	e := s.e
	e.t.Helper()
	e.mu.Lock()
	reps := append([]*webReplica(nil), e.reps...)
	e.mu.Unlock()
	if len(reps) == 0 {
		e.t.Fatal("anonymous access is set on a running replica")
	}
	// The switch is changed by an administrator of its own, so a scenario need not have one.
	const who = "switch-admin"
	e.mu.Lock()
	_, have := e.tokens[who]
	e.mu.Unlock()
	if !have {
		u := e.stack.User(who)
		// The replicas are running: the binding goes through the policy service, which
		// notifies them, and the switch waits until the first one holds it.
		if _, _, err := reps[0].policy.BindRole(context.Background(), e.actor(), authz.RoleAdmin, "user:"+who); err != nil {
			e.t.Fatalf("bind %s: %v", who, err)
		}
		sub := authz.Subject{Kind: principal.KindUser, ID: u.ID, Name: u.Name}
		testenv.WaitFor(e.t, who+" to hold the admin role", 10*time.Second, func() bool {
			return reps[0].h.App.Authorizer().Require(context.Background(), sub, "settings:write") == nil
		})
		e.mu.Lock()
		e.tokens[who] = e.stack.Token(u, scope.Scope{Inherit: true, Admin: true})
		e.mu.Unlock()
	}
	body, _ := json.Marshal(map[string]any{"anonymousAccess": on})
	res := reps[0].do(who, "PATCH", "/api/v1/settings/auth", bytes.NewReader(body),
		"Content-Type", "application/merge-patch+json", "If-Match", "*")
	if res.status != 200 {
		e.t.Fatalf("set anonymous access: %d %s", res.status, res.body)
	}
	for _, r := range reps {
		testenv.WaitFor(e.t, r.name+" to report anonymous access "+map[bool]string{true: "on", false: "off"}[on], 10*time.Second, func() bool {
			got := r.do(who, "GET", "/api/v1/settings/auth", nil)
			return got.status == 200 && got.json(e.t)["anonymousAccess"] == on
		})
	}
}

// objectStore counts the requests the replica sent to the object store, from the
// upstream log it writes for each one (the readiness probe excluded). It is how a
// test tells an answer from PostgreSQL from one that went to the store.
type objectStore struct{ r *webReplica }

func (o objectStore) count(method string) int {
	n := 0
	for _, line := range strings.Split(o.r.h.Logs.String(), "\n") {
		if strings.Contains(line, `"purpose":"objectstore"`) && strings.Contains(line, `"method":"`+method+`"`) && !strings.Contains(line, "/_probe/") {
			n++
		}
	}
	return n
}

func (o objectStore) gets() int { return o.count("GET") }
func (o objectStore) puts() int { return o.count("PUT") }
