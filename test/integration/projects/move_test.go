//go:build integration

package projects

import (
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
)

// TestR_REPO_060_MoveARepository: the move of a repository to another project over HTTP (R-REPO-060, Q-REPO-217):
// refused while something of the source project names it, refused when the target holds the key, allowed for an
// Owner of both projects or the holder of repositories:write-config, answered with the document at the new address,
// audited and journaled, both change areas notified.
func TestR_REPO_060_MoveARepository(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.create("mobile")
	f.seedRepo("web", "npm-libs", "active")
	f.e.Exec(`INSERT INTO repositories (key, class, format, config, project_id) SELECT 'npm-all', 'virtual', 'npm', '{}'::jsonb, id FROM project WHERE key = 'web'`)
	f.e.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
	          SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-libs'`)
	dave, erin := f.e.User("dave"), f.e.User("erin")
	wantStatus(t, f.member("web", "user:alice", "owner"), 201, "")
	wantStatus(t, f.member("mobile", "user:alice", "owner"), 201, "")
	wantStatus(t, f.member("web", "user:dave", "owner"), 201, "")
	wantStatus(t, f.member("mobile", "user:dave", "developer"), 201, "")
	last := f.member("web", "user:erin", "owner")
	wantStatus(t, last, 201, "")
	epoch := fmt.Sprint(last.Epoch())
	id := f.repoID("web", "npm-libs")
	move := func(who string, path, target string) (int, map[string]any, string) {
		t.Helper()
		r := f.cl.Do(who, "POST", base+"/"+path+"/move", map[string]any{"project": target}, authz.HeaderMinEpoch, epoch)
		var body map[string]any
		if len(r.Body) > 0 {
			body = r.JSON(t)
		}
		return r.Status, body, r.Code()
	}

	t.Run("R-REPO-060 a member list of the source project refuses the move with the reference", func(t *testing.T) {
		status, body, code := move(f.alice.Name, "web/repositories/npm-libs", "mobile")
		if status != 409 || code != "repository_in_use" || fmt.Sprint(body["references"]) != "[map[kind:virtual_member name:npm-all]]" {
			t.Fatalf("%d %s %v", status, code, body)
		}
		if f.projectOfRepo(id) != "web" || f.events("repository.moved") != 0 {
			t.Fatal("a refused move changed something")
		}
	})
	t.Run("R-REPO-060 a virtual repository with members does not move", func(t *testing.T) {
		status, body, code := move(f.admin.Name, "web/repositories/npm-all", "mobile")
		if status != 409 || code != "repository_in_use" || fmt.Sprint(body["references"]) != "[map[kind:member name:npm-libs]]" {
			t.Fatalf("%d %s %v", status, code, body)
		}
	})
	f.e.Exec("DELETE FROM virtual_members")
	t.Run("R-REPO-060 an Owner of the source with a lower role in the target is refused 403, one that is no member there 404", func(t *testing.T) {
		if status, _, _ := move(dave.Name, "web/repositories/npm-libs", "mobile"); status != 403 {
			t.Errorf("developer in the target: %d", status)
		}
		if status, _, _ := move(erin.Name, "web/repositories/npm-libs", "mobile"); status != 404 {
			t.Errorf("no member of the target: %d", status)
		}
		if status, _, _ := move(f.alice.Name, "web/repositories/npm-libs", "nothing"); status != 404 {
			t.Errorf("an unknown target: %d", status)
		}
	})
	t.Run("R-REPO-060 a target that holds the key is 409 already_exists, also by a repository being deleted", func(t *testing.T) {
		f.seedRepo("mobile", "npm-libs", "deleting")
		if status, _, code := move(f.alice.Name, "web/repositories/npm-libs", "mobile"); status != 409 || code != "already_exists" {
			t.Errorf("%d %s", status, code)
		}
		f.e.Exec("DELETE FROM repositories WHERE key = 'npm-libs' AND project_id = (SELECT id FROM project WHERE key = 'mobile')")
	})
	t.Run("R-REPO-060 a target equal to the source changes nothing", func(t *testing.T) {
		version, events := f.configVersion(id), f.events("repository.moved")
		if status, _, _ := move(f.alice.Name, "web/repositories/npm-libs", "web"); status != 200 {
			t.Errorf("%d", status)
		}
		if f.configVersion(id) != version || f.events("repository.moved") != events {
			t.Error("a move into its own project changed something")
		}
	})
	t.Run("R-REPO-060 an Owner of both projects moves it: the document at the new address, the epochs, the event", func(t *testing.T) {
		version, authzEpoch, repoEpoch := f.configVersion(id), f.epoch(), f.repoEpoch()
		r := f.cl.Do(f.alice.Name, "POST", base+"/web/repositories/npm-libs/move", map[string]any{"project": "mobile"}, authz.HeaderMinEpoch, epoch)
		wantStatus(t, r, 200, "")
		if loc := r.Header.Get("Location"); loc != "http://trove.test"+base+"/mobile/repositories/npm-libs" {
			t.Errorf("Location %q", loc)
		}
		if body := r.JSON(t); body["key"] != "npm-libs" || r.Header.Get("ETag") == "" || r.Epoch() != f.epoch() {
			t.Errorf("%v, ETag %q, epoch %d/%d", body, r.Header.Get("ETag"), r.Epoch(), f.epoch())
		}
		if f.projectOfRepo(id) != "mobile" || f.repoID("mobile", "npm-libs") != id || f.configVersion(id) <= version {
			t.Errorf("project %s, version %d (was %d)", f.projectOfRepo(id), f.configVersion(id), version)
		}
		if f.epoch() <= authzEpoch || f.repoEpoch() <= repoEpoch {
			t.Errorf("the epochs did not move: authz %d/%d, repo_config %d/%d", f.epoch(), authzEpoch, f.repoEpoch(), repoEpoch)
		}
		if f.events("repository.moved") != 1 || f.journal("repository.moved") != 1 ||
			f.detail("repository.moved", "from") != "web" || f.detail("repository.moved", "to") != "mobile" {
			t.Errorf("events %d, journal %d", f.events("repository.moved"), f.journal("repository.moved"))
		}
		// Q-REPO-233: the move shows in the audit of both projects, once each: the source records the repository leaving
		// under its former name, the target its arrival under the new one; only the leaving is journaled.
		for _, c := range []struct{ project, action, repository string }{
			{"web", "repository.moved", "web/npm-libs"}, {"mobile", "repository.moved_in", "mobile/npm-libs"},
		} {
			n := f.e.Number(`SELECT count(*) FROM audit_event WHERE project = $1 AND action = $2 AND repository = $3
			                  AND details->>'repositoryId' = $4 AND details->>'from' = 'web' AND details->>'to' = 'mobile'`,
				c.project, c.action, c.repository, fmt.Sprint(id))
			if n != 1 {
				t.Errorf("the audit of %s holds %d %s events of %s", c.project, n, c.action, c.repository)
			}
		}
		if f.journal("repository.moved_in") != 0 {
			t.Error("the arrival is journaled as well")
		}
		wantStatus(t, f.do(f.admin, "POST", base+"/web/repositories/npm-libs/move", map[string]any{"project": "mobile"}), 404, "")
	})
	t.Run("R-REPO-060 the holder of repositories:write-config moves without a membership", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "POST", base+"/mobile/repositories/npm-libs/move", map[string]any{"project": "web"}), 200, "")
		if f.projectOfRepo(id) != "web" {
			t.Error("not moved back")
		}
	})
	t.Run("Q-REPO-232 a global policy's key selector keeps an Owner from moving the repository, not an administrator", func(t *testing.T) {
		// The administrator's grant is held by the repository's ID and would follow it into mobile, whose other Owners
		// cannot remove it: only a holder of repositories:write-config carries it there.
		const policies = "/api/v1/permission-policies"
		policy := func(selector string) map[string]any {
			return map[string]any{
				"name": "libs-global", "resources": map[string]any{"repositories": []string{selector}},
				"entries": []map[string]any{{"principal": "user:bob", "actions": []string{"read"}}},
			}
		}
		created := f.do(f.admin, "POST", policies, policy("web/npm-libs"))
		wantStatus(t, created, 201, "")
		epoch = fmt.Sprint(created.Epoch())
		moved := f.events("repository.moved")
		status, body, code := move(f.alice.Name, "web/repositories/npm-libs", "mobile")
		if status != 409 || code != "repository_in_use" || fmt.Sprint(body["references"]) != "[map[kind:global_policy name:libs-global]]" {
			t.Fatalf("%d %s %v", status, code, body)
		}
		if f.projectOfRepo(id) != "web" || f.events("repository.moved") != moved {
			t.Fatal("a refused move changed something")
		}
		for _, target := range []string{"mobile", "web"} {
			if status, body, _ := move(f.admin.Name, f.projectOfRepo(id)+"/repositories/npm-libs", target); status != 200 {
				t.Fatalf("the administrator's move to %s: %d %v", target, status, body)
			}
		}
		// A pattern names no repository: it matches by address and keeps nobody from moving it.
		replaced := f.do(f.admin, "PUT", policies+"/libs-global", policy("web/npm-*"), "If-Match", "*")
		wantStatus(t, replaced, 200, "")
		epoch = fmt.Sprint(replaced.Epoch())
		if status, body, _ := move(f.alice.Name, "web/repositories/npm-libs", "mobile"); status != 200 {
			t.Fatalf("an Owner's move under a pattern: %d %v", status, body)
		}
	})
}
