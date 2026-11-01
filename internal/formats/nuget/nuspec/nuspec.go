package nuspec

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The defaults of Limits (R-NUGET-005 configuration, R-ARCH-028).
const (
	DefaultMaxBytes = 1 << 20
	DefaultMaxDepth = 64
)

// namespacePrefix starts the namespace of every version of the manifest schema
// (".../packaging/2010/07/nuspec.xsd" and its successors).
const namespacePrefix = "http://schemas.microsoft.com/packaging/"

// Limits bound the cost of one .nuspec. A zero field takes its default; a
// caller can only tighten them (R-ARCH-028).
type Limits struct {
	// MaxBytes caps the document as it is stored, UTF-16 included
	// (nuget.archive.max_nuspec_bytes).
	MaxBytes int64
	// MaxDepth caps the element nesting.
	MaxDepth int
}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes <= 0 || l.MaxBytes > DefaultMaxBytes {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxDepth <= 0 || l.MaxDepth > DefaultMaxDepth {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// Metadata is what the .nuspec declares, in the shape of the nuget_version
// metadata of R-NUGET-005. Text values are trimmed and otherwise verbatim.
type Metadata struct {
	// ID and Version are the strings of the document, not yet validated or
	// normalized (R-NUGET-003, R-NUGET-004).
	ID      string
	Version string

	Title, Summary, Description, ReleaseNotes, Copyright, Language string
	// Authors is kept as one string.
	Authors string
	// Tags are the tags element split on white space.
	Tags []string

	ProjectURL, IconURL, LicenseURL string
	// License is nil when the document declares none.
	License *License
	// Icon and Readme are the paths of embedded files, as the document spells
	// them.
	Icon, Readme string

	RequireLicenseAcceptance bool
	DevelopmentDependency    bool
	Serviceable              bool

	// MinClientVersion is the attribute of the metadata element, verbatim.
	MinClientVersion string

	// Repository is nil when the document declares none.
	Repository *Repository

	// DependencyGroups are the groups of the dependencies element in document
	// order. A flat dependency list is one group with an empty TargetFramework.
	DependencyGroups []DependencyGroup

	// PackageTypes are the declared package types; none declared means
	// Dependency (see Types).
	PackageTypes []PackageType

	// Namespace is the namespace of the root element ("" when it has none).
	Namespace string
}

// License is the license element.
type License struct {
	// Type is "expression" or "file" (an absent attribute reads as
	// "expression"); other values are kept verbatim.
	Type    string
	Version string
	Value   string
}

// Repository is the repository element.
type Repository struct {
	Type, URL, Branch, Commit string
}

// DependencyGroup is one dependencies/group element.
type DependencyGroup struct {
	// TargetFramework is the attribute verbatim, "" when the group has none.
	TargetFramework string
	Dependencies    []Dependency
}

// Dependency is one dependency element. Range is the version attribute verbatim
// ("" when absent); the range grammar is R-NUGET-004's.
type Dependency struct {
	ID      string
	Range   string
	Include string
	Exclude string
}

// PackageType is one packageTypes/packageType element.
type PackageType struct {
	Name    string
	Version string
}

// Types returns the declared package types, or the single type Dependency when
// the document declares none (R-NUGET-005).
func (m *Metadata) Types() []PackageType {
	if len(m.PackageTypes) == 0 {
		return []PackageType{{Name: "Dependency"}}
	}
	return m.PackageTypes
}

// HasType reports whether the package declares the type name, compared
// case-insensitively like the NuGet client does.
func (m *Metadata) HasType(name string) bool {
	for _, t := range m.Types() {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}

// IsSymbols reports whether the package declares the SymbolsPackage type, which
// the package pipeline refuses (R-NUGET-005 step 4, R-NUGET-020).
func (m *Metadata) IsSymbols() bool { return m.HasType("SymbolsPackage") }

// Parse reads one .nuspec. data is the stored document: UTF-8 (a byte order mark
// is optional) or UTF-16 with a byte order mark. Every failure is a 400
// *errs.Error with CodeInvalidPackage and one of the Err reasons as its cause.
func Parse(data []byte, lim Limits) (*Metadata, error) {
	lim = lim.withDefaults()
	if int64(len(data)) > lim.MaxBytes {
		return nil, Invalid(ErrTooLarge, fmt.Sprintf("the .nuspec is larger than %d bytes", lim.MaxBytes))
	}
	text, utf16, bigEndian, err := toUTF8(data)
	if err != nil {
		return nil, err
	}
	root, err := readTree(text, utf16, bigEndian, lim.MaxDepth)
	if err != nil {
		return nil, err
	}
	return extract(root)
}

// toUTF8 returns the document as UTF-8 without a byte order mark.
func toUTF8(data []byte) (text []byte, utf16, bigEndian bool, err error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], data[0] == 0xFE)
	case len(data) >= 2 && (data[0] == 0 || data[1] == 0):
		return nil, false, false, Invalid(ErrEncoding, "the .nuspec is not UTF-8 and has no UTF-16 byte order mark")
	}
	if !utf8.Valid(data) {
		return nil, false, false, Invalid(ErrEncoding, "the .nuspec is not valid UTF-8")
	}
	return data, false, false, nil
}

func decodeUTF16(b []byte, bigEndian bool) ([]byte, bool, bool, error) {
	if len(b)%2 != 0 {
		return nil, true, bigEndian, Invalid(ErrEncoding, "the UTF-16 .nuspec has an odd number of bytes")
	}
	out := make([]byte, 0, len(b))
	unit := func(i int) rune {
		if bigEndian {
			return rune(b[i])<<8 | rune(b[i+1])
		}
		return rune(b[i+1])<<8 | rune(b[i])
	}
	for i := 0; i < len(b); i += 2 {
		r := unit(i)
		if r >= 0xD800 && r < 0xE000 {
			// a surrogate pair is one code point; a lone surrogate is no text
			if r >= 0xDC00 || i+2 >= len(b) {
				return nil, true, bigEndian, Invalid(ErrEncoding, "the UTF-16 .nuspec holds an unpaired surrogate")
			}
			lo := unit(i + 2)
			if lo < 0xDC00 || lo >= 0xE000 {
				return nil, true, bigEndian, Invalid(ErrEncoding, "the UTF-16 .nuspec holds an unpaired surrogate")
			}
			r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
			i += 2
		}
		out = utf8.AppendRune(out, r)
	}
	return out, true, bigEndian, nil
}

// node is one element of the tree; text segments are nodes of their own so that
// the value of an element is its text in document order, as the NuGet client
// reads it.
type node struct {
	space, local string
	attrs        []xml.Attr
	content      []*node
	text         string
	isText       bool
}

func (n *node) value() string {
	var b strings.Builder
	n.collect(&b)
	return strings.TrimSpace(b.String())
}

func (n *node) collect(b *strings.Builder) {
	for _, c := range n.content {
		if c.isText {
			b.WriteString(c.text)
		} else {
			c.collect(b)
		}
	}
}

func (n *node) attr(name string) string {
	for _, a := range n.attrs {
		if a.Name.Local == name && a.Name.Space == "" {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

var declEncoding = regexp.MustCompile(`encoding\s*=\s*["']([^"']*)["']`)

// readTree reads the document into a tree. Every token that could define or
// expand something is refused before the decoder would act on it: the standard
// decoder reports a declaration (<!DOCTYPE, <!ENTITY, <!ELEMENT, ...) as a
// Directive token, knows no entity but the five predefined ones and the numeric
// references, and so cannot expand anything else.
func readTree(text []byte, utf16, bigEndian bool, maxDepth int) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(text))
	dec.Strict = true
	mismatch := false
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		if charsetMatches(label, utf16, bigEndian) {
			return in, nil
		}
		mismatch = true
		return nil, errors.New("the declared encoding does not match the document")
	}
	var (
		root  *node
		stack []*node
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if mismatch {
				return nil, Invalid(ErrEncoding, "the encoding the .nuspec declares is not the encoding it is written in")
			}
			return nil, Invalid(ErrNotXML, "the .nuspec is not well-formed XML: "+shorten(err.Error()))
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, Invalid(ErrDoctype, "the .nuspec holds a declaration; a document type declaration is not accepted")
		case xml.ProcInst:
			if t.Target == "xml" {
				if m := declEncoding.FindSubmatch(t.Inst); m != nil && !charsetMatches(string(m[1]), utf16, bigEndian) {
					return nil, Invalid(ErrEncoding, "the encoding the .nuspec declares is not the encoding it is written in")
				}
			}
		case xml.StartElement:
			if len(stack) >= maxDepth {
				return nil, Invalid(ErrTooLarge, fmt.Sprintf("the .nuspec is nested deeper than %d elements", maxDepth))
			}
			n := &node{space: t.Name.Space, local: t.Name.Local, attrs: t.Attr}
			switch {
			case len(stack) > 0:
				p := stack[len(stack)-1]
				p.content = append(p.content, n)
			case root != nil:
				return nil, Invalid(ErrNotXML, "the .nuspec holds more than one root element")
			default:
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.content = append(p.content, &node{isText: true, text: string(t)})
			} else if len(bytes.TrimSpace(t)) > 0 {
				return nil, Invalid(ErrNotXML, "the .nuspec holds text outside its root element")
			}
		}
	}
	if root == nil {
		return nil, Invalid(ErrNotXML, "the .nuspec holds no element")
	}
	return root, nil
}

// charsetMatches reports whether a declared encoding names the one the document
// is written in. An absent declaration always matches.
func charsetMatches(label string, utf16, bigEndian bool) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "":
		return true
	case "utf-8", "utf8":
		return !utf16
	case "utf-16", "utf16":
		return utf16
	case "utf-16le":
		return utf16 && !bigEndian
	case "utf-16be":
		return utf16 && bigEndian
	}
	return false
}

func shorten(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// children returns the child elements of n with the local name in namespace
// space, in document order.
func (n *node) children(space, local string) []*node {
	var out []*node
	for _, c := range n.content {
		if !c.isText && c.local == local && c.space == space {
			out = append(out, c)
		}
	}
	return out
}

func (n *node) first(space, local string) *node {
	if c := n.children(space, local); len(c) > 0 {
		return c[0]
	}
	return nil
}

// once returns the one child with the name, nil when there is none, and refuses
// a second one: two readers that pick the first and the last would see two
// documents.
func (n *node) once(space, local string) (*node, error) {
	c := n.children(space, local)
	if len(c) > 1 {
		return nil, Invalid(ErrDuplicate, fmt.Sprintf("the .nuspec has more than one %s element", local))
	}
	if len(c) == 0 {
		return nil, nil
	}
	return c[0], nil
}

func (n *node) text1(space, local string) string {
	if c := n.first(space, local); c != nil {
		return c.value()
	}
	return ""
}

func (n *node) flag(space, local string) bool {
	return strings.EqualFold(n.text1(space, local), "true")
}

func extract(root *node) (*Metadata, error) {
	if root.local != "package" {
		return nil, Invalid(ErrShape, "the root element of the .nuspec is not package")
	}
	ns := root.space
	if ns != "" && !strings.HasPrefix(ns, namespacePrefix) {
		return nil, Invalid(ErrShape, "the .nuspec is in a namespace that is no manifest namespace")
	}
	md, err := root.once(ns, "metadata")
	if err != nil {
		return nil, err
	}
	if md == nil {
		return nil, Invalid(ErrMissingFields, "the .nuspec has no metadata element")
	}
	m := &Metadata{Namespace: ns, MinClientVersion: md.attr("minClientVersion")}

	for _, f := range []struct {
		name string
		dst  *string
	}{{"id", &m.ID}, {"version", &m.Version}} {
		c, err := md.once(ns, f.name)
		if err != nil {
			return nil, err
		}
		if c == nil || c.value() == "" {
			return nil, Invalid(ErrMissingFields, fmt.Sprintf("the .nuspec has no %s", f.name))
		}
		for _, k := range c.content {
			if !k.isText {
				return nil, Invalid(ErrShape, fmt.Sprintf("the %s of the .nuspec holds an element", f.name))
			}
		}
		*f.dst = c.value()
	}

	m.Title, m.Summary, m.Description = md.text1(ns, "title"), md.text1(ns, "summary"), md.text1(ns, "description")
	m.ReleaseNotes, m.Copyright, m.Language = md.text1(ns, "releaseNotes"), md.text1(ns, "copyright"), md.text1(ns, "language")
	m.Authors = md.text1(ns, "authors")
	if tags := strings.Fields(md.text1(ns, "tags")); len(tags) > 0 {
		m.Tags = tags
	}
	m.ProjectURL, m.IconURL, m.LicenseURL = md.text1(ns, "projectUrl"), md.text1(ns, "iconUrl"), md.text1(ns, "licenseUrl")
	m.Icon, m.Readme = md.text1(ns, "icon"), md.text1(ns, "readme")
	m.RequireLicenseAcceptance = md.flag(ns, "requireLicenseAcceptance")
	m.DevelopmentDependency = md.flag(ns, "developmentDependency")
	m.Serviceable = md.flag(ns, "serviceable")

	lic, err := md.once(ns, "license")
	if err != nil {
		return nil, err
	}
	if lic != nil {
		m.License = &License{Type: lic.attr("type"), Version: lic.attr("version"), Value: lic.value()}
		if m.License.Type == "" {
			m.License.Type = "expression"
		}
	}
	if r := md.first(ns, "repository"); r != nil {
		m.Repository = &Repository{Type: r.attr("type"), URL: r.attr("url"), Branch: r.attr("branch"), Commit: r.attr("commit")}
	}
	if m.DependencyGroups, err = dependencyGroups(md, ns); err != nil {
		return nil, err
	}
	if m.PackageTypes, err = packageTypes(md, ns); err != nil {
		return nil, err
	}
	return m, nil
}

func dependencyGroups(md *node, ns string) ([]DependencyGroup, error) {
	deps, err := md.once(ns, "dependencies")
	if err != nil || deps == nil {
		return nil, err
	}
	groups, flat := deps.children(ns, "group"), deps.children(ns, "dependency")
	switch {
	case len(groups) > 0 && len(flat) > 0:
		return nil, Invalid(ErrMixedDeps, "the .nuspec lists dependencies both inside and outside of groups")
	case len(flat) > 0:
		ds, err := readDependencies(flat)
		if err != nil {
			return nil, err
		}
		return []DependencyGroup{{Dependencies: ds}}, nil
	}
	var out []DependencyGroup
	for _, g := range groups {
		ds, err := readDependencies(g.children(ns, "dependency"))
		if err != nil {
			return nil, err
		}
		out = append(out, DependencyGroup{TargetFramework: g.attr("targetFramework"), Dependencies: ds})
	}
	return out, nil
}

func readDependencies(nodes []*node) ([]Dependency, error) {
	if len(nodes) == 0 {
		return nil, nil
	}
	out := make([]Dependency, 0, len(nodes))
	for _, d := range nodes {
		id := d.attr("id")
		if id == "" {
			return nil, Invalid(ErrShape, "a dependency of the .nuspec has no id")
		}
		out = append(out, Dependency{ID: id, Range: d.attr("version"), Include: d.attr("include"), Exclude: d.attr("exclude")})
	}
	return out, nil
}

func packageTypes(md *node, ns string) ([]PackageType, error) {
	pts, err := md.once(ns, "packageTypes")
	if err != nil || pts == nil {
		return nil, err
	}
	var out []PackageType
	for _, p := range pts.children(ns, "packageType") {
		name := p.attr("name")
		if name == "" {
			return nil, Invalid(ErrShape, "a package type of the .nuspec has no name")
		}
		out = append(out, PackageType{Name: name, Version: p.attr("version")})
	}
	return out, nil
}
