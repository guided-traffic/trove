//go:build integration

package ansibleremote

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func listOf(v any) []any { l, _ := v.([]any); return l }

// TestR_ANS_026_ACollectionInstalledThroughARemoteIsListedAndItsDocumentationIsShown: the archive a client installs through a remote makes
// the package, the version and the files appear in the package API, the details announce the documentation the job fetched, and the three
// documentation operations answer for the cached version.
func TestR_ANS_026_ACollectionInstalledThroughARemoteIsListedAndItsDocumentationIsShown(t *testing.T) {
	e := newEnv(t, options{})
	r := e.jobsReplica()
	repo := e.fresh()
	if a := e.get(r, repo, "v3/collections/acme/tools/versions/2.0.0/"); a.Status != http.StatusOK {
		t.Fatalf("version document: %d %s", a.Status, a.Body)
	}
	if a := e.get(r, repo, "v3/artifacts/collections/acme-tools-2.0.0.tar.gz"); a.Status != http.StatusOK {
		t.Fatalf("archive: %d", a.Status)
	}
	eventually(t, 30*time.Second, "the documentation job", func() bool { return e.docsState(repo) == "available" })

	list := e.st.Do(r, http.MethodGet, "/api/v1/packages?format=ansible", e.admin, nil)
	items := listOf(list.JSON(t)["items"])
	if list.Status != http.StatusOK || len(items) != 1 || obj(items[0])["name"] != "acme.tools" {
		t.Fatalf("list: %d %s", list.Status, list.Body)
	}
	lookup := e.st.Do(r, http.MethodGet, "/api/v1/packages/lookup?format=ansible&name=acme.tools", e.admin, nil)
	id, _ := lookup.JSON(t)["id"].(string)
	if lookup.Status != http.StatusOK || id == "" {
		t.Fatalf("lookup: %d %s", lookup.Status, lookup.Body)
	}
	p := "/api/v1/packages/" + id + "/versions/2.0.0"
	d := e.st.Do(r, http.MethodGet, p, e.admin, nil)
	md := obj(d.JSON(t)["metadata"])
	if d.Status != http.StatusOK || obj(md["docs"])["state"] != "available" || obj(md["docs"])["source"] != "upstream" {
		t.Fatalf("details: %d %s", d.Status, d.Body)
	}
	detail := d.JSON(t)
	deps, _ := md["dependencies"].(map[string]any)
	if detail["licenses"].([]any)[0] != "GPL-3.0-or-later" || md["requiresAnsible"] != ">=2.15.0" || deps["acme.base"] != ">=1.0.0" || md["authors"] == nil {
		t.Errorf("the page shows %v", detail)
	}
	if items[0].(map[string]any)["description"] != "trove test collection acme.tools" {
		t.Errorf("the list shows %v", items[0])
	}
	if files := listOf(detail["files"]); len(files) != 1 {
		t.Errorf("files = %v", files)
	}
	tree := e.st.Do(r, http.MethodGet, p+"/docs", e.admin, nil)
	td := tree.JSON(t)
	if tree.Status != http.StatusOK || td["state"] != "available" || td["repository"] != project+"/"+repo || len(listOf(td["groups"])) != 3 {
		t.Fatalf("tree: %d %s", tree.Status, tree.Body)
	}
	entry := e.st.Do(r, http.MethodGet, p+"/docs/entries/module/ping_host", e.admin, nil)
	if entry.Status != http.StatusOK || strings.Contains(string(entry.Body), "<script") || !strings.Contains(string(entry.Body), `"hasDocs":true`) {
		t.Errorf("entry: %d %s", entry.Status, entry.Body)
	}
	file := e.st.Do(r, http.MethodGet, p+"/docs/files/README.md", e.admin, nil)
	if file.Status != http.StatusOK || strings.Contains(string(file.Body), "<script") || strings.Contains(string(file.Body), "onerror") {
		t.Errorf("file: %d %s", file.Status, file.Body)
	}
}
