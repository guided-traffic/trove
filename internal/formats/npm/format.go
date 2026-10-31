// Package npm is the npm registry format (R-NPM-001 to R-NPM-048): the routes
// below /api/npm/<repoKey>/, the classification of paths, the storage hooks, the
// publish of a version and the native error shape, for the three classes. The local
// repository's protocol and its publish, the documents, the tarball downloads, the
// dist-tags, unpublish and the identity endpoints are in the handler files that
// render them (routes.go lists every handler); the remote class is the adapter of
// remote.go with the handlers of handlers_remote.go, the virtual class the adapter
// of virtual.go with handlers_virtual.go.
//
// Like every format it imports the format contract and almost nothing else
// (R-ARCH-008): its tables are reached through the [Store] port, its generic
// package rows through the PackageWriter of the hook, and the core through
// Host. The leaf packages hold the parts that need no host: name (R-NPM-002),
// semver, manifest and tarball (R-NPM-007), publish (the streaming reader of the
// request body) and rows (the values of the data port).
package npm

import (
	"reflect"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ID is the format name.
const ID formats.ID = "npm"

// Layout is the name of the one layout of the format (R-NPM-006): a tarball lives
// on its canonical path and nowhere else.
const Layout = "npm"

// MaxDocumentBytes bounds a package document held in memory: four times the 31.5 MB
// document of the largest package measured in Q-NPM-5 (plan D-8).
const MaxDocumentBytes = 128 << 20

// Format implements formats.Format for npm.
type Format struct {
	formats.Base
	routes   []formats.Route
	replicas replicas
	ports    replicas
	exposure *exposure
}

var _ formats.Format = (*Format)(nil)

// New creates the format.
func New() *Format {
	f := &Format{}
	f.routes = f.buildRoutes()
	f.exposure = newExposure()
	return f
}

// Descriptor returns the catalog entry of R-REPO-003 and the contract facts.
func (f *Format) Descriptor() formats.Descriptor {
	metadataTTL, notFoundTTL := 7200, 1800
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:               ID,
			DisplayName:      "npm",
			Classes:          []formats.Class{formats.Local, formats.Remote, formats.Virtual},
			Layout:           Layout,
			ProtocolBasePath: "/api/npm/{key}/",
			// The upstream registry the remote class proposes (R-NPM-023); the probe of
			// the upstream test is its ping endpoint (R-REPO-024).
			DefaultUpstreamURL:        "https://registry.npmjs.org/",
			ProbePath:                 "-/ping",
			DefaultMetadataTTLSeconds: &metadataTTL,
			DefaultNotFoundTTLSeconds: &notFoundTTL,
			PatternSubject:            formats.SubjectName,
			PackageNames:              true,
			PriorityMembers:           true,
			PinsAndClaims:             true,
			// A local repository of npm holds package ownership enforced until its
			// document says otherwise (R-AUTHZ-018, Q-AUTHZ-2).
			OwnershipDefault: catalog.OwnershipEnforce,
			SettingsSchema:   settingsSchema,
		},
		ContractVersion:  formats.ContractVersion,
		Names:            names{},
		Clients:          clients(),
		PropertyPrefix:   "npm.",
		MaxDocumentBytes: MaxDocumentBytes,
		// The bounds R-NPM-007 and R-ARCH-028 give a package.json: 1 MiB, nesting depth
		// 64, compression ratio 100:1. The entry count and the unpacked size of a
		// publish are npm.publish.max_tar_entries and max_unpacked_size, which are above
		// the defaults of an inspection, so they are not tightened here.
		Limits:   untrusted.Limits{ManifestEntryBytes: 1 << 20, JSONDepth: 64, Ratio: 100},
		DataPort: reflect.TypeFor[Store](),
	}
}

// names is the package-name rule of R-AUTHZ-005 for npm: a name is exact and case
// sensitive, so normalizing is validating (name.Parse); a pattern that holds
// characters no name has stays as it was written.
type names struct{}

func (names) Normalize(s string) (string, bool) {
	n, err := name.Parse(s)
	if err != nil {
		return "", false
	}
	return n.String(), true
}

// Routes returns the endpoint map of R-NPM-001.
func (f *Format) Routes() []formats.Route { return f.routes }

// Match resolves the escaped path to a route. The package name the matchers found
// is decoded here, exactly once, and validated as a name to look up (R-NPM-002):
// a malformed name never reaches a handler, whichever route it is for.
func (f *Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	r, p, err := formats.MatchRoutes(f.routes, method, escapedPath)
	if err != nil {
		return nil, nil, err
	}
	if raw, ok := p["name"]; ok {
		n, err := name.ParseEscaped(raw)
		if err != nil {
			return nil, nil, err
		}
		p["name"] = n.String()
	}
	return r, p, nil
}

// Classify is the request descriptor of R-NPM-003 for a canonical path. A path it
// cannot classify is metadata, which costs a refetch and never freezes a package
// list (R-REPO-003).
func (f *Format) Classify(p formats.Path) formats.PathInfo {
	if p.Path == "" {
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	segs := strings.Split(p.Path, "/")
	if segs[0] == "-" { // a protocol operation
		if isAudit(p.Path) { // forwarded and never cached (R-NPM-038)
			return formats.PathInfo{Kind: formats.PathVolatile}
		}
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	text, rest := segs[0], segs[1:]
	if strings.HasPrefix(text, "@") && len(segs) > 1 {
		text, rest = segs[0]+"/"+segs[1], segs[2:]
	}
	n, err := name.Parse(text)
	if err != nil {
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	info := formats.PathInfo{Kind: formats.PathMetadata, Package: n.String()}
	switch {
	case len(rest) == 1: // a version or a dist-tag
		if v, err := semver.Parse(rest[0]); err == nil && v.Canonical() == rest[0] {
			info.Version = rest[0]
		}
	case len(rest) == 2 && rest[0] == "-": // a tarball or a provenance bundle
		info.Kind = formats.Content
		for _, suffix := range []string{tarballSuffix, sigstoreSuffix} {
			if v, ok := VersionOfFile(n, rest[1], suffix); ok {
				info.Version = v
				break
			}
		}
	case len(rest) > 1:
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	return info
}

// PackageDocumentPath is the package document "n" of R-NPM-003: its canonical path
// is the normalized package name.
func (f *Format) PackageDocumentPath(pkg string) (formats.Path, bool) {
	n, err := name.Parse(pkg)
	if err != nil {
		return formats.Path{}, false
	}
	return formats.Path{Path: n.String()}, true
}

// clients are the native clients of the acceptance matrix (R-NPM-045).
func clients() []formats.Client {
	return []formats.Client{
		{ID: "npm", DisplayName: "npm"},
		{ID: "pnpm", DisplayName: "pnpm"},
		{ID: "yarn", DisplayName: "Yarn"},
		{ID: "yarn-classic", DisplayName: "Yarn Classic"},
	}
}
