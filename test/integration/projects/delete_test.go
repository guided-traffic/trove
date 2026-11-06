//go:build integration

package projects

import (
	"fmt"
	"strings"
	"testing"
)

// TestR_AUTHZ_026_DeleteNeedsAnEmptyProject is the acceptance criterion of the delete:
// 409 project_not_empty while a repository (in any lifecycle state) or a project
// service account belongs to the project, 204 when both are gone; its policies and
// its member entries go with it.
func TestR_AUTHZ_026_DeleteNeedsAnEmptyProject(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.seedRepo("web", "extra", "active")
	f.seedRepo("web", "old", "deleting")
	f.seedProjectServiceAccount("web", "partner")
	wantStatus(t, f.member("web", "group:web-leads", "owner"), 201, "")
	f.seedProjectPolicy("web", "read")

	refused := func(why string) {
		t.Helper()
		epoch, events := f.epoch(), f.events("authz.project.deleted")
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web", nil), 409, "project_not_empty")
		if f.epoch() != epoch || f.events("authz.project.deleted") != events {
			t.Fatalf("%s: a refused delete changed state", why)
		}
		if f.e.Number("SELECT count(*) FROM authz_policy WHERE name = 'web/read'") != 1 {
			t.Fatalf("%s: a refused delete removed a policy", why)
		}
	}
	refused("repositories and an account")
	t.Run("R-AUTHZ-026 a repository being deleted still counts", func(t *testing.T) {
		f.e.Exec("DELETE FROM repositories WHERE key = 'extra'")
		f.e.Exec("DELETE FROM service_account WHERE principal_id = (SELECT id FROM principal WHERE name = 'web/partner')")
		refused("only the repository in lifecycle deleting")
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t); got["repositories"] != float64(1) {
			t.Errorf("repositories = %v", got["repositories"])
		}
	})
	t.Run("R-AUTHZ-026 a project service account counts", func(t *testing.T) {
		f.e.Exec("DELETE FROM repositories WHERE key = 'old'")
		f.e.Exec("INSERT INTO service_account (principal_id, project_id) SELECT id, (SELECT id FROM project WHERE key = 'web') FROM principal WHERE name = 'web/partner'")
		refused("only the service account")
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t); got["serviceAccounts"] != float64(1) {
			t.Errorf("serviceAccounts = %v", got["serviceAccounts"])
		}
	})
	t.Run("R-AUTHZ-026 a stale If-Match answers 412 and deletes nothing", func(t *testing.T) {
		f.e.Exec("DELETE FROM service_account WHERE principal_id = (SELECT id FROM principal WHERE name = 'web/partner')")
		r := f.do(f.admin, "DELETE", base+"/web", nil, "If-Match", `"7"`)
		wantStatus(t, r, 412, "precondition_failed")
		if r.JSON(t)["currentEtag"] != `"1"` || f.e.Number("SELECT count(*) FROM authz_policy WHERE name = 'web/read'") != 1 {
			t.Errorf("currentEtag %v; the policy was deleted before the version check", r.JSON(t)["currentEtag"])
		}
	})
	t.Run("R-AUTHZ-026 an empty project is deleted with its policies, one by one, and its member entries", func(t *testing.T) {
		f.seedProjectPolicy("web", "write")
		epoch := f.epoch()
		policyEvents, policyJournal := f.events("authz.policy.deleted"), f.journal("authz.policy.deleted")
		r := f.do(f.admin, "DELETE", base+"/web", nil, "If-Match", `"1"`)
		wantStatus(t, r, 204, "")
		if r.Epoch() != f.epoch() || r.Epoch() <= epoch {
			t.Errorf("epoch header %d, database %d, before %d", r.Epoch(), f.epoch(), epoch)
		}
		if f.e.Number("SELECT count(*) FROM project WHERE key = 'web'") != 0 ||
			f.e.Number("SELECT count(*) FROM project_member") != 0 ||
			f.e.Number("SELECT count(*) FROM authz_policy WHERE name LIKE 'web/%'") != 0 {
			t.Error("the project, its members or its policies remain")
		}
		if got := f.events("authz.policy.deleted") - policyEvents; got != 2 {
			t.Errorf("%d policy deletions audited, want 2", got)
		}
		if got := f.journal("authz.policy.deleted") - policyJournal; got != 2 {
			t.Errorf("%d policy deletions journaled, want 2", got)
		}
		if f.events("authz.project.deleted") != 1 || f.journal("authz.project.deleted") != 1 {
			t.Errorf("events %d, journal %d", f.events("authz.project.deleted"), f.journal("authz.project.deleted"))
		}
		if got := f.detail("authz.project.deleted", "policiesDeleted"); got != `["web/read", "web/write"]` {
			t.Errorf("policiesDeleted = %s", got)
		}
		if got := f.detail("authz.project.deleted", "epoch"); got != fmt.Sprint(r.Epoch()) {
			t.Errorf("epoch in the event %s, header %d", got, r.Epoch())
		}
		// Each policy deletion ran before the project's: their epochs are lower.
		if f.e.Number("SELECT max((details->>'epoch')::bigint) FROM audit_event WHERE action = 'authz.policy.deleted'") >= r.Epoch() {
			t.Error("a policy was deleted at or after the project")
		}
		wantStatus(t, f.do(f.admin, "GET", base+"/web", nil), 404, "")
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web", nil), 404, "")
	})
	t.Run("R-AUTHZ-026 the key can be used again once the project is gone", func(t *testing.T) {
		r := f.create("web")
		if r.Header.Get("ETag") != `"1"` {
			t.Errorf("ETag %s", r.Header.Get("ETag"))
		}
	})
	t.Run("R-AUTHZ-026 the writes need projects:write", func(t *testing.T) {
		wantStatus(t, f.do(f.auditor, "DELETE", base+"/web", nil), 403, "")
		wantStatus(t, f.do(f.alice, "DELETE", base+"/web", nil), 403, "")
	})
}

// TestR_AUTHZ_026_DeleteWithoutPolicies deletes an empty project that has none.
func TestR_AUTHZ_026_DeleteWithoutPolicies(t *testing.T) {
	f := newFixture(t)
	f.create("docs")
	r := f.do(f.admin, "DELETE", base+"/docs", nil)
	wantStatus(t, r, 204, "")
	if r.Epoch() != f.epoch() || f.events("authz.policy.deleted") != 0 {
		t.Errorf("epoch %d/%d, %d policy events", r.Epoch(), f.epoch(), f.events("authz.policy.deleted"))
	}
	if got := f.detail("authz.project.deleted", "policiesDeleted"); got != "<none>" && !strings.Contains(got, "null") {
		t.Errorf("policiesDeleted = %s", got)
	}
}
