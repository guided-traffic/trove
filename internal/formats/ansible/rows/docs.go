package rows

import (
	"encoding/json"
	"time"
)

// The documentation of a collection version (R-ANS-033 to R-ANS-036): the neutral form every source is turned into and every output is
// rendered from. The generator (internal/formats/ansible/docgen) builds a [DocSet] from an archive, the upstream parser
// (internal/formats/ansible/galaxydoc) from a docs-blob document, the data port from its rows; the protocol document, the tree, an entry
// and a file are rendered from it.

// The sources of a [DocSet] (the column ansible_version_docs.source).
const (
	DocSourceGenerated = "generated"
	DocSourceUpstream  = "upstream"
)

// The formats of a [DocFile] (the column ansible_doc_files.format): Markdown that trove generated from an archive, or the HTML an
// upstream sent, which is untrusted and sanitized on every serving (R-ANS-039).
const (
	DocFormatMarkdown = "markdown"
	DocFormatHTML     = "html"
)

// The states of the documentation of a version (R-ANS-033).
const (
	DocAvailable   = "available"
	DocPending     = "pending"
	DocUnavailable = "unavailable"
	DocNone        = "none"
)

// The states of a fetch row of a remote version (the column ansible_doc_fetches.state).
const (
	DocFetchQueued      = "queued"
	DocFetchUnavailable = "unavailable"
	DocFetchNone        = "none"
)

// The reasons a documentation is unavailable (the column ansible_doc_fetches.reason).
const (
	DocReasonUpstreamError   = "upstream_error"
	DocReasonTooLarge        = "too_large"
	DocReasonInvalidDocument = "invalid_document"
)

// The bounds of the documentation of a version that the rows and the operations share (R-ANS-031, R-ANS-033, R-ANS-036).
const (
	MaxDocWarnings          = 100
	MaxDocSearchText        = 4 << 10
	MaxDocFiles             = 64
	MaxDocEntriesPerVersion = 5000
	MaxDocEntryBytes        = 1 << 20
	MaxDocSearchTerms       = 8
	MaxDocSearchTermLength  = 64
	MinDocQueryLength       = 2
	MaxDocQueryLength       = 512
)

// DocSet is the documentation of one version in the one form every source is turned into (generated from an archive, parsed from an
// upstream's docs-blob, read from the rows) and every output is rendered from. It is bounded by R-ANS-031 before it is built.
type DocSet struct {
	// Source is [DocSourceGenerated] or [DocSourceUpstream].
	Source string
	// Generator is docgen.Version for a generated set, 0 for an upstream's.
	Generator int
	// Readme is the README, nil when there is none.
	Readme *DocFile
	// Files are the Markdown files of docs/, at most [MaxDocFiles], in name order.
	Files   []DocFile
	Entries []DocEntry
	// Truncated says that a bound cut entries or files off.
	Truncated bool
	// Warnings are at most [MaxDocWarnings].
	Warnings []DocWarning
	// MoreWarnings counts the warnings left out beyond [MaxDocWarnings]; the import task reports them in one last message.
	MoreWarnings int
}

// DocFile is the README or a Markdown file of docs/. Format is [DocFormatMarkdown] for a generated set and [DocFormatHTML] for an
// upstream's.
type DocFile struct{ Name, Format, Body string }

// DocEntry is one content item: a module, a plugin of any type, or a role.
type DocEntry struct {
	// Type is the Galaxy's content_type and Name its content_name.
	Type, Name, ShortDescription string
	// HasDocs says that Doc is set.
	HasDocs bool
	// Doc is doc_strings in the Galaxy form of R-ANS-035; nil when HasDocs is false.
	Doc json.RawMessage
	// ReadmeMD and ReadmeHTML are set for roles: the generated source, or the upstream's HTML as received.
	ReadmeMD, ReadmeHTML string
	// SearchText is at most [MaxDocSearchText] bytes, built by the producer (R-ANS-033).
	SearchText string
}

// DocWarning is something the generator or the parser left out, with the member it concerned.
type DocWarning struct{ Path, Code, Message string }

// DocState is the state of the documentation of a version as the screens and the operations report it (R-ANS-033).
type DocState struct {
	// State is one of the Doc* states, Reason one of the DocReason* values or "", Source [DocSourceGenerated], [DocSourceUpstream] or "".
	State, Reason, Source string
	Entries               int
	Truncated             bool
	BuiltAt               time.Time
}

// DocQuery is a validated search (R-ANS-036): 1 to [MaxDocSearchTerms] terms of 1 to [MaxDocSearchTermLength] letters or digits, never
// raw text. The API builds it; the data port never sees the text a caller sent.
type DocQuery struct{ Terms []string }

// DocFileHead is a file in the tree: its name and title.
type DocFileHead struct{ Name, Title string }

// DocEntryHead is an entry in the tree.
type DocEntryHead struct {
	Type, Name, ShortDescription string
	HasDocs                      bool
}

// DocTree is the tree before grouping: the entries and the file titles that match, with the counts before the query (R-ANS-036).
type DocTree struct {
	State   DocState
	Files   []DocFileHead
	Entries []DocEntryHead
	// CountBeforeQuery is the number of entries before q filtered them; with no query it is len(Entries).
	CountBeforeQuery int
	// Counts are the entries per type before q.
	Counts map[string]int
	// Warnings are the stored warnings of the version.
	Warnings []DocWarning
}
