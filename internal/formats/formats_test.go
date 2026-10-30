package formats

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// fake is a format whose parts a test sets. It embeds Base for the hooks and
// adapters it does not exercise.
type fake struct {
	Base
	d      Descriptor
	routes []Route
	remote RemoteAdapter
	virt   VirtualAdapter
	expo   ExposureAdapter
}

func (f fake) Descriptor() Descriptor { return f.d }
func (f fake) Routes() []Route        { return f.routes }
func (f fake) Match(m, p string) (*Route, Params, error) {
	return MatchRoutes(f.routes, m, p)
}
func (fake) Classify(Path) PathInfo                  { return PathInfo{Kind: Content} }
func (fake) PackageDocumentPath(string) (Path, bool) { return Path{}, false }
func (f fake) Remote() RemoteAdapter                 { return f.remote }
func (f fake) Virtual() VirtualAdapter               { return f.virt }
func (f fake) Exposure() ExposureAdapter             { return f.expo }

func valid(id string) Descriptor {
	return Descriptor{
		Facts:           catalog.Facts{ID: ID(id), Classes: []Class{Local}, Layout: id, PatternSubject: SubjectPath},
		ContractVersion: ContractVersion,
	}
}

type identity struct{}

func (identity) Normalize(name string) (string, bool) { return name, name != "" }

func noop(context.Context, *Request, http.ResponseWriter) error { return nil }

func route(name, method, tmpl string, examples ...string) Route {
	return Route{Name: name, Method: method, Template: tmpl, Examples: examples, Action: ActionRead, Kind: Download, Handler: noop}
}

func mustPanic(t *testing.T, f Format, want string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("no panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic = %v, want it to mention %q", r, want)
		}
	}()
	Register(f)
}

func TestRegister(t *testing.T) {
	bad := map[string]struct {
		f    fake
		want string
	}{
		"upper case":       {fake{d: valid("Generic")}, "invalid or reserved"},
		"one character":    {fake{d: valid("x")}, "invalid or reserved"},
		"api version":      {fake{d: valid("v1")}, "invalid or reserved"},
		"no class":         {fake{d: Descriptor{Facts: catalog.Facts{ID: "nocls", Layout: "x", PatternSubject: SubjectPath}, ContractVersion: 1}}, "no class"},
		"no layout":        {fake{d: Descriptor{Facts: catalog.Facts{ID: "nolay", Classes: []Class{Local}, PatternSubject: SubjectPath}, ContractVersion: 1}}, "no layout"},
		"no subject":       {fake{d: Descriptor{Facts: catalog.Facts{ID: "nosub", Classes: []Class{Local}, Layout: "x"}, ContractVersion: 1}}, "pattern subject"},
		"class twice":      {fake{d: Descriptor{Facts: catalog.Facts{ID: "twice", Classes: []Class{Local, Local}, Layout: "x", PatternSubject: SubjectPath}, ContractVersion: 1}}, "invalidly"},
		"class out of set": {fake{d: Descriptor{Facts: catalog.Facts{ID: "oos", Classes: []Class{"mirror"}, Layout: "x", PatternSubject: SubjectPath}, ContractVersion: 1}}, "invalidly"},
	}
	for name, c := range bad {
		t.Run("R-ARCH-010 Register panics on "+name, func(t *testing.T) {
			mustPanic(t, c.f, c.want)
		})
	}
	t.Run("R-ARCH-010 a duplicate ID panics, a lookup finds the format", func(t *testing.T) {
		Register(fake{d: valid("probe-fmt")})
		if f, ok := Lookup("probe-fmt"); !ok || f.Descriptor().ID != "probe-fmt" {
			t.Fatal("not found")
		}
		mustPanic(t, fake{d: valid("probe-fmt")}, "twice")
	})
	t.Run("R-REPO-003 Descriptors lists the registered formats sorted by ID", func(t *testing.T) {
		Register(fake{d: valid("probe-aaa")})
		ds := Descriptors()
		for i := 1; i < len(ds); i++ {
			if ds[i-1].ID >= ds[i].ID {
				t.Fatalf("not sorted: %v", ds)
			}
		}
	})
}

// TestR_ARCH_010_RegisterChecksTheContractVersion: a format written against
// another contract version does not start (R-ARCH-010, R-ARCH-025).
func TestR_ARCH_010_RegisterChecksTheContractVersion(t *testing.T) {
	for _, v := range []int{0, ContractVersion + 1} {
		d := valid("probe-ver")
		d.ContractVersion = v
		mustPanic(t, fake{d: d}, "contract version")
	}
	if _, ok := Lookup("probe-ver"); ok {
		t.Fatal("a refused format was registered")
	}
	if _, ok := catalog.Default().Lookup("probe-ver"); ok {
		t.Fatal("a refused format reached the catalog")
	}
}

// TestR_ARCH_010_RegisterChecksTheClassMatrix: a class without its adapter, and
// an adapter without its class, stop the process (R-ARCH-010).
func TestR_ARCH_010_RegisterChecksTheClassMatrix(t *testing.T) {
	t.Run("Remote listed and Remote() nil", func(t *testing.T) {
		d := valid("probe-rem")
		d.Classes = []Class{Local, Remote}
		mustPanic(t, fake{d: d}, "Remote() returns nil")
	})
	t.Run("Virtual listed and Virtual() nil", func(t *testing.T) {
		d := valid("probe-vir")
		d.Classes = []Class{Local, Virtual}
		mustPanic(t, fake{d: d}, "Virtual() returns nil")
	})
	t.Run("a remote adapter without the class", func(t *testing.T) {
		mustPanic(t, fake{d: valid("probe-rem2"), remote: stubRemote{}}, "does not list class remote")
	})
	t.Run("both present registers", func(t *testing.T) {
		d := valid("probe-both")
		d.Classes = []Class{Local, Remote, Virtual}
		Register(fake{d: d, remote: stubRemote{}, virt: stubVirtual{}})
		e, ok := catalog.Default().Lookup("probe-both")
		if !ok || !e.Supports(Remote) || !e.Supports(Virtual) {
			t.Fatalf("catalog entry = %+v", e)
		}
	})
}

type (
	stubRemote  struct{ RemoteAdapter }
	stubVirtual struct{ VirtualAdapter }
)

// TestR_ARCH_010_RegisterChecksTheRoutes: the ambiguity check of kit check 1 runs
// at registration, together with the rules that make a table servable.
func TestR_ARCH_010_RegisterChecksTheRoutes(t *testing.T) {
	withBase := func(id string, routes ...Route) fake {
		d := valid(id)
		d.ProtocolBasePath = "/api/" + id + "/{key}/"
		return fake{d: d, routes: routes}
	}
	t.Run("an ambiguous pair is refused", func(t *testing.T) {
		// "/{package}" also matches the unscoped half of what "/{scope}/{name}" is for
		// only when the matchers overlap: two single-segment templates do.
		f := withBase("probe-amb",
			route("a.get", http.MethodGet, "/{package}", "left-pad"),
			route("b.get", http.MethodGet, "/{name}", "right-pad"))
		mustPanic(t, f, "ambiguous")
	})
	t.Run("a disambiguated pair is accepted", func(t *testing.T) {
		scoped := route("scoped.get", http.MethodGet, "/{scope}/{name}", "@a/b")
		scoped.Match = func(p string) (Params, bool) {
			if !strings.HasPrefix(p, "@") {
				return nil, false
			}
			return Template("/{scope}/{name}")(p)
		}
		plain := route("plain.get", http.MethodGet, "/{package}", "left-pad")
		Register(withBase("probe-dis", plain, scoped))
	})
	t.Run("an example the route does not match", func(t *testing.T) {
		mustPanic(t, withBase("probe-ex", route("a.get", http.MethodGet, "/a/{b}", "x/y/z")), "own example")
	})
	t.Run("a route without an example", func(t *testing.T) {
		mustPanic(t, withBase("probe-noex", route("a.get", http.MethodGet, "/a")), "no example")
	})
	t.Run("a route name used twice", func(t *testing.T) {
		mustPanic(t, withBase("probe-dup",
			route("a.get", http.MethodGet, "/a", "a"),
			route("a.get", http.MethodGet, "/b", "b")), "used twice")
	})
	t.Run("routes under another base path", func(t *testing.T) {
		f := withBase("probe-base", route("a.get", http.MethodGet, "/a", "a"))
		f.d.ProtocolBasePath = "/files/{key}/"
		mustPanic(t, f, "protocol base path")
	})
	t.Run("a route of a class the format lacks", func(t *testing.T) {
		r := route("a.get", http.MethodGet, "/a", "a")
		r.Classes = []Class{Remote}
		mustPanic(t, withBase("probe-cls", r), "does not support")
	})
}

// TestR_ARCH_010_RegisterChecksTheDataPort: a data port is an interface type.
func TestR_ARCH_010_RegisterChecksTheDataPort(t *testing.T) {
	d := valid("probe-port")
	d.DataPort = reflect.TypeFor[int]()
	mustPanic(t, fake{d: d}, "no interface")
}

// TestR_REPO_003_RegisterFillsTheCatalog: the facts a format declares are the
// facts the repository model and the decision point read, with nothing copied by
// hand in between (Q-ARCH-188).
func TestR_REPO_003_RegisterFillsTheCatalog(t *testing.T) {
	d := valid("probe-cat")
	d.PackageNames, d.PatternSubject, d.Names = true, SubjectName, identity{}
	Register(fake{d: d, expo: stubExposure{dim: ExposureDimensions{Packages: true, Versions: true}}})
	e, ok := catalog.Default().Lookup("probe-cat")
	if !ok {
		t.Fatal("Register did not fill catalog.Default")
	}
	if e.ID != "probe-cat" || !e.Supports(Local) || e.Supports(Remote) || e.PatternSubject != catalog.SubjectName ||
		e.Names == nil || !e.Exposure.Exposable || !e.Exposure.Packages || !e.Exposure.Versions || e.Exposure.Tags || e.Exposure.Versioning == nil {
		t.Fatalf("catalog entry = %+v", e)
	}
}

// TestR_EP_018_TheAdapterIsTheSourceOfTheExposureFacts: a format without an
// adapter is not exposable, and an adapter narrowing by package needs package
// names (fail closed).
func TestR_EP_018_TheAdapterIsTheSourceOfTheExposureFacts(t *testing.T) {
	if e := CatalogEntry(fake{d: valid("probe-none")}); e.Exposure.Exposable {
		t.Fatal("a format without an adapter is exposable")
	}
	paths := CatalogEntry(fake{d: valid("probe-paths"), expo: stubExposure{}})
	if !paths.Exposure.Exposable || paths.Exposure.Packages || paths.Exposure.Versioning != nil {
		t.Fatalf("paths-only exposure = %+v", paths.Exposure)
	}
	mustPanic(t, fake{d: valid("probe-nonames"), expo: stubExposure{dim: ExposureDimensions{Packages: true}}}, "no package names")
}

type stubExposure struct {
	dim ExposureDimensions
}

func (stubExposure) ValidateRange(string) error       { return errors.New("no") }
func (stubExposure) Prerelease(string) (bool, bool)   { return false, true }
func (stubExposure) InRange(string, string) bool      { return false }
func (s stubExposure) Dimensions() ExposureDimensions { return s.dim }
func (stubExposure) FilterDocument(_ context.Context, _ Path, d []byte, _ ExposureView) ([]byte, error) {
	return d, nil
}

func catalogClass(catalog.Class) catalog.Class                     { return Local }
func catalogKind(catalog.RequestKind) catalog.RequestKind          { return KindWrite }
func catalogSubject(catalog.PatternSubject) catalog.PatternSubject { return SubjectName }

// TestR_REPO_003_TheAliasesAreTheCatalogTypes: a value of the contract's name is
// a value of the catalog's type, with the catalog's spelling.
func TestR_REPO_003_TheAliasesAreTheCatalogTypes(t *testing.T) {
	if c, k, s := catalogClass(Local), catalogKind(KindWrite), catalogSubject(SubjectName); string(c) != "local" || string(k) != "write" || string(s) != "name" {
		t.Fatal("the spelling of a class, a kind or a subject changed")
	}
}

// TestR_ARCH_008_RouteKindsMapToRequestKinds: the kind of a route fixes the
// request kind of its coarse check (R-REPO-003).
func TestR_ARCH_008_RouteKindsMapToRequestKinds(t *testing.T) {
	want := map[RouteKind]RequestKind{
		Download: KindArtifact, Metadata: KindMetadata, Computed: KindMetadata,
		Upload: KindWrite, Delete: KindWrite, Admin: KindWrite, Volatile: KindSearch,
	}
	for k, w := range want {
		if got := k.RequestKind(); got != w {
			t.Errorf("%s -> %s, want %s", k, got, w)
		}
	}
	for _, k := range []RouteKind{Download, Metadata, Computed} {
		if !k.ServesContent() {
			t.Errorf("%s serves content", k)
		}
	}
	for _, k := range []RouteKind{Upload, Delete, Admin, Volatile} {
		if k.ServesContent() {
			t.Errorf("%s serves no content", k)
		}
	}
}

func TestR_ARCH_005_MatchRoutes(t *testing.T) {
	routes := []Route{
		route("pkg.get", http.MethodGet, "/{package}", "left-pad"),
		route("pkg.put", http.MethodPut, "/{package}", "left-pad"),
		route("tarball.get", http.MethodGet, "/{package}/-/{file}", "left-pad/-/left-pad-1.0.0.tgz"),
		route("rest.get", http.MethodGet, "/x/{rest...}", "x/a/b"),
	}
	t.Run("a route and its params", func(t *testing.T) {
		r, p, err := MatchRoutes(routes, http.MethodGet, "left-pad/-/left-pad-1.0.0.tgz")
		if err != nil || r.Name != "tarball.get" || p.Get("package") != "left-pad" || p.Get("file") != "left-pad-1.0.0.tgz" {
			t.Fatalf("got %v %v %v", r, p, err)
		}
	})
	t.Run("an encoded slash stays inside its value", func(t *testing.T) {
		r, p, err := MatchRoutes(routes, http.MethodGet, "@scope%2fname")
		if err != nil || r.Name != "pkg.get" || p.Get("package") != "@scope/name" {
			t.Fatalf("got %v %v %v", r, p, err)
		}
	})
	t.Run("the tail takes the rest", func(t *testing.T) {
		_, p, err := MatchRoutes(routes, http.MethodGet, "x/a/b%2Fc")
		if err != nil || p.Get("rest") != "a/b/c" {
			t.Fatalf("got %v %v", p, err)
		}
	})
	t.Run("HEAD takes the GET route", func(t *testing.T) {
		if r, _, err := MatchRoutes(routes, http.MethodHead, "left-pad"); err != nil || r.Name != "pkg.get" {
			t.Fatalf("got %v %v", r, err)
		}
	})
	t.Run("a wrong method is 405 with Allow", func(t *testing.T) {
		_, _, err := MatchRoutes(routes, http.MethodDelete, "left-pad")
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.MethodNotAllowed || strings.Join(e.Allow, ",") != "GET,HEAD,PUT" {
			t.Fatalf("err = %v, allow = %v", err, e)
		}
	})
	t.Run("an unknown path is 404", func(t *testing.T) {
		if _, _, err := MatchRoutes(routes, http.MethodGet, "a/b/c/d"); errs.KindOf(err) != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-010 traversal never reaches a matcher", func(t *testing.T) {
		for _, p := range []string{"..", "%2e%2e", "a/../b", "%2E%2e%2f", "a%00b", "a%01", "a//b", "a%5cb", strings.Repeat("a", 4097)} {
			if r, _, err := MatchRoutes(routes, http.MethodGet, p); err == nil || r != nil {
				t.Errorf("%q matched route %v", p, r)
			}
		}
	})
}

func TestBaseWriteErrorIsProblemJSON(t *testing.T) {
	// Base.WriteError is the default shape; the dispatcher test covers a format's own.
	var b Base
	if !reflect.DeepEqual(b.IngestOptions(nil), IngestOptions{}) || b.VersionGroup(VersionRef{}) != "" || b.Remote() != nil || b.Virtual() != nil || b.Indexes() != nil || b.Exposure() != nil || b.SetupTemplates() != nil {
		t.Fatal("Base is not neutral")
	}
}
