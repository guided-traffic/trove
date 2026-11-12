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
// The format registers the classes local, remote and virtual: the local handlers (manifests, blobs, uploads and the mount, the
// tags list, referrers, deletes), the remote adapter with its token handshake, revalidation and mirror lister, and the virtual
// adapter that walks the members. docs/developer/oci.md is the contributor's map of the package.
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

// upstreamPresets are the four public registries the owner named (R-OCI-001, R-REPO-062, Q-REPO-321), Docker Hub the default.
// Docker Hub's and GitLab's token services live on a host of their own, which receives the repository's credential (R-OCI-021);
// Quay's and ghcr.io's are on the registry's origin. The host Docker Hub redirects blob downloads to
// (production.cloudfront.docker.com, seen on 2026-11-08 with an anonymous request) is not listed: a redirect is followed whatever
// extraHosts says and carries no credential (R-REPO-039), so listing it would only widen the hosts a realm may name (Q-OCI-125).
func upstreamPresets() []catalog.UpstreamPreset {
	return []catalog.UpstreamPreset{
		{
			ID: "docker-hub", DisplayName: "Docker Hub", URL: "https://registry-1.docker.io/", Default: true,
			ExtraHosts: []catalog.PresetHost{{Pattern: "auth.docker.io", SendCredentials: true}},
		},
		{ID: "quay", DisplayName: "Quay", URL: "https://quay.io/"},
		{ID: "ghcr", DisplayName: "GitHub (ghcr.io)", URL: "https://ghcr.io/"},
		{
			ID: "gitlab", DisplayName: "GitLab", URL: "https://registry.gitlab.com/",
			ExtraHosts: []catalog.PresetHost{{Pattern: "gitlab.com", SendCredentials: true}},
		},
	}
}

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
// The format registers the classes local, remote and virtual. The facts that only a remote repository reads (the upstream, the extra
// hosts, the periods) were declared from the start, so the catalog answers for the whole format.
func (f *Format) Descriptor() formats.Descriptor {
	metadataTTL, notFoundTTL := 300, 300
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:               ID,
			DisplayName:      "OCI (Docker)",
			Classes:          []formats.Class{formats.Local, formats.Remote, formats.Virtual},
			Layout:           Layout,
			ProtocolBasePath: catalog.RegistryRootBasePath,
			RegistryRoot:     true,
			UpstreamPresets:  upstreamPresets(),
			// The upstream test requests /v2/, the version check of the distribution API; a registry answers it 401 with a Bearer
			// challenge, which reads as reachable (R-REPO-024).
			ProbePath:                 "v2/",
			ProbeChallengeReachable:   true,
			DefaultMetadataTTLSeconds: &metadataTTL,
			DefaultNotFoundTTLSeconds: &notFoundTTL,
			ChecksumAlwaysVerified:    true,
			PatternSubject:            formats.SubjectName,
			PackageNames:              true,
			PriorityMembers:           true,
			PinsAndClaims:             true,
			// Writes pass the ownership check with the image name as the package (R-OCI-006).
			OwnershipDefault: catalog.OwnershipEnforce,
			SettingsSchema:   settingsSchema,
		},
		ContractVersion:  formats.ContractVersion,
		Names:            names{},
		PropertyPrefix:   "oci.",
		Clients:          clients(),
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
