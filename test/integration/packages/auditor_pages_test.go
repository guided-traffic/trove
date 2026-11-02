//go:build integration

package packages

import (
	"strings"
	"testing"
)

// TestQ_AUTHZ_172_AuditorSeesThePackagePages: the package screens of the management
// API (R-API-029, R-API-030) list every package to the auditor, its versions and the
// version page; the user without a role sees none of it (R-AUTHZ-011, Q-AUTHZ-172).
func TestQ_AUTHZ_172_AuditorSeesThePackagePages(t *testing.T) {
	e := newAuditorEnv(t)
	for _, who := range []struct{ name, token string }{{"a user bound to auditor", e.tokAud}, {"a member of a group bound through authz.role_bindings", e.tokMem}} {
		t.Run("Q-AUTHZ-172 "+who.name+" sees the package pages", func(t *testing.T) {
			if got := names(e.all(who.token, "/api/v1/packages")); len(got) != 1 || got[0] != "left-pad" {
				t.Fatalf("packages %v", got)
			}
			id := e.idOf(who.token, "left-pad")
			if a := e.get(who.token, "/api/v1/packages/"+id); a.Status != 200 {
				t.Fatalf("package: %d %s", a.Status, a.Body)
			}
			if v := e.all(who.token, "/api/v1/packages/"+id+"/versions"); len(v) != 1 {
				t.Fatalf("versions %v", v)
			}
			if a := e.get(who.token, "/api/v1/packages/"+id+"/versions/1.0.0"); a.Status != 200 || !strings.Contains(string(a.Body), `"1.0.0"`) {
				t.Fatalf("version: %d %s", a.Status, a.Body)
			}
		})
	}
	t.Run("R-AUTHZ-013 the auditor's token without the system item lists no package", func(t *testing.T) {
		if got := names(e.all(e.tokMemDefault, "/api/v1/packages")); len(got) != 0 {
			t.Fatalf("packages %v", got)
		}
	})
	t.Run("R-AUTHZ-001 a user without a grant or role lists no package", func(t *testing.T) {
		if got := names(e.all(e.tokNobody, "/api/v1/packages")); len(got) != 0 {
			t.Fatalf("packages %v", got)
		}
	})
}
