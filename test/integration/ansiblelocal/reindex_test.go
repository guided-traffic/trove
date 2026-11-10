//go:build integration

package ansiblelocal

import (
	"strings"
	"testing"
	"time"
)

// TestR_ANS_012_AReindexRestoresARowFromItsArchive: after the metadata of a row was corrupted and the row of another archive was lost (an
// import by copy, a restore), the job ansible-reindex rewrites the first from the archive and creates the second, raises the revision of
// the collection and changes no stored byte (R-ANS-012, Q-ANS-26).
func TestR_ANS_012_AReindexRestoresARowFromItsArchive(t *testing.T) {
	e := newEnv(t)
	e.mustPublish("1.0.0")
	e.mustPublish("1.1.0")
	id := e.repoID()
	before := e.st.Number(`SELECT rev_seq FROM packages WHERE repo_id = $1 AND name = 'acme.tools'`, id)
	blobs := e.st.Text(`SELECT string_agg(blob_sha256::text, ',' ORDER BY path) FROM items WHERE repo_id = $1 AND kind = 'file'`, id)
	e.st.Exec(`UPDATE ansible_collection_versions SET requires_ansible = 'corrupt', sort_key = '\x00' WHERE repo_id = $1 AND version = '1.0.0'`, id)
	e.st.Exec(`DELETE FROM ansible_collection_versions WHERE repo_id = $1 AND version = '1.1.0'`, id)

	a := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", e.tokAdmin, nil)
	if a.Status != 202 {
		t.Fatalf("reindex: %d %s", a.Status, a.Body)
	}
	job, _ := a.JSON(t)["job"].(map[string]any)
	jobID, _ := job["id"].(string)
	waitFor(t, "the reindex job", 30*time.Second, func() bool {
		return e.st.Text(`SELECT state FROM job WHERE id = $1::uuid`, jobID) == "succeeded"
	})
	if got := e.st.Text(`SELECT requires_ansible FROM ansible_collection_versions WHERE repo_id = $1 AND version = '1.0.0'`, id); got != ">=2.14.0" {
		t.Errorf("requires_ansible = %q, want the archive's", got)
	}
	if e.versionRows() != 2 {
		t.Errorf("rows = %d, want the lost one created again", e.versionRows())
	}
	if got := e.st.Number(`SELECT rev_seq FROM packages WHERE repo_id = $1 AND name = 'acme.tools'`, id); got <= before {
		t.Errorf("rev_seq %d did not rise above %d", got, before)
	}
	if got := e.st.Text(`SELECT string_agg(blob_sha256::text, ',' ORDER BY path) FROM items WHERE repo_id = $1 AND kind = 'file'`, id); got != blobs {
		t.Errorf("the reindex changed stored bytes")
	}
	list := e.get(e.tokC, "v3/collections/acme/tools/versions/")
	if i, j := strings.Index(string(list.Body), `"1.1.0"`), strings.Index(string(list.Body), `"1.0.0"`); i < 0 || j < 0 || i > j {
		t.Errorf("the list after the reindex: %s", list.Body)
	}
	// A second reindex finds nothing to change.
	waitFor(t, "the job to leave the queue", 10*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'ansible-reindex' AND state NOT IN ('succeeded', 'failed', 'cancelled')`) == 0
	})
	rev := e.st.Number(`SELECT rev_seq FROM packages WHERE repo_id = $1 AND name = 'acme.tools'`, id)
	b := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", e.tokAdmin, nil)
	if b.Status != 202 {
		t.Fatalf("second reindex: %d %s", b.Status, b.Body)
	}
	second, _ := b.JSON(t)["job"].(map[string]any)
	waitFor(t, "the second reindex", 30*time.Second, func() bool {
		return e.st.Text(`SELECT state FROM job WHERE id = $1::uuid`, second["id"].(string)) == "succeeded"
	})
	if got := e.st.Number(`SELECT rev_seq FROM packages WHERE repo_id = $1 AND name = 'acme.tools'`, id); got != rev {
		t.Errorf("an unchanged reindex raised the revision: %d to %d", rev, got)
	}
}

// TestR_ANS_012_TheReindexOfAnotherClassOrFormatIsNotApplicable: a remote repository's index is the upstream's (R-REPO-016).
func TestR_ANS_012_TheReindexOfAnotherFormatIsNotApplicable(t *testing.T) {
	e := newEnvOpts(t, envOpts{seed: func(e *env) { e.st.Repository("generic-local", "local", "generic", "{}") }})
	if a := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/generic-local/reindex", e.tokAdmin, nil); a.Status != 405 {
		t.Errorf("reindex of a generic repository: %d %s", a.Status, a.Body)
	}
}

// TestR_ANS_009_TheSweepDeletesOldTasksAndKeepsTheVersions: the daily task removes the tasks older than ansible.import_task_keep.
func TestR_ANS_009_TheSweepDeletesOldTasksAndKeepsTheVersions(t *testing.T) {
	e := newEnv(t)
	e.mustPublish("1.0.0")
	e.mustPublish("1.1.0")
	id := e.repoID()
	e.st.Exec(`UPDATE ansible_import_tasks SET created_at = now() - interval '800 hours' WHERE repo_id = $1 AND version = '1.0.0'`, id)
	// The scheduler may have a run queued already; a run is started once none is, so it reads the rows as they are now.
	waitFor(t, "the sweep to be started", 30*time.Second, func() bool {
		return e.st.Do(e.r, "POST", "/api/v1/system/tasks/ansible-import-task-sweep/run", e.tokAdmin, nil).Status == 202
	})
	waitFor(t, "the sweep", 30*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM ansible_import_tasks WHERE repo_id = $1`, id) == 1
	})
	if e.versionRows() != 2 {
		t.Errorf("the sweep deleted a version: %d rows", e.versionRows())
	}
}
