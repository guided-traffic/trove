//go:build integration

package projects

import "testing"

// F-32: a move names the owner the caller saw, and the write compares it under the lock.
func TestR_AUTHZ_026_AssignNamesTheExpectedOwner(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.create("docs")
	f.seedRepo("web-npm", "active")
	f.seedRepo("web-gen", "active")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/web-npm", nil), 204, "")
	// Another administrator moved the repository to docs after this caller read "web" as the owner.
	wantStatus(t, f.do(f.admin, "PUT", base+"/docs/repositories/web-npm?move=true", nil), 204, "")

	t.Run("R-AUTHZ-026 F-32 a move from an owner that is not the current one answers 409 project_owner_changed and writes nothing", func(t *testing.T) {
		epoch, version := f.epoch(), f.configVersion("web-npm")
		r := f.do(f.admin, "PUT", base+"/web/repositories/web-npm?move=true&from=web", nil)
		wantStatus(t, r, 409, "project_owner_changed")
		if r.JSON(t)["currentProject"] != "docs" {
			t.Errorf("currentProject = %v", r.JSON(t)["currentProject"])
		}
		if f.projectOf("web-npm") != "docs" || f.epoch() != epoch || f.configVersion("web-npm") != version {
			t.Errorf("a refused move wrote: project %q, epoch %d, version %d", f.projectOf("web-npm"), f.epoch(), f.configVersion("web-npm"))
		}
	})
	t.Run("R-AUTHZ-026 F-32 an empty from expects an unassigned repository", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/web-npm?move=true&from=", nil), 409, "project_owner_changed")
		if f.projectOf("web-npm") != "docs" {
			t.Errorf("project = %q", f.projectOf("web-npm"))
		}
		r := f.do(f.admin, "PUT", base+"/web/repositories/web-gen?from=docs", nil)
		wantStatus(t, r, 409, "project_owner_changed")
		if v, ok := r.JSON(t)["currentProject"]; !ok || v != nil {
			t.Errorf("an unassigned repository names no project: %v, %v", v, ok)
		}
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/web-gen?from=", nil), 204, "")
		if f.projectOf("web-gen") != "web" {
			t.Errorf("project = %q", f.projectOf("web-gen"))
		}
	})
	t.Run("R-AUTHZ-026 F-32 the expected owner is the current one: the move succeeds; no from keeps today's behaviour", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/web-npm?move=true&from=docs", nil), 204, "")
		if f.projectOf("web-npm") != "web" {
			t.Fatalf("project = %q", f.projectOf("web-npm"))
		}
		wantStatus(t, f.do(f.admin, "PUT", base+"/docs/repositories/web-npm?move=true", nil), 204, "")
		if f.projectOf("web-npm") != "docs" {
			t.Errorf("project = %q", f.projectOf("web-npm"))
		}
	})
	t.Run("R-AUTHZ-026 F-32 the check precedes the no-op: a repository already in the project still answers 409 for a wrong from", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/docs/repositories/web-npm?from=web", nil), 409, "project_owner_changed")
		wantStatus(t, f.do(f.admin, "PUT", base+"/docs/repositories/web-npm?from=docs", nil), 204, "")
	})
	t.Run("R-AUTHZ-026 F-32 a from that is no project key answers 400", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/repositories/web-npm?from=Not_A_Key", nil), 400, "")
	})
}
