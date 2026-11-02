//go:build integration

package transfer

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// What a project administrator's remote repository may send where (R-AUTHZ-026,
// Q-REPO-127, Q-REPO-128), the delete of a project repository that a virtual
// repository outside the project lists (Q-REPO-129), and the saved upstream test of
// a repository that left the project (Q-AUTHZ-184).

// authStub is a plain-http upstream that answers every request and records the
// Authorization header it got. Plain http keeps the credential on the request: the
// credential policy sends it to an http upstream that is itself http (Q-ARCH-224).
type authStub struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string
}

func newAuthStub(t *testing.T) *authStub {
	t.Helper()
	s := &authStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.got = append(s.got, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *authStub) url() string { return s.srv.URL + "/" }

func (s *authStub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

func (s *authStub) saw(value string) bool { return slices.Contains(s.seen(), value) }

func (s *authStub) requests() int { return len(s.seen()) }

// remoteReplica starts a replica that may reach the stub on the loopback address and
// resolves file references in dir.
func (e *webEnv) remoteReplica(name, dir string) *webReplica {
	e.t.Helper()
	return e.newReplica(name, webOpts{env: []string{
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.0/8","::1/128"]`,
		"TROVE_CONFIG_AS_CODE_SECRETS_DIR=" + dir,
	}})
}

// fieldCode is the code of errors[] at the pointer, "" when there is none.
func fieldCode(t testing.TB, res response, pointer string) string {
	t.Helper()
	list, _ := res.json(t)["errors"].([]any)
	for _, e := range list {
		if m := e.(map[string]any); m["pointer"] == pointer {
			return m["code"].(string)
		}
	}
	return ""
}

func patchRepo(r *webReplica, user, key, patch string) response {
	r.t.Helper()
	return r.do(user, "PATCH", "/api/v1/repositories/"+key, strings.NewReader(patch), "Content-Type", "application/merge-patch+json", "If-Match", "*")
}

// TestR_AUTHZ_026_ADelegatedUpstreamSendsOnlyItsOwnSecret: a project administrator
// cannot route a secret of the installation, or a credential an administrator of trove
// stored, to a host it chooses.
func TestR_AUTHZ_026_ADelegatedUpstreamSendsOnlyItsOwnSecret(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("web", true, true)
	e.projectAdmin("wanda", "web")
	stub := newAuthStub(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "partner-token"), []byte("installation-file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := e.remoteReplica("a", dir)
	remote := func(key string, settings map[string]any) {
		t.Helper()
		res := a.postJSON("admin", "/api/v1/repositories", map[string]any{"key": key, "class": "remote", "format": "npm", "project": "web", "remote": settings})
		if res.status != 201 {
			t.Fatalf("create %s: %d %s", key, res.status, res.body)
		}
	}
	// What the administrator of trove configured for the project: a sealed credential, a
	// reference into secrets_dir, and the outbound path of a partner's network.
	partner := "https://partner.example.test/"
	remote("web-sealed", map[string]any{"url": partner, "authMode": "bearer", "password": map[string]any{"value": "administrator-sealed"}})
	remote("web-hosts", map[string]any{"url": partner, "authMode": "bearer", "password": map[string]any{"value": "administrator-hosts"}})
	remote("web-ref", map[string]any{"url": partner, "authMode": "bearer", "password": map[string]any{"secretRef": map[string]any{"file": "partner-token"}}})
	remote("web-routed", map[string]any{"url": partner, "sourceNetwork": "10.99.0.0/24", "outboundProxy": map[string]any{"mode": "direct"}})
	unsaved := func(user string, settings map[string]any) response {
		return a.postJSON(user, "/api/v1/upstream-tests", map[string]any{"format": "npm", "remote": settings})
	}
	saved := func(user, key string) response {
		return a.do(user, "POST", "/api/v1/repositories/"+key+"/upstream-test", nil)
	}

	t.Run("Q-REPO-127 the unsaved test of a project administrator takes no secret reference", func(t *testing.T) {
		res := unsaved("wanda", map[string]any{"url": stub.url(), "authMode": "bearer", "password": map[string]any{"secretRef": map[string]any{"file": "partner-token"}}})
		if stub.saw("Bearer installation-file-secret") {
			t.Error("the file of secrets_dir reached the host the project administrator named")
		}
		problemOf(t, res, 422, "secret_value_required")
		if got := fieldCode(t, res, "/remote/password/secretRef"); got != "secret_value_required" {
			t.Errorf("errors = %s", res.body)
		}
		if res := unsaved("wanda", map[string]any{"url": stub.url(), "authMode": "bearer", "password": map[string]any{"value": "wanda-own"}}); res.status != 200 || !stub.saw("Bearer wanda-own") {
			t.Fatalf("an inline value is the project administrator's own: %d %s", res.status, res.body)
		}
		// A holder of repositories:write-config keeps the reference (H-281).
		if res := unsaved("admin", map[string]any{"url": stub.url(), "authMode": "bearer", "password": map[string]any{"secretRef": map[string]any{"file": "partner-token"}}}); res.status != 200 {
			t.Fatalf("the administrator: %d %s", res.status, res.body)
		}
	})
	t.Run("Q-REPO-127 a project administrator's write names no secret reference of its own", func(t *testing.T) {
		res := a.postJSON("wanda", "/api/v1/repositories", map[string]any{
			"key": "web-mine", "class": "remote", "format": "npm", "project": "web",
			"remote": map[string]any{"url": stub.url(), "authMode": "bearer", "password": map[string]any{"secretRef": map[string]any{"file": "partner-token"}}},
		})
		problemOf(t, res, 422, "secret_value_required")
		res = patchRepo(a, "wanda", "web-sealed", `{"remote":{"password":{"secretRef":{"file":"partner-token"}}}}`)
		if res.status != 422 || fieldCode(t, res, "/remote/password/secretRef") != "secret_value_required" {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if n := e.number(`SELECT count(*) FROM repositories WHERE key = 'web-mine'`); n != 0 {
			t.Fatal("the refused create left a row")
		}
	})
	t.Run("Q-REPO-127 a document read back keeps the administrator's reference while the url stays", func(t *testing.T) {
		doc := a.get("wanda", "/api/v1/repositories/web-ref").json(t)
		doc["description"] = "kept by the project"
		res := a.do("wanda", "PUT", "/api/v1/repositories/web-ref", jsonBody(t, doc), "Content-Type", "application/json", "If-Match", "*")
		if res.status != 200 {
			t.Fatalf("%d %s", res.status, res.body)
		}
	})
	t.Run("Q-REPO-127 a new url needs the secret value again", func(t *testing.T) {
		before := stub.requests() // the administrator's own test above used the reference
		for _, key := range []string{"web-sealed", "web-ref"} {
			res := patchRepo(a, "wanda", key, `{"remote":{"url":"`+stub.url()+`"}}`)
			if res.status != 422 || res.json(t)["code"] != "secret_value_required" || fieldCode(t, res, "/remote/password") != "secret_value_required" {
				t.Errorf("%s: %d %s", key, res.status, res.body)
			}
			if res := saved("wanda", key); res.status != 200 {
				t.Fatalf("%s: %d %s", key, res.status, res.body)
			}
		}
		if got := stub.seen()[before:]; len(got) != 0 {
			t.Fatalf("a stored credential reached the new url: %v", got)
		}
		res := patchRepo(a, "wanda", "web-sealed", `{"remote":{"url":"`+stub.url()+`","password":{"value":"wanda-new"}}}`)
		if res.status != 200 {
			t.Fatalf("with the value: %d %s", res.status, res.body)
		}
		if res := saved("wanda", "web-sealed"); res.status != 200 || !stub.saw("Bearer wanda-new") {
			t.Fatalf("the saved test sends the new value: %d %v", res.status, stub.seen())
		}
		if res := patchRepo(a, "wanda", "web-ref", `{"remote":{"url":"`+stub.url()+`","authMode":"none","password":null}}`); res.status != 200 {
			t.Fatalf("without a credential the url is the project administrator's: %d %s", res.status, res.body)
		}
	})
	t.Run("Q-REPO-127 a new host that receives credentials, and forwarding them on redirects, need the value again", func(t *testing.T) {
		res := patchRepo(a, "wanda", "web-hosts", `{"remote":{"extraHosts":[{"pattern":"*.evil.example.test","sendCredentials":true}]}}`)
		if res.status != 422 || fieldCode(t, res, "/remote/password") != "secret_value_required" {
			t.Fatalf("a credential host: %d %s", res.status, res.body)
		}
		res = patchRepo(a, "wanda", "web-hosts", `{"remote":{"forwardAuthOnRedirect":true}}`)
		if res.status != 422 || fieldCode(t, res, "/remote/password") != "secret_value_required" {
			t.Fatalf("forwardAuthOnRedirect: %d %s", res.status, res.body)
		}
		if res := patchRepo(a, "wanda", "web-hosts", `{"remote":{"extraHosts":[{"pattern":"*.cdn.example.test","sendCredentials":false}]}}`); res.status != 200 {
			t.Fatalf("a host without credentials: %d %s", res.status, res.body)
		}
		if res := patchRepo(a, "admin", "web-hosts", `{"remote":{"forwardAuthOnRedirect":true}}`); res.status != 200 {
			t.Fatalf("the administrator of trove: %d %s", res.status, res.body)
		}
		if res := patchRepo(a, "wanda", "web-hosts", `{"remote":{"forwardAuthOnRedirect":false}}`); res.status != 200 {
			t.Fatalf("narrowing: %d %s", res.status, res.body)
		}
	})
	t.Run("Q-REPO-128 a project administrator does not choose the outbound path", func(t *testing.T) {
		res := a.postJSON("wanda", "/api/v1/repositories", map[string]any{
			"key": "web-path", "class": "remote", "format": "npm", "project": "web",
			"remote": map[string]any{"url": stub.url(), "sourceNetwork": "10.99.0.0/24", "outboundProxy": map[string]any{"mode": "direct"}},
		})
		problemOf(t, res, 422, "outbound_path_restricted")
		if fieldCode(t, res, "/remote/sourceNetwork") != "outbound_path_restricted" || fieldCode(t, res, "/remote/outboundProxy/mode") != "outbound_path_restricted" {
			t.Fatalf("%s", res.body)
		}
		before := stub.requests()
		res = unsaved("wanda", map[string]any{"url": stub.url(), "sourceNetwork": "10.99.0.0/24"})
		if res.status != 422 || fieldCode(t, res, "/remote/sourceNetwork") != "outbound_path_restricted" || stub.requests() != before {
			t.Fatalf("the unsaved test: %d %s", res.status, res.body)
		}
		if res := patchRepo(a, "wanda", "web-routed", `{"description":"the path stays"}`); res.status != 200 {
			t.Fatalf("a write that keeps the path: %d %s", res.status, res.body)
		}
		res = patchRepo(a, "wanda", "web-routed", `{"remote":{"url":"`+stub.url()+`"}}`)
		if res.status != 422 || fieldCode(t, res, "/remote/sourceNetwork") != "outbound_path_restricted" || fieldCode(t, res, "/remote/outboundProxy/mode") != "outbound_path_restricted" {
			t.Fatalf("a new url on the stored path: %d %s", res.status, res.body)
		}
		res = patchRepo(a, "wanda", "web-routed", `{"remote":{"url":"`+stub.url()+`","sourceNetwork":null,"outboundProxy":{"mode":"default"}}}`)
		if res.status != 200 {
			t.Fatalf("a new url on the installation's path: %d %s", res.status, res.body)
		}
	})
}

// TestR_AUTHZ_026_ADelegatedDeleteLeavesForeignVirtualsAlone: detach=true of a project
// administrator never edits a virtual repository outside the project, and the 409
// names only the project's own (Q-REPO-129).
func TestR_AUTHZ_026_ADelegatedDeleteLeavesForeignVirtualsAlone(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("web", true, true)
	e.projectAdmin("wanda", "web")
	a := e.newReplica("a", webOpts{})
	create := func(body map[string]any) {
		t.Helper()
		if res := a.postJSON("admin", "/api/v1/repositories", body); res.status != 201 {
			t.Fatalf("create %v: %d %s", body["key"], res.status, res.body)
		}
	}
	virtual := func(key, project string, members ...string) map[string]any {
		m := map[string]any{"key": key, "class": "virtual", "format": "npm", "virtual": map[string]any{"members": members, "deployTarget": nil}}
		if project != "" {
			m["project"] = project
		}
		return m
	}
	create(map[string]any{"key": "web-npm", "class": "local", "format": "npm", "project": "web"})
	create(map[string]any{"key": "web-lib", "class": "local", "format": "npm", "project": "web"})
	create(virtual("web-all", "web", "web-npm", "web-lib"))
	create(virtual("company-npm", "", "web-npm")) // the installation's shared virtual repository
	del := func(user, key, query string) response {
		return a.do(user, "DELETE", "/api/v1/repositories/"+key+query, nil, "If-Match", "*")
	}
	members := func(key string) []any {
		return a.get("admin", "/api/v1/repositories/"+key).json(t)["virtual"].(map[string]any)["members"].([]any)
	}

	t.Run("Q-REPO-129 detach by a project administrator is refused while a virtual outside the project lists the repository", func(t *testing.T) {
		for _, q := range []string{"", "?detach=true"} {
			m := problemOf(t, del("wanda", "web-npm", q), 409, "repository_referenced")
			detail, _ := m["detail"].(string)
			if strings.Contains(detail, "company-npm") || !strings.Contains(detail, "web-all") {
				t.Fatalf("%q: the answer names %q", q, detail)
			}
		}
		if got := members("company-npm"); len(got) != 1 || got[0] != "web-npm" {
			t.Fatalf("the shared virtual repository was edited: %v", got)
		}
		if got := members("web-all"); len(got) != 2 {
			t.Fatalf("a refused delete detached: %v", got)
		}
	})
	t.Run("Q-REPO-129 detach by a project administrator edits the project's own virtual repositories", func(t *testing.T) {
		if res := del("wanda", "web-lib", ""); res.status != 409 {
			t.Fatalf("without detach: %d %s", res.status, res.body)
		}
		if res := del("wanda", "web-lib", "?detach=true"); res.status != 202 && res.status != 204 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if got := members("web-all"); len(got) != 1 || got[0] != "web-npm" {
			t.Fatalf("web-all: %v", got)
		}
	})
	t.Run("R-REPO-013 an administrator of trove still detaches everywhere", func(t *testing.T) {
		if res := del("admin", "web-npm", "?detach=true"); res.status != 202 && res.status != 204 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if got := members("company-npm"); len(got) != 0 {
			t.Fatalf("company-npm: %v", got)
		}
	})
}

// TestR_AUTHZ_026_TheSavedUpstreamTestRechecksTheProject: the saved upstream test asks
// the database for the repository's project, as replace, patch and delete do. The
// repository is moved by a statement that tells no replica, so the replica's index
// still names the old project when the handler decides.
func TestR_AUTHZ_026_TheSavedUpstreamTestRechecksTheProject(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("web", true, true)
	e.project("shop", true, true)
	e.projectAdmin("wanda", "web")
	stub := newAuthStub(t)
	a := e.remoteReplica("a", t.TempDir())
	res := a.postJSON("admin", "/api/v1/repositories", map[string]any{"key": "web-moving", "class": "remote", "format": "npm", "project": "web", "remote": map[string]any{"url": stub.url()}})
	if res.status != 201 {
		t.Fatalf("%d %s", res.status, res.body)
	}
	waitFor(t, "the project's administrator to test its repository", 10*time.Second, func() bool {
		return a.do("wanda", "POST", "/api/v1/repositories/web-moving/upstream-test", nil).status == 200
	})
	sent := stub.requests()
	if sent == 0 {
		t.Fatal("the test of the project's repository sent no probe")
	}
	e.exec(`UPDATE repositories SET project_id = (SELECT id FROM project WHERE key = 'shop') WHERE key = 'web-moving'`)
	problemOf(t, a.do("wanda", "POST", "/api/v1/repositories/web-moving/upstream-test", nil), 404, "repository_not_found")
	if n := stub.requests(); n != sent {
		t.Fatalf("the probe of a repository of another project was sent (%d requests, %d before)", n, sent)
	}
}
