// Package deb is the Debian (APT) format (R-DEB-001 to R-DEB-035): the repository layout below /api/deb/<projectKey>/<repoKey>/, the
// grammars of packages, versions, architectures, suites and components, the classification of paths, the settings of a repository
// and the error shape. One format, `deb`, serves Debian, Ubuntu and every archive in the Debian repository format; its clients are
// apt, apt-get and every tool that reads an APT source (Q-DEB-1).
//
// Like every format it imports the format contract and almost nothing else (R-ARCH-008): its tables are reached through the [Store]
// port and its generic package rows through the PackageWriter of the hook. The leaf packages hold the parts that need no host: rows
// (the values of the data port) and, as the work packages land, control (deb822), debfile (the package archive), version (dpkg
// order) and index (the renderers).
//
// This is the skeleton the work packages of T-17 fill (docs/projektplan/apt-work-packages.md, 2.5). It registers, the conformance
// kit runs, the routes of every class are declared (routes.go) and answer 501 deb_not_implemented until a work package registers
// the handler of a route by its name (handlers.go), and the descriptor, the grammars, the layout, the classification, the error shape
// and the settings schema are final. A class joins the format when its class_*.go file registers it: the local class in
// class_local.go, the remote and virtual classes with their adapters (class_remote.go, class_virtual.go).
package deb

import (
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ID is the format name.
const ID formats.ID = "deb"

// Layout is the name of the one layout of the format (R-DEB-008): a package file lives on its canonical pool path, the generated
// files below dists/, and nothing else.
const Layout = "deb"

// DefaultUpstreamURL is the upstream a remote repository proposes (R-DEB-001).
const DefaultUpstreamURL = "https://deb.debian.org/debian/"

// MaxDocumentBytes bounds an upstream document held in memory: a release document is at most 16 MiB (R-DEB-034) and the largest
// index a remote stores as a metadata document is a compressed Contents file of a few tens of MiB; the parse of a Packages index
// reads a stream (R-DEB-023) and is not bound by this.
const MaxDocumentBytes = 128 << 20

// classes holds the classes that class_*.go files registered, with the setup that gives the format what the class needs (its
// adapter). A file registers its class in init, so a class joins the format by adding a file and no existing file changes.
var classes = struct {
	mu sync.Mutex
	by map[formats.Class]func(*Format)
}{by: map[formats.Class]func(*Format){}}

// registerClass registers a class of the format; setup may be nil. Registering a class twice panics.
func registerClass(c formats.Class, setup func(*Format)) {
	classes.mu.Lock()
	defer classes.mu.Unlock()
	if _, dup := classes.by[c]; dup {
		panic("deb: the class " + string(c) + " is registered twice")
	}
	classes.by[c] = setup
}

// Format implements formats.Format for Debian.
type Format struct {
	formats.Base
	classes []formats.Class
	routes  []formats.Route
	remote  formats.RemoteAdapter
	virtual formats.VirtualAdapter
	view    formats.ViewAdapter

	// replicas and ports bind the format to the Hosts and data ports a process built (Bind): the metrics of the remote class.
	replicas sync.Map
	ports    sync.Map
	// port is the data port bound last, for the parts of a fill that have no Host.
	port atomic.Pointer[portBox]
}

var _ formats.Format = (*Format)(nil)

// New creates the format with the classes registered by the class_*.go files, in the order local, remote, virtual.
func New() *Format {
	f := &Format{}
	classes.mu.Lock()
	for _, c := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		setup, ok := classes.by[c]
		if !ok {
			continue
		}
		f.classes = append(f.classes, c)
		if setup != nil {
			setup(f)
		}
	}
	classes.mu.Unlock()
	f.routes = f.buildRoutes()
	return f
}

// Classes returns the classes the format registers.
func (f *Format) Classes() []formats.Class { return slices.Clone(f.classes) }

// Descriptor returns the catalog entry of R-DEB-001 and the contract facts. The facts that only a remote repository reads (the
// upstream, the periods) are declared from the start, so the catalog answers for the whole format whichever classes are registered.
func (f *Format) Descriptor() formats.Descriptor {
	metadataTTL, notFoundTTL := 600, 300
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:               ID,
			DisplayName:      "Debian (APT)",
			Classes:          f.Classes(),
			Layout:           Layout,
			ProtocolBasePath: "/api/deb/{project}/{key}/",
			// The upstream test requests the upstream URL itself: there is no probe path (R-DEB-001, R-REPO-024).
			DefaultUpstreamURL:        DefaultUpstreamURL,
			DefaultMetadataTTLSeconds: &metadataTTL,
			DefaultNotFoundTTLSeconds: &notFoundTTL,
			PatternSubject:            formats.SubjectName,
			PackageNames:              true,
			PriorityMembers:           true,
			PinsAndClaims:             true,
			// Writes pass the ownership check with the binary package name as the package (R-DEB-004).
			OwnershipDefault: catalog.OwnershipEnforce,
			SettingsSchema:   settingsSchema,
		},
		ContractVersion:  formats.ContractVersion,
		Names:            names{},
		Clients:          clients(),
		PropertyPrefix:   "deb.",
		MaxDocumentBytes: MaxDocumentBytes,
		// A by-hash digest is verified because it addresses the file (R-DEB-021) and a pool file when a parsed index names its digest
		// (R-DEB-022): the format's own gate announces them, not the checksum policy's blanket rule (R-DEB-001).
		VerifyAnnouncedDigests: false,
		// The bounds of R-DEB-034 on a control archive: 1,000 entries, 64 MiB decompressed, a ratio of 100:1 and a control file of
		// at most 1 MiB. The ar archive's own bounds are the reader's (untrusted.NewAr).
		Limits:   untrusted.Limits{Entries: 1000, UncompressedBytes: 64 << 20, Ratio: 100, ManifestEntryBytes: 1 << 20},
		DataPort: reflect.TypeFor[Store](),
		// A remote fill stores a release document without raising a package row, so it signals the early eviction of merged
		// documents itself (R-REPO-020).
		NotifyAreas: []string{catalog.MetadataChangedArea},
	}
}

// Routes returns the endpoint map of R-DEB-003.
func (f *Format) Routes() []formats.Route { return f.routes }

// Remote returns the remote adapter, nil until the remote class registers (class_remote.go).
func (f *Format) Remote() formats.RemoteAdapter { return f.remote }

// Virtual returns the virtual adapter, nil until the virtual class registers (class_virtual.go).
func (f *Format) Virtual() formats.VirtualAdapter { return f.virtual }

// View returns the package screens' adapter, nil until the local class provides it (R-DEB-030).
func (f *Format) View() formats.ViewAdapter { return f.view }
