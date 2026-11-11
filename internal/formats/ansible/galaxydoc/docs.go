package galaxydoc

import (
	"errors"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// The docs-blob document of a collection version (R-ANS-035): {"docs_blob": {"collection_readme", "documentation_files", "contents"}},
// parsed into the neutral form [rows.DocSet] and rendered from it for every class.
//
// Status: the contract of work package WP0 (docs/projektplan/galaxy-docs-work-packages.md, section 2.3). The signatures are frozen; the
// bodies are the next step and today answer [ErrDocsNotBuilt].

// ErrDocsNotBuilt is the answer of the documentation parser and renderer until their step has landed.
var ErrDocsNotBuilt = errors.New("galaxydoc: the docs-blob parser and renderer are not built yet")

// Markup is what the renderer needs of the Host's text capability (formats.Markup, ADR-196): the package is a leaf and cannot import the
// format contract, so it declares the two methods it calls and the Host's value satisfies them structurally.
type Markup interface {
	// Markdown renders a Markdown source to sanitized HTML.
	Markdown(src string) (html string, truncated bool)
	// SanitizeHTML applies the allowlist of the policy of R-UI-008 to HTML somebody else wrote.
	SanitizeHTML(html string) string
}

// ParseUpstreamDocs parses the docs-blob document of an upstream (R-ANS-035) into a [rows.DocSet] with the source upstream and the
// generator 0. The document is bounded by [MaxDocumentBytes], its shape is checked before anything is built, and an item without a
// content_name or a content_type is an error, as is a document that is not the shape of R-ANS-035. An item above 1 MiB of doc_strings
// is kept as a name and short description with no documentation, and the set is marked truncated.
func ParseUpstreamDocs(doc []byte) (rows.DocSet, error) {
	_ = doc
	return rows.DocSet{}, ErrDocsNotBuilt
}

// RenderDocsBlob renders the protocol document of any class from a [rows.DocSet]: Markdown rows go through m.Markdown, HTML rows through
// m.SanitizeHTML, so the served document holds no HTML that the Host's policy did not see. The order of contents is by type, then name.
func RenderDocsBlob(d rows.DocSet, m Markup) ([]byte, error) {
	_, _ = d, m
	return nil, ErrDocsNotBuilt
}
