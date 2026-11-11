package rows_test

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// TestR_ANS_036_AFileTitleIsItsFirstHeadingOrItsName: the title of a file of the tree is the text of the first heading of a Markdown
// source outside a code fence, at most 120 characters, and the file name without its extension otherwise.
func TestR_ANS_036_AFileTitleIsItsFirstHeadingOrItsName(t *testing.T) {
	for name, tc := range map[string]struct {
		file rows.DocFile
		want string
	}{
		"a heading":            {rows.DocFile{Name: "README.md", Format: rows.DocFormatMarkdown, Body: "intro\n\n# hans_fischer.common\n\ntext"}, "hans_fischer.common"},
		"emphasis and code":    {rows.DocFile{Name: "a.md", Format: rows.DocFormatMarkdown, Body: "## The `x` **role** ##\n"}, "The x role"},
		"a heading in a fence": {rows.DocFile{Name: "install.md", Format: rows.DocFormatMarkdown, Body: "```\n# not a heading\n```\n"}, "install"},
		"no space after marks": {rows.DocFile{Name: "tags.md", Format: rows.DocFormatMarkdown, Body: "#hashtag\n"}, "tags"},
		"seven marks":          {rows.DocFile{Name: "seven.md", Format: rows.DocFormatMarkdown, Body: "####### x\n"}, "seven"},
		"no extension":         {rows.DocFile{Name: "NOTES", Format: rows.DocFormatMarkdown, Body: "plain"}, "NOTES"},
		"html is not read":     {rows.DocFile{Name: "README.md", Format: rows.DocFormatHTML, Body: "<h1>x</h1>"}, "README"},
		"a long heading":       {rows.DocFile{Name: "l.md", Format: rows.DocFormatMarkdown, Body: "# " + strings.Repeat("é", 300)}, strings.Repeat("é", rows.MaxDocTitleRunes)},
	} {
		if got := rows.DocFileTitle(tc.file); got != tc.want {
			t.Errorf("%s: title = %q, want %q", name, got, tc.want)
		}
	}
}
