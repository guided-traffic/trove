// Package catalog is the one registry of what every layer may know of a package
// format (R-REPO-003, R-ARCH-008): its identity, the classes it supports, how
// its package names compare, and what endpoint exposure may narrow.
//
// It is a leaf. The repository model and the decision point are below
// internal/formats and cannot import the format contract, so the facts they need
// live here, declared once by the format and read by everyone: there is no
// mirror of a format's facts in another package. A format registers through
// formats.Register, which derives the [Entry] from the format and registers it in
// [Default]; nothing else registers in production. A package that reads the
// catalog takes a *[Registry] and treats nil as [Default] ([Or]), so a test builds
// its own registry and shares no state.
package catalog

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ID is the name of a format: "generic", "npm", "nuget". It matches
// ^[a-z][a-z0-9-]{1,31}$ and is never ^v[0-9]+$, which is reserved for API
// versions (R-ARCH-005).
type ID string

// Class is a repository class (R-REPO-001), spelled as it is stored and served.
type Class string

// The classes.
const (
	Local   Class = "local"
	Remote  Class = "remote"
	Virtual Class = "virtual"
)

// Valid reports whether c is one of the three classes of R-REPO-001.
func (c Class) Valid() bool { return c == Local || c == Remote || c == Virtual }

// PatternSubject says what the include and exclude patterns of a format match
// (R-REPO-005).
type PatternSubject string

// The subjects.
const (
	// SubjectName: patterns match the normalized package name.
	SubjectName PatternSubject = "name"
	// SubjectPath: patterns match the canonical path.
	SubjectPath PatternSubject = "path"
)

// RequestKind is how a request behaves for authorization and in caches
// (R-REPO-003).
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

// Facts are the registration of R-REPO-003 that every layer may read;
// GET /api/v1/formats serves them.
type Facts struct {
	ID          ID
	DisplayName string
	// Classes is the single source of truth for the classes the implementation
	// accepts (R-ARCH-008): the repository model, the format list of the API and
	// the create form all read it.
	Classes []Class
	// Layout is the name of the format's one built-in layout (R-REPO-006).
	Layout string
	// ProtocolBasePath is the client address template, {key} the repository key.
	ProtocolBasePath string
	// DefaultUpstreamURL is empty for a format without a well-known upstream;
	// DefaultExtraHosts are the further hosts a remote repository of the format
	// may fetch from by default (R-REPO-024).
	DefaultUpstreamURL string
	DefaultExtraHosts  []string
	// ProbePath is the resource the upstream test of a remote repository requests
	// (R-REPO-024), relative to the upstream URL: "-/ping" for npm. Empty: the
	// upstream URL itself, which is the probe for a format whose URL names the
	// service index.
	ProbePath string
	// DefaultMetadataTTLSeconds and DefaultNotFoundTTLSeconds are the format's
	// cache periods of a remote repository (R-REPO-024). Nil: the period does not
	// exist for the format.
	DefaultMetadataTTLSeconds *int
	DefaultNotFoundTTLSeconds *int
	// PatternSubject is what include and exclude patterns match.
	PatternSubject PatternSubject
	// PackageNames is true for a format whose content has package names:
	// ownership, claims and name patterns act on them. It holds exactly when the
	// entry has a [Names].
	PackageNames bool
	// PriorityMembers, PinsAndClaims and Caps say whether the format supports
	// priority members (R-REPO-022), pin rules and namespace claims (R-REPO-023)
	// and repository caps (R-REPO-007).
	PriorityMembers bool
	PinsAndClaims   bool
	Caps            bool
	// OwnershipDefault is the packageOwnership a local repository of the format
	// holds while its document does not set one (R-AUTHZ-018, Q-AUTHZ-2): enforce
	// or off. Empty for a format without package names, where ownership does not
	// apply (Q-AUTHZ-155).
	OwnershipDefault string
	// SettingsSchema is the JSON Schema 2020-12 of the format's settings member of the
	// repository document, the member named after the format (R-ARCH-008, R-REPO-004).
	// Nil for a format without settings. The root must be an object schema with
	// additionalProperties false; a property may name the classes it applies to in
	// x-trove-classes. [Entry.ValidateSettings] checks a document against it, and
	// GET /api/v1/formats serves it to the create form.
	SettingsSchema []byte
}

// The values of OwnershipDefault (R-AUTHZ-018 packageOwnership).
const (
	OwnershipEnforce = "enforce"
	OwnershipOff     = "off"
)

// Supports reports whether the format accepts the class.
func (f Facts) Supports(c Class) bool {
	for _, k := range f.Classes {
		if k == c {
			return true
		}
	}
	return false
}

// Names is the one package-name rule of a format (R-REPO-003 "its package-name
// normalization", R-AUTHZ-005). Grants, token scopes, exposure rules, claims and
// repository patterns fold a name and a pattern with the same function, so what
// a name is compared as never differs between two features.
type Names interface {
	// Normalize returns the normalized name; ok is false for a string that is no
	// name of the format. It implements the widest rule the format accepts anywhere
	// (a remote repository may serve names a local one would refuse), so that !ok
	// means "no repository of the format holds or may be asked for this string":
	// routing then refuses the name instead of looking up its spelling as written,
	// which would miss a claim or a pin rule on the folded form and fail open. A
	// rule only local repositories apply is checked in Inspect. It is idempotent and
	// leaves the pattern characters * ? [ ] { } , ! and \ unchanged, so a pattern is
	// folded with the function that folds a name.
	Normalize(name string) (norm string, ok bool)
}

// Versioning is the part of a format's exposure adapter that the decision point
// compiles rules with (R-EP-012, R-EP-018): the format's range syntax and its
// notion of a pre-release.
type Versioning interface {
	// ValidateRange checks one range in the format's native syntax.
	ValidateRange(expr string) error
	// Prerelease reports whether a normalized version is a pre-release; ok is
	// false for a string that is not a version of the format.
	Prerelease(version string) (pre, ok bool)
	// InRange reports whether a normalized version lies in a validated range.
	InRange(version, expr string) bool
}

// Exposure says what exposure rules may narrow for the repositories of one
// format (R-EP-012, R-EP-018). The zero value is a format without exposure
// support: an endpoint with exposure rules exposes nothing of it and a rule for
// one of its repositories is refused (fail closed).
type Exposure struct {
	Exposable bool
	// Packages, Versions and Tags say whether the format has package names,
	// versions and tags; a rule that narrows a dimension the format lacks is
	// refused with 422 dimension_not_supported.
	Packages, Versions, Tags bool
	// Versioning parses versions and ranges; required when Versions is true.
	Versioning Versioning
}

// Entry is one format in the catalog.
type Entry struct {
	Facts
	// Names is nil exactly when the format has no package names.
	Names    Names
	Exposure Exposure

	// settings is the compiled SettingsSchema, filled by Register.
	settings *settingsSchema
}

var (
	idPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	versionLike = regexp.MustCompile(`^v[0-9]+$`)
)

// ValidID reports whether id is a format name: well formed and not an API
// version.
func ValidID(id string) bool { return idPattern.MatchString(id) && !versionLike.MatchString(id) }

// Registry holds the formats of one process or one test. It is safe for
// concurrent use.
type Registry struct {
	mu     sync.RWMutex
	by     map[ID]Entry
	frozen bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{by: map[ID]Entry{}} }

var process = NewRegistry()

// Default returns the registry of the process: formats.Register fills it at
// start-up (internal/formats/all).
func Default() *Registry { return process }

// Or returns r, or [Default] for nil: the rule every consumer of a registry
// applies to an option it was not given.
func Or(r *Registry) *Registry {
	if r == nil {
		return process
	}
	return r
}

// Register adds a format. It panics on a programming error: an invalid or
// reserved ID, a duplicate, no class or a class twice, no layout, a pattern
// subject that is neither name nor path, package names without a [Names] (or a
// [Names] without package names), exposure of versions without [Versioning],
// exposure dimensions of a format that is not exposable, a settings schema that
// does not compile or is no object schema that refuses unknown members, or a
// registration after [Registry.Freeze].
func (r *Registry) Register(e Entry) {
	if err := check(e); err != nil {
		panic(fmt.Sprintf("catalog: %v", err))
	}
	var compiled *settingsSchema
	if len(e.SettingsSchema) > 0 {
		var err error
		if compiled, err = compileSettings(e.ID, e.SettingsSchema); err != nil {
			panic(fmt.Sprintf("catalog: %v", err))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		panic(fmt.Sprintf("catalog: %q is registered after the registry was frozen", e.ID))
	}
	if _, dup := r.by[e.ID]; dup {
		panic(fmt.Sprintf("catalog: %q is registered twice", e.ID))
	}
	e.Classes = append([]Class(nil), e.Classes...)
	e.settings = compiled
	r.by[e.ID] = e
}

func check(e Entry) error {
	switch {
	case !ValidID(string(e.ID)):
		return fmt.Errorf("invalid or reserved format ID %q", e.ID)
	case len(e.Classes) == 0:
		return fmt.Errorf("%q supports no class", e.ID)
	case e.Layout == "":
		return fmt.Errorf("%q has no layout", e.ID)
	case e.PatternSubject != SubjectName && e.PatternSubject != SubjectPath:
		return fmt.Errorf("%q has pattern subject %q", e.ID, e.PatternSubject)
	case e.PackageNames && e.Names == nil:
		return fmt.Errorf("%q has package names and no name rule", e.ID)
	case !e.PackageNames && e.Names != nil:
		return fmt.Errorf("%q has a name rule and no package names", e.ID)
	case e.OwnershipDefault != "" && e.OwnershipDefault != OwnershipEnforce && e.OwnershipDefault != OwnershipOff:
		return fmt.Errorf("%q has the ownership default %q", e.ID, e.OwnershipDefault)
	case e.OwnershipDefault != "" && !e.PackageNames:
		return fmt.Errorf("%q has an ownership default and no package names", e.ID)
	case e.Exposure.Versions && e.Exposure.Versioning == nil:
		return fmt.Errorf("%q exposes versions and has no versioning", e.ID)
	case !e.Exposure.Exposable && (e.Exposure.Packages || e.Exposure.Versions || e.Exposure.Tags):
		return fmt.Errorf("%q narrows exposure dimensions and is not exposable", e.ID)
	}
	seen := map[Class]bool{}
	for _, c := range e.Classes {
		if !c.Valid() || seen[c] {
			return fmt.Errorf("%q lists class %q invalidly", e.ID, c)
		}
		seen[c] = true
	}
	return nil
}

// Freeze ends registration: a later Register panics. It makes the registry
// read-only once the formats are registered, before requests run.
func (r *Registry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Lookup returns the entry of a format name; blanks around the name are ignored.
func (r *Registry) Lookup(name string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.by[ID(strings.TrimSpace(name))]
	return e, ok
}

// List returns the entries sorted by ID, which is the body of
// GET /api/v1/formats (R-REPO-003).
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.by))
	for _, e := range r.by {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NormalizeName normalizes a package name or a name pattern with the rule of
// the format. A format without a name rule, and a format the registry does not
// know, leave the string unchanged: nothing is folded that no format declared.
func (r *Registry) NormalizeName(format, name string) (string, bool) {
	e, ok := r.Lookup(format)
	if !ok || e.Names == nil {
		return name, true
	}
	return e.Names.Normalize(name)
}
