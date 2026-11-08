//go:build integration

package debkeys

import (
	"fmt"
	"strings"
	"testing"
)

func jobsOf(e *env, repoID int64) int64 {
	return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'deb-index' AND dedup_key = $1 AND state = 'queued'`, fmt.Sprintf("index:%d", repoID))
}

// A change of a repository's keys enqueues an index build at once (R-DEB-017), and a reindex (R-REPO-016, Q-ARCH-384) upgrades that
// waiting build to a full one instead of queuing a second; a second reindex meets the first.
func TestR_REPO_016_ReindexUpgradesTheWaitingBuildAndConflictsWithAReindex(t *testing.T) {
	e := newEnv(t)
	id := e.localRepo("apt-reindex")
	path := "/api/v1/projects/" + project + "/repositories/apt-reindex/reindex"

	// A key change enqueues an automatic build, due now.
	if a := e.call("maintainer", "POST", keysPath("apt-reindex"), map[string]any{"generate": true}); a.Status != 201 {
		t.Fatalf("create next: %d %s", a.Status, a.Body)
	}
	if n := jobsOf(e, id); n != 1 {
		t.Fatalf("%d waiting builds after a key change, want 1", n)
	}
	auto := e.st.Text(`SELECT id::text FROM job WHERE kind = 'deb-index' AND dedup_key = $1 AND state = 'queued'`, fmt.Sprintf("index:%d", id))
	if args := e.st.Text(`SELECT args::text FROM job WHERE id = $1::uuid`, auto); strings.Contains(args, "reindex") {
		t.Errorf("the automatic build is a reindex: %s", args)
	}
	if d := e.st.Number(`SELECT extract(epoch FROM run_at - now())::bigint FROM job WHERE id = $1::uuid`, auto); d > 5 {
		t.Errorf("the build after a key change is due in %d s, want now", d)
	}

	// The reindex upgrades that job and answers with it.
	a := e.call("maintainer", "POST", path, nil)
	if a.Status != 202 {
		t.Fatalf("reindex: %d %s", a.Status, a.Body)
	}
	job, _ := a.JSON(t)["job"].(map[string]any)
	if job["id"] != auto || job["kind"] != "deb-index" {
		t.Errorf("job %v, want the waiting build %s", job, auto)
	}
	if loc := a.Header.Get("Location"); !strings.HasSuffix(loc, "/jobs/"+auto) {
		t.Errorf("Location %q", loc)
	}
	if jobsOf(e, id) != 1 {
		t.Errorf("%d waiting builds after the reindex, want the one, upgraded", jobsOf(e, id))
	}
	args := e.st.Text(`SELECT args::text FROM job WHERE id = $1::uuid`, auto)
	if !strings.Contains(args, `"reindex": true`) || !strings.Contains(args, `"full": true`) {
		t.Errorf("args %s, want a full reindex", args)
	}

	// A second reindex meets the first.
	b := e.call("maintainer", "POST", path, nil)
	if b.Status != 409 || b.Code(t) != "reindex_running" {
		t.Fatalf("second reindex: %d %s", b.Status, b.Body)
	}
	if j, _ := b.JSON(t)["job"].(map[string]any); j["id"] != auto {
		t.Errorf("the conflict names %v, want %s", j, auto)
	}
	if jobsOf(e, id) != 1 {
		t.Errorf("%d waiting builds after the conflict", jobsOf(e, id))
	}
}

func TestR_REPO_016_ReindexWithoutAJobBuiltIndexAnswers405(t *testing.T) {
	e := newEnv(t)
	e.st.Repository("apt-remote", "remote", "deb", "{}")
	if a := e.createRepo("npm-local", "local", "npm"); a.Status != 201 {
		t.Fatalf("create: %d %s", a.Status, a.Body)
	}
	e.localRepo("apt-perm-reindex")
	for _, repo := range []string{"apt-remote", "npm-local"} {
		a := e.call("admin", "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", nil)
		if a.Status != 405 {
			t.Errorf("%s: %d %s", repo, a.Status, a.Body)
		}
	}
	if a := e.call("admin", "POST", "/api/v1/projects/"+project+"/repositories/no-such/reindex", nil); a.Status != 404 && a.Status != 403 {
		t.Errorf("an unknown repository: %d", a.Status)
	}
	for _, who := range []string{"reader", "developer", "stranger"} {
		if a := e.call(who, "POST", "/api/v1/projects/"+project+"/repositories/apt-perm-reindex/reindex", nil); a.Status != 403 && a.Status != 404 {
			t.Errorf("%s reindexes: %d", who, a.Status)
		}
	}
	// No build was queued by any refusal.
	if n := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'deb-index'`); n != 0 {
		t.Errorf("%d builds queued by refusals", n)
	}
}
