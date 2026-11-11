package galaxydoc_test

import (
	"encoding/json"
	"errors"
	"html"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

const upstream = `{"docs_blob":{
 "collection_readme":{"name":"README.md","html":"<h1>x</h1><script>alert(1)</script>"},
 "documentation_files":[{"name":"install.md","html":"<h2>Install</h2>"},{"name":"../evil.md","html":"x"}],
 "contents":[
  {"content_name":"ping_host","content_type":"module","doc_strings":{"doc":{"module":"ping_host","short_description":"Pings","description":["Sends C(count) probes"],"options":[{"name":"host","type":"str"}],"junk":"dropped"},"return":[{"name":"rtt"}],"examples":"- x","metadata":null},"readme_file":null,"readme_html":null},
  {"content_name":"is_cidr","content_type":"filter","doc_strings":null,"readme_file":null,"readme_html":null},
  {"content_name":"web","content_type":"role","doc_strings":null,"readme_file":"README.md","readme_html":"<p>role</p><script>x</script>"}
 ]}}`

// TestR_ANS_035_AnUpstreamsDocumentIsParsedToTheNeutralForm: the entries, the files and the README are read, the keys outside the contract are
// dropped, a file with a name that is a path is left out with a warning, and the HTML stays as received.
func TestR_ANS_035_AnUpstreamsDocumentIsParsedToTheNeutralForm(t *testing.T) {
	set, err := galaxydoc.ParseUpstreamDocs([]byte(upstream))
	if err != nil {
		t.Fatal(err)
	}
	if set.Source != rows.DocSourceUpstream || set.Generator != 0 || len(set.Entries) != 3 || set.Readme == nil || !strings.Contains(set.Readme.Body, "<script>") {
		t.Fatalf("set = %+v", set)
	}
	if len(set.Files) != 1 || set.Files[0].Name != "install.md" || len(set.Warnings) != 1 {
		t.Errorf("files %+v, warnings %+v", set.Files, set.Warnings)
	}
	var module rows.DocEntry
	for _, e := range set.Entries {
		if e.Name == "ping_host" {
			module = e
		}
	}
	if !module.HasDocs || module.ShortDescription != "Pings" || !strings.Contains(module.SearchText, "ping host") || !strings.Contains(module.SearchText, "host") {
		t.Errorf("module = %+v", module)
	}
	if strings.Contains(string(module.Doc), "junk") || !strings.Contains(string(module.Doc), `"examples":"- x"`) {
		t.Errorf("doc_strings = %s", module.Doc)
	}
	if set.Entries[0].Type != "filter" || set.Entries[1].Type != "module" || set.Entries[2].Type != "role" {
		t.Errorf("order = %v %v %v", set.Entries[0].Type, set.Entries[1].Type, set.Entries[2].Type)
	}
	if role := set.Entries[2]; role.HasDocs || !strings.Contains(role.ReadmeHTML, "<script>") {
		t.Errorf("role = %+v", role)
	}
}

// TestR_ANS_035_ADocumentThatIsNotTheShapeIsRefused: an item without a content_name or a content_type, a name or a type outside the grammar, a
// duplicate and a document above the bound are errors of the kinds the fill records.
func TestR_ANS_035_ADocumentThatIsNotTheShapeIsRefused(t *testing.T) {
	for name, doc := range map[string]string{
		"not json":        `<html>`,
		"no docs_blob":    `{"x":1}`,
		"no content_name": `{"docs_blob":{"contents":[{"content_type":"module"}]}}`,
		"no content_type": `{"docs_blob":{"contents":[{"content_name":"x"}]}}`,
		"a bad name":      `{"docs_blob":{"contents":[{"content_name":"A__b","content_type":"module"}]}}`,
		"a bad type":      `{"docs_blob":{"contents":[{"content_name":"x","content_type":"nope"}]}}`,
		"twice":           `{"docs_blob":{"contents":[{"content_name":"x","content_type":"module"},{"content_name":"x","content_type":"module"}]}}`,
		"contents object": `{"docs_blob":{"contents":{}}}`,
	} {
		if _, err := galaxydoc.ParseUpstreamDocs([]byte(doc)); !errors.Is(err, galaxydoc.ErrDocsInvalid) {
			t.Errorf("%s: %v", name, err)
		} else if galaxydoc.FetchFailure(err) != rows.DocReasonInvalidDocument {
			t.Errorf("%s: reason %s", name, galaxydoc.FetchFailure(err))
		}
	}
	big := make([]byte, galaxydoc.MaxDocumentBytes+1)
	if _, err := galaxydoc.ParseUpstreamDocs(big); !errors.Is(err, galaxydoc.ErrDocsTooLarge) || galaxydoc.FetchFailure(err) != rows.DocReasonTooLarge {
		t.Errorf("a large document: %v", err)
	}
	if galaxydoc.FetchFailure(errors.New("timeout")) != rows.DocReasonUpstreamError {
		t.Error("another failure is an upstream error")
	}
	var many strings.Builder
	many.WriteString(`{"docs_blob":{"contents":[`)
	for i := range 5001 {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString(`{"content_name":"m` + strings.Repeat("a", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26)) + `","content_type":"module"}`)
	}
	many.WriteString(`]}}`)
	if _, err := galaxydoc.ParseUpstreamDocs([]byte(many.String())); !errors.Is(err, galaxydoc.ErrDocsInvalid) {
		t.Errorf("5,001 items: %v", err)
	}
}

type markup struct{ empty bool }

func (m markup) Markdown(src string) (string, bool) {
	if m.empty {
		return "", true
	}
	return "<p>" + html.EscapeString(src) + "</p>", false
}
func (markup) SanitizeHTML(h string) string { return strings.ReplaceAll(h, "<script>", "") }

// TestR_ANS_035_TheDocumentIsRenderedFromAnyClassWithTheHostsRenderer: Markdown rows go through Markdown, HTML rows through SanitizeHTML, the
// order is by type and name, and a Host that renders nothing for a source fails the document.
func TestR_ANS_035_TheDocumentIsRenderedFromAnyClassWithTheHostsRenderer(t *testing.T) {
	set, err := galaxydoc.ParseUpstreamDocs([]byte(upstream))
	if err != nil {
		t.Fatal(err)
	}
	out, err := galaxydoc.RenderDocsBlob(set, markup{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "<script>") {
		t.Errorf("an unsanitized member was served: %s", out)
	}
	var back struct {
		DocsBlob struct {
			Contents []struct {
				Name string `json:"content_name"`
				Type string `json:"content_type"`
			} `json:"contents"`
			Files []struct{ Name string } `json:"documentation_files"`
		} `json:"docs_blob"`
	}
	if err := json.Unmarshal(out, &back); err != nil || len(back.DocsBlob.Contents) != 3 || back.DocsBlob.Contents[0].Type != "filter" {
		t.Fatalf("document = %s (%v)", out, err)
	}
	again, err := galaxydoc.ParseUpstreamDocs(out)
	if err != nil || len(again.Entries) != 3 {
		t.Errorf("the served document does not parse again: %v", err)
	}

	gen := rows.DocSet{
		Source: rows.DocSourceGenerated, Readme: &rows.DocFile{Name: "README.md", Format: rows.DocFormatMarkdown, Body: "# x"},
		Entries: []rows.DocEntry{{Type: "role", Name: "web", ReadmeMD: "# web"}},
	}
	out, err = galaxydoc.RenderDocsBlob(gen, markup{})
	if err != nil || !strings.Contains(string(out), "<p># x</p>") || !strings.Contains(string(out), "<p># web</p>") {
		t.Errorf("generated: %s (%v)", out, err)
	}
	if _, err := galaxydoc.RenderDocsBlob(gen, markup{empty: true}); !errors.Is(err, galaxydoc.ErrDocsNotRendered) {
		t.Errorf("a Host without a renderer: %v", err)
	}
	if href := galaxydoc.UpstreamHref([]byte(`{"href":"/api/x/versions/1.0.0/"}`)); href != "/api/x/versions/1.0.0/" || galaxydoc.UpstreamHref([]byte(`nope`)) != "" {
		t.Errorf("href = %q", href)
	}
}
