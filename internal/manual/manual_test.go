package manual

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	docs "github.com/guided-traffic/trove/docs/manual"
)

// TestR_API_054_EmbeddedManualLoads: the manual the binary embeds loads without a
// problem: every page of the contents exists, starts with a title and a summary, and
// every link between pages reaches a page and a heading that exist.
func TestR_API_054_EmbeddedManualLoads(t *testing.T) {
	m, err := Load(docs.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Sections()) != len(contents()) {
		t.Fatalf("got %d sections", len(m.Sections()))
	}
	for _, s := range m.Sections() {
		for _, p := range s.Pages {
			if p.Title == "" || p.Summary == "" || p.HTML == "" {
				t.Errorf("%s/%s: title %q, summary %q, %d bytes of HTML", p.Section, p.ID, p.Title, p.Summary, len(p.HTML))
			}
			if len(p.Headings) == 0 {
				t.Errorf("%s/%s has no section headings", p.Section, p.ID)
			}
		}
	}
	if m.ETag() == "" || !strings.HasPrefix(m.ETag(), `"`) {
		t.Errorf("ETag %q", m.ETag())
	}
}

// TestR_API_054_ReadmeListsTheContents: docs/manual/README.md, the contents for
// readers on GitHub, lists the sections and pages in the order of the help area and
// with the titles of the pages.
func TestR_API_054_ReadmeListsTheContents(t *testing.T) {
	raw, err := os.ReadFile("../../docs/manual/README.md")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(docs.Files)
	if err != nil {
		t.Fatal(err)
	}
	item := regexp.MustCompile(`^\d+\. \[([^\]]+)\]\(([a-z]+)/([a-z0-9-]+)\.md\)$`)
	var got, want []string
	section := ""
	for line := range strings.SplitSeq(string(raw), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			section = h
			continue
		}
		if g := item.FindStringSubmatch(line); g != nil {
			got = append(got, section+" > "+g[2]+"/"+g[3]+" > "+g[1])
		}
	}
	for _, s := range m.Sections() {
		for _, p := range s.Pages {
			want = append(want, s.Title+" > "+s.ID+"/"+p.ID+" > "+p.Title)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("README.md lists\n%s\n\nthe manual is\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func page(title, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("# " + title + "\n\nWhat the page is for.\n\n" + body)}
}

var small = toc{
	{"user", "User guide", []string{"start", "tokens"}},
	{"admin", "Administrator guide", []string{"setup"}},
}

// TestR_API_054_LinksBecomeHelpRoutes: a link to another page becomes a route of the
// help area, a heading fragment the heading's anchor, and the reading order links
// the neighbours across the sections.
func TestR_API_054_LinksBecomeHelpRoutes(t *testing.T) {
	fsys := fstest.MapFS{
		"user/start.md":  page("Start", "## Next steps\n\nSee [tokens](tokens.md#create-a-token), [setup](../admin/setup.md) and [below](#next-steps).\n"),
		"user/tokens.md": page("Tokens", "## Create a token\n\nText.\n"),
		"admin/setup.md": page("Setup", "## Install\n\nText.\n\n### Verify\n\nText.\n"),
	}
	m, err := load(fsys, small)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := m.Page("user", "start")
	for _, want := range []string{
		`href="help/user/tokens#user-content-create-a-token"`,
		`href="help/admin/setup"`,
		`href="#user-content-next-steps"`,
		`<p>What the page is for.</p>`,
	} {
		if !strings.Contains(start.HTML, want) {
			t.Errorf("missing %s in %s", want, start.HTML)
		}
	}
	if start.Summary != "What the page is for." || start.Title != "Start" {
		t.Errorf("title %q, summary %q", start.Title, start.Summary)
	}
	setup, _ := m.Page("admin", "setup")
	if len(setup.Headings) != 2 || setup.Headings[1] != (Heading{Level: 3, ID: "user-content-verify", Text: "Verify"}) {
		t.Errorf("headings %+v", setup.Headings)
	}
	tokens, _ := m.Page("user", "tokens")
	if tokens.Previous != start || tokens.Next != setup || start.Previous != nil || setup.Next != nil {
		t.Error("the reading order does not link the neighbours")
	}
}

// TestR_API_054_DefectsAreRefused: a broken link, a page outside the contents, a
// missing page and a page without a title or summary each stop Load.
func TestR_API_054_DefectsAreRefused(t *testing.T) {
	good := func() fstest.MapFS {
		return fstest.MapFS{
			"user/start.md":  page("Start", "## A\n"),
			"user/tokens.md": page("Tokens", "## B\n"),
			"admin/setup.md": page("Setup", "## C\n"),
		}
	}
	cases := map[string]struct {
		change func(fstest.MapFS)
		want   string
	}{
		"missing page":       {func(f fstest.MapFS) { delete(f, "admin/setup.md") }, "admin/setup"},
		"unlisted page":      {func(f fstest.MapFS) { f["user/extra.md"] = page("Extra", "") }, "not in the contents"},
		"no title":           {func(f fstest.MapFS) { f["user/start.md"] = &fstest.MapFile{Data: []byte("Text\n")} }, "Title"},
		"no summary":         {func(f fstest.MapFS) { f["user/start.md"] = &fstest.MapFile{Data: []byte("# Start\n\n## A\n")} }, "summary"},
		"link to no page":    {func(f fstest.MapFS) { f["user/start.md"] = page("Start", "[x](nothing.md)\n") }, "points at no page"},
		"link to no heading": {func(f fstest.MapFS) { f["user/start.md"] = page("Start", "[x](tokens.md#nothing)\n") }, "no heading"},
		"link to the repo":   {func(f fstest.MapFS) { f["user/start.md"] = page("Start", "[x](../../spec/README.md)\n") }, "points at no page"},
		"link to a file":     {func(f fstest.MapFS) { f["user/start.md"] = page("Start", "[x](values.yaml)\n") }, "does not point at a page"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := good()
			c.change(f)
			_, err := load(f, small)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error with %q", err, c.want)
			}
		})
	}
	if _, err := load(good(), small); err != nil {
		t.Fatal(err)
	}
}
