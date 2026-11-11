package galaxydoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/docgen"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// The docs-blob document of a collection version (R-ANS-035): {"docs_blob": {"collection_readme", "documentation_files", "contents"}},
// parsed into the neutral form [rows.DocSet] and rendered from it for every class.

// The errors of the parser. A fill maps [ErrDocsTooLarge] to the reason too_large and [ErrDocsInvalid] to invalid_document (R-ANS-035).
var (
	ErrDocsTooLarge = errors.New("galaxydoc: the docs-blob document is larger than the bound")
	ErrDocsInvalid  = errors.New("galaxydoc: the docs-blob document is not the shape of R-ANS-035")
	// ErrDocsNotRendered is the error of a renderer that rendered nothing for a source that is not empty: the Host has no renderer.
	ErrDocsNotRendered = errors.New("galaxydoc: the Host's renderer returned nothing for a Markdown source")
)

// Markup is what the renderer needs of the Host's text capability (formats.Markup, ADR-196): the package is a leaf and cannot import the
// format contract, so it declares the two methods it calls and the Host's value satisfies them structurally.
type Markup interface {
	// Markdown renders a Markdown source to sanitized HTML.
	Markdown(src string) (html string, truncated bool)
	// SanitizeHTML applies the allowlist of the policy of R-UI-008 to HTML somebody else wrote.
	SanitizeHTML(html string) string
}

// The bounds of an upstream's document (R-ANS-031, R-ANS-035).
const (
	maxContents      = rows.MaxDocEntriesPerVersion
	maxEntryDocBytes = rows.MaxDocEntryBytes
	maxFileBytes     = 512 << 10
	maxVersionBytes  = 16 << 20
	maxShortRunes    = 300
)

// contentTypes are the types of a content item (R-ANS-034) and the role.
func contentType(t string) bool {
	switch t {
	case "module", "module_utils", "doc_fragments", "action", "become", "cache", "callback", "cliconf", "connection", "filter", "httpapi",
		"inventory", "lookup", "netconf", "shell", "strategy", "terminal", "test", "vars", "role":
		return true
	}
	return false
}

// contentName reports whether s matches ^(?!.*__)[a-z_][0-9a-z_.]*$.
func contentName(s string) bool {
	if s == "" || strings.Contains(s, "__") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (i == 0 || c != '.' && (c < '0' || c > '9')) {
			return false
		}
	}
	return true
}

type blobFile struct {
	Name string `json:"name"`
	HTML string `json:"html"`
}

type blobItem struct {
	ContentName string          `json:"content_name"`
	ContentType string          `json:"content_type"`
	DocStrings  json.RawMessage `json:"doc_strings"`
	ReadmeFile  *string         `json:"readme_file"`
	ReadmeHTML  *string         `json:"readme_html"`
}

type blobDoc struct {
	DocsBlob *struct {
		CollectionReadme   *blobFile       `json:"collection_readme"`
		DocumentationFiles []blobFile      `json:"documentation_files"`
		Contents           json.RawMessage `json:"contents"`
	} `json:"docs_blob"`
}

// docKeys are the keys of doc that are kept (R-ANS-035); another key is dropped when the rows are written.
var docKeys = [...]string{
	"module", "plugin_type", "short_description", "description", "version_added", "author", "notes", "requirements", "options", "seealso",
	"attributes", "deprecated", "extends_documentation_fragment",
}

// ParseUpstreamDocs parses the docs-blob document of an upstream (R-ANS-035) into a [rows.DocSet] with the source upstream and the
// generator 0. The document is bounded by [MaxDocumentBytes] ([ErrDocsTooLarge]), its shape is checked before anything is built, and an item
// without a content_name or a content_type, with a name or a type outside their grammar, or a document that is not the shape, is
// [ErrDocsInvalid]. An item above 1 MiB of doc_strings is kept as a name and short description with no documentation, and the set is marked
// truncated; the contents beyond 5,000 items are an error. The HTML members stay as received: they are sanitized on every serving.
func ParseUpstreamDocs(doc []byte) (rows.DocSet, error) {
	if len(doc) > MaxDocumentBytes {
		return rows.DocSet{}, ErrDocsTooLarge
	}
	var b blobDoc
	if err := json.Unmarshal(doc, &b); err != nil || b.DocsBlob == nil {
		return rows.DocSet{}, fmt.Errorf("%w: not a docs_blob object", ErrDocsInvalid)
	}
	set := rows.DocSet{Source: rows.DocSourceUpstream}
	var total int64
	if r := b.DocsBlob.CollectionReadme; r != nil && r.HTML != "" {
		set.Readme = &rows.DocFile{Name: "README.md", Format: rows.DocFormatHTML, Body: r.HTML}
		total += int64(len(r.HTML))
	}
	seen := map[string]bool{"README.md": set.Readme != nil}
	for _, f := range b.DocsBlob.DocumentationFiles {
		switch {
		case f.Name == "" || strings.ContainsAny(f.Name, "/\\\x00") || len(f.Name) > 256 || seen[f.Name]:
			warnSet(&set, "", "docs_file_skipped", "a documentation file has no usable name")
		case len(f.HTML) > maxFileBytes:
			warnSet(&set, f.Name, "docs_file_too_large", "the file is larger than the bound of a file")
		case len(set.Files) >= rows.MaxDocFiles:
			set.Truncated = true
		case total+int64(len(f.HTML)) > maxVersionBytes:
			set.Truncated = true
		default:
			seen[f.Name] = true
			set.Files = append(set.Files, rows.DocFile{Name: f.Name, Format: rows.DocFormatHTML, Body: f.HTML})
			total += int64(len(f.HTML))
		}
	}
	sort.Slice(set.Files, func(i, j int) bool { return set.Files[i].Name < set.Files[j].Name })
	var items []json.RawMessage
	if len(b.DocsBlob.Contents) > 0 && string(b.DocsBlob.Contents) != "null" {
		if err := json.Unmarshal(b.DocsBlob.Contents, &items); err != nil {
			return rows.DocSet{}, fmt.Errorf("%w: contents is not an array", ErrDocsInvalid)
		}
	}
	if len(items) > maxContents {
		return rows.DocSet{}, fmt.Errorf("%w: more than %d contents", ErrDocsInvalid, maxContents)
	}
	keys := map[string]bool{}
	for i, raw := range items {
		var it blobItem
		if err := json.Unmarshal(raw, &it); err != nil {
			return rows.DocSet{}, fmt.Errorf("%w: content %d is not an object", ErrDocsInvalid, i)
		}
		if it.ContentName == "" || it.ContentType == "" || !contentName(it.ContentName) || !contentType(it.ContentType) {
			return rows.DocSet{}, fmt.Errorf("%w: content %d has no usable content_name and content_type", ErrDocsInvalid, i)
		}
		k := it.ContentType + "\x00" + it.ContentName
		if keys[k] {
			return rows.DocSet{}, fmt.Errorf("%w: content %s %s is listed twice", ErrDocsInvalid, it.ContentType, it.ContentName)
		}
		keys[k] = true
		e, err := upstreamEntry(&set, it)
		if err != nil {
			return rows.DocSet{}, err
		}
		size := int64(len(e.Doc) + len(e.ReadmeHTML) + len(e.SearchText))
		if total+size > maxVersionBytes {
			e.HasDocs, e.Doc, e.ReadmeHTML = false, nil, ""
			set.Truncated = true
			size = int64(len(e.SearchText))
		}
		total += size
		set.Entries = append(set.Entries, e)
	}
	sort.Slice(set.Entries, func(i, j int) bool {
		if set.Entries[i].Type != set.Entries[j].Type {
			return set.Entries[i].Type < set.Entries[j].Type
		}
		return set.Entries[i].Name < set.Entries[j].Name
	})
	return set, nil
}

// warnSet adds a warning to a set, at most rows.MaxDocWarnings.
func warnSet(set *rows.DocSet, path, code, msg string) {
	if len(set.Warnings) >= rows.MaxDocWarnings {
		set.MoreWarnings++
		return
	}
	set.Warnings = append(set.Warnings, rows.DocWarning{Path: path, Code: code, Message: msg})
}

func upstreamEntry(set *rows.DocSet, it blobItem) (e rows.DocEntry, err error) {
	e = rows.DocEntry{Type: it.ContentType, Name: it.ContentName}
	words := []string{strings.NewReplacer("_", " ", ".", " ").Replace(it.ContentName)}
	defer func() { e.SearchText = cutSearch(strings.Join(words, " ")) }()
	if it.ReadmeHTML != nil && len(*it.ReadmeHTML) <= maxFileBytes {
		e.ReadmeHTML = *it.ReadmeHTML
	}
	ds := bytes.TrimSpace(it.DocStrings)
	if len(ds) == 0 || string(ds) == "null" {
		return e, nil
	}
	if len(ds) > maxEntryDocBytes {
		set.Truncated = true
		warnSet(set, it.ContentType+"/"+it.ContentName, "docs_entry_too_large", "the documentation of the entry is larger than the bound, so it is kept without it")
		return e, nil
	}
	var parts struct {
		Doc      map[string]json.RawMessage `json:"doc"`
		Return   json.RawMessage            `json:"return"`
		Examples json.RawMessage            `json:"examples"`
	}
	if err = json.Unmarshal(ds, &parts); err != nil {
		return e, fmt.Errorf("%w: doc_strings of %s is not an object", ErrDocsInvalid, it.ContentName)
	}
	if parts.Doc == nil {
		return e, nil
	}
	doc := map[string]json.RawMessage{}
	for _, k := range docKeys {
		if v, ok := parts.Doc[k]; ok && string(v) != "null" {
			doc[k] = v
		}
	}
	var short string
	if raw, ok := doc["short_description"]; ok {
		_ = json.Unmarshal(raw, &short) //nolint:errcheck // a short description that is no string is none
	}
	short = shortText(short)
	e.ShortDescription = short
	words = append(words, docgen.PlainText(short))
	words = append(words, namesOf(doc["options"])...)
	if r := bytes.TrimSpace(parts.Return); len(r) > 0 && r[0] == '[' {
		words = append(words, namesOf(r)...)
	} else {
		parts.Return = nil
	}
	out := map[string]any{"doc": doc, "return": rawOrNil(parts.Return), "examples": examples(parts.Examples), "metadata": nil}
	blob, err := json.Marshal(out)
	if err != nil || len(blob) > maxEntryDocBytes {
		set.Truncated = true
		return e, nil
	}
	e.HasDocs, e.Doc = true, blob
	return e, nil
}

func rawOrNil(r json.RawMessage) any {
	if len(bytes.TrimSpace(r)) == 0 || string(bytes.TrimSpace(r)) == "null" {
		return nil
	}
	return r
}

func examples(r json.RawMessage) any {
	var s string
	if json.Unmarshal(r, &s) == nil && s != "" {
		return s
	}
	return nil
}

// namesOf collects the names of a list of named objects and of the lists nested in them (suboptions, contains).
func namesOf(raw json.RawMessage) []string {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var out []string
	for _, o := range list {
		var n string
		if json.Unmarshal(o["name"], &n) == nil && n != "" {
			out = append(out, n)
		}
		for _, k := range []string{"suboptions", "contains"} {
			out = append(out, namesOf(o[k])...)
		}
	}
	return out
}

func shortText(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) > maxShortRunes {
		s = string([]rune(s)[:maxShortRunes])
	}
	return s
}

func cutSearch(s string) string {
	if len(s) <= rows.MaxDocSearchText {
		return s
	}
	s = s[:rows.MaxDocSearchText]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// RenderDocsBlob renders the protocol document of any class from a [rows.DocSet]: Markdown rows go through m.Markdown, HTML rows through
// m.SanitizeHTML, so the served document holds no HTML that the Host's policy did not see. The order of contents is by type, then name. A
// Markdown source that the renderer returns nothing for is [ErrDocsNotRendered]: a Host without a renderer serves nothing unrendered.
func RenderDocsBlob(d rows.DocSet, m Markup) ([]byte, error) {
	html := func(f rows.DocFile) (string, error) {
		if f.Format == rows.DocFormatHTML {
			return m.SanitizeHTML(f.Body), nil
		}
		return renderMarkdown(m, f.Body)
	}
	type fileOut struct {
		Name string `json:"name"`
		HTML string `json:"html"`
	}
	type itemOut struct {
		ContentName string          `json:"content_name"`
		ContentType string          `json:"content_type"`
		DocStrings  json.RawMessage `json:"doc_strings"`
		ReadmeFile  *string         `json:"readme_file"`
		ReadmeHTML  *string         `json:"readme_html"`
	}
	readme := fileOut{Name: "README.md"}
	if d.Readme != nil {
		h, err := html(*d.Readme)
		if err != nil {
			return nil, err
		}
		readme = fileOut{Name: d.Readme.Name, HTML: h}
	}
	files := make([]fileOut, 0, len(d.Files))
	for _, f := range d.Files {
		h, err := html(f)
		if err != nil {
			return nil, err
		}
		files = append(files, fileOut{Name: f.Name, HTML: h})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	entries := append([]rows.DocEntry(nil), d.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type < entries[j].Type
		}
		return entries[i].Name < entries[j].Name
	})
	contents := make([]itemOut, 0, len(entries))
	for _, e := range entries {
		it := itemOut{ContentName: e.Name, ContentType: e.Type}
		if e.HasDocs && len(e.Doc) > 0 {
			it.DocStrings = e.Doc
		}
		switch {
		case e.ReadmeHTML != "":
			h := m.SanitizeHTML(e.ReadmeHTML)
			it.ReadmeFile, it.ReadmeHTML = ptr("README.md"), &h
		case e.ReadmeMD != "":
			h, err := renderMarkdown(m, e.ReadmeMD)
			if err != nil {
				return nil, err
			}
			it.ReadmeFile, it.ReadmeHTML = ptr("README.md"), &h
		}
		contents = append(contents, it)
	}
	return marshalNoEscape(map[string]any{"docs_blob": map[string]any{
		"collection_readme": readme, "documentation_files": files, "contents": contents,
	}})
}

func ptr(s string) *string { return &s }

func renderMarkdown(m Markup, src string) (string, error) {
	h, truncated := m.Markdown(src)
	if h == "" && src != "" && truncated {
		return "", ErrDocsNotRendered
	}
	return h, nil
}

// marshalNoEscape is json.Marshal without the escaping of <, > and &, as the other documents are written.
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// UpstreamHref is the href member of a version document an upstream sent, "" when it has none or the document does not read. The address of
// the version's documentation is this href plus "docs-blob/" (R-ANS-035).
func UpstreamHref(doc []byte) string {
	var u struct {
		Href string `json:"href"`
	}
	if len(doc) > MaxDocumentBytes || json.Unmarshal(doc, &u) != nil {
		return ""
	}
	return u.Href
}

// FetchFailure is the reason a fetch of an upstream's documentation is unavailable (the column ansible_doc_fetches.reason): too_large and
// invalid_document for what the parser refused, upstream_error for anything else.
func FetchFailure(err error) string {
	if e, ok := errs.As(err); ok && e.Code == errs.CodeUpstreamItemTooLarge {
		return rows.DocReasonTooLarge // the engine refused the size before the parser saw it
	}
	switch {
	case errors.Is(err, ErrDocsTooLarge):
		return rows.DocReasonTooLarge
	case errors.Is(err, ErrDocsInvalid):
		return rows.DocReasonInvalidDocument
	}
	return rows.DocReasonUpstreamError
}
