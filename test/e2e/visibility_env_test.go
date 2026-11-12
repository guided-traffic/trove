//go:build e2e

package e2e

// The visibility levels in the harness (R-AUTHZ-033 to R-AUTHZ-035, T-18): the repositories of the suite that anonymous clients read
// are created with public artifacts in a public project, replacing the runtime switch and the policy entries for the principal
// anonymous, which the installation no longer has. Every other repository of the suite keeps the default level, private, and so
// challenges a request without a credential.

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// publicLevels is the visibility of a repository that anyone reads: artifacts and wiki public.
func publicLevels() map[string]any {
	return map[string]any{"artifacts": "public", "wiki": "public"}
}

// setProjectLevel sets the level of e2eProject through its own operation (R-API-057) and returns the level it had; the request is
// not sent when the project has the level already. Lowering a project lowers its repositories (R-AUTHZ-033), so a project put back to
// private also puts back every repository a test made public.
func setProjectLevel(t testing.TB, adm *api, level string) (previous string) {
	t.Helper()
	g := adm.get("/api/v1/projects/"+e2eProject).want(t, "read the project "+e2eProject, 200)
	var p struct {
		Visibility string `json:"visibility"`
	}
	g.JSON(t, &p)
	if p.Visibility == level {
		return level
	}
	adm.do(http.MethodPut, "/api/v1/projects/"+e2eProject+"/visibility", map[string]any{"level": level}, "If-Match", g.Header.Get("ETag")).
		want(t, "set the level of "+e2eProject+" to "+level, 200)
	return p.Visibility
}

// restoreProjectLevel puts the project back to the level it had; a failure is reported, never fatal, because it runs when a test or the
// suite ends.
func restoreProjectLevel(adm *api, level string) {
	g, err := adm.send(http.MethodGet, "/api/v1/projects/"+e2eProject, nil)
	if err != nil || g.Status != 200 {
		fmt.Fprintf(os.Stderr, "e2e: restore the level of %s: read: %v %s\n", e2eProject, err, g)
		return
	}
	if r, err := adm.send(http.MethodPut, "/api/v1/projects/"+e2eProject+"/visibility", map[string]any{"level": level}, "If-Match", g.Header.Get("ETag")); err != nil || r.Status != 200 {
		fmt.Fprintf(os.Stderr, "e2e: restore the level of %s to %s: %v %s\n", e2eProject, level, err, r)
	}
}

// ensureProjectLevel sets the level of e2eProject for the test and puts the previous level back when the test ends (t.Cleanup).
func ensureProjectLevel(t testing.TB, adm *api, level string) {
	t.Helper()
	if previous := setProjectLevel(t, adm, level); previous != level {
		t.Cleanup(func() { restoreProjectLevel(adm, previous) })
	}
}

var (
	suiteRestoreMu sync.Mutex
	suiteRestores  []func()
)

// ensureProjectLevelForSuite is ensureProjectLevel for a fixture that is built once for the whole run, by the first test that needs it
// (sync.Once): the previous level comes back when the suite ends, in TestMain, not when that first test does.
func ensureProjectLevelForSuite(t testing.TB, adm *api, level string) {
	t.Helper()
	if previous := setProjectLevel(t, adm, level); previous != level {
		suiteRestoreMu.Lock()
		defer suiteRestoreMu.Unlock()
		suiteRestores = append(suiteRestores, func() { restoreProjectLevel(adm, previous) })
	}
}

// visibilityTeardown puts back what the fixtures of the suite changed, newest first; TestMain calls it after the last test.
func visibilityTeardown() {
	suiteRestoreMu.Lock()
	defer suiteRestoreMu.Unlock()
	for i := len(suiteRestores) - 1; i >= 0; i-- {
		suiteRestores[i]()
	}
	suiteRestores = nil
}

// setRepoLevels writes the levels of a repository of e2eProject with a merge patch (R-REPO-004). A repository that exists from an
// earlier run of the suite has the levels it was created with.
func setRepoLevels(t testing.TB, adm *api, key string, levels map[string]any) {
	t.Helper()
	g := adm.get(repoAPI(key)).want(t, "read "+key, 200)
	adm.do(http.MethodPatch, repoAPI(key), map[string]any{"visibility": levels}, "If-Match", g.Header.Get("ETag"),
		"Content-Type", "application/merge-patch+json").want(t, "set the levels of "+key, 200)
}

// createPublicRepo creates a repository whose artifacts and wiki are public in the public project e2eProject, or sets the levels of one
// that exists. The body is the repository document without `visibility`. The project goes back to its level when the test ends; the
// repository is the test's to delete.
func createPublicRepo(t testing.TB, adm *api, body map[string]any) {
	t.Helper()
	createRepoAt(t, adm, body, publicLevels())
}

// createSuitePublicRepo is createPublicRepo for a repository of a fixture that is built once per run: the project goes back to its level
// when the suite ends, and with it the repository to private (lowering a project lowers its repositories).
func createSuitePublicRepo(t testing.TB, adm *api, body map[string]any) {
	t.Helper()
	ensureProjectLevelForSuite(t, adm, "public")
	postRepoAt(t, adm, body, publicLevels())
}

// createRepoAt creates a repository with the given artifacts and wiki levels in e2eProject, which is made public for the test so that no
// level is above the project's, or sets the levels of one that exists.
func createRepoAt(t testing.TB, adm *api, body map[string]any, levels map[string]any) {
	t.Helper()
	ensureProjectLevel(t, adm, "public")
	postRepoAt(t, adm, body, levels)
}

func postRepoAt(t testing.TB, adm *api, body map[string]any, levels map[string]any) {
	t.Helper()
	body["visibility"] = levels
	r := adm.do(http.MethodPost, reposRoute, body)
	r.want(t, "create "+body["key"].(string), 201, 409)
	if r.Status == 409 {
		setRepoLevels(t, adm, body["key"].(string), levels)
	}
}

// awaitStatusOnEveryReplica waits until nine requests, which the scatter profile spreads over the packages replicas, all answer want from
// the client mk returns, and at least three replicas answered: a level reaches every replica within the bound of R-AUTHZ-028.
func awaitStatusOnEveryReplica(t testing.TB, what string, mk func() *api, path string, want int) {
	t.Helper()
	seen := map[string]bool{}
	last := ""
	defer func() {
		if t.Failed() {
			t.Logf("%s: the last request answered %s", what, last)
		}
	}()
	waitFor(t, what, time.Minute, time.Second, func() bool {
		clear(seen)
		for range 9 {
			r, err := mk().send(http.MethodGet, path, nil)
			if err != nil {
				last = err.Error()
				return false
			}
			last = r.String()
			if r.Status != want {
				return false
			}
			seen[r.Replica()] = true
		}
		return len(seen) >= 3
	})
}

// userToken signs the user in through the test identity provider and creates a personal access token for it: the credential of an
// internal user (R-AUTHZ-030). The token is revoked with the test.
func userToken(t *testing.T, user string) string {
	t.Helper()
	b := newBrowser(t)
	b.oidcLogin(user, secretValue(t, "trove-keycloak", user+"-password"))
	r := b.do(http.MethodPost, "/api/v1/tokens", map[string]any{"description": "e2e visibility", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true}}).
		want(t, user+"'s personal access token", 201)
	var c createdToken
	r.JSON(t, &c)
	t.Cleanup(func() { b.do(http.MethodDelete, "/api/v1/tokens/"+c.ID, nil) })
	return c.Token
}

// setExternal flags a user external or clears the flag (R-AUTH-044).
func setExternal(t testing.TB, adm *api, user string, external bool) {
	t.Helper()
	g := adm.get("/api/v1/users/"+user).want(t, "read "+user, 200)
	adm.do(http.MethodPatch, "/api/v1/users/"+user, map[string]any{"external": external}, "If-Match", g.Header.Get("ETag"),
		"Content-Type", "application/merge-patch+json").want(t, "set external of "+user, 200)
}
