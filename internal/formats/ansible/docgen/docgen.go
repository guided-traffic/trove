// Package docgen derives the documentation of a collection version from the text of its archive (R-ANS-034): the content items
// (modules, plugins of every type, roles), the documentation strings of their sources, the README and the Markdown files of docs/. It is
// the generator of a local publish; a remote's documentation is the upstream's and is parsed by internal/formats/ansible/galaxydoc.
//
// Nothing is executed. The generator reads text: it never starts a process, loads Python or imports a plugin, and `go list -deps` of
// this package names no os/exec (a test). The result is the neutral form [rows.DocSet] every output is rendered from.
//
// The archive reader's tap hands the members over in one pass ([Generator] implements collection.Tap): a Python source is scanned for its
// three literals at once and dropped, so the memory held is the literals and the Markdown, not the sources. The Ansible markup of the prose is
// converted by [RenderMarkup] when an answer is made; nothing stored is HTML.
//
// The package is a leaf: it imports the standard library, the rows leaf and the YAML module.
package docgen

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// Version is the version of the code that derives the rows, stored as ansible_version_docs.generator: the reindex rebuilds a version whose
// stored generator is older (R-ANS-044). It is raised with every change of what the generator makes of a source.
const Version = 1

// Limits are the bounds the generator reads under (R-ANS-031). A bound crossed drops that part, sets Truncated and adds a warning; it
// never fails a publish (R-ANS-034).
type Limits struct {
	// Literal is the longest DOCUMENTATION, EXAMPLES or RETURN literal read, Source the longest Python source read for one, and
	// SourceBudget the sum of all sources of an archive.
	Literal, Source, SourceBudget int64
	// Entry is the largest doc_strings kept for one entry, EntriesPerVersion the most entries of a version and BytesPerVersion the
	// most stored bytes of a version.
	Entry, EntriesPerVersion, BytesPerVersion int64
	// File is the largest Markdown file read and Files the most files of docs/ kept.
	File, Files int64
}

// DefaultLimits are the bounds of R-ANS-031.
func DefaultLimits() Limits {
	return Limits{
		Literal: 512 << 10, Source: 2 << 20, SourceBudget: 256 << 20,
		Entry: 1 << 20, EntriesPerVersion: rows.MaxDocEntriesPerVersion, BytesPerVersion: 16 << 20,
		File: 512 << 10, Files: rows.MaxDocFiles,
	}
}

// Member is the capped bytes of a wanted member of the archive: the archive reader's tap has read them already, while the member went
// through its digest.
type Member struct {
	Path string
	Body []byte
}

// The warning codes of the generator (R-ANS-034).
const (
	CodeLiteralTooLarge    = "docs_literal_too_large"
	CodeLiteralUnterminate = "docs_literal_unterminated"
	CodeYAMLRefused        = "docs_yaml_refused"
	CodeBudgetExhausted    = "docs_budget_exhausted"
	CodeInvalidName        = "docs_invalid_name"
	CodeEntryTooLarge      = "docs_entry_too_large"
	CodeEntriesTruncated   = "docs_entries_truncated"
	CodeVersionTooLarge    = "docs_version_too_large"
	CodeFileTooLarge       = "docs_file_too_large"
	CodeFileNotUTF8        = "docs_file_not_utf8"
	CodeFilesTruncated     = "docs_files_truncated"
	CodeFileNameConflict   = "docs_file_name_conflict"
	CodeReadmeNotMarkdown  = "readme_not_markdown"
)

// pluginType maps a directory below plugins/ to the content type of its items (R-ANS-034); ok is false for a directory that holds none.
func pluginType(dir string) (typ string, ok bool) {
	switch dir {
	case "modules":
		return "module", true
	case "module_utils", "doc_fragments", "action", "become", "cache", "callback", "cliconf", "connection", "filter", "httpapi",
		"inventory", "lookup", "netconf", "shell", "strategy", "terminal", "test", "vars":
		return dir, true
	}
	return "", false
}

// hasNoDocs are the types that are listed and never carry documentation of their own (H-891).
func hasNoDocs(typ string) bool { return typ == "module_utils" || typ == "doc_fragments" }

type item struct {
	typ, name, path string
	lits            map[string]literal
	cut             bool
}

// Generator collects the members of one archive and builds the documentation from them. It is single-use and not safe for concurrent
// use: one archive is read by one goroutine.
type Generator struct {
	lim       Limits
	items     map[string]*item // by type and name
	sidecars  map[string][]byte
	markdown  map[string][]byte
	roles     map[string]bool
	roleDocs  map[string][]byte
	warns     warnings
	spent     int64
	exhausted bool
	cutPaths  map[string]bool
}

// NewGenerator returns a generator that reads under lim.
func NewGenerator(lim Limits) *Generator {
	return &Generator{
		lim: lim, items: map[string]*item{}, sidecars: map[string][]byte{}, markdown: map[string][]byte{},
		roles: map[string]bool{}, roleDocs: map[string][]byte{}, cutPaths: map[string]bool{},
	}
}

// Wants reports whether the member at path is read, and how many bytes of it at most (R-ANS-034); a member it does not want is streamed
// through the digest and kept nowhere. The archive reader's tap asks it for every file member, so it also notes the roles it sees.
func (g *Generator) Wants(p string) (maxBytes int64, ok bool) {
	segs := strings.Split(p, "/")
	switch {
	case len(segs) >= 3 && segs[0] == "plugins":
		typ, known := pluginType(segs[1])
		if !known {
			return 0, false
		}
		file := segs[len(segs)-1]
		switch {
		case strings.HasSuffix(file, ".py"):
			if file == "__init__.py" || strings.HasPrefix(file, "_") {
				return 0, false
			}
			if hasNoDocs(typ) {
				return 0, true
			}
			if g.spent >= g.lim.SourceBudget {
				g.exhausted = true
				g.cutPaths[p] = true
				return 0, true
			}
			return g.lim.Source + 1, true
		case strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml"):
			return g.lim.Source + 1, true
		}
	case len(segs) >= 2 && segs[0] == "roles":
		if len(segs) >= 3 && validName(segs[1]) {
			g.roles[segs[1]] = true
		}
		if len(segs) == 3 && segs[2] == "README.md" {
			return g.lim.File + 1, true
		}
	case len(segs) == 1 && strings.HasSuffix(segs[0], ".md"), len(segs) == 2 && segs[0] == "docs" && strings.HasSuffix(segs[1], ".md"):
		return g.lim.File + 1, true
	}
	return 0, false
}

// Take implements the tap of the archive reader: it hands over a wanted member.
func (g *Generator) Take(p string, body []byte) { g.Add(Member{Path: p, Body: body}) }

// Add hands over a wanted member, in archive order. It never fails: a member that cannot be used becomes a warning of [Generator.Build].
func (g *Generator) Add(m Member) {
	segs := strings.Split(m.Path, "/")
	switch {
	case len(segs) >= 3 && segs[0] == "plugins":
		typ, known := pluginType(segs[1])
		if !known {
			return
		}
		g.addPlugin(m, typ, segs[2:])
	case len(segs) == 3 && segs[0] == "roles" && segs[2] == "README.md":
		g.roles[segs[1]] = true
		g.roleDocs[segs[1]] = m.Body
	case strings.HasSuffix(m.Path, ".md") && (len(segs) == 1 || len(segs) == 2 && segs[0] == "docs"):
		g.markdown[m.Path] = m.Body
	}
}

func (g *Generator) addPlugin(m Member, typ string, rest []string) {
	file := rest[len(rest)-1]
	ext := path.Ext(file)
	stem := strings.TrimSuffix(file, ext)
	switch ext {
	case ".yml", ".yaml":
		g.spent += int64(len(m.Body))
		g.sidecars[m.Path] = m.Body
		return
	case ".py":
	default:
		return
	}
	if file == "__init__.py" || strings.HasPrefix(file, "_") {
		return
	}
	parts := append(append([]string(nil), rest[:len(rest)-1]...), stem)
	for _, part := range parts {
		if !validName(part) {
			g.warns.add(m.Path, CodeInvalidName, "the name "+quote(part)+" is not a valid plugin name, so the file is no item")
			return
		}
	}
	it := &item{typ: typ, name: strings.Join(parts, "."), path: strings.TrimSuffix(m.Path, ".py")}
	g.items[typ+"\x00"+it.name] = it
	if hasNoDocs(typ) {
		return
	}
	if g.cutPaths[m.Path] {
		it.cut = true
		return
	}
	g.spent += int64(len(m.Body))
	src := m.Body
	if int64(len(src)) > g.lim.Source {
		src = src[:g.lim.Source]
	}
	it.lits = scanLiterals(src, int(g.lim.Literal))
}

// validName reports whether s matches ^(?!.*__)[a-z_][0-9a-z_]*$.
func validName(s string) bool {
	if s == "" || strings.Contains(s, "__") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func quote(s string) string {
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return fmt.Sprintf("%q", s)
}

// warnings collects at most rows.MaxDocWarnings warnings and counts the rest.
type warnings struct {
	list []rows.DocWarning
	more int
}

func (w *warnings) add(p, code, msg string) {
	if len(w.list) >= rows.MaxDocWarnings {
		w.more++
		return
	}
	w.list = append(w.list, rows.DocWarning{Path: p, Code: code, Message: msg})
}

// Build derives the documentation. readmeName is the name of the README the manifest declares (collection_info.readme), "" for the default
// "README.md"; the README is read when it is a Markdown file at the root of the archive or directly in docs/. The order of entries, files and
// warnings is deterministic, the bounds are applied and the warnings collected.
func (g *Generator) Build(readmeName string) rows.DocSet {
	set := rows.DocSet{Source: rows.DocSourceGenerated, Generator: Version}
	if g.exhausted {
		g.warns.add("plugins", CodeBudgetExhausted, "the Python sources past the budget of an archive are listed and not read")
	}
	var total int64
	set.Readme, set.Files, total = g.files(readmeName, &set)
	entries := g.entries(&total, &set)
	set.Entries = entries
	set.Warnings, set.MoreWarnings = g.warns.list, g.warns.more
	return set
}

// entries builds the entries in type and name order under the bounds of a version.
func (g *Generator) entries(total *int64, set *rows.DocSet) []rows.DocEntry {
	var out []rows.DocEntry
	keys := make([]string, 0, len(g.items))
	for k := range g.items {
		keys = append(keys, k)
	}
	for r := range g.roles {
		if !validName(r) {
			g.warns.add("roles/"+r, CodeInvalidName, "the name "+quote(r)+" is not a valid role name, so the directory is no item")
			continue
		}
		keys = append(keys, "role\x00"+r)
	}
	sort.Strings(keys)
	versionFull := false
	for _, k := range keys {
		if int64(len(out)) >= g.lim.EntriesPerVersion {
			set.Truncated = true
			g.warns.add("", CodeEntriesTruncated, fmt.Sprintf("only the first %d entries are kept", g.lim.EntriesPerVersion))
			break
		}
		var e rows.DocEntry
		if it, ok := g.items[k]; ok {
			e = g.entry(it)
		} else {
			e = g.role(strings.TrimPrefix(k, "role\x00"))
		}
		size := int64(len(e.Doc) + len(e.ReadmeMD) + len(e.SearchText) + len(e.ShortDescription))
		if *total+size > g.lim.BytesPerVersion {
			if !versionFull {
				versionFull = true
				set.Truncated = true
				g.warns.add("", CodeVersionTooLarge, "the stored documentation of the version reached its bound; later entries are listed without documentation")
			}
			e.HasDocs, e.Doc, e.ReadmeMD = false, nil, ""
			size = int64(len(e.SearchText) + len(e.ShortDescription))
		}
		*total += size
		out = append(out, e)
	}
	return out
}

// entry builds the entry of a plugin.
func (g *Generator) entry(it *item) (e rows.DocEntry) {
	e = rows.DocEntry{Type: it.typ, Name: it.name}
	var w searchWords
	w.add(searchName(it.name))
	defer func() { e.SearchText = w.text() }()
	if hasNoDocs(it.typ) {
		return e
	}
	if it.cut {
		return e
	}
	examples, docNode, retNode := g.sources(it)
	if docNode == nil {
		return e
	}
	doc, short := shapeDoc(docNode, it.typ, it.name, &w)
	if doc == nil {
		return e
	}
	e.ShortDescription = short
	var ret any
	if l := returnList(retNode, &w, 0); l != nil {
		ret = l
	}
	var ex any
	if examples != "" {
		ex = examples
	}
	blob, err := json.Marshal(map[string]any{"doc": doc, "return": ret, "examples": ex, "metadata": nil})
	if err != nil || int64(len(blob)) > g.lim.Entry {
		g.warns.add(it.path+".py", CodeEntryTooLarge, "the documentation of the entry is larger than the bound, so it is listed without it")
		e.ShortDescription = short
		return e
	}
	e.HasDocs, e.Doc = true, blob
	return e
}

// sources resolves the three strings of a plugin: a key of the sidecar wins over the source. The nodes are nil when a string is absent,
// refused or not YAML of the expected shape; a warning says why.
func (g *Generator) sources(it *item) (examples string, docNode, retNode *yamlNode) {
	var sidecar *yamlNode
	for _, ext := range []string{".yml", ".yaml"} {
		if body, ok := g.sidecars[it.path+ext]; ok {
			n, err := parseYAML(string(body))
			if err != nil {
				g.warns.add(it.path+ext, CodeYAMLRefused, err.Error())
			} else {
				sidecar = n
			}
			break
		}
	}
	pick := func(key string) (node *yamlNode, lit literal, fromSidecar, have bool) {
		if v := member(sidecar, key); v != nil {
			return v, literal{}, true, true
		}
		l, ok := it.lits[key]
		return nil, l, false, ok
	}
	resolve := func(key string) (node *yamlNode, txt string) {
		n, lit, side, have := pick(key)
		switch {
		case !have:
			return nil, ""
		case side:
			if n.Kind == scalarKind {
				txt = n.Value
				parsed, err := parseYAML(txt)
				if err != nil {
					g.warns.add(it.path+".yml", CodeYAMLRefused, err.Error())
					return nil, txt
				}
				return parsed, txt
			}
			return n, ""
		}
		switch lit.status {
		case litTooLarge:
			g.warns.add(it.path+".py", CodeLiteralTooLarge, key+" is larger than the bound of a literal")
			return nil, ""
		case litUnterminated:
			g.warns.add(it.path+".py", CodeLiteralUnterminate, key+" does not close")
			return nil, ""
		}
		parsed, err := parseYAML(lit.text)
		if err != nil {
			g.warns.add(it.path+".py", CodeYAMLRefused, key+": "+err.Error())
			return nil, lit.text
		}
		return parsed, lit.text
	}
	docNode, _ = resolve("DOCUMENTATION")
	retNode, _ = resolve("RETURN")
	if n, lit, side, have := pick("EXAMPLES"); have {
		switch {
		case side:
			examples = text(n)
		case lit.status == litOK:
			examples = lit.text
		case lit.status == litTooLarge:
			g.warns.add(it.path+".py", CodeLiteralTooLarge, "EXAMPLES is larger than the bound of a literal")
		default:
			g.warns.add(it.path+".py", CodeLiteralUnterminate, "EXAMPLES does not close")
		}
	}
	return examples, docNode, retNode
}

// role builds the entry of a role.
func (g *Generator) role(name string) rows.DocEntry {
	e := rows.DocEntry{Type: "role", Name: name}
	var w searchWords
	w.add(searchName(name))
	if body, ok := g.roleDocs[name]; ok {
		switch {
		case int64(len(body)) > g.lim.File:
			g.warns.add("roles/"+name+"/README.md", CodeFileTooLarge, "the README of the role is larger than the bound of a file")
		case !utf8.Valid(body):
			g.warns.add("roles/"+name+"/README.md", CodeFileNotUTF8, "the README of the role is not UTF-8")
		default:
			e.ReadmeMD = string(body)
		}
	}
	e.SearchText = w.text()
	return e
}

// files picks the README and the files of docs/.
func (g *Generator) files(readmeName string, set *rows.DocSet) (readme *rows.DocFile, files []rows.DocFile, total int64) {
	if readmeName == "" {
		readmeName = "README.md"
	}
	readOK := func(p string, body []byte) (string, bool) {
		switch {
		case int64(len(body)) > g.lim.File:
			g.warns.add(p, CodeFileTooLarge, "the file is larger than the bound of a file")
		case !utf8.Valid(body):
			g.warns.add(p, CodeFileNotUTF8, "the file is not UTF-8")
		default:
			return string(body), true
		}
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(readmeName), ".md") {
		g.warns.add(readmeName, CodeReadmeNotMarkdown, "the README is not a Markdown file, and reStructuredText is not rendered")
		readmeName = ""
	}
	if body, ok := g.markdown[readmeName]; ok && readmeName != "" {
		if text, ok := readOK(readmeName, body); ok {
			readme = &rows.DocFile{Name: path.Base(readmeName), Format: rows.DocFormatMarkdown, Body: text}
			total += int64(len(text))
		}
	}
	names := make([]string, 0, len(g.markdown))
	for p := range g.markdown {
		if strings.HasPrefix(p, "docs/") && p != readmeName {
			names = append(names, p)
		}
	}
	sort.Strings(names)
	for _, p := range names {
		text, ok := readOK(p, g.markdown[p])
		if !ok {
			continue
		}
		if readme != nil && path.Base(p) == readme.Name {
			g.warns.add(p, CodeFileNameConflict, "the file has the name of the README and is left out")
			continue
		}
		if int64(len(files)) >= g.lim.Files {
			set.Truncated = true
			g.warns.add("docs", CodeFilesTruncated, fmt.Sprintf("only the first %d files of docs/ are kept", g.lim.Files))
			break
		}
		files = append(files, rows.DocFile{Name: path.Base(p), Format: rows.DocFormatMarkdown, Body: text})
		total += int64(len(text))
	}
	return readme, files, total
}

// searchName is a content name as words: the underscores and dots are spaces.
func searchName(name string) string { return strings.NewReplacer("_", " ", ".", " ").Replace(name) }

// text joins the words of the search text, cut at rows.MaxDocSearchText on a character boundary.
func (w *searchWords) text() string {
	s := strings.Join(w.parts, " ")
	if len(s) <= rows.MaxDocSearchText {
		return s
	}
	s = s[:rows.MaxDocSearchText]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
