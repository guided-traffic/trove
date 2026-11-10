//go:build integration

package ansiblelocal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func code(t *testing.T, a testenv.Answer) string {
	t.Helper()
	var b struct {
		Errors []struct{ Code, Detail string }
	}
	if err := json.Unmarshal(a.Body, &b); err != nil || len(b.Errors) != 1 {
		t.Fatalf("status %d, body %s", a.Status, a.Body)
	}
	return b.Errors[0].Code
}

// TestR_ANS_008_PublishOverHTTP: the publish in both orders of its parts stores the version, the row, the item and the task; the task is
// final when the client polls it; the event names the principal and the method (R-ANS-008, R-ANS-009, R-ANS-030).
func TestR_ANS_008_PublishOverHTTP(t *testing.T) {
	e := newEnv(t)
	for i, shaFirst := range []bool{true, false} {
		ver := []string{"1.0.0", "1.1.0"}[i]
		a := e.publish(e.tokA, collectiontest.Build(spec(ver)), shaFirst)
		if a.Status != 202 {
			t.Fatalf("shaFirst=%v: %d %s", shaFirst, a.Status, a.Body)
		}
		task := e.task(e.tokA, a)
		if task["state"] != "completed" || task["finished_at"] == nil || task["error"] != nil || task["version"] != ver {
			t.Errorf("shaFirst=%v: task %v", shaFirst, task)
		}
	}
	if got := e.versionRows(); got != 2 {
		t.Errorf("rows = %d, want 2", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM ansible_import_tasks WHERE repo_id = $1 AND state = 'completed'`, e.repoID()); got != 2 {
		t.Errorf("tasks = %d, want 2", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path LIKE 'collections/acme/tools/%.tar.gz'`, e.repoID()); got != 2 {
		t.Errorf("archive items = %d, want 2", got)
	}
	if got := e.eventCount("package.published"); got != 2 {
		t.Errorf("package.published events = %d, want 2", got)
	}
	if got := e.st.Text(`SELECT actor_id::text FROM audit_event WHERE action = 'package.published' LIMIT 1`); got != e.alice.ID {
		t.Errorf("the event names %s, want alice %s", got, e.alice.ID)
	}
	// The generic rows: the package, its versions and the properties of the archive.
	if got := e.st.Number(`SELECT count(*) FROM packages p JOIN package_versions v ON v.package_id = p.id WHERE p.repo_id = $1 AND p.name = 'acme.tools'`, e.repoID()); got != 2 {
		t.Errorf("generic versions = %d, want 2", got)
	}
}

// TestR_ANS_008_ARepeatIsIdempotentOtherBytesAreRefusedAndAWrongDigestStoresNothing covers the criteria of R-ANS-008 one by one.
func TestR_ANS_008_ARepeatIsIdempotentOtherBytesAreRefusedAndAWrongDigestStoresNothing(t *testing.T) {
	e := newEnv(t)
	archive := collectiontest.Build(spec("1.0.0"))
	first := e.publish(e.tokA, archive, true)
	t1 := e.task(e.tokA, first)
	again := e.publish(e.tokA, archive, true)
	if again.Status != 202 {
		t.Fatalf("the repeat: %d %s", again.Status, again.Body)
	}
	t2 := e.task(e.tokA, again)
	if t2["id"] == t1["id"] || t2["state"] != "completed" {
		t.Errorf("repeat task %v, first %v", t2, t1)
	}
	if e.versionRows() != 1 || e.eventCount("package.published") != 1 {
		t.Errorf("the repeat changed rows (%d) or wrote an event (%d)", e.versionRows(), e.eventCount("package.published"))
	}
	dl := e.get(e.tokA, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz")
	if dl.Status != 200 || sum(dl.Body) != sum(archive) {
		t.Errorf("the download after the repeat: %d, digest %s", dl.Status, sum(dl.Body))
	}

	other := spec("1.0.0")
	other.Files["README.md"] = "other bytes"
	if a := e.publish(e.tokA, collectiontest.Build(other), true); a.Status != 409 || code(t, a) != "version_exists" || !strings.Contains(string(a.Body), "1.0.0") {
		t.Errorf("other bytes: %d %s", a.Status, a.Body)
	}

	body, ct := form(collectiontest.Build(spec("1.1.0")), true, strings.Repeat("0", 64))
	if a := e.st.Do(e.r, "POST", base+"v3/artifacts/collections/", e.tokA, body, "Content-Type", ct); a.Status != 400 || code(t, a) != "sha256_mismatch" {
		t.Errorf("a wrong digest: %d %s", a.Status, a.Body)
	}
	if e.versionRows() != 1 {
		t.Errorf("a wrong digest left %d rows", e.versionRows())
	}
	if a := e.get(e.tokA, "v3/collections/acme/tools/versions/1.1.0/"); a.Status != 404 {
		t.Errorf("the refused version is served: %d", a.Status)
	}
}

// TestR_ANS_008_ARawBodyPublishesAndFilesTakesNothing: a scripted publish posts the archive as the body (Q-ANS-34), and /files/ deploys
// nothing into a Galaxy repository, which takes collections only through the protocol (R-STOR-007).
func TestR_ANS_008_ARawBodyPublishesAndFilesTakesNothing(t *testing.T) {
	e := newEnv(t)
	archive := collectiontest.Build(spec("1.0.0"))
	a := e.st.Do(e.r, "POST", base+"v3/artifacts/collections/?sha256="+sum(archive), e.tokA, archive, "Content-Type", "application/octet-stream")
	if a.Status != 202 || e.task(e.tokA, a)["state"] != "completed" {
		t.Fatalf("raw publish: %d %s", a.Status, a.Body)
	}
	put := e.st.Do(e.r, "PUT", "/files/"+project+"/"+repo+"/collections/acme/tools/acme-tools-1.1.0.tar.gz", e.tokA, collectiontest.Build(spec("1.1.0")), "Content-Type", "application/gzip")
	if put.Status != 409 || e.versionRows() != 1 {
		t.Errorf("/files/ upload: %d %s, rows %d", put.Status, put.Body, e.versionRows())
	}
}

// TestR_ANS_009_AnArchiveThatIsNoCollectionIsAFailedTaskAndStoresNothing:a refused archive is a failed task the client prints, not an
// HTTP error; each crafted archive has its own message and the repository holds nothing (R-ANS-009, R-ANS-010, R-ANS-031).
func TestR_ANS_009_AnArchiveThatIsNoCollectionIsAFailedTaskAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	s := spec("1.0.0")
	crafted := map[string][]byte{
		"not a gzip tarball": []byte("not an archive"),
		"a symlink member": collectiontest.Pack(append(collectiontest.Entries(s),
			collectiontest.Entry{Name: "plugins/l", Type: '2', Link: "/etc/passwd"})),
		"a member ../x":      collectiontest.Pack(append(collectiontest.Entries(s), collectiontest.Entry{Name: "../x", Body: []byte("x")})),
		"an unlisted member": collectiontest.Pack(append(collectiontest.Entries(s), collectiontest.Entry{Name: "extra", Body: []byte("x")})),
	}
	seen := map[string]bool{}
	for name, archive := range crafted {
		a := e.publish(e.tokA, archive, true)
		if a.Status != 202 {
			t.Errorf("%s: %d %s", name, a.Status, a.Body)
			continue
		}
		task := e.task(e.tokA, a)
		errDoc, _ := task["error"].(map[string]any)
		if task["state"] != "failed" || errDoc == nil || errDoc["code"] != "invalid_archive" {
			t.Errorf("%s: task %v", name, task)
			continue
		}
		d, _ := errDoc["description"].(string)
		if d == "" || seen[d] {
			t.Errorf("%s: the description %q is empty or repeated", name, d)
		}
		seen[d] = true
	}
	if e.versionRows() != 0 || e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file'`, e.repoID()) != 0 {
		t.Error("a refused archive left rows or items")
	}
	if got := e.eventCount("ansible.import_task.failed"); got != int64(len(crafted)) {
		waitFor(t, "the failed-task events", 5e9, func() bool { return e.eventCount("ansible.import_task.failed") == int64(len(crafted)) })
	}
	// A name or a version outside the grammar is the failed task of its own code.
	bad := spec("1.0")
	if a := e.publish(e.tokA, collectiontest.Build(bad), true); e.task(e.tokA, a)["error"].(map[string]any)["code"] != "invalid_version" {
		t.Errorf("version 1.0: %s", a.Body)
	}
	upper := spec("1.0.0")
	upper.Name = "Tools"
	if a := e.publish(e.tokA, collectiontest.Build(upper), true); e.task(e.tokA, a)["error"].(map[string]any)["code"] != "invalid_name" {
		t.Errorf("name Tools: %s", a.Body)
	}
	if a := e.get(e.tokA, "v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/"); a.Status != 404 {
		t.Errorf("an unknown task: %d", a.Status)
	}
}

// TestR_ANS_009_TheImportTasksAreListedForTheRepository: the management list shows the tasks newest first and filters by state.
func TestR_ANS_009_TheImportTasksAreListedForTheRepository(t *testing.T) {
	e := newEnv(t)
	e.mustPublish("1.0.0")
	e.mustPublish("1.1.0")
	e.publish(e.tokA, []byte("junk"), true)
	list := "/api/v1/projects/" + project + "/repositories/" + repo + "/ansible/imports"
	a := e.st.Do(e.r, "GET", list, e.tokC, nil)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	items, _ := a.JSON(t)["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["state"] != "failed" {
		t.Errorf("tasks %s", a.Body)
	}
	failed := e.st.Do(e.r, "GET", list+"?state=failed", e.tokC, nil)
	if got, _ := failed.JSON(t)["items"].([]any); failed.Status != 200 || len(got) != 1 {
		t.Errorf("failed tasks: %d %s", failed.Status, failed.Body)
	}
	page := e.st.Do(e.r, "GET", list+"?limit=2", e.tokC, nil)
	if next, _ := page.JSON(t)["nextCursor"].(string); page.Status != 200 || next == "" {
		t.Errorf("first page: %d %s", page.Status, page.Body)
	} else if second := e.st.Do(e.r, "GET", list+"?limit=2&cursor="+next, e.tokC, nil); len(second.JSON(t)["items"].([]any)) != 1 {
		t.Errorf("second page: %s", second.Body)
	}
	if a := e.st.Do(e.r, "GET", list+"?state=weird", e.tokC, nil); a.Status != 400 {
		t.Errorf("a state that is none: %d", a.Status)
	}
}

// TestR_ANS_004_TheReadOperationsOverHTTP: the three documents and the download with absolute addresses, the order by precedence, the
// paging and a range request (R-ANS-004, R-ANS-005).
func TestR_ANS_004_TheReadOperationsOverHTTP(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"1.0.0", "2.0.0-rc.1", "1.1.0"} {
		e.mustPublish(v)
	}
	e.mustPublish("2.0.0")
	list := e.get(e.tokC, "v3/collections/acme/tools/versions/?limit=2")
	if list.Status != 200 {
		t.Fatalf("%d %s", list.Status, list.Body)
	}
	var doc struct {
		Meta  struct{ Count int }
		Links map[string]*string
		Data  []struct{ Version string }
	}
	if err := json.Unmarshal(list.Body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Meta.Count != 4 || len(doc.Data) != 2 || doc.Data[0].Version != "2.0.0" || doc.Data[1].Version != "2.0.0-rc.1" {
		t.Errorf("list = %s", list.Body)
	}
	if doc.Links["next"] == nil || !strings.HasPrefix(*doc.Links["next"], "http://trove.test/api/ansible/") && !strings.HasPrefix(*doc.Links["next"], "https://trove.test/api/ansible/") {
		t.Errorf("next = %v: an absolute address on the base URL", doc.Links["next"])
	}
	col := e.get(e.tokC, "v3/collections/acme/tools/").JSON(t)
	if hv, _ := col["highest_version"].(map[string]any); hv["version"] != "2.0.0" {
		t.Errorf("highest_version = %v", col["highest_version"])
	}
	v := e.get(e.tokC, "v3/collections/acme/tools/versions/1.0.0/").JSON(t)
	dl, _ := v["download_url"].(string)
	art, _ := v["artifact"].(map[string]any)
	if !strings.Contains(dl, "/api/ansible/"+project+"/"+repo+"/v3/artifacts/collections/acme-tools-1.0.0.tar.gz") || art["sha256"] != sum(collectiontest.Build(spec("1.0.0"))) {
		t.Errorf("version = %v", v)
	}
	if sigs, _ := v["signatures"].([]any); len(sigs) != 0 {
		t.Errorf("a local version has signatures %v", sigs)
	}
	file := dl[strings.Index(dl, "/api/ansible/"):]
	full := e.st.Do(e.r, "GET", file, e.tokC, nil)
	if full.Status != 200 || sum(full.Body) != art["sha256"] || full.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("download: %d, digest %s, ranges %q", full.Status, sum(full.Body), full.Header.Get("Accept-Ranges"))
	}
	part := e.st.Do(e.r, "GET", file, e.tokC, nil, "Range", "bytes=0-99")
	if part.Status != 206 || len(part.Body) != 100 || string(part.Body) != string(full.Body[:100]) {
		t.Errorf("range: %d, %d bytes", part.Status, len(part.Body))
	}
	if head := e.st.Do(e.r, "HEAD", file, e.tokC, nil); head.Status != 200 || len(head.Body) != 0 {
		t.Errorf("HEAD: %d with %d bytes", head.Status, len(head.Body))
	}
	if root := e.get(e.tokC, ""); root.Status != 200 || strings.Contains(string(root.Body), "v1") || !strings.Contains(string(root.Body), "available_versions") {
		t.Errorf("root: %d %s", root.Status, root.Body)
	}
	if a := e.get(e.tokC, "v3/collections/nope/nope/"); a.Status != 404 || code(t, a) != "not_found" {
		t.Errorf("an unknown collection: %d %s", a.Status, a.Body)
	}
	if a := e.get(e.tokC, "v1/roles/"); a.Status != 404 || !strings.Contains(string(a.Body), `"default"`) {
		t.Errorf("roles on a local repository: %d %s", a.Status, a.Body)
	}
	// No redirect for a path without the trailing slash.
	if a := e.get(e.tokC, "v3/collections/acme/tools"); a.Status != 200 {
		t.Errorf("without the trailing slash: %d", a.Status)
	}
}
