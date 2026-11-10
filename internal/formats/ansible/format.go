// Package ansible is the Ansible Galaxy format (R-ANS-001 to R-ANS-032): the Galaxy API v3 below /api/ansible/<projectKey>/<repoKey>/
// as far as `ansible-galaxy collection install`, `collection publish`, `role install` and a requirements.yml call it, the grammars of
// collection names and versions, the classification of paths, the route table of all classes, the settings of a repository, the error
// shape of the Galaxy and the data port. One format, `ansible`, serves collections as the stored artifact and roles by translation
// from a remote (Q-ANS-6).
//
// Like every format it imports the format contract and almost nothing else (R-ARCH-008): its tables are reached through the [Store]
// port and its generic package rows through the PackageWriter of the hook. The leaf packages hold the parts that need no host: version
// (semantic versioning and its sort key), collection (the archive reader), galaxydoc (the documents) and rows (the values of the data
// port).
//
// The route table (routes.go) is complete and frozen: every route is declared once with the classes that serve it, and the handler of a
// route is looked up by class and route name (handlers.go), so a class joins the format by adding a class_*.go file that registers
// itself and its handlers, and no existing file changes. A handler that no class file registered answers 501 ansible_not_implemented.
// The local class is class_local.go; the remote and virtual classes register their adapters in class_remote.go and class_virtual.go.
package ansible

import (
	"reflect"
	"slices"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ID is the format name.
const ID formats.ID = "ansible"

// Layout is the name of the one layout of the format (R-ANS-002): an archive lives on its canonical path
// collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz, the stored upstream documents beside it, and nothing else.
const Layout = "ansible"

// DefaultUpstreamURL is the API root a remote repository proposes (R-ANS-001, R-ANS-016).
const DefaultUpstreamURL = "https://galaxy.ansible.com/api/"

// MaxDocumentBytes bounds an upstream document held in memory (R-ANS-031): the largest version document seen is 0.85 MB.
const MaxDocumentBytes = 16 << 20

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
		panic("ansible: the class " + string(c) + " is registered twice")
	}
	classes.by[c] = setup
}

// Format implements formats.Format for the Ansible Galaxy.
type Format struct {
	formats.Base
	classes []formats.Class
	routes  []formats.Route
	remote  formats.RemoteAdapter
	virtual formats.VirtualAdapter
	view    formats.ViewAdapter
	// replicas holds the replica of each Host the composition root bound (bind.go); ports the same replica by the Host's data port, for
	// the hooks and the fill, which meet the format without a Host.
	replicas, ports sync.Map
}

var (
	_ formats.Format        = (*Format)(nil)
	_ formats.VersionPather = (*Format)(nil)
)

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

// Descriptor returns the catalog entry of R-ANS-001 and the contract facts. The facts that only a remote repository reads (the
// upstream, the periods, the extra hosts) are declared from the start, so the catalog answers for the whole format whichever classes
// are registered.
func (f *Format) Descriptor() formats.Descriptor {
	metadataTTL, notFoundTTL := 600, 300
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:               ID,
			DisplayName:      "Ansible Galaxy",
			Classes:          f.Classes(),
			Layout:           Layout,
			ProtocolBasePath: "/api/ansible/{project}/{key}/",
			// The upstream test requests the API root itself, which answers the discovery document: there is no probe path
			// (R-ANS-016). The GitHub hosts serve role archives (R-ANS-020).
			DefaultUpstreamURL:        DefaultUpstreamURL,
			DefaultExtraHosts:         []string{"github.com", "codeload.github.com"},
			DefaultMetadataTTLSeconds: &metadataTTL,
			DefaultNotFoundTTLSeconds: &notFoundTTL,
			// The archive digest is part of the protocol: a remote compares the SHA-256 its version document announces with the
			// bytes it fetched whatever the policy says (R-ANS-018), so the policies ignore and pass-through are refused.
			ChecksumAlwaysVerified: true,
			// Canonical paths are lower case (R-ANS-002), so a path pattern is compared in lower case (Q-AUTHZ-254).
			LowerCasePaths:  true,
			PatternSubject:  formats.SubjectName,
			PackageNames:    true,
			PriorityMembers: true,
			PinsAndClaims:   true,
			// A local repository holds package ownership enforced until its document says otherwise (R-AUTHZ-018).
			OwnershipDefault: catalog.OwnershipEnforce,
			SettingsSchema:   settingsSchema,
		},
		ContractVersion:        formats.ContractVersion,
		Names:                  names{},
		Clients:                clients(),
		PropertyPrefix:         "ansible.",
		MaxDocumentBytes:       MaxDocumentBytes,
		VerifyAnnouncedDigests: true,
		// The bounds of R-ANS-031 that the core's reader enforces are its defaults (10,000 entries, 2 GiB inflated, a ratio of
		// 100:1): a format may lower them and not raise them (Q-ANS-30). The three documents of an archive are read with bounds of
		// their own (internal/formats/ansible/collection).
		Limits:   untrusted.Limits{Ratio: 100},
		DataPort: reflect.TypeFor[Store](),
		// A remote fill stores a document without raising a package row, so it signals the early eviction of merged documents
		// itself (R-REPO-020).
		NotifyAreas: []string{catalog.MetadataChangedArea},
	}
}

// Routes returns the endpoint map of R-ANS-003 to R-ANS-009 and R-ANS-020.
func (f *Format) Routes() []formats.Route { return f.routes }

// Remote returns the remote adapter, nil until the remote class registers (class_remote.go).
func (f *Format) Remote() formats.RemoteAdapter { return f.remote }

// Virtual returns the virtual adapter, nil until the virtual class registers (class_virtual.go).
func (f *Format) Virtual() formats.VirtualAdapter { return f.virtual }

// View returns the package screens' adapter, nil until the local class provides it (R-ANS-026).
func (f *Format) View() formats.ViewAdapter { return f.view }
