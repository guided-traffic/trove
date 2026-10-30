package formats

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// ContractVersion changes whenever a method of the contract changes meaning
// (R-ARCH-025). It stays 1 until the contract freezes at the end of M3 (E3.3);
// before the freeze a change is applied to every format in the same change
// (Q-ARCH-213). A format declares the literal it was written against in
// [Descriptor.ContractVersion], and Register panics on a mismatch.
const ContractVersion = 1

// The types of other packages under the names a format spells them: one type
// each, no mirrors (Q-ARCH-188, Q-ARCH-189). A format imports this package and
// nothing else of the core; it reads rq.Subject, rq.Index and rq.Repo without
// naming their packages.
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
	// Action is a repository action (R-AUTHZ-006).
	Action = authz.Action
	// Path is the canonical repository path (R-ARCH-005): Path, "" for the root,
	// and Folder.
	Path = repository.Cleaned
	// Limits are the resource limits of untrusted content (R-ARCH-028).
	Limits = untrusted.Limits
	// Tx is the storage transaction as a format holds it: opaque, with no way to
	// run SQL (meta.Tx's own rule). A format passes it to its data port and never
	// unwraps it.
	Tx = meta.Tx
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

// The repository actions. They carry a prefix because Delete is also a route
// kind.
const (
	ActionRead      = authz.Read
	ActionAnnotate  = authz.Annotate
	ActionDeploy    = authz.Deploy
	ActionOverwrite = authz.Overwrite
	ActionDelete    = authz.Delete
	ActionManage    = authz.Manage
)

// Format is a package format: protocol, classification, storage hooks, adapters.
// Its methods never touch storage; a format reaches the core only through Host
// (R-ARCH-009). Embed [Base] for the defaults of what a format lacks.
type Format interface {
	Descriptor() Descriptor

	// Routes lists every protocol operation below /api/<id>/<repoKey>/. It is empty
	// for a format whose protocol is /files/ alone: generic (Q-GENERIC-7).
	Routes() []Route
	// Match resolves method and the repository-relative path, still
	// percent-encoded, to one route. It returns errs.NotFound or
	// errs.MethodNotAllowed (with Allow), and never touches storage. Most formats
	// return MatchRoutes(f.Routes(), method, escapedPath).
	Match(method, escapedPath string) (*Route, Params, error)
	// Classify says how a canonical path behaves in caches and which package it
	// belongs to. It gives a /files/ request its coordinate (R-EP-014) and every
	// request its R-REPO-003 descriptor.
	Classify(p Path) PathInfo
	// PackageDocumentPath returns the canonical path of the document that stands
	// for a normalized package name upstream (R-REPO-029, R-REPO-048); ok is false
	// for a format without package names.
	PackageDocumentPath(name string) (p Path, ok bool)

	// IngestOptions says what the upload pipeline expects of one request.
	IngestOptions(rq *Request) IngestOptions
	// Inspect reads identity and namespaced properties from an upload (FMT-008).
	Inspect(ctx context.Context, in InspectInput) (*Inspection, error)
	// OnCommit runs in the transaction of a commit (storage.md): package and
	// version rows, the lead item.
	OnCommit(ctx context.Context, tx Tx, c CommitEvent) error
	// OnItemsRemoved and OnItemsRestored run in the transaction that removed or
	// restored items.
	OnItemsRemoved(ctx context.Context, tx Tx, e ItemsEvent) error
	OnItemsRestored(ctx context.Context, tx Tx, e ItemsEvent) error
	// RemoveVersion removes a version for retention and caps; the items it names
	// are removed by the core. A stub until retention exists.
	RemoveVersion(ctx context.Context, tx Tx, v VersionRef) ([]ItemRef, error)
	// VersionGroup is the group a cap counts the version in. A stub until caps.
	VersionGroup(v VersionRef) string

	// ServeCheck runs before the core serves a stored item on any route, /files/
	// included; errs.NotFound refuses it because of format state the generic item
	// row does not carry. It reads through h and runs no SQL.
	ServeCheck(ctx context.Context, h Host, repo RepoRef, it ItemRef, mode ServeMode) error

	// Remote is nil exactly when Classes lacks Remote; Register checks.
	Remote() RemoteAdapter
	// Virtual is nil exactly when Classes lacks Virtual.
	Virtual() VirtualAdapter
	// Indexes is nil for a format with no rebuildable derived metadata.
	Indexes() IndexBuilder
	// Exposure is nil for a format that is not exposable (R-EP-018).
	Exposure() ExposureAdapter
	// View is nil for a format without package screens (R-API-029): the adapter
	// reads what the generic rows do not hold and orders the versions.
	View() ViewAdapter

	// SetupTemplates are the client setup snippets (R-API-033).
	SetupTemplates() []SetupTemplate
	// WriteError renders a core error in the protocol's native shape (R-ARCH-023).
	// The dispatcher also uses it for the refusals of the chain's stages on this
	// format's paths (Q-ARCH-214).
	WriteError(w http.ResponseWriter, r *http.Request, err error)
}

// Descriptor is what a format registers: the catalog facts every layer reads
// (Register puts them into the catalog) and what only the contract needs.
type Descriptor struct {
	catalog.Facts
	// ContractVersion is the literal the format was written against; Register
	// panics on a mismatch (R-ARCH-010).
	ContractVersion int
	// Names is the format's one package-name normaliser (R-AUTHZ-005); nil exactly
	// when PackageNames is false.
	Names catalog.Names
	// Clients are the native clients and their tested versions (FMT-002, FMT-003).
	Clients []Client
	// PropertyPrefix is the reserved property namespace, "npm."; Inspect and
	// SetProperties write only below it (FMT-008).
	PropertyPrefix string
	// MaxDocumentBytes bounds a document held in memory; the smaller of it and
	// proxy.max_metadata_bytes applies (R-ARCH-028).
	MaxDocumentBytes int64
	// VerifyAnnouncedDigests says that the format's protocol addresses artifacts by
	// the digests its metadata announces, so a remote fill compares them whatever
	// integrity.checksumPolicy says (R-REPO-041: npm always verifies, R-NPM-026).
	VerifyAnnouncedDigests bool
	// Limits tighten untrusted.DefaultLimits; a zero field keeps the default.
	Limits Limits
	// RepositoryIndex says the format has repository-wide derived indexes, which
	// enables the per-repository revision (R-ARCH-012, Q-ARCH-12).
	RepositoryIndex bool
	// DataPort is the interface type of the format's side-table port (Q-ARCH-193),
	// for example reflect.TypeFor[npm.Store](); nil for a format without side
	// tables. The composition root binds one value per format and refuses to
	// start when a binding is missing or does not implement the port.
	DataPort reflect.Type
}

// formatRegistry holds the registered formats; the facts live in the catalog.
var formatRegistry = struct {
	mu sync.RWMutex
	by map[ID]Format
}{by: map[ID]Format{}}

// Register adds a format to the process catalog ([catalog.Default]) and to the
// registry of formats. It panics, and the process exits, on a programming error
// (R-ARCH-010): a duplicate or reserved ID, a ContractVersion mismatch, a route
// set the ambiguity check refuses, a class without its adapter (or an adapter
// without its class), and every rule of [catalog.Registry.Register]. Formats are
// registered at compile time by internal/formats/all.
//
// The exposure facts of the catalog come from the format's [ExposureAdapter]: no
// adapter means the format is not exposable, and the adapter is the one source of
// its dimensions and its version syntax (C-13).
func Register(f Format) {
	d := f.Descriptor()
	if err := Validate(f); err != nil {
		panic(fmt.Sprintf("formats: %v", err))
	}
	formatRegistry.mu.Lock()
	defer formatRegistry.mu.Unlock()
	if _, dup := formatRegistry.by[d.ID]; dup {
		panic(fmt.Sprintf("formats: %q is registered twice", d.ID))
	}
	catalog.Default().Register(CatalogEntry(f))
	formatRegistry.by[d.ID] = f
}

// CatalogEntry derives the catalog entry of a format: its facts, its name rule
// and the exposure facts of its adapter.
func CatalogEntry(f Format) catalog.Entry {
	d := f.Descriptor()
	e := catalog.Entry{Facts: d.Facts, Names: d.Names}
	if x := f.Exposure(); x != nil {
		dim := x.Dimensions()
		e.Exposure = catalog.Exposure{Exposable: true, Packages: dim.Packages, Versions: dim.Versions, Tags: dim.Tags}
		if dim.Versions {
			e.Exposure.Versioning = x
		}
	}
	return e
}

// Validate runs the contract checks of Register on a format without registering
// it: the checks that need no registry. The conformance kit runs it too, so a
// format's own tests fail with the message the process would exit with.
func Validate(f Format) error {
	d := f.Descriptor()
	id := d.ID
	switch {
	case !catalog.ValidID(string(id)):
		return fmt.Errorf("invalid or reserved format ID %q", id)
	case d.ContractVersion != ContractVersion:
		return fmt.Errorf("%q was written against contract version %d, this is %d", id, d.ContractVersion, ContractVersion)
	}
	if d.PropertyPrefix != "" && !strings.HasSuffix(d.PropertyPrefix, ".") {
		return fmt.Errorf("%q: property prefix %q does not end in a dot", id, d.PropertyPrefix)
	}
	if d.DataPort != nil && d.DataPort.Kind() != reflect.Interface {
		return fmt.Errorf("%q: the data port %v is no interface type", id, d.DataPort)
	}
	if err := checkAdapter(id, "Remote", d.Supports(Remote), f.Remote() != nil); err != nil {
		return err
	}
	if err := checkAdapter(id, "Virtual", d.Supports(Virtual), f.Virtual() != nil); err != nil {
		return err
	}
	if x := f.Exposure(); x != nil {
		if dim := x.Dimensions(); (dim.Packages || dim.Versions || dim.Tags) && !d.PackageNames {
			return fmt.Errorf("%q narrows exposure by package, version or tag and has no package names", id)
		}
	}
	if f.View() != nil && !d.PackageNames {
		return fmt.Errorf("%q has a package view and no package names", id)
	}
	switch {
	case repository.IsReservedMember(string(id)):
		return fmt.Errorf("%q is named like a member of the repository document, whose member for the format's settings it must be", id)
	case len(d.SettingsSchema) > 0 && !repository.HasFormatMember(string(id)):
		return fmt.Errorf("%q declares a settings schema and the repository document has no %q member for it", id, id)
	}
	return checkRoutes(f, d)
}

func checkAdapter(id ID, class string, listed, present bool) error {
	switch {
	case listed && !present:
		return fmt.Errorf("%q lists class %s and %s() returns nil", id, strings.ToLower(class), class)
	case !listed && present:
		return fmt.Errorf("%q returns a %s adapter and does not list class %s", id, strings.ToLower(class), strings.ToLower(class))
	}
	return nil
}

// checkRoutes is the ambiguity check of R-ARCH-010 (kit check 1) and the rules
// that make a route table servable.
func checkRoutes(f Format, d Descriptor) error {
	routes := f.Routes()
	if len(routes) == 0 {
		return nil
	}
	if want := "/api/" + string(d.ID) + "/{key}/"; d.ProtocolBasePath != want {
		return fmt.Errorf("%q has routes and protocol base path %q, not %q", d.ID, d.ProtocolBasePath, want)
	}
	names := map[string]bool{}
	for i := range routes {
		r := &routes[i]
		switch {
		case r.Name == "":
			return fmt.Errorf("%q: route %d has no name", d.ID, i)
		case names[r.Name]:
			return fmt.Errorf("%q: route name %q is used twice", d.ID, r.Name)
		case !slices.Contains([]string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodPatch}, r.Method):
			return fmt.Errorf("%q: route %q has method %q", d.ID, r.Name, r.Method)
		case r.Template == "":
			return fmt.Errorf("%q: route %q has no template", d.ID, r.Name)
		case r.Handler == nil:
			return fmt.Errorf("%q: route %q has no handler", d.ID, r.Name)
		case !r.Kind.Valid():
			return fmt.Errorf("%q: route %q has no kind", d.ID, r.Name)
		case r.Action == "":
			return fmt.Errorf("%q: route %q has no action", d.ID, r.Name)
		case len(r.Examples) == 0:
			return fmt.Errorf("%q: route %q has no example", d.ID, r.Name)
		}
		names[r.Name] = true
		for _, c := range r.Classes {
			if !d.Supports(c) {
				return fmt.Errorf("%q: route %q serves class %q, which the format does not support", d.ID, r.Name, c)
			}
		}
	}
	for i := range routes {
		r := &routes[i]
		for _, ex := range r.Examples {
			if _, err := repository.CleanPath(ex); err != nil {
				return fmt.Errorf("%q: example %q of route %q is no clean path", d.ID, ex, r.Name)
			}
			// Clients never upload derived metadata (R-ARCH-012): what trove renders is
			// changed by a write to the rows it renders from, which is an Admin route.
			if r.Kind == Upload && f.Classify(Path{Path: ex}).Kind == PathComputed {
				return fmt.Errorf("%q: upload route %q accepts a write to %q, which Classify marks computed", d.ID, r.Name, ex)
			}
			for j := range routes {
				o := &routes[j]
				if o.Method != r.Method || !classesOverlap(r, o) {
					continue
				}
				_, ok := o.matcher()(ex)
				switch {
				case i == j && !ok:
					return fmt.Errorf("%q: route %q does not match its own example %q", d.ID, r.Name, ex)
				case i != j && ok:
					return fmt.Errorf("%q: example %q of route %q is also matched by route %q (ambiguous routes)", d.ID, ex, r.Name, o.Name)
				}
			}
		}
	}
	return nil
}

func classesOverlap(a, b *Route) bool {
	if len(a.Classes) == 0 || len(b.Classes) == 0 {
		return true
	}
	for _, c := range a.Classes {
		if slices.Contains(b.Classes, c) {
			return true
		}
	}
	return false
}

// Lookup returns the format with the given name.
func Lookup(name string) (Format, bool) {
	formatRegistry.mu.RLock()
	defer formatRegistry.mu.RUnlock()
	f, ok := formatRegistry.by[ID(strings.TrimSpace(name))]
	return f, ok
}

// List returns the registered formats sorted by ID.
func List() []Format {
	formatRegistry.mu.RLock()
	defer formatRegistry.mu.RUnlock()
	out := make([]Format, 0, len(formatRegistry.by))
	for _, f := range formatRegistry.by {
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
