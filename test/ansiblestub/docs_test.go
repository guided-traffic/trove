package ansiblestub

import (
	"strings"
	"testing"
)

func docsURL(f *fixture) string {
	return f.api("v3/plugin/ansible/content/published/collections/index/acme/tools/versions/1.0.0/docs-blob/")
}

// TestR_ANS_038_TheStubServesTheDocsBlobOfAVersion: the route answers the document in the Galaxy's shape (a README that holds script, a
// documentation file, a module with a full documentation, a filter without, a role with a README) and counts the request as the kind docs.
func TestR_ANS_038_TheStubServesTheDocsBlobOfAVersion(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.get(docsURL(f))
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	blob := obj(r.json(t)["docs_blob"])
	if !strings.Contains(obj(blob["collection_readme"])["html"].(string), "<script>") || len(list(blob["documentation_files"])) != 1 {
		t.Errorf("blob = %v", blob)
	}
	contents := list(blob["contents"])
	if len(contents) != 3 || obj(contents[0])["doc_strings"] == nil || obj(contents[1])["doc_strings"] != nil || obj(contents[2])["readme_html"] == nil {
		t.Errorf("contents = %v", contents)
	}
	if n := f.counts().OfKind(ClassDocs, "GET"); n != 1 {
		t.Errorf("docs requests = %d", n)
	}
	if r := f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/tools/versions/9.9.9/docs-blob/")); r.status != 404 {
		t.Errorf("an unknown version: %d", r.status)
	}
}

// TestR_ANS_038_TheDocsModesChangeOnlyTheDocsRoute: docs_404, docs_500, docs_huge, docs_invalid and the foreign href.
func TestR_ANS_038_TheDocsModesChangeOnlyTheDocsRoute(t *testing.T) {
	f := newFixture(t, Config{})
	for mode, want := range map[string]int{DocsNotFound: 404, DocsError: 500, DocsHuge: 200, DocsInvalid: 200} {
		if err := f.stub.SetMode(Mode{Docs: mode}); err != nil {
			t.Fatal(err)
		}
		r := f.get(docsURL(f))
		if r.status != want {
			t.Errorf("%s: status %d, want %d", mode, r.status, want)
		}
		if mode == DocsHuge && len(r.body) <= 16<<20 {
			t.Errorf("huge: %d bytes", len(r.body))
		}
		if mode == DocsInvalid && !strings.Contains(string(r.body), `"content_type":"module","doc_strings":null`) {
			t.Errorf("invalid document: %s", r.body)
		}
		if v := f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/tools/versions/1.0.0/")); v.status != 200 {
			t.Errorf("%s: the version document answers %d", mode, v.status)
		}
	}
	if err := f.stub.SetMode(Mode{DocsForeignHref: true}); err != nil {
		t.Fatal(err)
	}
	doc := f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/tools/versions/1.0.0/")).json(t)
	if !strings.HasPrefix(doc["href"].(string), "https://docs.elsewhere.invalid/") {
		t.Errorf("href = %v", doc["href"])
	}
	if err := f.stub.SetMode(Mode{Docs: "bogus"}); err == nil {
		t.Error("an unknown docs mode was accepted")
	}
}
