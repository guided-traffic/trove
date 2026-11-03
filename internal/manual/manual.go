// Package manual loads the trove manual (docs/manual) for the help area of the UI
// (R-API-054, R-UI-050): the pages of the user and the administrator guide, in the
// order of the contents below, each rendered once at start-up by the README renderer
// of R-UI-008, with the links between pages turned into links of the help area.
//
// A page is a Markdown file that starts with a `# Title` line and one paragraph of
// plain text, the summary. Links to another page are relative links to its file,
// optionally with the slug of a heading (`access-tokens.md#create-a-token`); Load
// refuses a manual with a link to a page or heading that does not exist, so a broken
// link fails the test of this package and never reaches a build.
package manual

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/guided-traffic/trove/internal/packages/readme"
)

// LinkPrefix starts the target of a link to another page: the route of the help
// area, relative to the base href of the UI (`/ui/<locale>/`), so that the link also
// works when it is opened in a new tab.
const LinkPrefix = "help/"

// maxSourceBytes bounds a page; the manual is trusted text and far below it.
const maxSourceBytes = 1 << 20

// contents is the order of the manual. docs/manual/README.md lists the same pages
// in the same order for readers on GitHub; the test of this package compares both.
// A function, not a variable: no package holds mutable state (R-ARCH-032).
func contents() toc {
	return toc{
		{"user", "User guide", []string{
			"getting-started", "repositories", "packages", "vulnerabilities", "files", "client-setup", "access-tokens", "gitlab-ci-pipelines",
			"troubleshooting",
		}},
		{"admin", "Administrator guide", []string{
			"overview", "installation", "configuration", "tls-and-certificates", "endpoints", "bastion", "sign-in-and-identity",
			"permissions", "projects", "tokens", "gitlab-ci", "repositories", "operations", "upgrades", "limitations",
		}},
	}
}

// toc is an ordered list of sections, each with its page ids.
type toc []struct {
	id, title string
	pages     []string
}

// Section is one guide of the manual.
type Section struct {
	ID    string
	Title string
	Pages []*Page
}

// Page is a rendered page.
type Page struct {
	Section string
	ID      string
	Title   string
	// Summary is the first paragraph, plain text.
	Summary string
	// HTML is the page below its title, sanitized.
	HTML string
	// Headings are the second- and third-level headings, for a table of contents.
	Headings []Heading
	// Previous and Next are the neighbours in reading order, nil at either end.
	Previous, Next *Page
}

// Heading is a heading of a page.
type Heading struct {
	Level int
	// ID is the anchor of the heading in HTML (user-content-<slug>).
	ID   string
	Text string
}

// Manual is the loaded manual. It does not change after Load and is safe for
// concurrent use.
type Manual struct {
	sections []Section
	pages    map[string]*Page
	etag     string
}

// Sections returns the guides in order.
func (m *Manual) Sections() []Section { return m.sections }

// Page returns a page by section and id.
func (m *Manual) Page(section, id string) (*Page, bool) {
	p, ok := m.pages[section+"/"+id]
	return p, ok
}

// ETag is the strong entity tag of every answer about the manual: it changes with
// any page, so the contents and a page share it.
func (m *Manual) ETag() string { return m.etag }

type source struct {
	key, title, summary, body string
}

// Load reads and renders the manual from fsys, which holds `<section>/<page>.md`.
func Load(fsys fs.FS) (*Manual, error) {
	return load(fsys, contents())
}

func load(fsys fs.FS, contents toc) (*Manual, error) {
	rd, err := readme.New(readme.Options{MaxSourceBytes: maxSourceBytes})
	if err != nil {
		return nil, err
	}
	var problems []error
	var sources []source
	listed := map[string]bool{}
	for _, s := range contents {
		for _, id := range s.pages {
			key := s.id + "/" + id
			listed[key] = true
			raw, err := fs.ReadFile(fsys, key+".md")
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", key, err))
				continue
			}
			src, err := parse(key, string(raw))
			if err != nil {
				problems = append(problems, err)
				continue
			}
			sources = append(sources, src)
		}
	}
	for _, s := range contents {
		files, _ := fs.Glob(fsys, s.id+"/*.md")
		for _, f := range files {
			if key := strings.TrimSuffix(f, ".md"); !listed[key] {
				problems = append(problems, fmt.Errorf("%s: the page is not in the contents of internal/manual", key))
			}
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	// The first pass collects the anchors of every page, the second resolves the links
	// against them.
	anchors := map[string]map[string]bool{}
	for _, src := range sources {
		res := rd.RenderLinked(src.body, func(string) (string, bool) { return "", false })
		set := map[string]bool{}
		for _, h := range headings(res.HTML, 1, 6) {
			set[strings.TrimPrefix(h.ID, readme.UserContent)] = true
		}
		anchors[src.key] = set
	}
	m := &Manual{pages: map[string]*Page{}}
	sum := sha256.New()
	var order []*Page
	for _, src := range sources {
		res := rd.RenderLinked(src.body, resolver(src.key, anchors, &problems))
		if res.Truncated {
			problems = append(problems, fmt.Errorf("%s: the page exceeds the renderer's limits", src.key))
		}
		section, id, _ := strings.Cut(src.key, "/")
		p := &Page{Section: section, ID: id, Title: src.title, Summary: src.summary, HTML: res.HTML, Headings: headings(res.HTML, 2, 3)}
		m.pages[src.key] = p
		order = append(order, p)
		fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%s\x00", src.key, p.Title, p.Summary, p.HTML)
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	for i, p := range order {
		if i > 0 {
			p.Previous = order[i-1]
		}
		if i < len(order)-1 {
			p.Next = order[i+1]
		}
	}
	for _, s := range contents {
		sec := Section{ID: s.id, Title: s.title}
		for _, id := range s.pages {
			sec.Pages = append(sec.Pages, m.pages[s.id+"/"+id])
		}
		m.sections = append(m.sections, sec)
	}
	fmt.Fprintf(sum, "renderer %d", readme.Version)
	m.etag = `"` + hex.EncodeToString(sum.Sum(nil)[:12]) + `"`
	return m, nil
}

// parse splits a page into its title, its summary and the body the renderer gets:
// everything below the title line, the summary included.
func parse(key, raw string) (source, error) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	first, body, _ := strings.Cut(strings.TrimLeft(text, "\n"), "\n")
	title, ok := strings.CutPrefix(first, "# ")
	if !ok || strings.TrimSpace(title) == "" {
		return source{}, fmt.Errorf("%s: the page does not start with a `# Title` line", key)
	}
	var summary []string
	for line := range strings.SplitSeq(strings.TrimLeft(body, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		summary = append(summary, strings.TrimSpace(line))
	}
	s := strings.Join(summary, " ")
	if s == "" || strings.HasPrefix(s, "#") || strings.ContainsAny(s, "`[]<>*") {
		return source{}, fmt.Errorf("%s: the title is not followed by a summary paragraph of plain text", key)
	}
	return source{key: key, title: strings.TrimSpace(title), summary: s, body: body}, nil
}

// resolver turns the links of the page from into links of the help area. A link to
// another page or to a heading that does not exist is recorded as a problem.
func resolver(from string, anchors map[string]map[string]bool, problems *[]error) readme.Links {
	section, _, _ := strings.Cut(from, "/")
	return func(href string) (string, bool) {
		target, fragment, _ := strings.Cut(href, "#")
		if f, err := url.PathUnescape(fragment); err == nil {
			fragment = f
		}
		key := from
		if target != "" {
			p, err := url.PathUnescape(target)
			if err != nil || !strings.HasSuffix(p, ".md") || strings.HasPrefix(p, "/") {
				*problems = append(*problems, fmt.Errorf("%s: the link %q does not point at a page of the manual", from, href))
				return "", false
			}
			key = strings.TrimSuffix(path.Clean(path.Join(section, p)), ".md")
		}
		set, ok := anchors[key]
		if !ok {
			*problems = append(*problems, fmt.Errorf("%s: the link %q points at no page of the manual", from, href))
			return "", false
		}
		if fragment != "" && !set[fragment] {
			*problems = append(*problems, fmt.Errorf("%s: the link %q points at no heading of %s", from, href, key))
			return "", false
		}
		out := ""
		if target != "" {
			out = LinkPrefix + key
		}
		if fragment != "" {
			out += "#" + readme.UserContent + fragment
		}
		return out, true
	}
}

// headings lists the headings of a rendered page between two levels.
func headings(page string, lowest, highest int) []Heading {
	root := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(bytes.NewReader([]byte(page)), root)
	if err != nil {
		return nil
	}
	levels := []atom.Atom{atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6}
	var out []Heading
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			if l := slices.Index(levels, n.DataAtom) + 1; l >= lowest && l <= highest {
				for _, a := range n.Attr {
					if a.Key == "id" {
						out = append(out, Heading{Level: l, ID: a.Val, Text: strings.TrimSpace(text(n))})
					}
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
}

func text(n *xhtml.Node) string {
	if n.Type == xhtml.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(text(c))
	}
	return b.String()
}
