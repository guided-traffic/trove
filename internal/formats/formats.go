package formats

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ContractVersion changes whenever a method below changes meaning (R-ARCH-025).
const ContractVersion = 1

// ID is the name of a format: "generic", "npm", "nuget". It matches
// ^[a-z][a-z0-9-]{1,31}$ and is never ^v[0-9]+$, which is reserved for API
// versions (R-ARCH-005).
type ID string

// Class is a repository class a format supports.
type Class uint8

// The classes (R-REPO-001).
const (
	Local Class = iota + 1
	Remote
	Virtual
)

// String returns the class as it is spelled in configuration documents.
func (c Class) String() string {
	switch c {
	case Local:
		return "local"
	case Remote:
		return "remote"
	case Virtual:
		return "virtual"
	}
	return fmt.Sprintf("class(%d)", uint8(c))
}

// PatternSubject says what include and exclude patterns of a format match
// (R-REPO-005).
type PatternSubject string

// The subjects.
const (
	// SubjectName: patterns match the normalized package name.
	SubjectName PatternSubject = "name"
	// SubjectPath: patterns match the canonical path.
	SubjectPath PatternSubject = "path"
)

// Descriptor is what a format registers in the format catalog (R-REPO-003) and
// what GET /api/v1/formats serves.
type Descriptor struct {
	ID          ID
	DisplayName string
	// Classes is the single source of truth for the classes the implementation
	// accepts (R-ARCH-008): the repository model, the format list of the API
	// and the create form all read it.
	Classes []Class
	// Layout is the name of the format's one built-in layout (R-REPO-006).
	Layout string
	// ProtocolBasePath is the client address template, {key} the repository key.
	ProtocolBasePath string
	// DefaultUpstreamURL is empty for a format without a well-known upstream.
	DefaultUpstreamURL string
	// DefaultMetadataTTLSeconds and DefaultNotFoundTTLSeconds are the format's
	// cache periods of a remote repository (R-REPO-024). Nil: the period does
	// not exist for the format.
	DefaultMetadataTTLSeconds *int
	DefaultNotFoundTTLSeconds *int
	// PatternSubject is what include and exclude patterns match.
	PatternSubject PatternSubject
	// PackageNames is true for a format whose content has package names:
	// ownership, claims and name patterns act on them.
	PackageNames bool
	// PriorityMembers, PinsAndClaims and Caps say whether the format supports
	// priority members (R-REPO-022), pin rules and namespace claims (R-REPO-023)
	// and repository caps (R-REPO-007).
	PriorityMembers bool
	PinsAndClaims   bool
	Caps            bool
}

// Supports reports whether the format accepts the class.
func (d Descriptor) Supports(c Class) bool {
	for _, k := range d.Classes {
		if k == c {
			return true
		}
	}
	return false
}

// RequestKind is how a request behaves in caches (R-REPO-003).
type RequestKind string

// The request kinds.
const (
	// KindArtifact is immutable binary content.
	KindArtifact RequestKind = "artifact"
	// KindMetadata is an upstream document that expires.
	KindMetadata RequestKind = "metadata"
	// KindSearch is a query, passed through and never cached.
	KindSearch RequestKind = "search"
	// KindWrite changes state: a deploy or a delete.
	KindWrite RequestKind = "write"
	// KindOther is everything else, such as a directory index that trove renders.
	KindOther RequestKind = "other"
)

// RequestDescriptor is what a format makes of a request (R-REPO-003): the
// canonical path, the kind, and the package identity where the request concerns
// a package. The generic format has no package names, so those stay empty.
type RequestDescriptor struct {
	Path string
	Kind RequestKind
	// Package and Version are the normalized package name and version.
	Package, Version string
	// RoutingName is the name a format derives for an artifact that belongs to no
	// package, so that patterns, claims and pin rules can protect it.
	RoutingName string
	// Variant distinguishes content-negotiated metadata.
	Variant string
}

// Format is a package format. Its methods never touch storage.
type Format interface {
	Descriptor() Descriptor
	// Describe classifies one request. path is the canonical repository path
	// (no leading slash, "" for the repository root) and folder says that the
	// request path ended in a slash. A path the format cannot classify is
	// KindMetadata: misclassifying a binary costs a refetch, misclassifying
	// metadata as immutable freezes package lists for good (R-REPO-003).
	Describe(method, path string, folder bool) RequestDescriptor
}

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	versionLike = regexp.MustCompile(`^v[0-9]+$`)
)

var reg = struct {
	mu sync.RWMutex
	by map[ID]Format
}{by: map[ID]Format{}}

// Register adds a format to the catalog. It panics on a programming error: an
// invalid or reserved ID, a duplicate, no class, a class twice, or no layout.
// Formats are registered at compile time by internal/formats/all (R-ARCH-010).
func Register(f Format) {
	d := f.Descriptor()
	switch {
	case !idPattern.MatchString(string(d.ID)) || versionLike.MatchString(string(d.ID)):
		panic(fmt.Sprintf("formats: invalid or reserved format ID %q", d.ID))
	case len(d.Classes) == 0:
		panic(fmt.Sprintf("formats: %q supports no class", d.ID))
	case d.Layout == "":
		panic(fmt.Sprintf("formats: %q has no layout", d.ID))
	case d.PatternSubject != SubjectName && d.PatternSubject != SubjectPath:
		panic(fmt.Sprintf("formats: %q has pattern subject %q", d.ID, d.PatternSubject))
	}
	seen := map[Class]bool{}
	for _, c := range d.Classes {
		if c < Local || c > Virtual || seen[c] {
			panic(fmt.Sprintf("formats: %q lists class %d invalidly", d.ID, c))
		}
		seen[c] = true
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, dup := reg.by[d.ID]; dup {
		panic(fmt.Sprintf("formats: %q is registered twice", d.ID))
	}
	reg.by[d.ID] = f
}

// Lookup returns the format with the given name.
func Lookup(name string) (Format, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	f, ok := reg.by[ID(strings.TrimSpace(name))]
	return f, ok
}

// List returns the registered formats sorted by ID.
func List() []Format {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]Format, 0, len(reg.by))
	for _, f := range reg.by {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Descriptor().ID < out[j].Descriptor().ID })
	return out
}

// Descriptors returns the descriptors of the registered formats sorted by ID,
// which is the body of GET /api/v1/formats (R-REPO-003).
func Descriptors() []Descriptor {
	fs := List()
	out := make([]Descriptor, len(fs))
	for i, f := range fs {
		out[i] = f.Descriptor()
	}
	return out
}
