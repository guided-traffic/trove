//go:build integration

package projects

import (
	"fmt"
	"strings"
	"testing"
)

// TestR_AUTHZ_026_PrincipalLookupBounds covers Q-AUTHZ-182: a prefix of two
// characters at least, 20 results at most, kind and name only, active users and
// groups only.
func TestR_AUTHZ_026_PrincipalLookupBounds(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	for i := range 25 {
		f.e.User(fmt.Sprintf("al-%02d", i))
	}
	f.e.User("al-gone")
	f.e.Exec("UPDATE principal SET status = 'disabled' WHERE name = 'al-gone'")
	f.e.Group("al-ops")
	f.e.Group("zed-ops")
	f.e.ServiceAccount("al-bot")
	lookup := func(query string) []map[string]any {
		t.Helper()
		r := f.do(f.admin, "GET", base+"/web/principals?"+query, nil)
		wantStatus(t, r, 200, "")
		return r.Items(t)
	}

	t.Run("R-AUTHZ-026 a prefix of two characters answers at most 20 users and groups", func(t *testing.T) {
		got := lookup("q=al")
		if len(got) != 20 {
			t.Fatalf("%d results, want 20", len(got))
		}
		for _, it := range got {
			if len(it) != 3 || it["kind"] == nil || it["name"] == nil || it["principal"] != fmt.Sprintf("%v:%v", it["kind"], it["name"]) {
				t.Fatalf("an item holds more than kind, name and the reference: %v", it)
			}
			if name := it["name"].(string); !strings.HasPrefix(name, "al") || name == "al-gone" || name == "al-bot" {
				t.Errorf("unexpected %v", it)
			}
		}
		if got[0]["kind"] != "group" || got[0]["name"] != "al-ops" {
			t.Errorf("ordered by kind and name: %v", got[0])
		}
	})
	t.Run("R-AUTHZ-026 a prefix of one character answers 422", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "GET", base+"/web/principals?q=a", nil), 422, "prefix_too_short")
		wantStatus(t, f.do(f.admin, "GET", base+"/web/principals?q=", nil), 422, "prefix_too_short")
		wantStatus(t, f.do(f.admin, "GET", base+"/web/principals", nil), 400, "")
	})
	t.Run("R-AUTHZ-026 limit and kind narrow the answer, a limit above 20 is refused", func(t *testing.T) {
		if got := lookup("q=al&limit=3"); len(got) != 3 {
			t.Errorf("limit=3: %d", len(got))
		}
		for _, it := range lookup("q=al&kind=group") {
			if it["kind"] != "group" {
				t.Errorf("kind=group: %v", it)
			}
		}
		if got := lookup("q=al&kind=user"); len(got) != 20 || got[0]["kind"] != "user" {
			t.Errorf("kind=user: %d", len(got))
		}
		wantStatus(t, f.do(f.admin, "GET", base+"/web/principals?q=al&limit=21", nil), 422, "invalid_limit")
		wantStatus(t, f.do(f.admin, "GET", base+"/web/principals?q=al&limit=0", nil), 422, "invalid_limit")
		wantStatus(t, f.do(f.admin, "GET", base+"/web/principals?q=al&kind=service_account", nil), 422, "invalid_kind")
	})
	t.Run("R-AUTHZ-026 the prefix is a prefix: no wildcard, no infix, no other kind", func(t *testing.T) {
		if got := lookup("q=%25%25"); len(got) != 0 {
			t.Errorf("q=%%%%: %v", got)
		}
		if got := lookup("q=ops"); len(got) != 0 {
			t.Errorf("an infix matched: %v", got)
		}
		if got := lookup("q=al-b"); len(got) != 0 {
			t.Errorf("a service account matched: %v", got)
		}
		if got := lookup("q=zed"); len(got) != 1 || got[0]["principal"] != "group:zed-ops" {
			t.Errorf("%v", got)
		}
	})
	t.Run("R-AUTHZ-026 an unknown project is 404, a caller with neither the capability nor the project is 403", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "GET", base+"/nothing/principals?q=al", nil), 404, "")
		wantStatus(t, f.do(f.alice, "GET", base+"/web/principals?q=al", nil), 403, "")
		wantStatus(t, f.do(f.auditor, "GET", base+"/web/principals?q=al", nil), 200, "") // the auditor holds identities:read
	})
}
