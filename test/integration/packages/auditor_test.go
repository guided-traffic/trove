//go:build integration

package packages

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// auditorEnv is an installation with an npm repository and a generic repository, a
// writer, a user bound to auditor directly, a member of a group that
// authz.role_bindings binds to auditor, and a user without anything.
type auditorEnv struct {
	*env
	tokAud, tokMem, tokMemDefault, tokNobody string
}

func newAuditorEnv(t *testing.T) *auditorEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "auditor"})
	e := &auditorEnv{env: &env{t: t, st: st}}
	e.alice = st.User("alice")
	aud, mem, nobody := st.User("aud"), st.User("mem", "gh-auditors"), st.User("nobody")
	st.Bind("auditor", aud)
	if err := st.Declare(config.AuthzRoleBinding{Role: "auditor", Group: "gh-auditors"}); err != nil {
		t.Fatal(err)
	}
	st.Repository("npm-local", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository("generic-local", "local", "generic", "{}")
	st.Policy("writers", []string{"read", "deploy", "delete", "annotate"}, []string{"npm-local", "generic-local"}, e.alice)
	// A token exercises a role's capabilities only through its system items (R-AUTHZ-013).
	auditor := scope.Scope{Inherit: true, System: []string{"content:metadata-read", "repositories:read-config", "policies:read", "projects:read"}}
	e.tokA = st.Token(e.alice, scope.Default())
	e.tokAud, e.tokMem = st.Token(aud, auditor), st.Token(mem, auditor)
	e.tokMemDefault, e.tokNobody = st.Token(mem, scope.Default()), st.Token(nobody, scope.Default())
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web1"})
	e.publish("npm-local", spec{Name: "left-pad", Version: "1.0.0", Description: "pads strings on the left"})
	if a := st.Do(e.r, "PUT", "/files/it/generic-local/a/b.bin", e.tokA, []byte("raw")); a.Status != 201 {
		t.Fatalf("raw upload: %d %s", a.Status, a.Body)
	}
	return e
}

// TestQ_AUTHZ_172_AuditorReadsNpmMetadata: the auditor, directly and through a
// group bound in the configuration, lists the repositories, reads the npm package
// document and the configuration, and downloads, publishes, tags, deletes and
// changes nothing (R-AUTHZ-011, Q-AUTHZ-172).
func TestQ_AUTHZ_172_AuditorReadsNpmMetadata(t *testing.T) {
	e := newAuditorEnv(t)
	for _, who := range []struct{ name, token string }{{"a user bound to auditor", e.tokAud}, {"a member of a group bound through authz.role_bindings", e.tokMem}} {
		t.Run("Q-AUTHZ-172 "+who.name+" reads the metadata", func(t *testing.T) {
			repos := e.get(who.token, "/api/v1/repositories")
			if repos.Status != 200 || !strings.Contains(string(repos.Body), `"npm-local"`) || !strings.Contains(string(repos.Body), `"generic-local"`) {
				t.Fatalf("repositories: %d %s", repos.Status, repos.Body)
			}
			doc := e.get(who.token, "/api/npm/it/npm-local/left-pad")
			if doc.Status != 200 || !strings.Contains(string(doc.Body), `"1.0.0"`) {
				t.Fatalf("package document: %d %s", doc.Status, doc.Body)
			}
			if a := e.get(who.token, "/api/npm/it/npm-local/left-pad/1.0.0"); a.Status != 200 {
				t.Fatalf("version document: %d %s", a.Status, a.Body)
			}
			if a := e.get(who.token, "/files/it/generic-local/a/"); a.Status != 200 || !strings.Contains(string(a.Body), "b.bin") {
				t.Fatalf("folder listing: %d %s", a.Status, a.Body)
			}
		})
		t.Run("R-AUTHZ-011 "+who.name+" reads the configuration and changes none of it", func(t *testing.T) {
			if a := e.get(who.token, "/api/v1/projects/it/repositories/npm-local"); a.Status != 200 || a.Header.Get("ETag") == "" {
				t.Fatalf("repository document: %d %s", a.Status, a.Body)
			}
			if a := e.get(who.token, "/api/v1/permission-policies"); a.Status != 200 || !strings.Contains(string(a.Body), `"writers"`) {
				t.Fatalf("policies: %d %s", a.Status, a.Body)
			}
			for _, w := range []struct{ method, path string }{
				{"PATCH", "/api/v1/projects/it/repositories/npm-local"},
				{"DELETE", "/api/v1/projects/it/repositories/npm-local"},
				{"POST", "/api/v1/projects/it/repositories"},
				{"POST", "/api/v1/permission-policies"},
			} {
				body := map[string]any{"key": "aud-repo", "class": "local", "format": "generic", "description": "x", "name": "aud-policy"}
				if a := e.st.Do(e.r, w.method, w.path, who.token, body, "Content-Type", "application/merge-patch+json", "If-Match", "*"); a.Status != 403 {
					t.Errorf("%s %s: %d %s", w.method, w.path, a.Status, a.Body)
				}
			}
		})
		t.Run("Q-AUTHZ-172 "+who.name+" downloads, publishes, tags and deletes nothing", func(t *testing.T) {
			for _, p := range []string{"/api/npm/it/npm-local/left-pad/-/left-pad-1.0.0.tgz", "/files/it/generic-local/a/b.bin", "/files/it/npm-local/left-pad/-/left-pad-1.0.0.tgz"} {
				if a := e.get(who.token, p); a.Status != 404 {
					t.Errorf("GET %s: %d %s", p, a.Status, a.Body)
				}
			}
			writes := []struct {
				method, path string
				body         any
			}{
				{"PUT", "/api/npm/it/npm-local/left-pad", publishBody(t, spec{Name: "left-pad", Version: "1.0.1"})},
				{"PUT", "/api/npm/it/npm-local/-/package/left-pad/dist-tags/beta", `"1.0.0"`},
				{"DELETE", "/api/npm/it/npm-local/-/package/left-pad/dist-tags/latest", nil},
				{"PUT", "/files/it/generic-local/c.bin", []byte("x")},
				{"DELETE", "/files/it/generic-local/a/b.bin", nil},
			}
			for _, w := range writes {
				if a := e.st.Do(e.r, w.method, w.path, who.token, w.body, "Content-Type", "application/json"); a.Status != 403 {
					t.Errorf("%s %s: %d %s", w.method, w.path, a.Status, a.Body)
				}
			}
			if a := e.get(e.tokA, "/api/npm/it/npm-local/left-pad"); a.Status != 200 || strings.Contains(string(a.Body), `"1.0.1"`) || strings.Contains(string(a.Body), `"beta"`) {
				t.Fatalf("the document changed: %s", a.Body)
			}
		})
	}
	t.Run("R-AUTHZ-013 a token of the auditor without the system item reads no metadata", func(t *testing.T) {
		if a := e.get(e.tokMemDefault, "/api/npm/it/npm-local/left-pad"); a.Status != 404 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-AUTHZ-001 a user without a grant or role reads nothing", func(t *testing.T) {
		if a := e.get(e.tokNobody, "/api/npm/it/npm-local/left-pad"); a.Status != 404 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
}
