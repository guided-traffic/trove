// Package oci is the OCI and Docker registry format (R-OCI-001 to R-OCI-035): the distribution API below
// /v2/<projectKey>/<repoKey>/, the grammars of keys, names, tags and digests, the storage layout of manifests, tags and blobs,
// the settings of a repository and the native error shape. One format, `oci`, serves Docker image manifests and OCI content
// alike (Q-OCI-1). It is the format that declares the registry root: its client address is the root of a host, and the three
// repository-less routes below /v2/ (the version check, the catalog and the token endpoint) belong to the core (Q-OCI-4).
//
// Like every format it imports the format contract and almost nothing else (R-ARCH-008): its tables are reached through the
// [Store] port and its generic package rows through the PackageWriter of the hook. The leaf package rows holds the values of the
// data port.
//
// This is the skeleton the work packages of T-16 fill: the format registers, the conformance kit runs, the routes answer 501
// (routes.go), and the descriptor, the grammars, the layout, the error shape and the settings schema are final. The classes
// remote and virtual join with their adapters.
package oci

import (
	"reflect"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ID is the format name.
const ID formats.ID = "oci"

// Layout is the name of the one layout of the format (R-OCI-007): manifests, tags and blobs live on the canonical paths of its
// storage layout and nowhere else.
const Layout = "oci"

// DefaultUpstreamURL is the upstream a remote repository proposes (R-OCI-001).
const DefaultUpstreamURL = "https://registry-1.docker.io/"

// MaxDocumentBytes bounds a manifest, a tags list or a catalog held in memory (R-OCI-020).
const MaxDocumentBytes = MaxManifestBytes

// Format implements formats.Format for OCI.
type Format struct {
	formats.Base
	routes []formats.Route
}

var _ formats.Format = (*Format)(nil)

// New creates the format.
func New() *Format {
	f := &Format{}
	f.routes = f.buildRoutes()
	return f
}

// Descriptor returns the catalog entry of R-OCI-001 and the contract facts.
//
// The format registers the class local; remote and virtual join with their adapters, which are work packages of their own
// (Q-NUGET-28 did the same). The facts that only a remote repository reads (the upstream, the extra hosts, the periods) are
// declared now, so the catalog answers for the whole format from the start.
func (f *Format) Descriptor() formats.Descriptor {
	metadataTTL, notFoundTTL := 300, 300
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:               ID,
			DisplayName:      "OCI (Docker)",
			Classes:          []formats.Class{formats.Local},
			Layout:           Layout,
			ProtocolBasePath: catalog.RegistryRootBasePath,
			RegistryRoot:     true,
			// Docker Hub's token realm receives the repository's credential (R-OCI-021); the hosts Docker Hub redirects blob
			// downloads to are added by the remote work package once confirmed against Docker Hub.
			DefaultUpstreamURL:        DefaultUpstreamURL,
			DefaultExtraHosts:         []string{"auth.docker.io"},
			DefaultCredentialHosts:    []string{"auth.docker.io"},
			DefaultMetadataTTLSeconds: &metadataTTL,
			DefaultNotFoundTTLSeconds: &notFoundTTL,
			PatternSubject:            formats.SubjectName,
			PackageNames:              true,
			PriorityMembers:           true,
			PinsAndClaims:             true,
			ChecksumAlwaysVerified:    true,
			// Writes pass the ownership check with the image name as the package (R-OCI-006).
			OwnershipDefault: catalog.OwnershipEnforce,
			SettingsSchema:   settingsSchema,
		},
		ContractVersion:  formats.ContractVersion,
		Names:            names{},
		PropertyPrefix:   "oci.",
		MaxDocumentBytes: MaxDocumentBytes,
		// Every digest is the SHA-256 of the bytes it names, so a fill verifies whatever the checksum policy says (R-REPO-041).
		VerifyAnnouncedDigests: true,
		// A manifest is read within 4 MiB, 32 levels of nesting and 100,000 JSON values (R-OCI-020).
		Limits:   untrusted.Limits{JSONDepth: 32, JSONMembers: 100_000},
		DataPort: reflect.TypeFor[Store](),
	}
}

// Routes returns the endpoint map of R-OCI-009 to R-OCI-018.
func (f *Format) Routes() []formats.Route { return f.routes }

// Match resolves a method and the escaped repository-relative path to one route.
func (f *Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(f.routes, method, escapedPath)
}
