//go:build integration

package ansiblelocal

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// docsSpecBytes is a collection with documentation: 8 modules, 8 filters and 16 module_utils files, a README with a heading, a file of docs/
// and a role with a README (R-ANS-034 acceptance).
func docsSpecBytes(version string) []byte {
	s := spec(version)
	for i := range 8 {
		s.Files[fmt.Sprintf("plugins/modules/mod_%d.py", i)] = fmt.Sprintf(
			"DOCUMENTATION = r'''\nmodule: mod_%[1]d\nshort_description: Module %[1]d checks a host\ndescription:\n  - C(host) is I(checked).\noptions:\n  host:\n    type: str\n    description: The host.\n    required: true\n  nested:\n    type: dict\n    description: Nested.\n    suboptions:\n      leaf:\n        type: int\n        default: 1000\n        description: A leaf.\n'''\nEXAMPLES = '''\n- mod_%[1]d:\n    host: a\n'''\nRETURN = '''\nok:\n  description: Whether it worked.\n  returned: always\n  type: bool\n''' \n", i)
		s.Files[fmt.Sprintf("plugins/filter/f_%d.py", i)] = "x = 1\n"
	}
	for i := range 16 {
		s.Files[fmt.Sprintf("plugins/module_utils/u_%d.py", i)] = "x = 1\n"
	}
	s.Files["docs/install.md"] = "# Install\n\nRun it.\n"
	s.Files["roles/web/README.md"] = "# web role\n"
	s.Files["roles/web/tasks/main.yml"] = "- debug: msg=x\n"
	return specBytes(s)
}

func (e *env) packageID() string {
	e.t.Helper()
	l := e.st.Do(e.r, "GET", "/api/v1/packages/lookup?format=ansible&name=acme.tools", e.tokC, nil)
	if l.Status != 200 {
		e.t.Fatalf("lookup: %d %s", l.Status, l.Body)
	}
	id, _ := l.JSON(e.t)["id"].(string)
	return id
}

// TestR_ANS_036_ThePublishedDocumentationIsServedByTheOperationsAndTheRoute: a publish stores the generated documentation with the version; the
// tree has the counts of its groups however one searches, the entry and the file are sanitized HTML, the details announce the documentation,
// and the protocol route serves the document with a validator.
func TestR_ANS_036_ThePublishedDocumentationIsServedByTheOperationsAndTheRoute(t *testing.T) {
	e := newEnv(t)
	a := e.publish(e.tokA, docsSpecBytes("1.0.0"), true)
	if a.Status != 202 {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	if got := e.st.Number(`SELECT entry_count FROM ansible_version_docs WHERE repo_id = $1 AND version = '1.0.0'`, e.repoID()); got != 34 {
		t.Errorf("entry_count = %d, want 9 modules + 8 filters + 16 module_utils + 1 role", got)
	}
	id := e.packageID()
	p := "/api/v1/packages/" + id + "/versions/1.0.0"
	d := e.st.Do(e.r, "GET", p, e.tokC, nil)
	docs, _ := d.JSON(t)["metadata"].(map[string]any)["docs"].(map[string]any)
	if d.Status != 200 || docs["state"] != "available" || docs["source"] != "generated" {
		t.Fatalf("details: %d %s", d.Status, d.Body)
	}

	tree := e.st.Do(e.r, "GET", p+"/docs", e.tokC, nil)
	if tree.Status != 200 {
		t.Fatalf("tree: %d %s", tree.Status, tree.Body)
	}
	counts := func(a map[string]any) map[string]float64 {
		out := map[string]float64{}
		for _, g := range a["groups"].([]any) {
			gm := g.(map[string]any)
			out[gm["category"].(string)] = gm["count"].(float64)
		}
		return out
	}
	doc := tree.JSON(t)
	if c := counts(doc); c["module"] != 9 || c["plugin"] != 24 || c["role"] != 1 || doc["repository"] != project+"/"+repo || doc["state"] != "available" {
		t.Errorf("tree = %v", doc)
	}
	files := doc["files"].([]any)
	if len(files) != 2 || files[0].(map[string]any)["name"] != "README.md" || files[0].(map[string]any)["title"] != "acme.tools 1.0.0" {
		t.Errorf("files = %v", files)
	}
	q := e.st.Do(e.r, "GET", p+"/docs?q=mod_3", e.tokC, nil)
	qd := q.JSON(t)
	if c := counts(qd); q.Status != 200 || c["module"] != 9 || c["plugin"] != 24 || qd["matchCount"] != float64(1) {
		t.Errorf("search: %d %s", q.Status, q.Body)
	}
	if bad := e.st.Do(e.r, "GET", p+"/docs?q=a", e.tokC, nil); bad.Status != 422 || !strings.Contains(string(bad.Body), "invalid_query") {
		t.Errorf("q=a: %d %s", bad.Status, bad.Body)
	}

	entry := e.st.Do(e.r, "GET", p+"/docs/entries/module/mod_2", e.tokC, nil)
	ed := entry.JSON(t)
	opts, _ := ed["options"].([]any)
	if entry.Status != 200 || ed["fullName"] != "acme.tools.mod_2" || ed["hasDocs"] != true || len(opts) != 2 {
		t.Fatalf("entry: %d %s", entry.Status, entry.Body)
	}
	if desc, _ := ed["description"].([]any); len(desc) != 1 || !strings.Contains(desc[0].(string), "<code>host</code>") || !strings.Contains(desc[0].(string), "<em>checked</em>") {
		t.Errorf("description = %v", ed["description"])
	}
	sub := opts[1].(map[string]any)["suboptions"].([]any)[0].(map[string]any)
	if sub["default"] != float64(1000) {
		t.Errorf("suboption = %v", sub)
	}
	if none := e.st.Do(e.r, "GET", p+"/docs/entries/module/nope", e.tokC, nil); none.Status != 404 {
		t.Errorf("a missing entry: %d", none.Status)
	}
	if bare := e.st.Do(e.r, "GET", p+"/docs/entries/filter/f_1", e.tokC, nil); bare.Status != 200 || bare.JSON(t)["hasDocs"] != false {
		t.Errorf("an entry without documentation: %d %s", bare.Status, bare.Body)
	}
	role := e.st.Do(e.r, "GET", p+"/docs/entries/role/web", e.tokC, nil)
	if role.Status != 200 || !strings.Contains(string(role.Body), "web role") {
		t.Errorf("role: %d %s", role.Status, role.Body)
	}

	file := e.st.Do(e.r, "GET", p+"/docs/files/README.md", e.tokC, nil)
	if file.Status != 200 || !strings.Contains(string(file.Body), "Tools for the tests") || file.Header.Get("ETag") == "" {
		t.Errorf("file: %d %s", file.Status, file.Body)
	}
	if cached := e.st.Do(e.r, "GET", p+"/docs/files/README.md", e.tokC, nil, "If-None-Match", file.Header.Get("ETag")); cached.Status != 304 {
		t.Errorf("a repeated file read: %d", cached.Status)
	}
	if other := e.st.Do(e.r, "GET", p+"/docs/files/other.md", e.tokC, nil); other.Status != 404 {
		t.Errorf("a file that is not listed: %d", other.Status)
	}

	blob := e.get(e.tokC, "v3/collections/acme/tools/versions/1.0.0/docs-blob/")
	if blob.Status != 200 || blob.Header.Get("ETag") == "" {
		t.Fatalf("docs-blob: %d %s", blob.Status, blob.Body)
	}
	for _, name := range []string{`"content_name":"mod_0"`, `"content_name":"web"`, `"collection_readme"`, `"install.md"`} {
		if !strings.Contains(string(blob.Body), name) {
			t.Errorf("docs-blob lacks %s", name)
		}
	}
	if cached := e.st.Do(e.r, "GET", base+"v3/collections/acme/tools/versions/1.0.0/docs-blob/", e.tokC, nil, "If-None-Match", blob.Header.Get("ETag")); cached.Status != 304 {
		t.Errorf("a repeated docs-blob read: %d", cached.Status)
	}
	if unknown := e.get(e.tokC, "v3/collections/acme/tools/versions/9.9.9/docs-blob/"); unknown.Status != 404 {
		t.Errorf("an unknown version: %d", unknown.Status)
	}
}

// TestR_ANS_033_ADeletedVersionLeavesNoDocumentationAndAReindexRebuildsIt: the removal of the version removes its rows; the reindex generates
// the rows of a version that has none from the archive and raises the revision.
func TestR_ANS_033_ADeletedVersionLeavesNoDocumentationAndAReindexRebuildsIt(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if a := e.publish(e.tokA, docsSpecBytes(v), true); a.Status != 202 {
			t.Fatalf("publish %s: %d %s", v, a.Status, a.Body)
		}
	}
	id := e.repoID()
	rowsOf := func(table string) int64 {
		return e.st.Number(`SELECT count(*) FROM `+table+` WHERE repo_id = $1`, id)
	}
	if rowsOf("ansible_version_docs") != 2 {
		t.Fatalf("version rows = %d", rowsOf("ansible_version_docs"))
	}
	e.deleteVersion("1.0.0")
	if got := e.st.Number(`SELECT count(*) FROM ansible_version_docs WHERE repo_id = $1 AND version = '1.0.0'`, id); got != 0 {
		t.Errorf("a deleted version kept %d rows", got)
	}
	if rowsOf("ansible_doc_entries") == 0 || e.st.Number(`SELECT count(*) FROM ansible_doc_entries WHERE repo_id = $1 AND version = '1.0.0'`, id) != 0 {
		t.Errorf("entries after the deletion: %d", rowsOf("ansible_doc_entries"))
	}

	e.st.Exec(`DELETE FROM ansible_version_docs WHERE repo_id = $1`, id)
	before := e.st.Number(`SELECT rev_seq FROM packages WHERE repo_id = $1 AND name = 'acme.tools'`, id)
	a := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", e.tokAdmin, nil)
	if a.Status != 202 {
		t.Fatalf("reindex: %d %s", a.Status, a.Body)
	}
	jobID, _ := a.JSON(t)["job"].(map[string]any)["id"].(string)
	waitFor(t, "the reindex job", 60*time.Second, func() bool {
		return e.st.Text(`SELECT state FROM job WHERE id = $1::uuid`, jobID) == "succeeded"
	})
	if got := e.st.Number(`SELECT count(*) FROM ansible_version_docs WHERE repo_id = $1 AND version = '1.1.0' AND source = 'generated'`, id); got != 1 {
		t.Errorf("the reindex rebuilt %d rows of the version", got)
	}
	if got := e.st.Number(`SELECT rev_seq FROM packages WHERE repo_id = $1 AND name = 'acme.tools'`, id); got <= before {
		t.Errorf("rev_seq %d did not rise above %d", got, before)
	}
	if blob := e.get(e.tokC, "v3/collections/acme/tools/versions/1.1.0/docs-blob/"); blob.Status != 200 {
		t.Errorf("docs-blob after the reindex: %d", blob.Status)
	}
}

// TestR_ANS_034_APublishWithAnUnterminatedLiteralIsAcceptedWithAWarning: the generator's bound never fails a publish; the warning is a message
// of the import task and of the tree for a caller who holds deploy.
func TestR_ANS_034_APublishWithAnUnterminatedLiteralIsAcceptedWithAWarning(t *testing.T) {
	e := newEnv(t)
	s := spec("1.0.0")
	s.Files["plugins/modules/open.py"] = "DOCUMENTATION = '''\nshort_description: x\n"
	a := e.publish(e.tokA, specBytes(s), true)
	if a.Status != 202 {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	task := e.task(e.tokA, a)
	if !strings.Contains(fmt.Sprint(task["messages"]), "docs_literal_unterminated") {
		t.Errorf("the task = %v", task["messages"])
	}
	id := e.packageID()
	p := "/api/v1/packages/" + id + "/versions/1.0.0/docs"
	withDeploy := e.st.Do(e.r, "GET", p, e.tokA, nil)
	if !strings.Contains(string(withDeploy.Body), "docs_literal_unterminated") {
		t.Errorf("a deployer sees no warning: %s", withDeploy.Body)
	}
	if readOnly := e.st.Do(e.r, "GET", p, e.tokC, nil); strings.Contains(string(readOnly.Body), "docs_literal_unterminated") {
		t.Errorf("a reader sees the warnings: %s", readOnly.Body)
	}
}

// TestR_ANS_036_ImportTasksCanBeFilteredByCollectionAndVersion: Q-API-262.
func TestR_ANS_036_ImportTasksCanBeFilteredByCollectionAndVersion(t *testing.T) {
	e := newEnv(t)
	e.mustPublish("1.0.0")
	e.mustPublish("1.1.0")
	u := "/api/v1/projects/" + project + "/repositories/" + repo + "/ansible/imports"
	count := func(q string) int {
		a := e.st.Do(e.r, "GET", u+q, e.tokA, nil)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", q, a.Status, a.Body)
		}
		return len(a.JSON(t)["items"].([]any))
	}
	if count("") != 2 || count("?package=acme.tools") != 2 || count("?package=acme.tools&version=1.1.0") != 1 || count("?package=acme.other") != 0 || count("?version=9.9.9") != 0 {
		t.Error("the filters package and version do not narrow the list")
	}
}
