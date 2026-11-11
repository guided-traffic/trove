package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/packages/readme"
)

func str(s string) *string { return &s }

// TestR_ANS_036_TheSearchTextIsValidatedBeforeItIsUsed: fewer than 2 characters, or more than 512, is 422 invalid_query; control characters are
// dropped, the text is split into at most 8 terms of at most 64 characters.
func TestR_ANS_036_TheSearchTextIsValidatedBeforeItIsUsed(t *testing.T) {
	for _, bad := range []string{"", "a", " a ", "\x00a\x01", strings.Repeat("a b ", 200)} {
		_, err := parseDocQuery(&bad)
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeInvalidQuery || e.Kind != errs.Unprocessable {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if q, err := parseDocQuery(nil); q != nil || err != nil {
		t.Errorf("no query: %v %v", q, err)
	}
	q, err := parseDocQuery(str("  host\tcheck\x07 reach  "))
	if err != nil || len(q.Terms) != 3 || q.Terms[0] != "host" || q.Terms[1] != "check" || q.Terms[2] != "reach" {
		t.Errorf("terms = %+v, %v", q, err)
	}
	q, err = parseDocQuery(str("a1 b2 c3 d4 e5 f6 g7 h8 i9 j0 " + strings.Repeat("x", 100)))
	if err != nil || len(q.Terms) != 8 {
		t.Fatalf("terms = %+v, %v", q, err)
	}
	long := strings.Repeat("y", 100) + " z1"
	q, _ = parseDocQuery(&long)
	if len(q.Terms[0]) != 64 {
		t.Errorf("a term of %d characters", len(q.Terms[0]))
	}
}

// TestR_ANS_036_TheTreeKeepsTheCountsOfTheGroupsWhileASearchFilters: the groups are in the order modules, plugins, roles, the counts are those
// before the search, matchCount is the number left, and the warnings are shown only to a caller who may.
func TestR_ANS_036_TheTreeKeepsTheCountsOfTheGroupsWhileASearchFilters(t *testing.T) {
	tree := rows.DocTree{
		State:    rows.DocState{State: rows.DocAvailable, Source: rows.DocSourceGenerated, BuiltAt: time.Unix(1, 0)},
		Files:    []rows.DocFileHead{{Name: "README.md", Title: "t"}},
		Entries:  []rows.DocEntryHead{{Type: "filter", Name: "is_cidr"}},
		Counts:   map[string]int{"module": 8, "filter": 8, "module_utils": 16},
		Warnings: []rows.DocWarning{{Path: "p", Code: "c", Message: "m"}},
	}
	base := gen.PackageDocTree{State: gen.PackageDocTreeStateAvailable, Repository: "p/r"}
	got := treeOf(base, tree, true, false)
	if got.Warnings != nil || got.MatchCount == nil || *got.MatchCount != 1 {
		t.Errorf("tree = %+v", got)
	}
	g := *got.Groups
	if len(g) != 3 || g[0].Category != "module" || g[0].Count != 8 || g[1].Category != "plugin" || g[1].Count != 24 || g[2].Category != "role" || g[2].Count != 0 {
		t.Fatalf("groups = %+v", g)
	}
	if len(g[0].Types) != 1 || len(g[0].Types[0].Entries) != 0 || g[0].Types[0].Count != 8 {
		t.Errorf("module types = %+v", g[0].Types)
	}
	if len(g[1].Types) != 2 || g[1].Types[0].Type != "filter" || len(g[1].Types[0].Entries) != 1 || g[1].Types[1].Type != "module_utils" || g[1].Types[1].Entries == nil {
		t.Errorf("plugin types = %+v", g[1].Types)
	}
	if g[2].Types == nil {
		t.Error("an empty group has no list of types")
	}
	if withWarnings := treeOf(base, tree, false, true); withWarnings.Warnings == nil || withWarnings.MatchCount != nil {
		t.Errorf("tree = %+v", withWarnings)
	}
}

func testReadme(t *testing.T) *readme.Renderer {
	t.Helper()
	rd, err := readme.New(readme.Options{MaxSourceBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return rd
}

// TestR_ANS_039_AnEntryHoldsSanitizedHTMLOnlyForItsProse: the markup of the prose is converted and sanitized, a hostile code macro is escaped
// text, a javascript link is its text, a default keeps its type and a non-http link of seealso is dropped.
func TestR_ANS_039_AnEntryHoldsSanitizedHTMLOnlyForItsProse(t *testing.T) {
	doc := `{"doc":{"module":"m","short_description":"S","description":["See C(<img src=x onerror=1>) and L(x,javascript:alert(1)) and I(em).","<script>alert(1)</script>"],
	  "version_added":"1.2.0","author":["A"],"options":[{"name":"n","type":"int","default":1000,"description":["B(bold)"],"suboptions":[{"name":"s","description":["x"]}]}],
	  "seealso":[{"module":"other","description":"D","link":"javascript:alert(1)"},{"module":"o2","link":"https://example.com"}],
	  "attributes":{"check_mode":{"support":"full","description":["C(x)"]}},"deprecated":{"why":"gone","removed_in":"3.0.0"}},
	  "return":[{"name":"r","type":"str","sample":"x","contains":[{"name":"c"}]}],"examples":"- a: </script>"}`
	d := &docsRead{ps: PackageServices{Readme: testReadme(t)}, name: "acme.tools", state: rows.DocState{Source: rows.DocSourceGenerated}}
	e := entryOf(d, rows.DocEntry{Type: "module", Name: "m", ShortDescription: "S", HasDocs: true, Doc: json.RawMessage(doc)})
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, bad := range []string{"<script", "<img", "javascript:", "<a "} {
		if strings.Contains(out, bad) {
			t.Errorf("the entry holds %q: %s", bad, out)
		}
	}
	for _, want := range []string{"<code>&lt;img src=x onerror=1&gt;</code>", "<em>em</em>", "<strong>bold</strong>", `"fullName":"acme.tools.acme.tools.m"`[:0], `"default":1000`, `"version_added`[:0], `"module":"o2"`, `"removedIn":"3.0.0"`} {
		if want != "" && !strings.Contains(out, want) {
			t.Errorf("the entry lacks %s: %s", want, out)
		}
	}
	if e.FullName != "acme.tools.m" || e.Examples == nil || !strings.Contains(*e.Examples, "</script>") || e.Options == nil || (*e.Options)[0].Suboptions == nil {
		t.Errorf("entry = %+v", e)
	}
	if e.Seealso == nil || len(*e.Seealso) != 2 || (*e.Seealso)[0].Link != nil || (*e.Seealso)[1].Link == nil {
		t.Errorf("seealso = %+v", e.Seealso)
	}
	header := entryOf(d, rows.DocEntry{Type: "filter", Name: "f"})
	if header.HasDocs || header.Options != nil || header.Description != nil {
		t.Errorf("an entry without documentation = %+v", header)
	}
	role := entryOf(d, rows.DocEntry{Type: "role", Name: "web", ReadmeMD: "# web\n\n<script>x</script>"})
	if role.ReadmeHtml == nil || strings.Contains(*role.ReadmeHtml, "<script") {
		t.Errorf("role = %+v", role)
	}
	// The nesting of the options is bounded (R-ANS-036).
	deep := `{"name":"x"}`
	for range 20 {
		deep = `{"name":"x","suboptions":[` + deep + `]}`
	}
	nested := entryOf(d, rows.DocEntry{Type: "module", Name: "m", HasDocs: true, Doc: json.RawMessage(`{"doc":{"options":[` + deep + `]}}`)})
	depth := 0
	for o := &(*nested.Options)[0]; o.Suboptions != nil; o = &(*o.Suboptions)[0] {
		depth++
	}
	if depth != maxDocDepth-1 {
		t.Errorf("options nest %d levels, want the bound %d", depth+1, maxDocDepth)
	}
}

func TestR_ANS_036_ThePathParametersOfAnEntryHaveAGrammar(t *testing.T) {
	for in, ok := range map[string]bool{"module": true, "module_utils": true, "Module": false, "a/b": false, "": false, "a b": false} {
		if typeOK(in) != ok {
			t.Errorf("typeOK(%q) = %v", in, !ok)
		}
	}
	for in, ok := range map[string]bool{"check_host": true, "net.helpers": true, "A": false, "../x": false, "": false} {
		if nameOK(in) != ok {
			t.Errorf("nameOK(%q) = %v", in, !ok)
		}
	}
}
