//go:build integration

package ci

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The bounds of a save (R-CI-009, R-CI-010, R-CI-016, Q-CI-47, Q-CI-49, Q-CI-51): what a mapping may hold so that an
// exchange stays cheap and a project administrator learns nothing outside its project's names.

// boundsEnv is the issuer gitlab-main (private), the project web with web-npm, which bob administers, and the
// project data; ci.mappings.max_per_project 2 and max_per_issuer 4.
type boundsEnv struct {
	*env
	a     *testenv.Replica
	epoch string
}

func newBoundsEnv(t *testing.T) *boundsEnv {
	t.Helper()
	e := newEnv(t, options{valkey: true, env: []string{"TROVE_CI_MAPPINGS_MAX_PER_PROJECT=2", "TROVE_CI_MAPPINGS_MAX_PER_ISSUER=4"}})
	b := &boundsEnv{env: e, a: e.web("web-a")}
	b.issuer(t, b.a, "gitlab-main", false)
	for _, key := range []string{"web", "data"} {
		wantStatus(t, e.do(b.a, "POST", "/api/v1/projects", map[string]any{"key": key, "displayName": key}), 201, "")
		e.stack.Repository(key+"-npm", "local", "npm", "{}")
		e.exec("UPDATE repositories SET project_id = (SELECT id FROM project WHERE key = $1) WHERE key = $2", key, key+"-npm")
	}
	added := e.do(b.a, "PUT", "/api/v1/projects/web/admins/user%3Abob", nil)
	wantStatus(t, added, 201, "")
	b.epoch = added.Header.Get("Trove-Authz-Epoch")
	return b
}

func (b *boundsEnv) bob(method, path string, body any) testenv.Answer {
	b.t.Helper()
	return b.as(b.a, b.bobPAT, method, path, body, "Trove-Authz-Min-Epoch", b.epoch)
}

// webReader is a mapping of the project web that reads web-npm.
func webReader(name string, priority int, extra ...map[string]any) map[string]any {
	return mapping(name, priority, all(eq("project_id", "4242")), grants(inline("web-npm", []string{"*"}, "read")),
		append([]map[string]any{{"issuer": "gitlab-main"}}, extra...)...)
}

func wantField(t *testing.T, a testenv.Answer, field string) {
	t.Helper()
	if !slices.Contains(problemFields(t, a), field) {
		t.Errorf("errors[] = %v, want %q", problemFields(t, a), field)
	}
}

// R-CI-016, Q-CI-47: a mapping's or an issuer's maxTtlSeconds below ci.token.min_ttl is refused at the save; it would
// otherwise refuse every exchange it bounds with ttl_below_minimum.
func TestR_CI_016_ABoundBelowTheFloorIsRefusedAtTheSave(t *testing.T) {
	b := newBoundsEnv(t)
	pinned := all(eq("project_id", "4242"))
	below := map[string]any{"maxTtlSeconds": 29}
	r := b.do(b.a, "POST", mappingsOf("gitlab-main"), mapping("sys-short", 10, pinned, grants(inline("web-npm", []string{"*"}, "read")), below))
	wantStatus(t, r, 422, "validation_failed")
	wantField(t, r, "/maxTtlSeconds out_of_range")
	r = b.bob("POST", projectMappings("web"), webReader("web-short", 10, below))
	wantStatus(t, r, 422, "validation_failed")
	wantField(t, r, "/maxTtlSeconds out_of_range")
	wantStatus(t, b.bob("POST", projectMappings("web"), webReader("web-floor", 10, map[string]any{"maxTtlSeconds": 30})), 201, "")
	r = b.do(b.a, "POST", issuersPath, map[string]any{"name": "gitlab-short", "kind": "gitlab", "issuer": "https://gitlab.short.test", "publicIssuer": false, "maxTtlSeconds": 29})
	wantStatus(t, r, 422, "validation_failed")
	wantField(t, r, "/maxTtlSeconds invalid_max_ttl")
}

// R-CI-009, Q-CI-49: a project's tier holds at most ci.mappings.max_per_project mappings of an issuer, so one project
// cannot fill ci.mappings.max_per_issuer and lock the system tier out.
func TestR_CI_009_AProjectCannotFillTheIssuersLimit(t *testing.T) {
	b := newBoundsEnv(t)
	for i := 1; i <= 2; i++ {
		wantStatus(t, b.bob("POST", projectMappings("web"), webReader(fmt.Sprintf("web-r%d", i), i)), 201, "")
	}
	r := b.bob("POST", projectMappings("web"), webReader("web-r3", 3))
	wantStatus(t, r, 422, "mapping_limit_reached")
	if !strings.Contains(r.JSON(t)["detail"].(string), "ci.mappings.max_per_project") {
		t.Errorf("the refusal does not name the key: %s", r.Body)
	}
	pinned := all(eq("project_id", "4242"))
	for i := 1; i <= 2; i++ {
		wantStatus(t, b.do(b.a, "POST", mappingsOf("gitlab-main"), mapping(fmt.Sprintf("sys-%d", i), i, pinned, grants(inline("data-npm", []string{"*"}, "read")))), 201, "")
	}
	wantStatus(t, b.do(b.a, "POST", mappingsOf("gitlab-main"), mapping("sys-3", 3, pinned, grants(inline("data-npm", []string{"*"}, "read")))), 422, "mapping_limit_reached")
}

// R-CI-010, Q-CI-49: a literal and a whole condition are bounded at the save.
func TestR_CI_010_TheSizeOfAConditionIsBoundedOverHTTP(t *testing.T) {
	b := newBoundsEnv(t)
	long := all(eq("project_id", "4242"), eq("ref", strings.Repeat("v", 1025)))
	r := b.do(b.a, "POST", mappingsOf("gitlab-main"), mapping("too-long", 10, long, grants(inline("data-npm", []string{"*"}, "read"))))
	wantStatus(t, r, 422, "condition_invalid")
	wantField(t, r, "/match/all/1/equals condition_invalid")
}

// H-413, Q-CI-51: a project administrator's create is refused for a name outside its prefix before the name is
// looked up, so the 409 of a taken name answers only for names under the project's own prefix.
func TestQ_CI_51_AProjectAdministratorProbesNoNameOutsideItsPrefix(t *testing.T) {
	b := newBoundsEnv(t)
	pinned := all(eq("project_id", "4242"))
	wantStatus(t, b.do(b.a, "POST", mappingsOf("gitlab-main"), mapping("acme-release", 10, pinned, grants(inline("data-npm", []string{"*"}, "read")))), 201, "")
	r := b.bob("POST", projectMappings("web"), webReader("acme-release", 10))
	wantStatus(t, r, 422, "project_prefix_required")
	r = b.bob("POST", projectMappings("web"), webReader("acme-unused", 10))
	wantStatus(t, r, 422, "project_prefix_required")
	t.Run("Q-AUTHZ-176 the keys of two projects never overlap, so a prefix names one project", func(t *testing.T) {
		wantStatus(t, b.do(b.a, "POST", "/api/v1/projects", map[string]any{"key": "web-c", "displayName": "web-c"}), 409, "project_key_overlap")
		wantStatus(t, b.do(b.a, "POST", "/api/v1/projects", map[string]any{"key": "ab-c", "displayName": "ab-c"}), 201, "")
		wantStatus(t, b.do(b.a, "POST", "/api/v1/projects", map[string]any{"key": "ab", "displayName": "ab"}), 409, "project_key_overlap")
	})
}
