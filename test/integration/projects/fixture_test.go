//go:build integration

package projects

import (
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/test/integration/stack"
)

const base = "/api/v1/projects"

// fixture is two web replicas on one database and the people of the scenarios: an
// `admin` (every capability), an `auditor` (projects:read, no write) and plain users.
type fixture struct {
	t                      *testing.T
	e                      *stack.Env
	cl                     *stack.Cluster
	admin, second, auditor stack.Person
	alice, bob             stack.Person
	web                    stack.Person // the group web-leads
	a, b                   *stack.Replica
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	e := stack.New(t)
	f := &fixture{t: t, e: e}
	f.admin, f.second = e.User("sec-root"), e.User("sec-other")
	e.Admin(f.admin)
	e.Admin(f.second)
	f.auditor = e.User("audit-ann")
	e.Exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('auditor', $1::uuid)", f.auditor.ID)
	f.alice, f.bob = e.User("alice"), e.User("bob")
	f.web = e.Group("web-leads")
	e.Join(f.web, f.alice)
	o := stack.Options{Validator: apitest.New(t, v1.EmbeddedYAML())}
	f.a, f.b = e.NewReplica("web-a", o), e.NewReplica("web-b", o)
	f.cl = stack.NewCluster(f.a, f.b)
	return f
}

// do sends a request as the person to whichever replica is next.
func (f *fixture) do(person stack.Person, method, path string, body any, hdr ...string) stack.Response {
	f.t.Helper()
	return f.cl.Do(person.Name, method, path, body, hdr...)
}

// create creates a project as the admin and fails the test unless it answers 201.
func (f *fixture) create(key string, extra ...map[string]any) stack.Response {
	f.t.Helper()
	body := map[string]any{"key": key, "displayName": "Project " + key}
	for _, m := range extra {
		for k, v := range m {
			body[k] = v
		}
	}
	r := f.do(f.admin, "POST", base, body)
	wantStatus(f.t, r, 201, "")
	return r
}

// wantStatus asserts the status and the code of an answer. A 422 names the
// resource-specific code of a violation in errors[], the top-level code being
// validation_failed (R-API-009).
func wantStatus(t *testing.T, r stack.Response, status int, code string) {
	t.Helper()
	if status == 422 && code != "" && code != "validation_failed" && code != "idempotency_key_reused" {
		list, _ := r.JSON(t)["errors"].([]any)
		found := false
		for _, e := range list {
			found = found || e.(map[string]any)["code"] == code
		}
		if r.Status != 422 || r.Code() != "validation_failed" || !found {
			t.Fatalf("answered %d %q, want 422 validation_failed with %q in errors[]\n%s", r.Status, r.Code(), code, r.Body)
		}
		return
	}
	if r.Status != status || (code != "" && r.Code() != code) {
		t.Fatalf("answered %d %q, want %d %q\n%s", r.Status, r.Code(), status, code, r.Body)
	}
}

// epoch is the authorization epoch in the database; repoEpoch that of repo_config.
func (f *fixture) epoch() int64 { return f.e.AuthzEpoch() }

func (f *fixture) repoEpoch() int64 {
	return f.e.Number("SELECT coalesce(max(epoch), 0) FROM cluster_epoch WHERE area = 'repo_config'")
}

// events counts the audit events of an action, and details reads a member of the
// details of the newest one.
func (f *fixture) events(action string) int64 {
	return f.e.Number("SELECT count(*) FROM audit_event WHERE action = $1", action)
}

func (f *fixture) detail(action, member string) string {
	return f.e.Text("SELECT coalesce((SELECT details->>$2 FROM audit_event WHERE action = $1 ORDER BY occurred_at DESC, id DESC LIMIT 1), '<none>')", action, member)
}

// journal counts the rows of the revocation journal of an event type.
func (f *fixture) journal(event string) int64 {
	return f.e.Number("SELECT count(*) FROM revocation_outbox WHERE event = $1", event)
}

// seedRepo inserts a repository into the project, which must exist, the way the harness of the other areas does.
func (f *fixture) seedRepo(project, key, lifecycle string) {
	f.t.Helper()
	f.e.Exec(`INSERT INTO repositories (key, class, format, config, lifecycle, project_id)
	          SELECT $1, 'local', 'npm', '{}'::jsonb, $2, id FROM project WHERE key = $3`, key, lifecycle, project)
	if f.e.Number("SELECT count(*) FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2", project, key) != 1 {
		f.t.Fatalf("the repository %s/%s was not seeded", project, key)
	}
}

// configVersion is the config_version of the repository with the ID.
func (f *fixture) configVersion(id int64) int64 {
	return f.e.Number("SELECT config_version FROM repositories WHERE id = $1", id)
}

// repoID is the ID of the repository of the project with the key.
func (f *fixture) repoID(project, key string) int64 {
	return f.e.Number("SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2", project, key)
}

// projectOfRepo is the key of the project of the repository with the ID.
func (f *fixture) projectOfRepo(id int64) string {
	return f.e.Text("SELECT p.key FROM repositories r JOIN project p ON p.id = r.project_id WHERE r.id = $1", id)
}

// seedProjectServiceAccount inserts a service account of the project: its stored name is <project>/<name>
// (Q-AUTHZ-209, the guard of the migration).
func (f *fixture) seedProjectServiceAccount(project, name string) {
	f.t.Helper()
	id := f.e.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'service_account', $1) RETURNING id::text", project+"/"+name)
	f.e.Exec("INSERT INTO service_account (principal_id, project_id) SELECT $1::uuid, id FROM project WHERE key = $2", id, project)
}

// seedProjectPolicy inserts a policy of the project without entries, stored as <project>/<name>.
func (f *fixture) seedProjectPolicy(project, name string) {
	f.t.Helper()
	f.e.Exec("INSERT INTO authz_policy (id, name, project_id) SELECT gen_random_uuid(), $1, id FROM project WHERE key = $2", project+"/"+name, project)
}

// member makes the principal a member of the project with the role, as the admin, and returns the answer.
func (f *fixture) member(project, ref, role string) stack.Response {
	f.t.Helper()
	return f.do(f.admin, "PUT", base+"/"+project+"/members/"+ref, map[string]any{"role": role})
}
