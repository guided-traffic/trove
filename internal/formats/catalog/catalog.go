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
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/qname"
)

// ID is the name of a format: "generic", "npm", "nuget". Its shape is qname.ValidFormat:
// the shape of a project key, and never ^v[0-9]+$, which is reserved for API versions
// (R-ARCH-005).
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

// RegistryRootBasePath is the protocol base path of the format that declares [Facts.RegistryRoot].
const RegistryRootBasePath = "/v2/{project}/{key}/"

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
	// ProtocolBasePath is the client address template, {project} and {key} the project and repository keys:
	// "/api/<id>/{project}/{key}/", or "/v2/{project}/{key}/" for the format that declares [Facts.RegistryRoot].
	ProtocolBasePath string
	// RegistryRoot is true for the one format whose client address is the root of a host: its ProtocolBasePath is
	// "/v2/{project}/{key}/" and the three repository-less routes below "/v2/" belong to the core (R-OCI-001, R-OCI-003,
	// Q-OCI-4). At most one registered format declares it; the catalog refuses a second.
	RegistryRoot bool
	// DefaultUpstreamURL is empty for a format without a well-known upstream;
	// DefaultExtraHosts are the further hosts a remote repository of the format
	// may fetch from by default (R-REPO-024).
	DefaultUpstreamURL string
	DefaultExtraHosts  []string
	// DefaultCredentialHosts are the entries of DefaultExtraHosts a remote repository of the format proposes with
	// sendCredentials: true, because the upstream's token realm lives there (R-OCI-021). Each is also in
	// DefaultExtraHosts; the catalog refuses an entry that is not.
	DefaultCredentialHosts []string
	// ProbePath is the resource the upstream test of a remote repository requests
	// (R-REPO-024), relative to the upstream URL: "-/ping" for npm. Empty: the
	// upstream URL itself, which is the probe for a format whose URL names the
	// service index.
	ProbePath string
	// ProbeChallengeReachable is true for a format whose upstream authenticates with a token handshake: its probe resource
	// answers 401 with a Bearer challenge to a client that holds no token, and the upstream test reads that answer as reachable
	// with no verdict on the credential (authenticated null), because the credential is judged by the realm, which the test does
	// not ask (R-OCI-021, R-REPO-024).
	ProbeChallengeReachable bool
	// DefaultMetadataTTLSeconds and DefaultNotFoundTTLSeconds are the format's
	// cache periods of a remote repository (R-REPO-024). Nil: the period does not
	// exist for the format.
	DefaultMetadataTTLSeconds *int
	DefaultNotFoundTTLSeconds *int
	// ChecksumRequireUnsupported is true for a format whose upstream announces no digest the
	// engine could verify an artifact against, so that `integrity.checksumPolicy: require` could
	// only ever refuse every artifact. A remote repository of the format cannot be created or
	// updated with that policy: 422 not_applicable (R-REPO-041, R-NUGET-015). False: the
	// policy is supported, which is what every format that announces digests holds.
	ChecksumRequireUnsupported bool
	// ChecksumAlwaysVerified is true for a format that compares the digest its metadata announces
	// whatever integrity.checksumPolicy says (R-REPO-041: npm, R-NPM-025). The policies ignore and
	// pass-through would do nothing for it, so a remote repository of the format cannot be created
	// or updated with them: 422 not_applicable (Q-NPM-79). Register holds it equal to
	// formats.Descriptor.VerifyAnnouncedDigests.
	ChecksumAlwaysVerified bool
	// LowerCasePaths is true for a format whose canonical package paths are lower case (NuGet's layout,
	// R-NUGET-005). The decision point compares the path dimension of a grant, a token scope, a CI inline grant
	// and an exposure rule in lower case for a repository of the format, pattern and path alike (Q-AUTHZ-254),
	// and the writers of those refuse a path pattern with an upper-case letter that covers one, as feedback to
	// whoever wrote it (Q-AUTHZ-253).
	LowerCasePaths bool
	// Wiki is true for a format with wiki content, the documentation of its packages that trove serves or shows apart from their
	// files (R-AUTHZ-034): npm and NuGet serve a README. A repository of such a format has a wiki level worth setting; the kit
	// holds the format to the wiki rules with check 15 (R-ARCH-010), and a format without wiki content declares no Wiki route.
	Wiki bool
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
	Names Names
	// DocumentPath returns the canonical path of the package document of a normalized name, the format's
	// PackageDocumentPath (R-REPO-029); ok is false for a string the format takes for no name. formats.Register
	// derives it for a format with package names. The decision point decides a question about a package as a whole
	// (a listing row, the ownership of a name, a check of the caller) on that path, so that path patterns apply to
	// it as they apply to the protocol's package document (R-AUTHZ-005, Q-AUTHZ-250). Nil: such a question carries
	// no path, and a grant with path patterns does not match it (fail closed).
	DocumentPath func(name string) (path string, ok bool)
	// VersionPath returns the canonical path of the file that holds a version's content - npm's tarball, NuGet's
	// .nupkg - for a normalized name and a version as the format stores it; ok is false for a pair the format takes
	// for no version. formats.Register derives it from the format's optional formats.VersionPather. The decision
	// point decides a version on that path as well as on the package document's, so a path pattern that hides the
	// file hides the version from every listing, every document and the search (R-AUTHZ-005, Q-AUTHZ-255). Nil: the
	// format's versions have no file of their own, and a version is decided on the package document's path alone.
	VersionPath func(name, version string) (path string, ok bool)
	Exposure    Exposure
	// Ecosystem is the OSV ecosystem name of the format's vulnerability adapter (R-VULN-009), "" for a format without
	// one. formats.Register derives it; the repository model reads it to refuse the member `vulnerabilities` where it
	// does not apply (R-VULN-002), and the scanning derives the ecosystems it syncs from the entries that have one
	// (R-VULN-003).
	Ecosystem string
	// ImageEcosystem is the ecosystem of a format whose content the image scanning reads (R-VULN-033), "" for any other: its repositories
	// have the scanning, and nothing matches them by name.
	ImageEcosystem string
	// Mirror is true for a format whose remote adapter can discover the packages of its upstream (the optional mirror
	// interface, repositories-and-proxy.md R-REPO-050, ADR-152). formats.Register derives it; the repository model reads
	// it to refuse remote.mode mirror where it is false.
	Mirror bool

	// settings is the compiled SettingsSchema, filled by Register.
	settings *settingsSchema
}

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
	if e.RegistryRoot {
		for id, other := range r.by {
			if other.RegistryRoot {
				panic(fmt.Sprintf("catalog: %q declares the registry root, which %q already holds (R-OCI-001)", e.ID, id))
			}
		}
	}
	e.Classes = append([]Class(nil), e.Classes...)
	e.settings = compiled
	r.by[e.ID] = e
}

func check(e Entry) error {
	switch {
	case !qname.ValidFormat(string(e.ID)):
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
	case e.RegistryRoot && e.ProtocolBasePath != RegistryRootBasePath:
		return fmt.Errorf("%q declares the registry root and has protocol base path %q, not %q", e.ID, e.ProtocolBasePath, RegistryRootBasePath)
	case e.Exposure.Versions && e.Exposure.Versioning == nil:
		return fmt.Errorf("%q exposes versions and has no versioning", e.ID)
	case !e.Exposure.Exposable && (e.Exposure.Packages || e.Exposure.Versions || e.Exposure.Tags):
		return fmt.Errorf("%q narrows exposure dimensions and is not exposable", e.ID)
	}
	for _, h := range e.DefaultCredentialHosts {
		if !slices.Contains(e.DefaultExtraHosts, h) {
			return fmt.Errorf("%q proposes credentials for %q, which is not among its default extra hosts", e.ID, h)
		}
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
