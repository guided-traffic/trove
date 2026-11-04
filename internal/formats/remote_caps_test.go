package formats

import (
	"context"
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// discoveringRemote has the two optional interfaces of a remote adapter.
type discoveringRemote struct{ stubRemote }

func (discoveringRemote) DiscoveryDocument() (Path, string, bool) {
	return Path{Path: ".service/index.json"}, "", true
}

func (discoveringRemote) ParseDiscovery(context.Context, RemoteContext, []byte) (any, error) {
	return "parsed", nil
}

func (discoveringRemote) InspectsFill(p Path) bool { return p.Path == "a.pkg" }

func (discoveringRemote) InspectFill(context.Context, RemoteContext, Path, Spooled) (*Inspection, error) {
	return &Inspection{Package: "a", Version: "1"}, nil
}

// TestR_ARCH_008_RegisterReadsTheOptionalInterfacesOfARemoteAdapter: the fill hook and the discovery
// document are optional interfaces of the remote adapter, found once at Register (WP3.0a, WP3.0b); an
// adapter that has neither, and a format that is not registered, have none.
func TestR_ARCH_008_RegisterReadsTheOptionalInterfacesOfARemoteAdapter(t *testing.T) {
	remote := func(id string) Descriptor {
		d := valid(id)
		d.Classes = []Class{Local, Remote}
		return d
	}
	Register(fake{d: remote("probe-caps"), remote: discoveringRemote{}})
	Register(fake{d: remote("probe-nocaps"), remote: stubRemote{}})
	Register(fake{d: valid("probe-local")})

	got := RemoteCapabilities("probe-caps")
	if got.Discovery == nil || got.Fill == nil {
		t.Fatalf("capabilities = %+v, want both", got)
	}
	if p, _, ok := got.Discovery.DiscoveryDocument(); !ok || p.Path != ".service/index.json" {
		t.Errorf("DiscoveryDocument = %v, %v", p, ok)
	}
	if !got.Fill.InspectsFill(Path{Path: "a.pkg"}) || got.Fill.InspectsFill(Path{Path: "a.nuspec"}) {
		t.Error("InspectsFill does not follow the path")
	}
	for _, id := range []ID{"probe-nocaps", "probe-local", "probe-unregistered"} {
		if c := RemoteCapabilities(id); c.Discovery != nil || c.Fill != nil {
			t.Errorf("%s: capabilities = %+v, want none", id, c)
		}
	}
}

// listingRemote is a remote adapter with the mirror's optional interface (ADR-152).
type listingRemote struct{ stubRemote }

func (listingRemote) MirrorMethods(context.Context, discover.Upstream) ([]discover.Method, error) {
	return []discover.Method{discover.MethodNames}, nil
}

func (listingRemote) MirrorNames(context.Context, discover.Upstream, discover.PageRequest) (discover.Page, error) {
	return discover.Page{Done: true}, nil
}

func (listingRemote) MirrorPlan(_ context.Context, _ discover.Upstream, name string) (discover.Plan, error) {
	return discover.Plan{Name: name}, nil
}

func (listingRemote) MirrorHosts(*url.URL) []string { return nil }

// TestRegisterRecordsTheMirrorLister: the mirror's discovery is an optional interface of the remote adapter,
// found once at Register; the catalog fact Mirror follows it, so the repository model refuses mode mirror for a format
// without it (ADR-152).
// Contract test of R-REPO-050 (the mirror mode, not built yet): the IDs stay out of the test names, so the
// coverage evidence does not count a requirement whose engine is missing (Q-ROAD-5).
func TestRegisterRecordsTheMirrorLister(t *testing.T) {
	remote := func(id string) Descriptor {
		d := valid(id)
		d.Classes = []Class{Local, Remote}
		return d
	}
	with := fake{d: remote("probe-mirror"), remote: listingRemote{}}
	without := fake{d: remote("probe-nomirror"), remote: stubRemote{}}
	Register(with)
	Register(without)
	if RemoteCapabilities("probe-mirror").Mirror == nil {
		t.Fatal("the lister was not recorded")
	}
	if RemoteCapabilities("probe-nomirror").Mirror != nil || RemoteCapabilities("probe-local").Mirror != nil {
		t.Fatal("a lister was recorded for an adapter without one")
	}
	if !CatalogEntry(with).Mirror || CatalogEntry(without).Mirror {
		t.Fatalf("catalog fact Mirror: with %v, without %v", CatalogEntry(with).Mirror, CatalogEntry(without).Mirror)
	}
}
