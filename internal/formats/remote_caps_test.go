package formats

import (
	"context"
	"testing"
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
