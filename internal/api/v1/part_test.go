package v1

import (
	"sort"
	"testing"
)

// TestR_ANS_037_ThePartMarkerIsValidated: x-trove-part accepts the one value wiki and refuses every other.
func TestR_ANS_037_ThePartMarkerIsValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		op   map[string]any
		want string
		ok   bool
	}{
		"absent":     {map[string]any{}, "", true},
		"wiki":       {map[string]any{"x-trove-part": "wiki"}, PartWiki, true},
		"other word": {map[string]any{"x-trove-part": "artifacts"}, "", false},
		"boolean":    {map[string]any{"x-trove-part": true}, "", false},
	} {
		got, err := parsePart(tc.op, "get", Requirement{Public: true})
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

// TestR_ANS_037_TheDocumentListsTheWikiOperations: the three documentation operations and the README operation are declared wiki content, and no other operation is,
// so a test and the check that runs before a handler can list the wiki surface from the document (Q-API-260).
func TestR_ANS_037_TheDocumentListsTheWikiOperations(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range doc.Operations() {
		if op.Part == PartWiki {
			got = append(got, op.ID)
		}
	}
	sort.Strings(got)
	want := []string{"getPackageVersionDocEntry", "getPackageVersionDocFile", "getPackageVersionDocs", "getPackageVersionReadme"}
	if len(got) != len(want) {
		t.Fatalf("wiki operations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("wiki operations = %v, want %v", got, want)
		}
	}
}
