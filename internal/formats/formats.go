package formats

import (
	"sort"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// ContractVersion changes whenever a method below changes meaning (R-ARCH-025).
const ContractVersion = 1

// The types of the catalog under the names a format spells them: one type each,
// no mirrors (Q-ARCH-188).
type (
	// ID is the name of a format: "generic", "npm", "nuget" (R-ARCH-005).
	ID = catalog.ID
	// Class is a repository class a format supports (R-REPO-001).
	Class = catalog.Class
	// PatternSubject says what include and exclude patterns of a format match
	// (R-REPO-005).
	PatternSubject = catalog.PatternSubject
	// RequestKind is how a request behaves in caches (R-REPO-003).
	RequestKind = catalog.RequestKind
)

// The classes (R-REPO-001).
const (
	Local   = catalog.Local
	Remote  = catalog.Remote
	Virtual = catalog.Virtual
)

// The pattern subjects.
const (
	// SubjectName: patterns match the normalized package name.
	SubjectName = catalog.SubjectName
	// SubjectPath: patterns match the canonical path.
	SubjectPath = catalog.SubjectPath
)

// The request kinds.
const (
	// KindArtifact is immutable binary content.
	KindArtifact = catalog.KindArtifact
	// KindMetadata is an upstream document that expires.
	KindMetadata = catalog.KindMetadata
	// KindSearch is a query, passed through and never cached.
	KindSearch = catalog.KindSearch
	// KindWrite changes state: a deploy or a delete.
	KindWrite = catalog.KindWrite
	// KindOther is everything else, such as a directory index that trove renders.
	KindOther = catalog.KindOther
)

// Descriptor is what a format registers in the format catalog (R-REPO-003) and
// what GET /api/v1/formats serves: the facts every layer reads, the format's one
// name rule and what exposure rules may narrow. Register puts them into the
// catalog; the repository model and the decision point read them there.
type Descriptor struct {
	catalog.Facts
	// Names is the format's one package-name normaliser (R-AUTHZ-005); nil
	// exactly when PackageNames is false.
	Names catalog.Names
	// Exposure says what exposure rules may narrow (R-EP-012, R-EP-018); the
	// zero value is not exposable.
	Exposure catalog.Exposure
}

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

var reg = struct {
	mu sync.RWMutex
	by map[ID]Format
}{by: map[ID]Format{}}

// Register adds a format to the process catalog ([catalog.Default]) and to the
// registry of formats. It panics on a programming error: an invalid or reserved
// ID, a duplicate, no class, a class twice, or no layout (see
// [catalog.Registry.Register] for the full list). Formats are registered at
// compile time by internal/formats/all (R-ARCH-010).
func Register(f Format) {
	d := f.Descriptor()
	catalog.Default().Register(catalog.Entry{Facts: d.Facts, Names: d.Names, Exposure: d.Exposure})
	reg.mu.Lock()
	defer reg.mu.Unlock()
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
