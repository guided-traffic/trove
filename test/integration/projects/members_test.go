//go:build integration

package projects

import (
	"fmt"
	"sync"
	"testing"
)

func TestR_AUTHZ_026_Administrators(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.e.ServiceAccount("ci-bot")
	version := func() string { return f.do(f.admin, "GET", base+"/web", nil).Header.Get("ETag") }
	start := version()

	t.Run("R-AUTHZ-026 adding a group answers 201, adding it again 200 and records nothing", func(t *testing.T) {
		epoch := f.epoch()
		r := f.do(f.admin, "PUT", base+"/web/admins/group:web-leads", nil)
		wantStatus(t, r, 201, "")
		if m := r.JSON(t); m["project"] != "web" || m["principal"] != "group:web-leads" {
			t.Errorf("%v", m)
		}
		if r.Epoch() <= epoch || r.Epoch() != f.epoch() {
			t.Errorf("epoch header %d, database %d, before %d", r.Epoch(), f.epoch(), epoch)
		}
		again := f.do(f.admin, "PUT", base+"/web/admins/group:web-leads", nil)
		wantStatus(t, again, 200, "")
		if again.Epoch() != f.epoch() || again.Epoch() != r.Epoch() {
			t.Errorf("an add that changed nothing answers the current epoch: %d, %d", again.Epoch(), f.epoch())
		}
		if f.events("authz.project.admin_added") != 1 {
			t.Errorf("%d events", f.events("authz.project.admin_added"))
		}
	})
	t.Run("R-AUTHZ-026 a user is an administrator too, and the reference may be percent-encoded", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/user%3Abob", nil), 201, "")
		got := f.do(f.admin, "GET", base+"/web", nil).JSON(t)["admins"]
		if fmt.Sprint(got) != "[group:web-leads user:bob]" {
			t.Errorf("admins = %v, ordered by kind and name", got)
		}
	})
	t.Run("R-AUTHZ-026 only users and groups administer: 422 for the rest, 404 for the unknown", func(t *testing.T) {
		for _, ref := range []string{"sa:ci-bot", "anonymous", "authenticated", "ci:main"} {
			wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/"+ref, nil), 422, "principal_kind_not_allowed")
		}
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/robot:x", nil), 422, "invalid_principal")
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/user:nobody", nil), 404, "")
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/group:nobody", nil), 404, "")
		wantStatus(t, f.do(f.admin, "PUT", base+"/nothing/admins/user:bob", nil), 404, "")
	})
	t.Run("R-AUTHZ-026 an administrator change leaves the project's version alone (Q-AUTHZ-185)", func(t *testing.T) {
		if got := version(); got != start {
			t.Errorf("ETag %s, was %s", got, start)
		}
	})
	t.Run("R-AUTHZ-026 a removal answers 204 and is journaled, a second one 404", func(t *testing.T) {
		epoch := f.epoch()
		r := f.do(f.admin, "DELETE", base+"/web/admins/user:bob", nil)
		wantStatus(t, r, 204, "")
		if r.Epoch() <= epoch || r.Epoch() != f.epoch() {
			t.Errorf("epoch header %d, database %d", r.Epoch(), f.epoch())
		}
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/admins/user:bob", nil), 404, "")
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/admins/sa:ci-bot", nil), 404, "")
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/admins/user:nobody", nil), 404, "")
		wantStatus(t, f.do(f.admin, "DELETE", base+"/nothing/admins/user:bob", nil), 404, "")
		if f.events("authz.project.admin_removed") != 1 || f.journal("authz.project.admin_removed") != 1 {
			t.Errorf("events %d, journal %d", f.events("authz.project.admin_removed"), f.journal("authz.project.admin_removed"))
		}
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t)["admins"]; fmt.Sprint(got) != "[group:web-leads]" {
			t.Errorf("admins = %v", got)
		}
	})
	t.Run("R-AUTHZ-026 the writes need projects:write", func(t *testing.T) {
		wantStatus(t, f.do(f.auditor, "PUT", base+"/web/admins/user:bob", nil), 403, "")
		wantStatus(t, f.do(f.auditor, "DELETE", base+"/web/admins/group:web-leads", nil), 403, "")
		wantStatus(t, f.do(f.alice, "PUT", base+"/web/admins/user:bob", nil), 403, "")
	})
}

func TestR_AUTHZ_026_AssignAndUnassign(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.create("docs")
	f.seedRepo("web-npm", "active")
	f.seedRepo("web-gen", "active")

	t.Run("R-AUTHZ-026 an assignment answers 204, notifies both areas and moves the repository's config version", func(t *testing.T) {
		authzEpoch, repoEpoch, version := f.epoch(), f.repoEpoch(), f.configVersion("web-npm")
		r := f.do(f.admin, "PUT", base+"/web/repositories/web-npm", nil)
		wantStatus(t, r, 204, "")
		if f.projectOf("web-npm") != "web" {
			t.Fatalf("project = %q", f.projectOf("web-npm"))
		}
		if r.Epoch() <= authzEpoch || r.Epoch() != f.epoch() {
			t.Errorf("epoch header %d, database %d, before %d", r.Epoch(), f.epoch(), authzEpoch)
		}
		if f.repoEpoch() <= repoEpoch {
			t.Errorf("repo_config epoch %d, before %d", f.repoEpoch(), repoEpoch)
		}
		if f.configVersion("web-npm") != version+1 {
			t.Errorf("config_version %d, was %d: the project is a member of the repository's document", f.configVersion("web-npm"), version)
		}
		if f.events("authz.project.repository_assigned") != 1 || f.detail("authz.project.repository_assigned", "repository") != "web-npm" {
			t.Errorf("events %d", f.events("authz.project.repository_assigned"))
		}
		if f.journal("authz.project.repository_assigned") != 0 {
			t.Error("an assignment grants and is not journaled")
		}
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t)["repositories"]; got != float64(1) {
			t.Errorf("repositories = %v", got)
		}
	})
	t.Run("R-AUTHZ-026 assigning to the project it belongs to is a no-op that answers success", func(t *testing.T) {
		epoch, version := f.epoch(), f.configVersion("web-npm")
		r := f.do(f.admin, "PUT", base+"/web/repositories/web-npm", nil)
		wantStatus(t, r, 204, "")
		if r.Epoch() != epoch || f.epoch() != epoch || f.configVersion("web-npm") != version || f.events("authz.project.repository_assigned") != 1 {
			t.Errorf("a no-op wrote: epoch %d/%d, version %d, events %d", r.Epoch(), f.epoch(), f.configVersion("web-npm"), f.events("authz.project.repository_assigned"))
		}
	})
	t.Run("R-AUTHZ-026 a repository of another project answers 409 repository_assigned without move", func(t *testing.T) {
		epoch := f.epoch()
		wantStatus(t, f.do(f.admin, "PUT", base+"/docs/repositories/web-npm", nil), 409, "repository_assigned")
		wantStatus(t, f.do(f.admin, "PUT", base+"/docs/repositories/web-npm?move=false", nil), 409, "repository_assigned")
		if f.projectOf("web-npm") != "web" || f.epoch() != epoch {
			t.Error("a refused assignment changed state")
		}
	})
	t.Run("R-AUTHZ-026 move=true moves it: an unassignment and an assignment in one transaction", func(t *testing.T) {
		unassigned, assigned := f.events("authz.project.repository_unassigned"), f.events("authz.project.repository_assigned")
		r := f.do(f.admin, "PUT", base+"/docs/repositories/web-npm?move=true", nil)
		wantStatus(t, r, 204, "")
		if f.projectOf("web-npm") != "docs" {
			t.Fatalf("project = %q", f.projectOf("web-npm"))
		}
		if f.events("authz.project.repository_unassigned") != unassigned+1 || f.events("authz.project.repository_assigned") != assigned+1 {
			t.Errorf("events: %d unassigned, %d assigned", f.events("authz.project.repository_unassigned"), f.events("authz.project.repository_assigned"))
		}
		// Both events carry the epoch of the move, and the unassignment is the journaled one.
		same := f.e.Number("SELECT count(DISTINCT details->>'epoch') FROM audit_event WHERE action IN ('authz.project.repository_unassigned', 'authz.project.repository_assigned') AND details->>'moved' = 'true'")
		if same != 1 || f.journal("authz.project.repository_unassigned") != 1 {
			t.Errorf("distinct epochs %d, journal %d", same, f.journal("authz.project.repository_unassigned"))
		}
		if f.detail("authz.project.repository_assigned", "otherProject") != "web" {
			t.Errorf("otherProject = %s", f.detail("authz.project.repository_assigned", "otherProject"))
		}
	})
	t.Run("R-AUTHZ-026 an unknown repository or project answers 404", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/nothing", nil), 404, "repository_not_found")
		wantStatus(t, f.do(f.admin, "PUT", base+"/nothing/repositories/web-gen", nil), 404, "")
		if f.projectOf("web-gen") != "" {
			t.Error("web-gen was assigned")
		}
	})
	t.Run("R-AUTHZ-026 unassign answers 204 and 404 for a repository of no or another project", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/repositories/web-npm", nil), 404, "") // it is docs' now
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/repositories/web-gen", nil), 404, "") // it has no project
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/repositories/nothing", nil), 404, "")
		unassigned, version := f.events("authz.project.repository_unassigned"), f.configVersion("web-npm")
		r := f.do(f.admin, "DELETE", base+"/docs/repositories/web-npm", nil)
		wantStatus(t, r, 204, "")
		if f.projectOf("web-npm") != "" || r.Epoch() != f.epoch() || f.configVersion("web-npm") != version+1 {
			t.Errorf("project %q, epoch %d/%d, config_version %d", f.projectOf("web-npm"), r.Epoch(), f.epoch(), f.configVersion("web-npm"))
		}
		if f.events("authz.project.repository_unassigned") != unassigned+1 {
			t.Error("no unassignment event")
		}
		wantStatus(t, f.do(f.admin, "DELETE", base+"/docs/repositories/web-npm", nil), 404, "")
	})
	t.Run("R-AUTHZ-026 an assignment leaves the project's version alone", func(t *testing.T) {
		if got := f.do(f.admin, "GET", base+"/web", nil).Header.Get("ETag"); got != `"1"` {
			t.Errorf("ETag %s", got)
		}
	})
	t.Run("R-AUTHZ-026 the auditor and a plain user may not assign", func(t *testing.T) {
		wantStatus(t, f.do(f.auditor, "PUT", base+"/web/repositories/web-gen", nil), 403, "")
		wantStatus(t, f.do(f.alice, "PUT", base+"/web/repositories/web-gen", nil), 403, "")
		wantStatus(t, f.do(f.alice, "DELETE", base+"/web/repositories/web-gen", nil), 403, "")
	})
	t.Run("R-AUTHZ-026 two assignments of one repository to different projects at once: exactly one wins", func(t *testing.T) {
		for trial := range 5 {
			repo := fmt.Sprintf("race-%d", trial)
			f.seedRepo(repo, "active")
			var wg sync.WaitGroup
			res := make([]int, 2)
			for i, key := range []string{"web", "docs"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res[i] = f.cl.Do(f.admin.Name, "PUT", base+"/"+key+"/repositories/"+repo, nil).Status
				}()
			}
			wg.Wait()
			if (res[0] != 204 || res[1] != 409) && (res[0] != 409 || res[1] != 204) {
				t.Fatalf("trial %d: statuses %v, want one 204 and one 409", trial, res)
			}
		}
	})
}

// TestR_AUTHZ_029_ARefusedWriteLeavesNoEvent: the audit event is recorded in the
// transaction of the write, so a write that is refused records nothing, raises no
// epoch and leaves no journal entry.
func TestR_AUTHZ_029_ARefusedWriteLeavesNoEvent(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.seedRepo("web-npm", "active")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/web-npm", nil), 204, "")
	events := f.e.Number("SELECT count(*) FROM audit_event WHERE category = 'authz'")
	epoch, outbox := f.epoch(), f.e.Number("SELECT count(*) FROM revocation_outbox")

	wantStatus(t, f.do(f.admin, "DELETE", base+"/web", nil), 409, "project_not_empty")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "X", "managePolicies": false, "manageRepositories": false}), 428, "")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "X", "managePolicies": false, "manageRepositories": false}, "If-Match", `"9"`), 412, "")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/user:nobody", nil), 404, "")
	wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "web-x", "displayName": "X"}), 409, "project_key_overlap")

	if got := f.e.Number("SELECT count(*) FROM audit_event WHERE category = 'authz'"); got != events {
		t.Errorf("%d authz events, was %d", got, events)
	}
	if f.epoch() != epoch || f.e.Number("SELECT count(*) FROM revocation_outbox") != outbox {
		t.Error("a refused write raised the epoch or wrote a journal entry")
	}
}
