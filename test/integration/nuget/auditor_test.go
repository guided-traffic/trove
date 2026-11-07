//go:build integration

package nuget

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// auditorScope is the scope of an auditor's token that carries the metadata read
// (R-AUTHZ-013: a token exercises a role's capability only through its system items).
func auditorScope() scope.Scope {
	return scope.Scope{Inherit: true, System: []string{"content:metadata-read", "repositories:read-config", "projects:read"}}
}

// auditorEnv is the write installation with a user bound to auditor directly, a
// member of a group bound to auditor through authz.role_bindings, a generic repository
// with a raw file and one pushed NuGet package.
type auditorEnv struct {
	*wenv
	r                             *testenv.Replica
	tokAud, tokMem, tokMemDefault string
}

func newAuditorEnv(t *testing.T) *auditorEnv {
	t.Helper()
	e := &auditorEnv{wenv: newWriteEnv(t)}
	aud, mem := e.st.User("aud"), e.st.User("mem", "gh-auditors")
	e.st.Bind("auditor", aud)
	if err := e.st.Declare(config.AuthzRoleBinding{Role: "auditor", Group: "gh-auditors"}); err != nil {
		t.Fatal(err)
	}
	e.st.Repository("gen-local", "local", "generic", "{}")
	e.st.Policy("gen-writers", []string{"read", "deploy"}, []string{"gen-local"}, e.st.User("gen-dev"))
	e.tokAud, e.tokMem = e.st.Token(aud, auditorScope()), e.st.Token(mem, auditorScope())
	e.tokMemDefault = e.st.Token(mem, scope.Default())
	e.r = e.replica("web1")
	e.mustPush(e.r, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3", tags: "widgets"}))
	if a := e.st.Do(e.r, "PUT", "/files/it/gen-local/a/b.bin", e.tokAdmin, []byte("raw")); a.Status != 201 {
		t.Fatalf("raw upload: %d %s", a.Status, a.Body)
	}
	return e
}

// TestQ_AUTHZ_172_AuditorReadsNuGetMetadata: a user bound to auditor directly and a
// member of a group bound to auditor through authz.role_bindings read the version list
// and the registration of a NuGet repository, and download, push, delete and relist
// nothing (R-AUTHZ-011, Q-AUTHZ-172).
func TestQ_AUTHZ_172_AuditorReadsNuGetMetadata(t *testing.T) {
	e := newAuditorEnv(t)
	r, tokAud, tokMem, tokMemDefault := e.r, e.tokAud, e.tokMem, e.tokMemDefault

	get := func(token, path string) (int, string) {
		t.Helper()
		a := e.st.Do(r, "GET", path, token, nil)
		return a.Status, string(a.Body)
	}
	for _, who := range []struct{ name, token string }{{"a user bound to auditor", tokAud}, {"a member of a group bound through authz.role_bindings", tokMem}} {
		t.Run("Q-AUTHZ-172 "+who.name+" reads the metadata", func(t *testing.T) {
			if s, b := get(who.token, "/api/nuget/it/nuget-local/v3/flatcontainer/acme.widgets/index.json"); s != 200 || !strings.Contains(b, `"1.2.3"`) {
				t.Fatalf("version list: %d %s", s, b)
			}
			if s, b := get(who.token, "/api/nuget/it/nuget-local/v3/registration/acme.widgets/index.json"); s != 200 || !strings.Contains(b, "1.2.3") {
				t.Fatalf("registration: %d %s", s, b)
			}
			if s, b := get(who.token, "/api/nuget/it/nuget-local/v3/registration/acme.widgets/1.2.3.json"); s != 200 {
				t.Fatalf("registration leaf: %d %s", s, b)
			}
			if s, b := get(who.token, "/api/v1/projects/it/repositories/nuget-local"); s != 200 || !strings.Contains(b, `"nuget-local"`) {
				t.Fatalf("the repository document: %d %s", s, b)
			}
		})
		t.Run("Q-AUTHZ-172 "+who.name+" downloads and changes nothing", func(t *testing.T) {
			for _, p := range []string{
				"/api/nuget/it/nuget-local/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg",
				"/api/nuget/it/nuget-local/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.nuspec",
				"/files/it/gen-local/a/b.bin",
			} {
				if s, b := get(who.token, p); s != 404 {
					t.Errorf("GET %s: %d %s", p, s, b)
				}
			}
			if a := e.push(r, "nuget-local", who.token, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "9.9.9"})); a.Status != 403 {
				t.Errorf("push: %d %s", a.Status, a.Body)
			}
			if a := e.send(r, "DELETE", "nuget-local", "acme.widgets/1.2.3", who.token); a.Status != 403 {
				t.Errorf("delete: %d %s", a.Status, a.Body)
			}
			if a := e.send(r, "POST", "nuget-local", "acme.widgets/1.2.3", who.token); a.Status != 403 {
				t.Errorf("relist: %d %s", a.Status, a.Body)
			}
			if a := e.st.Do(r, "PUT", "/files/it/gen-local/c.bin", who.token, []byte("x")); a.Status != 403 {
				t.Errorf("raw upload: %d %s", a.Status, a.Body)
			}
			if a := e.st.Do(r, "PATCH", "/api/v1/projects/it/repositories/nuget-local", who.token, map[string]any{"description": "x"},
				"Content-Type", "application/merge-patch+json", "If-Match", "*"); a.Status != 403 {
				t.Errorf("repository change: %d %s", a.Status, a.Body)
			}
			if n := e.versions("nuget-local", "acme.widgets"); n != 1 {
				t.Errorf("%d versions", n)
			}
		})
	}

	t.Run("R-AUTHZ-013 a token without the system item reads no metadata", func(t *testing.T) {
		if s, b := get(tokMemDefault, "/api/nuget/it/nuget-local/v3/flatcontainer/acme.widgets/index.json"); s != 404 {
			t.Fatalf("%d %s", s, b)
		}
	})
	t.Run("R-AUTHZ-001 a user without a grant or role reads nothing", func(t *testing.T) {
		if s, b := get(e.tokNobody, "/api/nuget/it/nuget-local/v3/registration/acme.widgets/index.json"); s != 404 {
			t.Fatalf("%d %s", s, b)
		}
	})
}
