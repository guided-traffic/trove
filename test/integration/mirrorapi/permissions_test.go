//go:build integration

package mirrorapi

import (
	"net/http"
	"testing"
)

// TestR_REPO_056_PermissionsOfTheStatus: the status is open to repositories:read-config, to manage on the repository and to an
// administrator of its project; read on the repository is not enough (403), and a repository the caller has no right on is
// hidden (404, R-AUTHZ-016).
func TestR_REPO_056_PermissionsOfTheStatus(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		who, repo string
		status    int
		code      string
	}{
		{"rita", "npm-mirror", http.StatusOK, ""},
		{"olga", "npm-mirror", http.StatusOK, ""}, // repositories:read-config
		{"ravi", "npm-mirror", http.StatusOK, ""},
		{"mona", "npm-mirror", http.StatusOK, ""},     // manage on the repository
		{"pam", "web/web-mirror", http.StatusOK, ""},  // Owner of its project
		{"lena", "lab/lab-mirror", http.StatusOK, ""}, // Maintainer of its project: manage on its repositories
		{"rex", "npm-mirror", http.StatusForbidden, "forbidden"},
		{"dave", "npm-mirror", http.StatusNotFound, "not_found"},
		{"pam", "npm-mirror", http.StatusNotFound, "not_found"}, // another project's Owner has no right here
		{"dave", "no-such-repo", http.StatusNotFound, "not_found"},
	} {
		a := e.status(c.who, c.repo)
		if a.Status != c.status || (c.code != "" && a.Code(t) != c.code) {
			t.Errorf("%s reads %s: %d %s", c.who, c.repo, a.Status, a.Body)
		}
	}
	if a := e.do("", "GET", mirrorPath("npm-mirror"), nil); a.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d %s", a.Status, a.Body)
	}
}

// TestR_REPO_056_PermissionsOfTheRun: the run needs repositories:write-config, manage on the repository, or administration of its
// project (which is manage on it, Q-API-199); read-config alone and read alone are 403, an invisible repository 404. A refused
// caller changes nothing.
func TestR_REPO_056_PermissionsOfTheRun(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		who, repo string
		status    int
		code      string
	}{
		{"olga", "npm-mirror", http.StatusForbidden, "forbidden"}, // repositories:read-config alone
		{"rex", "npm-mirror", http.StatusForbidden, "forbidden"},  // read alone
		{"dave", "npm-mirror", http.StatusNotFound, "not_found"},
		{"pam", "npm-mirror", http.StatusNotFound, "not_found"},
		{"dave", "no-such-repo", http.StatusNotFound, "not_found"},
	} {
		a := e.do(c.who, "POST", runPath(c.repo), nil)
		if a.Status != c.status || (c.code != "" && a.Code(t) != c.code) {
			t.Errorf("%s runs %s: %d %s", c.who, c.repo, a.Status, a.Body)
		}
	}
	if a := e.do("", "POST", runPath("npm-mirror"), nil); a.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d %s", a.Status, a.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`) + e.st.Number(`SELECT count(*) FROM mirror_state`) +
		e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'mirror.pass_requested'`); n != 0 {
		t.Fatalf("a refused caller left %d rows", n)
	}
	for _, c := range []struct{ who, repo string }{
		{"rita", "npm-bounded"},
		{"ravi", "npm-state"},
		{"mona", "npm-mirror"},
		{"pam", "web/web-mirror"},
		// Q-API-199: the roles maintainer and owner hold manage on the repositories of their project.
		{"lena", "lab/lab-mirror"},
	} {
		a := e.do(c.who, "POST", runPath(c.repo), nil)
		if a.Status != http.StatusAccepted {
			t.Errorf("%s runs %s: %d %s", c.who, c.repo, a.Status, a.Body)
		}
		if got := e.st.Text(`SELECT actor_id::text FROM audit_event WHERE action = 'mirror.pass_requested' AND repository = $1`, qualified(c.repo)); got != e.person(c.who).ID {
			t.Errorf("the event of %s names actor %s", c.who, got)
		}
		if got := e.st.Text(`SELECT pass_requested_by::text FROM mirror_state WHERE repo_id = $1::text::bigint`, e.repoID(c.repo)); got != e.person(c.who).ID {
			t.Errorf("the request of %s names %s", c.who, got)
		}
	}
}
