// Package nuget is the NuGet v3 format (R-NUGET-001 to R-NUGET-034): the routes below
// /api/nuget/<repoKey>/v3/, the classification of paths, the settings of a repository, the
// native error shape, the service index, and the write side: the push, the unlist, the relist
// and the delete of a version, the tombstones that keep a removed number from returning, and
// the serve check (handlers_publish.go, handlers_delete.go, hooks.go, hooks_remove.go,
// servecheck.go). The operations behind the other routes - package content, registrations,
// search and symbols - are written by the work packages that own them; until then their
// handlers (handlers_stub.go) answer 501 nuget_not_implemented, so the format registers, the
// conformance kit runs and no route is missing from the table (WP3.1c).
//
// Like every format it imports the format contract and almost nothing else (R-ARCH-008): its
// tables are reached through the [Store] port, its generic package rows through the
// PackageWriter of the hook, and the core through Host. The leaf packages hold the parts that
// need no host: id and version (R-NUGET-003), nuspec and nupkg (R-NUGET-005) and rows (the
// values of the data port).
//
// Two kinds of path meet here, and paths.go says which is which: the protocol path a client
// requests ("v3/flatcontainer/…") and the canonical path of an item ("{lower_id}/…").
package nuget

import (
	"context"
	"reflect"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ID is the format name.
const ID formats.ID = "nuget"

// Layout is the name of the one layout of the format (R-NUGET-005): a package lives on the
// canonical paths of its storage layout and nowhere else.
const Layout = "nuget"

// DefaultUpstreamURL is the service index of the upstream a remote repository proposes
// (R-NUGET-014).
const DefaultUpstreamURL = "https://api.nuget.org/v3/index.json"

// MaxDocumentBytes bounds an upstream document held in memory: a registration page of the
// largest package of nuget.org is a few megabytes, so this leaves a wide margin (R-ARCH-028).
const MaxDocumentBytes = 128 << 20

// Format implements formats.Format for NuGet.
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
	f := &Format{exposure: newExposure()}
	f.routes = f.buildRoutes()
	return f
}

// Descriptor returns the catalog entry of R-NUGET-001 and the contract facts.
//
// The format registers the classes whose adapters exist: local and virtual (Q-NUGET-28,
// Q-NUGET-56); remote joins with its adapter, which is a work package of its own. The facts that only a remote repository
// reads (the upstream, the periods, the checksum rule) are declared now, so the catalog
// answers for the whole format from the start.
func (f *Format) Descriptor() formats.Descriptor {
	metadataTTL, notFoundTTL := 300, 1800
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:               ID,
			DisplayName:      "NuGet",
			Classes:          []formats.Class{formats.Local, formats.Remote, formats.Virtual},
			Layout:           Layout,
			ProtocolBasePath: "/api/nuget/{project}/{key}/",
			// The upstream a remote repository proposes and the host family nuget.org serves
			// search and symbols from (R-NUGET-014). The upstream test requests the service
			// index itself, so there is no probe path (R-REPO-024).
			DefaultUpstreamURL: DefaultUpstreamURL,
			DefaultExtraHosts:  []string{"*.nuget.org"},
			// 300 s for the service index, version lists, registrations and vulnerability
			// data; the not-found period stays the generic 1800 s (Q-NUGET-2).
			DefaultMetadataTTLSeconds: &metadataTTL,
			DefaultNotFoundTTLSeconds: &notFoundTTL,
			// NuGet v3 announces no content digest, so checksumPolicy: require could only
			// refuse every package (R-NUGET-015, Q-ARCH-269).
			ChecksumRequireUnsupported: true,
			PatternSubject:             formats.SubjectName,
			PackageNames:               true,
			PriorityMembers:            true,
			PinsAndClaims:              true,
			// A local repository of NuGet holds package ownership enforced until its
			// document says otherwise (R-AUTHZ-018, Q-AUTHZ-2).
			OwnershipDefault: catalog.OwnershipEnforce,
			SettingsSchema:   settingsSchema,
		},
		ContractVersion:  formats.ContractVersion,
		Names:            names{},
		Clients:          clients(),
		PropertyPrefix:   "nuget.",
		MaxDocumentBytes: MaxDocumentBytes,
		// The bounds R-NUGET-005 and R-ARCH-028 give a .nuspec: 1 MiB and a compression ratio
		// of 100:1. The entry count and the declared sizes of the archive are the
		// nuget.archive settings, which the package reader enforces itself (Q-NUGET-23).
		Limits:   untrusted.Limits{ManifestEntryBytes: 1 << 20, Ratio: 100},
		DataPort: reflect.TypeFor[Store](),
		// A remote fill replaces a stored document without raising a package row, so it
		// signals the early eviction of merged documents itself (R-REPO-020); every other
		// change of a package is signalled by the writer (Q-ARCH-259).
		NotifyAreas: []string{catalog.MetadataChangedArea},
	}
}

// Routes returns the endpoint map of R-NUGET-001.
func (f *Format) Routes() []formats.Route { return f.routes }

// Exposure returns the format's exposure adapter (R-NUGET-034, R-EP-018): NuGet repositories
// can be served on an endpoint with exposure rules.
func (f *Format) Exposure() formats.ExposureAdapter { return f.exposure }

// Inspect reads the identity of a package that enters the repository around the push route
// (FMT-008, R-NUGET-005 steps 1 to 4): the archive within the limits, the one .nuspec, the
// ID by the rule of a local repository, the version, the dependencies, and the refusal of a
// symbol package. The limits of the call tighten the bounds that are read into memory; the
// entry count and the declared sizes are the defaults of nuget.archive, because Inspect
// meets the package without its replica (the push route applies the settings).
//
// What the archive rules refuse as ambiguous or forbidden is content trove does not read,
// 422 content_refused, as for npm; the push answers 400 for the same rules (R-NUGET-005).
// A path that names another ID or version than the package declares is refused too.
func (f *Format) Inspect(ctx context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	got, err := readIncoming(ctx, in.Body, nupkg.Limits{
		MaxRatio: in.Limits.Ratio, MaxNuspecBytes: in.Limits.ManifestEntryBytes, Deadline: in.Limits.Deadline,
	})
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.Invalid {
			return nil, errs.E(errs.Unprocessable, errs.CodeContentRefused, e.Detail, errs.WithCause(err))
		}
		return nil, err
	}
	if info := f.Classify(in.Path); info.Kind == formats.Content && info.Package != "" &&
		(info.Package != got.lowerID || (info.Version != "" && info.Version != got.lowerVersion)) {
		return nil, errs.E(errs.Unprocessable, errs.CodeContentRefused,
			"The path names "+info.Package+" "+info.Version+", the package declares "+got.lowerID+" "+got.lowerVersion+".")
	}
	return &formats.Inspection{
		Package: got.lowerID, Version: got.lowerVersion,
		Properties: map[string]string{"nuget.id": got.lowerID, "nuget.version": got.lowerVersion},
		Payload:    got,
	}, nil
}
