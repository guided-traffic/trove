//go:build integration

package projects

import (
	"fmt"
	"testing"
)

// TestR_AUTHZ_026_ReadIgnoresIfNoneMatch: the ETag of a project is the version of the
// fields a PUT replaces; administrators and counts are in the body and do not move it
// (Q-AUTHZ-185), so a conditional read must not answer 304 for them (Q-AUTHZ-202).
func TestR_AUTHZ_026_ReadIgnoresIfNoneMatch(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	first := f.do(f.admin, "GET", base+"/web", nil)
	wantStatus(t, first, 200, "")
	tag := first.Header.Get("ETag")
	if tag == "" {
		t.Fatal("the read carries no ETag")
	}

	t.Run("R-AUTHZ-026 an administrator added after the read is in the answer to a read with its tag", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/admins/group:web-leads", nil), 201, "")
		r := f.do(f.admin, "GET", base+"/web", nil, "If-None-Match", tag)
		wantStatus(t, r, 200, "")
		if got := fmt.Sprint(r.JSON(t)["admins"]); got != "[group:web-leads]" {
			t.Errorf("admins = %s", got)
		}
		if r.Header.Get("ETag") != tag {
			t.Errorf("the version moved with an administrator: %s, was %s", r.Header.Get("ETag"), tag)
		}
	})
	t.Run("R-AUTHZ-026 the tag still guards the PUT", func(t *testing.T) {
		body := map[string]any{"displayName": "Web", "managePolicies": true, "manageRepositories": false}
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", body, "If-Match", `"99"`), 412, "precondition_failed")
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", body, "If-Match", tag), 200, "")
	})
}
