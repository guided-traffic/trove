//go:build integration

package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// The repository operations of projects (R-AUTHZ-026, Q-AUTHZ-173, Q-AUTHZ-178,
// Q-AUTHZ-184). The tests of a project administrator over HTTP need the decision
// point to know projects (the engine work package); each says so and is skipped by
// requireProjectEngine until it does. The service tests and the tests of a holder of
// repositories:write-config run today.

// project inserts a project row. It runs before the replica that must know it
// starts: the decision point builds its index from PostgreSQL when it starts.
func (e *webEnv) project(key string, managePolicies, manageRepositories bool) {
	e.t.Helper()
	e.exec(`INSERT INTO project (id, key, display_name, manage_policies, manage_repositories)
		VALUES (gen_random_uuid(), $1, $1, $2, $3)`, key, managePolicies, manageRepositories)
}

// setSwitches changes the switches of a project; a replica started afterwards builds
// its index with them.
func (e *webEnv) setSwitches(key string, managePolicies, manageRepositories bool) {
	e.t.Helper()
	e.exec(`UPDATE project SET manage_policies = $2, manage_repositories = $3, version = version + 1 WHERE key = $1`, key, managePolicies, manageRepositories)
}

// projectAdmin creates a user that administers the projects through a token with
// admin: true (Q-AUTHZ-183) and holds no capability of its own. The bearer name is the
// user's name.
func (e *webEnv) projectAdmin(name string, projects ...string) {
	e.t.Helper()
	u := e.stack.User(name)
	for _, p := range projects {
		e.exec(`INSERT INTO project_admin (project_id, principal_id) SELECT id, $2::uuid FROM project WHERE key = $1`, p, u.ID)
	}
	e.mu.Lock()
	e.tokens[name] = e.stack.Token(u, scope.Scope{Inherit: true, Admin: true})
	e.mu.Unlock()
}

// requireProjectEngine skips a test that needs the decision point to know the caller
// as a project administrator: view=full is answered 403 to a caller that holds no
// capability and administers no project.
func requireProjectEngine(t *testing.T, r *webReplica, user string) {
	t.Helper()
	if r.get(user, "/api/v1/repositories?view=full").status == 200 {
		return
	}
	t.Skip("the grant index does not know projects yet (the engine work package)")
}

func fieldsOf(t testing.TB, err error) map[string]string {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("not a problem: %v", err)
	}
	out := map[string]string{}
	for _, f := range e.Fields {
		out[f.Pointer] = f.Code
	}
	return out
}

// TestR_AUTHZ_026_RepositoryProjectMember: the member `project` of the document, as a
// holder of repositories:write-config uses it.
func TestR_AUTHZ_026_RepositoryProjectMember(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("acme", true, false)
	e.project("other", true, false)
	a := e.newReplica("a", webOpts{})
	create := func(m map[string]any) response { return a.postJSON("admin", "/api/v1/repositories", m) }

	t.Run("R-AUTHZ-026 the create assigns the repository in the same transaction, and any key is allowed to the administrator", func(t *testing.T) {
		res := create(map[string]any{"key": "plain-one", "class": "local", "format": "generic", "project": "acme"})
		if res.status != 201 || res.json(t)["project"] != "acme" {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if got := e.text(`SELECT p.key FROM repositories r JOIN project p ON p.id = r.project_id WHERE r.key = 'plain-one'`); got != "acme" {
			t.Fatalf("assigned to %q", got)
		}
		res = create(map[string]any{"key": "free-one", "class": "local", "format": "generic"})
		if res.status != 201 || res.json(t)["project"] != nil {
			t.Fatalf("a repository without a project has project null: %d %s", res.status, res.body)
		}
		violation(t, create(map[string]any{"key": "no-such", "class": "local", "format": "generic", "project": "nope"}), "unknown_project")
		if n := e.number(`SELECT count(*) FROM repositories WHERE key = 'no-such'`); n != 0 {
			t.Fatal("a refused create left a row")
		}
		problemOf(t, create(map[string]any{"key": "bad-type", "class": "local", "format": "generic", "project": 3}), 400, "malformed_request")
	})
	t.Run("R-AUTHZ-026 the read and the list show the project, and the filter selects it", func(t *testing.T) {
		if got := a.get("admin", "/api/v1/repositories/plain-one").json(t)["project"]; got != "acme" {
			t.Errorf("get: %v", got)
		}
		page := a.get("admin", "/api/v1/repositories?project=acme").json(t)
		items := page["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["key"] != "plain-one" || items[0].(map[string]any)["project"] != "acme" {
			t.Fatalf("project=acme: %v", items)
		}
		if items := a.get("admin", "/api/v1/repositories?project=other").json(t)["items"].([]any); len(items) != 0 {
			t.Errorf("project=other: %v", items)
		}
		if items := a.get("admin", "/api/v1/repositories?project=nope").json(t)["items"].([]any); len(items) != 0 {
			t.Errorf("an unknown project selects none: %v", items)
		}
		full := a.get("admin", "/api/v1/repositories?view=full&project=acme").json(t)["items"].([]any)
		if len(full) != 1 || full[0].(map[string]any)["project"] != "acme" {
			t.Errorf("view=full: %v", full)
		}
		for _, it := range a.get("admin", "/api/v1/repositories?limit=100").json(t)["items"].([]any) {
			m := it.(map[string]any)
			if m["key"] == "free-one" && m["project"] != nil {
				t.Errorf("free-one: %v", m)
			}
		}
	})
	t.Run("Q-AUTHZ-184 the project is read-only on a replace and a patch", func(t *testing.T) {
		doc := a.get("admin", "/api/v1/repositories/plain-one")
		put := func(project any) response {
			m := doc.json(t)
			m["project"] = project
			return a.do("admin", "PUT", "/api/v1/repositories/plain-one", jsonBody(t, m), "Content-Type", "application/json", "If-Match", "*")
		}
		if res := put("acme"); res.status != 200 {
			t.Fatalf("a body obtained by GET goes back: %d %s", res.status, res.body)
		}
		violation(t, put("other"), "immutable")
		violation(t, put(nil), "immutable")
		patch := func(body string) response {
			return a.do("admin", "PATCH", "/api/v1/repositories/plain-one", strings.NewReader(body), "Content-Type", "application/merge-patch+json", "If-Match", "*")
		}
		if res := patch(`{"project":"acme","description":"kept"}`); res.status != 200 || res.json(t)["project"] != "acme" {
			t.Fatalf("%d %s", res.status, res.body)
		}
		violation(t, patch(`{"project":"other"}`), "immutable")
		violation(t, patch(`{"project":null}`), "immutable")
		if res := a.do("admin", "PATCH", "/api/v1/repositories/free-one", strings.NewReader(`{"project":null,"description":"x"}`),
			"Content-Type", "application/merge-patch+json", "If-Match", "*"); res.status != 200 {
			t.Fatalf("null for a repository without a project: %d %s", res.status, res.body)
		}
		if got := e.text(`SELECT p.key FROM repositories r JOIN project p ON p.id = r.project_id WHERE r.key = 'plain-one'`); got != "acme" {
			t.Fatalf("the project moved to %q", got)
		}
	})
}

// TestR_AUTHZ_026_ProjectAdministratorRepositoryWrites is the service-level rule set of
// a project administrator's repository writes, on the real database and the real
// service: the handler has decided the right; the service applies the prefix, the
// assignment and the virtual rule (Q-AUTHZ-178).
func TestR_AUTHZ_026_ProjectAdministratorRepositoryWrites(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("web", true, true)
	e.project("shop", true, true)
	a := e.newReplica("a", webOpts{})
	svc := a.h.App.RepositoryService()
	ctx := context.Background()
	caller := repository.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}
	web := repository.Write{Project: ptr("web"), Delegate: "web"}
	doc := func(key, class, extra string) []byte {
		var members []string
		if class == "remote" {
			members = append(members, `"remote":{"url":"https://registry.example.test/"}`)
		}
		if extra != "" {
			members = append(members, extra)
		}
		tail := ""
		if len(members) > 0 {
			tail = "," + strings.Join(members, ",")
		}
		return []byte(fmt.Sprintf(`{"key":%q,"class":%q,"format":"npm"%s}`, key, class, tail))
	}
	mustCreate := func(w repository.Write, key, class, extra string) repository.Stored {
		t.Helper()
		st, err := svc.CreateIn(ctx, caller, doc(key, class, extra), w)
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
		return st
	}
	// What the administrator of trove placed: a repository of another project, an unassigned one.
	mustCreate(repository.Write{Project: ptr("shop")}, "shop-npm", "local", "")
	mustCreate(repository.Write{}, "shared-npm", "local", "")

	t.Run("Q-AUTHZ-176 a project administrator's key starts with the project key and a hyphen", func(t *testing.T) {
		_, err := svc.CreateIn(ctx, caller, doc("npm-web-extra", "local", ""), web)
		if ce, _ := errs.As(err); ce == nil || ce.Status() != 422 || ce.Code != errs.CodeProjectPrefixRequired || fieldsOf(t, err)["/key"] != errs.CodeProjectPrefixRequired {
			t.Fatalf("%v", err)
		}
		st := mustCreate(web, "web-extra", "local", "")
		if st.Repository.ProjectKey != "web" || st.Repository.ProjectID == "" {
			t.Fatalf("%+v", st.Repository)
		}
		if got := e.text(`SELECT p.key FROM repositories r JOIN project p ON p.id = r.project_id WHERE r.key = 'web-extra'`); got != "web" {
			t.Fatalf("assigned to %q", got)
		}
		if _, err := svc.CreateIn(ctx, caller, doc("web-lost", "local", ""), repository.Write{Delegate: "web"}); err == nil {
			t.Error("a project administrator's create names its project")
		}
		if _, err := svc.CreateIn(ctx, caller, doc("web-other", "local", ""), repository.Write{Project: ptr("shop"), Delegate: "web"}); err == nil {
			t.Error("a project administrator creates in its own project only")
		}
		if n := e.number(`SELECT count(*) FROM repositories WHERE key IN ('npm-web-extra', 'web-lost', 'web-other')`); n != 0 {
			t.Fatalf("%d rows of refused creates", n)
		}
	})
	t.Run("Q-AUTHZ-173 a project administrator creates a remote repository; the upstream is no concern of the create", func(t *testing.T) {
		st := mustCreate(web, "web-proxy", "remote", "")
		if st.Document.Class != repository.Remote || st.Repository.ProjectKey != "web" {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("Q-AUTHZ-178 a new member and a new deploy target are repositories of the project, an unknown key answers the same", func(t *testing.T) {
		mustCreate(web, "web-npm", "local", "")
		v := func(key string, members ...string) error {
			_, err := svc.CreateIn(ctx, caller, doc(key, "virtual", `"virtual":{"members":["`+strings.Join(members, `","`)+`"],"deployTarget":null}`), web)
			return err
		}
		if err := v("web-virt", "web-npm", "web-proxy"); err != nil {
			t.Fatal(err)
		}
		outside, unknown := v("web-v2", "web-npm", "shop-npm"), v("web-v3", "web-npm", "ghost-npm")
		for _, err := range []error{outside, unknown} {
			ce, _ := errs.As(err)
			if ce == nil || ce.Status() != 422 || ce.Code != errs.CodeRepositoryOutsideProject || fieldsOf(t, err)["/virtual/members/1"] != errs.CodeRepositoryOutsideProject {
				t.Fatalf("%v", err)
			}
		}
		o, _ := errs.As(outside)
		u, _ := errs.As(unknown)
		if o.Detail != u.Detail || o.Fields[0].Detail != u.Fields[0].Detail {
			t.Errorf("the answers differ: %q / %q", o.Fields[0].Detail, u.Fields[0].Detail)
		}
		if err := v("web-v4", "shared-npm"); err == nil {
			t.Error("the shared repository is an administrator's to add")
		}
		// The administrator of trove may place it; the project administrator keeps what was placed and cannot extend it.
		mustCreate(repository.Write{Project: ptr("web")}, "web-keep", "virtual", `"virtual":{"members":["web-npm","shared-npm"],"deployTarget":null}`)
	})
	t.Run("Q-AUTHZ-178 the members an administrator of trove placed stay, a new outside one is refused", func(t *testing.T) {
		upd := func(members string) (repository.Stored, error) {
			cur, err := svc.Get(ctx, "web-keep")
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(fmt.Sprintf(`{"key":"web-keep","class":"virtual","format":"npm","description":%q,"virtual":{"members":[%s],"deployTarget":null}}`, cur.Document.Description+".", members))
			return svc.ReplaceIn(ctx, caller, "web-keep", repository.MatchAny(), body, web)
		}
		if _, err := upd(`"web-npm","shared-npm"`); err != nil {
			t.Fatalf("a write that keeps the placed member: %v", err)
		}
		if _, err := upd(`"shared-npm","web-npm","web-proxy"`); err != nil {
			t.Fatalf("a new project member: %v", err)
		}
		if _, err := upd(`"web-npm","shared-npm","shop-npm"`); fieldsOf(t, err)["/virtual/members/2"] != errs.CodeRepositoryOutsideProject {
			t.Fatalf("a new outside member: %v", err)
		}
		if _, err := upd(`"web-npm"`); err != nil {
			t.Fatalf("dropping the placed member: %v", err)
		}
		if _, err := upd(`"web-npm","shared-npm"`); fieldsOf(t, err)["/virtual/members/1"] != errs.CodeRepositoryOutsideProject {
			t.Fatalf("adding it again is a new member: %v", err)
		}
	})
	t.Run("Q-AUTHZ-178 a deploy target the project administrator picks is a project repository", func(t *testing.T) {
		mustCreate(repository.Write{Project: ptr("web")}, "web-placed", "virtual", `"virtual":{"members":["web-npm","shared-npm"],"deployTarget":null}`)
		set := func(target string) error {
			body := []byte(fmt.Sprintf(`{"key":"web-placed","class":"virtual","format":"npm","virtual":{"members":["web-npm","shared-npm"],"deployTarget":%s}}`, target))
			_, err := svc.ReplaceIn(ctx, caller, "web-placed", repository.MatchAny(), body, web)
			return err
		}
		if err := set(`"shared-npm"`); fieldsOf(t, err)["/virtual/deployTarget"] != errs.CodeRepositoryOutsideProject {
			t.Fatalf("a target outside the project: %v", err)
		}
		if err := set(`"web-npm"`); err != nil {
			t.Fatalf("a target in the project: %v", err)
		}
	})
	t.Run("Q-AUTHZ-184 a repository of another project answers not found to a project administrator's write", func(t *testing.T) {
		body := []byte(`{"key":"shop-npm","class":"local","format":"npm","description":"mine now"}`)
		_, err := svc.ReplaceIn(ctx, caller, "shop-npm", repository.MatchAny(), body, web)
		if ce, _ := errs.As(err); ce == nil || ce.Status() != 404 || ce.Code != errs.CodeRepositoryNotFound {
			t.Fatalf("%v", err)
		}
		_, err = svc.ReplaceIn(ctx, caller, "shared-npm", repository.MatchAny(), []byte(`{"key":"shared-npm","class":"local","format":"npm"}`), web)
		if ce, _ := errs.As(err); ce == nil || ce.Status() != 404 {
			t.Fatalf("a repository without a project: %v", err)
		}
	})
	t.Run("R-AUTHZ-026 the readers fill the project key", func(t *testing.T) {
		got, err := svc.Get(ctx, "web-npm")
		if err != nil || got.Repository.ProjectKey != "web" {
			t.Fatalf("%+v %v", got.Repository, err)
		}
		res, err := svc.List(ctx, repository.ListFilter{Project: "web"})
		if err != nil || len(res.Items) < 5 {
			t.Fatalf("%d %v", len(res.Items), err)
		}
		for _, st := range res.Items {
			if st.Repository.ProjectKey != "web" || !strings.HasPrefix(st.Document.Key, "web-") {
				t.Errorf("%s: %q", st.Document.Key, st.Repository.ProjectKey)
			}
		}
		waitFor(t, "the snapshot of the replica to know the project", 10*time.Second, func() bool {
			r, err := a.h.App.Repositories().ByKey(ctx, "web-npm")
			return err == nil && r.ProjectKey == "web"
		})
		shared, _ := a.h.App.Repositories().ByKey(ctx, "shared-npm")
		if shared.ProjectKey != "" || shared.ProjectID != "" {
			t.Errorf("shared-npm: %+v", shared)
		}
	})
}

// TestR_AUTHZ_026_ProjectAdministratorOverHTTP: the acceptance of R-AUTHZ-026 for the
// repository operations, as the project administrator meets them.
func TestR_AUTHZ_026_ProjectAdministratorOverHTTP(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("web", true, false) // manageRepositories is off
	e.project("shop", true, true)
	e.projectAdmin("wanda", "web")
	e.stack.Repository("shop-api", "local", "generic", "{}")
	e.exec(`UPDATE repositories SET project_id = (SELECT id FROM project WHERE key = 'shop') WHERE key = 'shop-api'`)
	e.stack.Repository("global-one", "local", "generic", "{}")
	off := e.newReplica("off", webOpts{})
	requireProjectEngine(t, off, "wanda")

	body := func(key string, extra map[string]any) map[string]any {
		m := map[string]any{"key": key, "class": "local", "format": "generic", "project": "web", "adminNotes": "team note"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	t.Run("R-AUTHZ-026 with manageRepositories off the same administrator creating npm-web-extra gets 403", func(t *testing.T) {
		problemOf(t, off.postJSON("wanda", "/api/v1/repositories", body("npm-web-extra", nil)), 403, "project_switch_off")
		problemOf(t, off.postJSON("wanda", "/api/v1/repositories", body("web-extra", nil)), 403, "project_switch_off")
		if n := e.number(`SELECT count(*) FROM repositories WHERE key IN ('npm-web-extra', 'web-extra')`); n != 0 {
			t.Fatalf("%d rows", n)
		}
	})
	e.setSwitches("web", true, true)
	on := e.newReplica("on", webOpts{})
	t.Run("R-AUTHZ-026 with the switch on npm-web-extra gets 422 for the missing prefix and web-extra succeeds", func(t *testing.T) {
		m := problemOf(t, on.postJSON("wanda", "/api/v1/repositories", body("npm-web-extra", nil)), 422, "project_prefix_required")
		if errs, _ := m["errors"].([]any); len(errs) == 0 || errs[0].(map[string]any)["pointer"] != "/key" {
			t.Errorf("errors = %v", m["errors"])
		}
		res := on.postJSON("wanda", "/api/v1/repositories", body("web-extra", nil))
		if res.status != 201 || res.json(t)["project"] != "web" || res.json(t)["adminNotes"] != "team note" {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if got := e.text(`SELECT p.key FROM repositories r JOIN project p ON p.id = r.project_id WHERE r.key = 'web-extra'`); got != "web" {
			t.Fatalf("assigned to %q", got)
		}
		problemOf(t, on.postJSON("wanda", "/api/v1/repositories", body("shop-extra", map[string]any{"project": "shop"})), 404, "not_found")
		problemOf(t, on.postJSON("wanda", "/api/v1/repositories", body("web-free", map[string]any{"project": nil})), 403, "forbidden")
	})
	t.Run("Q-AUTHZ-184 the project administrator reads the full document, lists its project and replaces, patches and deletes", func(t *testing.T) {
		get := on.get("wanda", "/api/v1/repositories/web-extra")
		if get.status != 200 || get.json(t)["adminNotes"] != "team note" || get.json(t)["project"] != "web" {
			t.Fatalf("%d %s", get.status, get.body)
		}
		page := on.get("wanda", "/api/v1/repositories?project=web&view=full").json(t)["items"].([]any)
		if len(page) != 1 || page[0].(map[string]any)["adminNotes"] != "team note" {
			t.Fatalf("%v", page)
		}
		keys := func(q string) string {
			var out []string
			for _, it := range on.get("wanda", "/api/v1/repositories"+q).json(t)["items"].([]any) {
				out = append(out, it.(map[string]any)["key"].(string))
			}
			return fmt.Sprint(out)
		}
		if got := keys("?project=shop"); got != "[]" {
			t.Errorf("another project: %s", got)
		}
		doc := get.json(t)
		doc["description"] = "replaced"
		if res := on.do("wanda", "PUT", "/api/v1/repositories/web-extra", jsonBody(t, doc), "Content-Type", "application/json", "If-Match", get.header.Get("ETag")); res.status != 200 || res.json(t)["adminNotes"] != "team note" {
			t.Fatalf("PUT: %d %s", res.status, res.body)
		}
		if res := on.do("wanda", "PATCH", "/api/v1/repositories/web-extra", strings.NewReader(`{"description":"patched"}`), "Content-Type", "application/merge-patch+json", "If-Match", "*"); res.status != 200 {
			t.Fatalf("PATCH: %d %s", res.status, res.body)
		}
		violation(t, on.do("wanda", "PATCH", "/api/v1/repositories/web-extra", strings.NewReader(`{"project":"shop"}`), "Content-Type", "application/merge-patch+json", "If-Match", "*"), "immutable")
		if res := on.do("wanda", "DELETE", "/api/v1/repositories/web-extra", nil, "If-Match", "*"); res.status != 204 && res.status != 202 {
			t.Fatalf("DELETE: %d %s", res.status, res.body)
		}
	})
	t.Run("R-AUTHZ-026 a repository outside the project is reached by no operation", func(t *testing.T) {
		for _, key := range []string{"shop-api", "global-one", "no-such-repo"} {
			problemOf(t, on.get("wanda", "/api/v1/repositories/"+key), 404, "not_found")
			problemOf(t, on.do("wanda", "PUT", "/api/v1/repositories/"+key, jsonBody(t, body(key, nil)), "Content-Type", "application/json", "If-Match", "*"), 404, "not_found")
			problemOf(t, on.do("wanda", "PATCH", "/api/v1/repositories/"+key, strings.NewReader(`{"description":"x"}`), "Content-Type", "application/merge-patch+json", "If-Match", "*"), 404, "not_found")
			problemOf(t, on.do("wanda", "DELETE", "/api/v1/repositories/"+key, nil, "If-Match", "*"), 404, "not_found")
			problemOf(t, on.do("wanda", "POST", "/api/v1/repositories/"+key+"/upstream-test", nil), 404, "not_found")
		}
	})
	t.Run("Q-AUTHZ-173 the unsaved upstream test needs a project with the switch, and answers as for anyone", func(t *testing.T) {
		if res := off.do("wanda", "POST", "/api/v1/upstream-tests", strings.NewReader(`{"format":"npm","remote":{"url":"https://registry.example.test/"}}`), "Content-Type", "application/json"); res.status != 403 {
			t.Fatalf("switch off: %d %s", res.status, res.body)
		}
		if res := on.do("wanda", "POST", "/api/v1/upstream-tests", strings.NewReader(`{"format":"npm","remote":{"url":"https://registry.example.test/"}}`), "Content-Type", "application/json"); res.status != 200 {
			t.Fatalf("switch on: %d %s", res.status, res.body)
		}
	})
	t.Run("R-AUTHZ-026 a project administrator without a capability sees no other repository", func(t *testing.T) {
		for _, it := range on.get("wanda", "/api/v1/repositories?limit=100").json(t)["items"].([]any) {
			if k := it.(map[string]any)["key"]; k == "shop-api" || k == "global-one" {
				t.Errorf("%v is listed", k)
			}
		}
	})
}

func ptr[T any](v T) *T { return &v }

func jsonBody(t testing.TB, v any) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}
