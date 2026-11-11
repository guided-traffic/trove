// Package docgen derives the documentation of a collection version from the text of its archive (R-ANS-034): the content items
// (modules, plugins of every type, roles), the documentation strings of their sources, the README and the Markdown files of docs/. It is
// the generator of a local publish; a remote's documentation is the upstream's and is parsed by internal/formats/ansible/galaxydoc.
//
// Nothing is executed. The generator reads text: it never starts a process, loads Python or imports a plugin, and `go list -deps` of
// this package names no os/exec (a test of the step that builds the generator). The result is the neutral form [rows.DocSet] every output
// is rendered from.
//
// The package is a leaf: it imports the standard library, the rows leaf and the archive reader's YAML walker of the format
// (internal/formats/ansible/collection).
//
// Status: the contract of work package WP0 (docs/projektplan/galaxy-docs-work-packages.md, section 2.3). The signatures are frozen; the
// bodies of [Generator.Wants], [Generator.Add] and [Generator.Build] are the next step and today read nothing: a generator built from
// this file wants no member and builds an empty set.
package docgen

import (
	"html"

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

// Generator collects the members of one archive and builds the documentation from them. It is single-use and not safe for concurrent
// use: one archive is read by one goroutine.
type Generator struct {
	lim     Limits
	members []Member
}

// NewGenerator returns a generator that reads under lim.
func NewGenerator(lim Limits) *Generator { return &Generator{lim: lim} }

// Wants reports whether the member at path is read, and how many bytes of it at most (R-ANS-034); a member it does not want is streamed
// through the digest and kept nowhere. The archive reader's tap asks it for every member.
func (g *Generator) Wants(string) (maxBytes int64, ok bool) { return 0, false }

// Add hands over a wanted member, in archive order. It never fails: a member that cannot be used becomes a warning of [Generator.Build].
func (g *Generator) Add(m Member) { g.members = append(g.members, m) }

// Build derives the documentation. Its argument is the name of the README the manifest declares (collection_info.readme), "README.md"
// when it names none. The order of entries, files and warnings is deterministic, the bounds are applied and the warnings collected.
func (g *Generator) Build(_ string) rows.DocSet {
	return rows.DocSet{Source: rows.DocSourceGenerated, Generator: Version}
}

// RenderMarkup converts the Ansible markup of a documentation string (I(), B(), C(), M(), U(), L(), R(), HORIZONTALLINE and the escaped
// forms of R-ANS-039) to HTML. All text is escaped first and the macros are recognized after; a link is made only for an http or https
// address. The caller sanitizes the result with the policy of R-UI-008: the converter is not the sanitizer.
//
// Status: the escaping is built; the macros are the next step, so today every macro stays the escaped text it was written as.
func RenderMarkup(text string) string { return html.EscapeString(text) }
