//go:build integration

package nuget

import (
	"strings"
	"testing"
)

// TestQ_AUTHZ_172_AuditorFindsNuGetPackages: the search of a NuGet repository counts
// and lists every package for the auditor, directly and through a group bound in the
// configuration, and nothing for a token without the capability (R-AUTHZ-011,
// R-AUTHZ-016, Q-AUTHZ-172).
func TestQ_AUTHZ_172_AuditorFindsNuGetPackages(t *testing.T) {
	e := newAuditorEnv(t)
	search := func(token string) (int, string) {
		t.Helper()
		a := e.st.Do(e.r, "GET", "/api/nuget/it/nuget-local/v3/search?q=widgets", token, nil)
		return a.Status, string(a.Body)
	}
	for _, who := range []struct{ name, token string }{{"a user bound to auditor", e.tokAud}, {"a member of a group bound through authz.role_bindings", e.tokMem}} {
		t.Run("Q-AUTHZ-172 "+who.name+" finds the package", func(t *testing.T) {
			if s, b := search(who.token); s != 200 || !strings.Contains(b, `"Acme.Widgets"`) || !strings.Contains(b, `"totalHits":1`) {
				t.Fatalf("search: %d %s", s, b)
			}
		})
	}
	t.Run("R-AUTHZ-013 R-AUTHZ-016 a token without the system item cannot search the repository", func(t *testing.T) {
		if s, b := search(e.tokMemDefault); s != 404 {
			t.Fatalf("search: %d %s", s, b)
		}
	})
}
