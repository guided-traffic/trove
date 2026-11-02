package nuget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

const upstreamIndex = `{"version":"3.0.0","resources":[
 {"@id":"https://api.up.test/v3/reg-gz/","@type":"RegistrationsBaseUrl/3.6.0"},
 {"@id":"https://content.up.test/flat/","@type":"PackageBaseAddress/3.0.0"},
 {"@id":"https://content.up.test/query","@type":"SearchQueryService/3.5.0"}]}`

func testDiscovery(t testing.TB) formats.RemoteContext {
	t.Helper()
	d, err := remote.ParseDiscovery([]byte(upstreamIndex))
	if err != nil {
		t.Fatal(err)
	}
	up, _ := url.Parse("https://api.up.test/v3/index.json")
	return formats.RemoteContext{Upstream: up, Discovery: d}
}

func TestR_NUGET_015_ACanonicalPathIsTakenApartOrIsNoResourceOfARemote(t *testing.T) {
	for _, tc := range []struct {
		path string
		want target
	}{
		{".nuget/index.json", target{kind: kindDiscovery}},
		{"acme.widgets/index.json", target{kind: kindVersions, id: "acme.widgets"}},
		{"acme.widgets/.registration/index.json", target{kind: kindRegIndex, id: "acme.widgets"}},
		{"acme.widgets/.registration/page/1.0.0/2.0.0-beta.1.json", target{kind: kindRegPage, id: "acme.widgets", lower: "1.0.0", upper: "2.0.0-beta.1"}},
		{"acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", target{kind: kindPackage, id: "acme.widgets", version: "1.2.3"}},
		{"acme.widgets/1.2.3/acme.widgets.nuspec", target{kind: kindSpec, id: "acme.widgets", version: "1.2.3"}},
		{"Ünïcode.pkg/1.0.0/Ünïcode.pkg.1.0.0.nupkg", target{kind: kindPackage, id: "Ünïcode.pkg", version: "1.0.0"}},
	} {
		got, err := parseTarget(formats.Path{Path: tc.path})
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v %v, want %+v", tc.path, got, err, tc.want)
		}
	}
	for _, p := range []string{
		"", "acme.widgets", "Acme.Widgets/index.json", "acme.widgets/1.2.3", "acme.widgets/1.2.3.0/acme.widgets.1.2.3.0.nupkg",
		"acme.widgets/1.2.3/other.1.2.3.nupkg", "acme.widgets/1.2.3/acme.widgets.1.2.3.snupkg", "acme.widgets/.registration/page/1.0.0.json",
		"acme.widgets/.registration/page/1.0/2.0.json", "acme.widgets/.registration/page/x/y.json", ".symbols/a.pdb/0/a.pdb",
		".vulnerabilities/index.json", "acme.widgets/.embedded/icon", "acme.widgets/1.2.3/acme.widgets.nupkg/extra",
	} {
		if got, err := parseTarget(formats.Path{Path: p}); err == nil {
			t.Errorf("%q is a resource of a remote: %+v", p, got)
		}
	}
}

func TestR_NUGET_015_TheUpstreamRequestComesFromTheServiceIndexAndThePath(t *testing.T) {
	rc := testDiscovery(t)
	a := New().Remote()
	for path, want := range map[string]string{
		".nuget/index.json":                                     "https://api.up.test/v3/index.json",
		"acme.widgets/index.json":                               "https://content.up.test/flat/acme.widgets/index.json",
		"acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg":           "https://content.up.test/flat/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg",
		"acme.widgets/1.2.3/acme.widgets.nuspec":                "https://content.up.test/flat/acme.widgets/1.2.3/acme.widgets.nuspec",
		"acme.widgets/.registration/index.json":                 "https://api.up.test/v3/reg-gz/acme.widgets/index.json",
		"acme.widgets/.registration/page/1.0.0/2.0.0-rc.1.json": "https://api.up.test/v3/reg-gz/acme.widgets/page/1.0.0/2.0.0-rc.1.json",
		"Ünïcode.pkg/1.0.0/Ünïcode.pkg.1.0.0.nupkg":             "https://content.up.test/flat/%C3%9Cn%C3%AFcode.pkg/1.0.0/%C3%9Cn%C3%AFcode.pkg.1.0.0.nupkg",
	} {
		req, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: path}, documentVariant)
		if err != nil || req.URL.String() != want || req.Method != http.MethodGet {
			t.Errorf("%s: %v %v, want %s", path, req, err, want)
		}
	}
	// A client's own host never decides: the request holds what the service index announced.
	req, _ := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: "acme.widgets/index.json"}, documentVariant)
	if req.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", req.Header.Get("Accept"))
	}
	// Without the service index nothing but the index itself can be addressed.
	bare := formats.RemoteContext{Upstream: rc.Upstream}
	if _, err := a.UpstreamRequest(context.Background(), bare, formats.Path{Path: "acme.widgets/index.json"}, documentVariant); err == nil {
		t.Error("an upstream address was built without the service index")
	}
	if req, err := a.UpstreamRequest(context.Background(), bare, formats.Path{Path: ".nuget/index.json"}, documentVariant); err != nil || req.URL.String() != "https://api.up.test/v3/index.json" {
		t.Errorf("service index request: %v %v", req, err)
	}
	if _, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: ".symbols/a.pdb/1/a.pdb"}, ""); err == nil {
		t.Error("a symbol file was mapped before symbols exist")
	}
}

func TestR_NUGET_016_TheQuerySeamMapsAPassThroughSearchWhenTheIndexIsResolved(t *testing.T) {
	a := New().Remote()
	q := formats.Query{Path: formats.Path{Path: "v3/search"}, Params: url.Values{"q": {"json"}, "take": {"5"}}}
	req, err := a.UpstreamQuery(context.Background(), testDiscovery(t), q)
	if err != nil || req.URL.String() != "https://content.up.test/query?q=json&take=5" {
		t.Fatalf("%v %v", req, err)
	}
	if _, err := a.UpstreamQuery(context.Background(), formats.RemoteContext{}, q); err == nil {
		t.Error("a query was mapped without the service index")
	}
	d, _ := remote.ParseDiscovery([]byte(`{"resources":[{"@id":"https://h.test/c/","@type":"PackageBaseAddress/3.0.0"},{"@id":"https://h.test/r/","@type":"RegistrationsBaseUrl"}]}`))
	_, err = a.UpstreamQuery(context.Background(), formats.RemoteContext{Discovery: d}, q)
	if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
		t.Errorf("an upstream without a search resource: %v", err)
	}
}

func TestR_NUGET_015_PrepareFillRefusesADocumentThatIsNotWhatItsPathPromises(t *testing.T) {
	rc := testDiscovery(t)
	a := New().Remote()
	fill := func(path, doc string) (formats.Prepared, error) {
		return a.PrepareFill(context.Background(), rc, formats.Path{Path: path}, documentVariant, strings.NewReader(doc), int64(len(doc)))
	}
	for path, doc := range map[string]string{
		"acme.widgets/index.json":                          `<html>captive portal</html>`,
		"acme.widgets/.registration/index.json":            `{"items":[{"@id":"x"}]}`,
		"acme.widgets/.registration/page/1.0.0/2.0.0.json": `{"items":null}`,
	} {
		_, err := fill(path, doc)
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeNuGetInvalidUpstreamDocument || e.Kind != errs.UpstreamFailed {
			t.Errorf("%s: %v, want 502 nuget_invalid_upstream_document", path, err)
		}
	}
	if _, err := fill(".nuget/index.json", `{"resources":[]}`); err == nil {
		t.Error("an invalid service index was accepted")
	}
	if _, err := fill("acme.widgets/index.json", `{"versions":["1.0.0"]}`); err != nil {
		t.Errorf("a version list: %v", err)
	}
	// Only the variants of the stored upstream bytes are filled: the document itself and the two a virtual
	// repository asks its remote members for (Q-NUGET-67).
	for _, v := range []string{"doc", VariantFlat, VariantRegistration} {
		if _, err := a.PrepareFill(context.Background(), rc, formats.Path{Path: "acme.widgets/index.json"}, v, strings.NewReader(`{"versions":["1.0.0"]}`), 23); err != nil {
			t.Errorf("variant %q: %v", v, err)
		}
	}
	_, err := a.PrepareFill(context.Background(), rc, formats.Path{Path: "acme.widgets/index.json"}, "other", strings.NewReader("{}"), 2)
	if err == nil {
		t.Error("another variant was filled")
	}
	// Nothing but metadata documents is filled.
	if _, err := fill("acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg", `PK`); err == nil {
		t.Error("a package was filled as a document")
	}
}

func leafJSON(version, content string) string {
	return `{"@id":"https://api.up.test/v3/reg-gz/acme.widgets/` + version + `.json","catalogEntry":{"id":"Acme.Widgets","version":"` + version + `"},` +
		`"packageContent":"` + content + `"}`
}

func TestR_NUGET_015_PrepareFillRecordsOnlyTheAddressesThatAreNotTheDerivedOnes(t *testing.T) {
	rc := testDiscovery(t)
	a := New().Remote()
	const flat = "https://content.up.test/flat/acme.widgets/"
	index := `{"count":3,"items":[
	  {"@id":"https://api.up.test/v3/reg-gz/acme.widgets/index.json#page/1.0.0/1.1.0","lower":"1.0.0","upper":"1.1.0","count":2,"items":[` +
		leafJSON("1.0.0", flat+"1.0.0/acme.widgets.1.0.0.nupkg") + `,` + leafJSON("1.1.0", "https://other.up.test/alt/acme.widgets.1.1.0.nupkg") + `]},
	  {"@id":"https://api.up.test/v3/reg-gz/acme.widgets/page/2.0.0/2.1.0.json","lower":"2.0.0","upper":"2.1.0","count":2},
	  {"@id":"https://api.up.test/pages/3","lower":"3.0.0-BETA","upper":"3.0.0","count":2}]}`
	got, err := a.PrepareFill(context.Background(), rc, formats.Path{Path: "acme.widgets/.registration/index.json"}, documentVariant,
		strings.NewReader(index), int64(len(index)))
	if err != nil {
		t.Fatal(err)
	}
	var maps []string
	for _, m := range got.Mappings {
		maps = append(maps, m.Path.Path+" -> "+m.Upstream)
	}
	want := []string{
		"acme.widgets/1.1.0/acme.widgets.1.1.0.nupkg -> https://other.up.test/alt/acme.widgets.1.1.0.nupkg",
		"acme.widgets/.registration/page/3.0.0-beta/3.0.0.json -> https://api.up.test/pages/3",
	}
	if strings.Join(maps, "\n") != strings.Join(want, "\n") {
		t.Errorf("mappings:\n%s\nwant\n%s", strings.Join(maps, "\n"), strings.Join(want, "\n"))
	}
	page := `{"items":[` + leafJSON("2.0.0", "https://elsewhere.test/p.nupkg") + `,` + leafJSON("2.1.0", flat+"2.1.0/acme.widgets.2.1.0.nupkg") + `]}`
	got, err = a.PrepareFill(context.Background(), rc, formats.Path{Path: "acme.widgets/.registration/page/2.0.0/2.1.0.json"}, documentVariant,
		strings.NewReader(page), int64(len(page)))
	if err != nil || len(got.Mappings) != 1 || got.Mappings[0].Path.Path != "acme.widgets/2.0.0/acme.widgets.2.0.0.nupkg" {
		t.Errorf("page mappings: %+v %v", got.Mappings, err)
	}
	// A page whose address cannot be used is no document.
	bad := `{"items":[{"@id":"ftp://x/p","lower":"1.0.0","upper":"2.0.0"}]}`
	if _, err := a.PrepareFill(context.Background(), rc, formats.Path{Path: "acme.widgets/.registration/index.json"}, documentVariant,
		strings.NewReader(bad), int64(len(bad))); err == nil {
		t.Error("a page with an ftp address was accepted")
	}
}

func TestR_NUGET_015_APackageIsInspectedOnlyForTheNupkgPath(t *testing.T) {
	a := New().Remote().(formats.FillInspector)
	for path, want := range map[string]bool{
		"acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg": true,
		"acme.widgets/1.2.3/acme.widgets.nuspec":      false,
		"acme.widgets/index.json":                     false,
		"acme.widgets/.registration/index.json":       false,
		".nuget/index.json":                           false,
		"junk":                                        false,
	} {
		if got := a.InspectsFill(formats.Path{Path: path}); got != want {
			t.Errorf("InspectsFill(%q) = %v, want %v", path, got, want)
		}
	}
}

// spool is a package as the fill hook receives it.
type spool struct{ b []byte }

func (s spool) Digests() formats.Digests       { return formats.Digests{} }
func (s spool) Size() int64                    { return int64(len(s.b)) }
func (s spool) Open() (io.ReadCloser, error)   { return io.NopCloser(bytes.NewReader(s.b)), nil }
func (s spool) ReaderAt() (io.ReaderAt, error) { return bytes.NewReader(s.b), nil }

func inspectFill(t testing.TB, path string, pkg []byte) (*formats.Inspection, error) {
	t.Helper()
	return New().Remote().(formats.FillInspector).InspectFill(context.Background(), formats.RemoteContext{}, formats.Path{Path: path}, spool{pkg})
}

func TestR_NUGET_015_InspectFillHoldsTheDeclaredIdentityToThePath(t *testing.T) {
	const path = "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"
	ok, err := inspectFill(t, path, buildNupkg(t, "Acme.Widgets", "1.2.3"))
	if err != nil || ok == nil || ok.Package != "acme.widgets" || ok.Version != "1.2.3" {
		t.Fatalf("matching package: %+v %v", ok, err)
	}
	in, isIncoming := ok.Payload.(*incoming)
	if !isIncoming || in.declaredID != "Acme.Widgets" || in.lowerVersion != "1.2.3" {
		t.Errorf("payload = %+v", ok.Payload)
	}
	// Spellings of one identity are the same identity.
	for _, pkg := range [][]byte{
		buildNupkg(t, "ACME.WIDGETS", "1.2.3"), buildNupkg(t, "acme.widgets", "1.2.3.0"), buildNupkg(t, "Acme.Widgets", "1.2.3+build.9"),
	} {
		if got, err := inspectFill(t, path, pkg); err != nil || got == nil {
			t.Errorf("a spelling of the same identity: %v %v", got, err)
		}
	}
	// A symbol package is not refused on the road of a cache (R-NUGET-015).
	if got, err := inspectFill(t, path, buildNupkg(t, "Acme.Widgets", "1.2.3", withType("SymbolsPackage"))); err != nil || got == nil {
		t.Errorf("a package of the type SymbolsPackage: %v %v", got, err)
	}
	for name, pkg := range map[string][]byte{
		"another ID":      buildNupkg(t, "Other.Pkg", "1.2.3"),
		"another version": buildNupkg(t, "Acme.Widgets", "1.2.4"),
		"a prerelease":    buildNupkg(t, "Acme.Widgets", "1.2.3-beta"),
		"a longer ID":     buildNupkg(t, "Acme.Widgets.Extra", "1.2.3"),
	} {
		got, err := inspectFill(t, path, pkg)
		e, isErr := errs.As(err)
		if got != nil || !isErr || e.Code != errs.CodeUpstreamPackageMismatch || e.Kind != errs.UpstreamFailed {
			t.Errorf("%s: %+v %v, want 502 upstream_package_mismatch", name, got, err)
		}
	}
}

func TestR_NUGET_003_InspectFillComparesANonASCIIIDAsAClientLowerCasesIt(t *testing.T) {
	const path = "Ünïcode.pkg/1.0.0/Ünïcode.pkg.1.0.0.nupkg" // trove lowercases ASCII letters only
	if got, err := inspectFill(t, path, buildNupkg(t, "Ünïcode.Pkg", "1.0.0")); err != nil || got == nil {
		t.Errorf("the declared casing: %v %v", got, err)
	}
	if got, err := inspectFill(t, path, buildNupkg(t, "ünïcode.pkg", "1.0.0")); err != nil || got == nil {
		t.Errorf("the folded casing: %v %v", got, err)
	}
	if _, err := inspectFill(t, path, buildNupkg(t, "Unicode.Pkg", "1.0.0")); err == nil {
		t.Error("another ID was accepted")
	}
}

func TestR_NUGET_015_AnUnreadablePackageIsServedAsItCameAndGetsNoRows(t *testing.T) {
	const path = "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"
	for name, pkg := range map[string][]byte{
		"not a ZIP archive": []byte("this is not a package"),
		"no .nuspec":        buildNupkg(t, "Acme.Widgets", "1.2.3", withoutNuspec()),
		"an empty file":     {},
	} {
		got, err := inspectFill(t, path, pkg)
		if err != nil || got != nil {
			t.Errorf("%s: %+v %v, want neither an inspection nor an error", name, got, err)
		}
	}
	// A cancelled request is not an unreadable package.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Remote().(formats.FillInspector).InspectFill(ctx, formats.RemoteContext{}, formats.Path{Path: path}, spool{buildNupkg(t, "Acme.Widgets", "1.2.3")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
}

// fillHost is a Host of a remote repository with the twin of the data port.
func fillHost(t testing.TB) (*formattest.Host, *Format, *nugettest.Store) {
	t.Helper()
	f := New()
	st := nugettest.New()
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Remote, Data: st})
	st.Revisions = func(_ int64, lowerID string) (int64, string) { return int64(h.RevSeq(lowerID)), lowerID }
	return h, f, st
}

// commitFilled runs the hook of a fill of the package at its path the way the item store does:
// the inspection of the fill travels to OnCommit.
func commitFilled(t testing.TB, h *formattest.Host, f *Format, st *nugettest.Store, id, v string, pkg []byte) error {
	t.Helper()
	path := NupkgPath(id, v)
	ins, err := inspectFill(t, path, pkg)
	if err != nil {
		return err
	}
	item := h.PutItem(path, "application/octet-stream", pkg)
	sum := sha512Of(pkg)
	return h.Write(context.Background(), nil, func(tx formats.Tx, w formats.PackageWriter) error {
		return f.OnCommit(context.Background(), tx, formats.CommitEvent{
			Origin: formats.OriginFill, Repo: h.Ref(), Item: item, Path: formats.Path{Path: path}, Size: int64(len(pkg)),
			Digests: formats.Digests{SHA512: sum}, Inspection: ins, Packages: w, Data: st,
		})
	})
}

func TestR_NUGET_015_AFilledPackageIsRecordedWithTheRowsOfOriginCache(t *testing.T) {
	h, f, st := fillHost(t)
	pkg := buildNupkg(t, "Acme.Widgets", "1.2.3")
	if err := commitFilled(t, h, f, st, "acme.widgets", "1.2.3", pkg); err != nil {
		t.Fatal(err)
	}
	if got := h.RevSeq("acme.widgets"); got != 1 {
		t.Errorf("rev_seq = %d, want 1: a package created with its first version in one transaction", got)
	}
	if len(st.Cached) != 1 {
		t.Fatalf("%d NuGet rows", len(st.Cached))
	}
	row := st.Cached[0]
	if row.Origin != rows.OriginCache || row.LowerID != "acme.widgets" || row.DeclaredID != "Acme.Widgets" || row.LowerVersion != "1.2.3" {
		t.Errorf("row = %+v", row)
	}
	if len(st.Tombstones) != 0 || len(h.Claims()) != 0 {
		t.Error("a fill wrote a tombstone or claimed the ID: no request published anything")
	}
	// The same version again (the item was evicted and fetched again) keeps its one row.
	if err := commitFilled(t, h, f, st, "acme.widgets", "1.2.3", pkg); err != nil {
		t.Fatal(err)
	}
	if len(st.Cached) != 1 {
		t.Errorf("%d NuGet rows after a second fill of the same version", len(st.Cached))
	}
	if err := commitFilled(t, h, f, st, "acme.widgets", "1.3.0", buildNupkg(t, "Acme.Widgets", "1.3.0")); err != nil {
		t.Fatal(err)
	}
	if len(st.Cached) != 2 {
		t.Errorf("%d NuGet rows after a second version", len(st.Cached))
	}
}

func TestR_NUGET_015_AnUnreadableFillAndANuspecGetNoRows(t *testing.T) {
	h, f, st := fillHost(t)
	for _, c := range []struct {
		path string
		data []byte
	}{
		{NupkgPath("acme.widgets", "1.2.3"), []byte("garbage")},
		{NuspecPath("acme.widgets", "1.2.3"), []byte("<package/>")},
	} {
		item := h.PutItem(c.path, "application/octet-stream", c.data)
		err := h.Write(context.Background(), nil, func(tx formats.Tx, w formats.PackageWriter) error {
			return f.OnCommit(context.Background(), tx, formats.CommitEvent{
				Origin: formats.OriginFill, Repo: h.Ref(), Item: item, Path: formats.Path{Path: c.path}, Packages: w, Data: st,
			})
		})
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
		}
	}
	if len(st.Cached) != 0 || h.RevSeq("acme.widgets") != 0 {
		t.Errorf("rows %d, rev_seq %d: an item that is no readable package made a version", len(st.Cached), h.RevSeq("acme.widgets"))
	}
}

func TestR_NUGET_015_RemovingACachedPackageRemovesItsVersionAndWritesNoTombstone(t *testing.T) {
	h, f, st := fillHost(t)
	if err := commitFilled(t, h, f, st, "acme.widgets", "1.2.3", buildNupkg(t, "Acme.Widgets", "1.2.3")); err != nil {
		t.Fatal(err)
	}
	rev := h.RevSeq("acme.widgets")
	rq, err := h.NewRequest(context.Background(), http.MethodDelete, "v3/package/acme.widgets/1.2.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Remove(context.Background(), rq, "delete", NupkgPath("acme.widgets", "1.2.3")); err != nil {
		t.Fatal(err)
	}
	if h.RevSeq("acme.widgets") <= rev {
		t.Error("the removal did not raise the revision of the package")
	}
	if len(st.Tombstones) != 0 {
		t.Error("the removal of a cached package wrote a tombstone: a remote can fetch the version again")
	}
	live, _, err := h.Packages().Versions(context.Background(), h.Ref(), "acme.widgets")
	if err != nil || len(live) != 0 {
		t.Errorf("versions after the removal: %v %v", live, err)
	}
}

// remoteHost is a Host of a remote repository over the in-memory upstream.
func remoteHost(t testing.TB, m *formattest.MemRemote) *formattest.Host {
	t.Helper()
	return formattest.NewHost(New(), formattest.HostOptions{Class: formats.Remote, Remote: m, Data: nugettest.New(), BaseURL: "https://trove.example.com"})
}

func rget(t testing.TB, h *formattest.Host, path string, hdr ...string) *formattest.Response {
	t.Helper()
	r, err := h.Serve(context.Background(), http.MethodGet, path, nil, false, func(r *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestR_NUGET_008_TheVersionListOfARemoteIsRenderedInURLFormAndAscending(t *testing.T) {
	m := &formattest.MemRemote{Docs: map[string][]byte{
		"acme.widgets/index.json": []byte(`{"versions":["2.0.0","1.0.0-Beta.10","1.0.0-beta.2","1.0.0.0","1.0.0","not-a-version","1.0.0-beta.2"]}`),
	}}
	h := remoteHost(t, m)
	r := rget(t, h, "v3/flatcontainer/Acme.Widgets/index.json")
	if r.Code != 200 || r.Body.String() != `{"versions":["1.0.0-beta.2","1.0.0-beta.10","1.0.0","2.0.0"]}` {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if ct := r.Header().Get("Content-Type"); ct != document.ContentType {
		t.Errorf("Content-Type = %q", ct)
	}
	etag := r.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q", etag)
	}
	if c := rget(t, h, "v3/flatcontainer/acme.widgets/index.json", "If-None-Match", etag); c.Code != 304 || c.Body.Len() != 0 {
		t.Errorf("conditional request: %d", c.Code)
	}
	// Another document is another validator.
	m.Docs["acme.widgets/index.json"] = []byte(`{"versions":["2.0.0","3.0.0"]}`)
	if r2 := rget(t, h, "v3/flatcontainer/acme.widgets/index.json"); r2.Header().Get("ETag") == etag || !strings.Contains(r2.Body.String(), "3.0.0") {
		t.Errorf("a new upstream list was not rendered: %s %s", r2.Header().Get("ETag"), r2.Body.String())
	}
	// An ID the upstream does not have, and one without a version, are 404.
	if r := rget(t, h, "v3/flatcontainer/unknown.pkg/index.json"); r.Code != 404 {
		t.Errorf("unknown ID: %d", r.Code)
	}
	m.Docs["empty.pkg/index.json"] = []byte(`{"versions":[]}`)
	if r := rget(t, h, "v3/flatcontainer/empty.pkg/index.json"); r.Code != 404 {
		t.Errorf("an ID without a version: %d", r.Code)
	}
	// A document that is no list is a failure of the upstream, never a result.
	m.Docs["junk.pkg/index.json"] = []byte(`<html/>`)
	if r := rget(t, h, "v3/flatcontainer/junk.pkg/index.json"); r.Code != 502 || code(t, r) != "nuget_invalid_upstream_document" {
		t.Errorf("junk: %d %s", r.Code, r.Body.String())
	}
}

// registrationUpstream builds the stored documents of a remote registration: n versions of
// Acme.Widgets in pages the way an upstream pages them.
func registrationUpstream(versions []string, pageSize int, inline bool) map[string][]byte {
	const reg = "https://api.up.test/v3/reg/acme.widgets/"
	leaf := func(v string) string {
		extra := ""
		if v == "1.1.0" {
			extra = `,"deprecation":{"message":"use 2.0","reasons":["Legacy"]},"vulnerabilities":[{"advisoryUrl":"https://adv.test/1","severity":"1"}],"packageHash":"h"`
		}
		return `{"@id":"` + reg + v + `.json","@type":"Package","catalogEntry":{"@id":"` + reg + v + `.json","id":"Acme.Widgets","version":"` + v + `",` +
			`"authors":"Acme","description":"Widgets","listed":true,"published":"2026-10-01T10:00:00Z","projectUrl":"https://acme.test/",` +
			`"packageContent":"https://content.up.test/flat/acme.widgets/` + v + `/acme.widgets.` + v + `.nupkg",` +
			`"dependencyGroups":[{"targetFramework":"net8.0","dependencies":[{"id":"Ünï.Dep","range":"[1.0.0, )"}]}]` + extra + `},` +
			`"packageContent":"https://content.up.test/flat/acme.widgets/` + v + `/acme.widgets.` + v + `.nupkg","registration":"` + reg + `index.json"}`
	}
	docs := map[string][]byte{}
	var pages []string
	for lo := 0; lo < len(versions); lo += pageSize {
		chunk := versions[lo:min(lo+pageSize, len(versions))]
		var leaves []string
		for _, v := range chunk {
			leaves = append(leaves, leaf(v))
		}
		lower, upper := chunk[0], chunk[len(chunk)-1]
		page := fmt.Sprintf(`{"@id":"%spage/%s/%s.json","count":%d,"lower":"%s","upper":"%s"`, reg, lower, upper, len(chunk), lower, upper)
		if inline {
			page = fmt.Sprintf(`{"@id":"%sindex.json#page/%s/%s","count":%d,"lower":"%s","upper":"%s","items":[%s]`, reg, lower, upper, len(chunk), lower, upper, strings.Join(leaves, ","))
		} else {
			docs[RegistrationPagePath("acme.widgets", lower, upper)] = []byte(page + fmt.Sprintf(`,"items":[%s]}`, strings.Join(leaves, ",")))
		}
		pages = append(pages, page+"}")
	}
	docs[RegistrationIndexPath("acme.widgets")] = []byte(fmt.Sprintf(`{"@id":"%sindex.json","count":%d,"items":[%s]}`, reg, len(pages), strings.Join(pages, ",")))
	return docs
}

func jsonOf(t testing.TB, r *formattest.Response) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", r.Code, r.Body.String(), err)
	}
	return v
}

func TestR_NUGET_009_ARemoteRegistrationIsRenderedPerHiveWithTheDocumentsOfTheUpstream(t *testing.T) {
	m := &formattest.MemRemote{Docs: registrationUpstream([]string{"1.0.0", "1.1.0", "2.0.0-rc.1", "2.0.0"}, 64, true)}
	h := remoteHost(t, m)
	const feed = "https://trove.example.com/api/nuget/main/v3/"

	semver2 := rget(t, h, "v3/registration-semver2/Acme.Widgets/index.json")
	if semver2.Code != 200 {
		t.Fatalf("%d %s", semver2.Code, semver2.Body.String())
	}
	body := semver2.Body.String()
	if strings.Contains(body, "api.up.test") || strings.Contains(body, "content.up.test") {
		t.Errorf("an address of the upstream is in the document: %s", body)
	}
	if !strings.Contains(body, `"packageContent":"`+feed+`flatcontainer/acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg"`) {
		t.Errorf("package content is not the flat container of the repository: %s", body)
	}
	// Deprecation and vulnerabilities pass through, an unknown member is kept, the web link stays.
	for _, want := range []string{
		`"deprecation":{"message":"use 2.0","reasons":["Legacy"]}`, `"vulnerabilities":[{"advisoryUrl":"https://adv.test/1","severity":"1"}]`,
		`"packageHash":"h"`, `"projectUrl":"https://acme.test/"`, `"id":"Acme.Widgets"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	// A dependency's ID outside ASCII is a valid ID of a remote: its registration is an address.
	if !strings.Contains(body, `"registration":"`+feed+`registration-semver2/%C3%9Cn%C3%AF.dep/index.json"`) {
		t.Errorf("the registration of a dependency outside ASCII: %s", body)
	}
	if n := countLeaves(jsonOf(t, semver2)); n != 4 {
		t.Errorf("semver2 hive: %d leaves, want 4", n)
	}
	// The older hives leave out the version with a dotted pre-release label (R-NUGET-004).
	plain := rget(t, h, "v3/registration/acme.widgets/index.json")
	if n := countLeaves(jsonOf(t, plain)); n != 3 || strings.Contains(plain.Body.String(), "2.0.0-rc.1") {
		t.Errorf("plain hive: %d leaves: %s", n, plain.Body.String())
	}
	if plain.Header().Get("Content-Encoding") != "" {
		t.Error("the plain hive was compressed")
	}
	gz := rget(t, h, "v3/registration-gz/acme.widgets/index.json", "Accept-Encoding", "gzip")
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("gz hive headers: %v", gz.Header())
	}
	if gz.Header().Get("ETag") != rget(t, h, "v3/registration-gz/acme.widgets/index.json").Header().Get("ETag") {
		t.Error("the gzip and the identity variants have different validators")
	}
	leaf := rget(t, h, "v3/registration-semver2/acme.widgets/2.0.0-RC.1.json")
	if leaf.Code != 200 || !strings.Contains(leaf.Body.String(), `"registration":"`+feed+`registration-semver2/acme.widgets/index.json"`) {
		t.Errorf("leaf: %d %s", leaf.Code, leaf.Body.String())
	}
	if r := rget(t, h, "v3/registration/acme.widgets/2.0.0-rc.1.json"); r.Code != 404 {
		t.Errorf("a leaf the hive does not list: %d", r.Code)
	}
	page := rget(t, h, "v3/registration-semver2/acme.widgets/page/1.0.0/2.0.0.json")
	if page.Code != 200 || countLeaves(map[string]any{"items": []any{jsonOf(t, page)}}) != 4 {
		t.Errorf("page: %d %s", page.Code, page.Body.String())
	}
}

func countLeaves(index map[string]any) int {
	n := 0
	pages, _ := index["items"].([]any)
	for _, p := range pages {
		leaves, _ := p.(map[string]any)["items"].([]any)
		n += len(leaves)
	}
	return n
}

func TestR_NUGET_015_TheRegistrationOfAnIDWithManyVersionsFollowsThePagesOfTheUpstream(t *testing.T) {
	var vs []string
	for i := range 300 {
		vs = append(vs, fmt.Sprintf("1.%d.0", i))
	}
	m := &formattest.MemRemote{Docs: registrationUpstream(vs, 100, false)}
	h := remoteHost(t, m)
	idx := rget(t, h, "v3/registration-semver2/acme.widgets/index.json")
	if idx.Code != 200 {
		t.Fatalf("%d %s", idx.Code, idx.Body.String())
	}
	d := jsonOf(t, idx)
	pages := d["items"].([]any)
	// trove's paging: 64 versions to a page, listed to fetch (the upstream's 100 do not matter).
	if len(pages) != 5 || countLeaves(d) != 0 {
		t.Fatalf("%d pages, %d inlined leaves", len(pages), countLeaves(d))
	}
	first := pages[0].(map[string]any)
	if first["lower"] != "1.0.0" || first["upper"] != "1.63.0" || first["count"].(float64) != 64 {
		t.Errorf("first page = %v", first)
	}
	total := 0
	for _, p := range pages {
		id := p.(map[string]any)["@id"].(string)
		r := rget(t, h, strings.TrimPrefix(id, "https://trove.example.com/api/nuget/main/"))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", id, r.Code, r.Body.String())
		}
		total += int(jsonOf(t, r)["count"].(float64))
	}
	if total != 300 {
		t.Errorf("%d leaves over the pages, want 300", total)
	}
	// A page the upstream's index lists and the upstream does not have: the upstream contradicts itself.
	delete(m.Docs, RegistrationPagePath("acme.widgets", "1.100.0", "1.199.0"))
	h2 := remoteHost(t, m)
	r := rget(t, h2, "v3/registration-semver2/acme.widgets/index.json")
	if r.Code != 502 || code(t, r) != "nuget_invalid_upstream_document" {
		t.Errorf("missing page: %d %s", r.Code, r.Body.String())
	}
}

func TestR_NUGET_015_TheValidatorOfARemoteDocumentFollowsTheStoredBytesAndTheServingRepository(t *testing.T) {
	m := &formattest.MemRemote{Docs: registrationUpstream([]string{"1.0.0"}, 64, true)}
	h := remoteHost(t, m)
	first := rget(t, h, "v3/registration-semver2/acme.widgets/index.json")
	again := rget(t, h, "v3/registration-semver2/acme.widgets/index.json")
	if first.Header().Get("ETag") != again.Header().Get("ETag") || !bytes.Equal(first.Body.Bytes(), again.Body.Bytes()) {
		t.Error("two renderings of the same stored bytes differ")
	}
	if c := rget(t, h, "v3/registration-semver2/acme.widgets/index.json", "If-None-Match", first.Header().Get("ETag")); c.Code != 304 {
		t.Errorf("conditional: %d", c.Code)
	}
	// Another stored document renders and validates differently, though it is the same URL.
	m.Docs = registrationUpstream([]string{"1.0.0", "1.2.0"}, 64, true)
	changed := rget(t, h, "v3/registration-semver2/acme.widgets/index.json")
	if changed.Header().Get("ETag") == first.Header().Get("ETag") || countLeaves(jsonOf(t, changed)) != 2 {
		t.Errorf("a changed upstream document: etag %s, %d leaves", changed.Header().Get("ETag"), countLeaves(jsonOf(t, changed)))
	}
	// The cache view names itself in every address.
	cv := formattest.NewHost(New(), formattest.HostOptions{Class: formats.Remote, Remote: m, Data: nugettest.New(), BaseURL: "https://trove.example.com"})
	r, err := cv.Serve(context.Background(), http.MethodGet, "v3/registration-semver2/acme.widgets/index.json", nil, false)
	if err != nil || r.Code != 200 {
		t.Fatalf("%v %v", r, err)
	}
	// HEAD tells what GET carries.
	head, err := h.Serve(context.Background(), http.MethodHead, "v3/registration-semver2/acme.widgets/index.json", nil, false)
	if err != nil || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(changed.Body.Len()) {
		t.Errorf("HEAD: %v %v", head, err)
	}
}

func TestR_NUGET_015_APackageFileOfARemoteIsFetchedThroughTheEngineAfterTheReadPermission(t *testing.T) {
	m := &formattest.MemRemote{Artifacts: map[string][]byte{
		NupkgPath("acme.widgets", "1.2.3"): []byte("PK-package"), NuspecPath("acme.widgets", "1.2.3"): []byte("<package/>"),
	}}
	h := remoteHost(t, m)
	p := rget(t, h, "v3/flatcontainer/Acme.Widgets/1.2.3.0/acme.widgets.1.2.3.nupkg")
	if p.Code != 200 || p.Body.String() != "PK-package" || p.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("package: %d %q %s", p.Code, p.Body.String(), p.Header().Get("Content-Type"))
	}
	s := rget(t, h, "v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.nuspec")
	if s.Code != 200 || s.Header().Get("Content-Type") != "application/xml" {
		t.Errorf("nuspec: %d %s", s.Code, s.Header().Get("Content-Type"))
	}
	if r := rget(t, h, "v3/flatcontainer/acme.widgets/9.9.9/acme.widgets.9.9.9.nupkg"); r.Code != 404 {
		t.Errorf("an unknown version: %d", r.Code)
	}
	var read bool
	for _, a := range h.Authorizations() {
		if a.Target.Package == "acme.widgets" && a.Target.Version == "1.2.3" {
			read = true
		}
	}
	if !read {
		t.Error("the read permission on the version was not asked")
	}
}

func TestR_NUGET_034_ARemoteHidesWhatTheEndpointDoesNotExposeBeforeAnyFetch(t *testing.T) {
	m := &formattest.MemRemote{
		Docs:      registrationUpstream([]string{"1.0.0", "1.1.0"}, 64, true),
		Artifacts: map[string][]byte{NupkgPath("acme.widgets", "1.1.0"): []byte("PK")},
	}
	m.Docs["acme.widgets/index.json"] = []byte(`{"versions":["1.0.0","1.1.0"]}`)
	h := remoteHost(t, m)
	// The kit's Host has no endpoint with exposure rules; the pure functions carry the rule.
	keep := func(v string) bool { return v == "1.0.0" }
	body, found, err := renderVersionList([]string{"1.1.0", "1.0.0"}, keep)
	if err != nil || !found || string(body) != `{"versions":["1.0.0"]}` {
		t.Errorf("filtered list: %s %v %v", body, found, err)
	}
	if _, found, _ := renderVersionList([]string{"1.1.0"}, keep); found {
		t.Error("a list without an exposed version has a document")
	}
	_ = h
}

func TestR_NUGET_015_RewriteRendersTheSemVer2HiveOfTheServingRepository(t *testing.T) {
	a := New().Remote()
	docs := registrationUpstream([]string{"1.0.0", "2.0.0-rc.1"}, 64, true)
	ln := formattest.Links("https://trove.example.com", "nuget", "main")
	out, err := a.Rewrite(context.Background(), formats.RemoteContext{}, formats.Path{Path: RegistrationIndexPath("acme.widgets")}, docs[RegistrationIndexPath("acme.widgets")], ln)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "https://trove.example.com/api/nuget/main/v3/registration-semver2/acme.widgets/index.json") || strings.Contains(string(out), "up.test") {
		t.Errorf("rewritten index: %s", out)
	}
	again, err := a.Rewrite(context.Background(), formats.RemoteContext{}, formats.Path{Path: RegistrationIndexPath("acme.widgets")}, out, ln)
	if err != nil || !bytes.Equal(out, again) {
		t.Errorf("not idempotent: %v", err)
	}
	if _, err := a.Rewrite(context.Background(), formats.RemoteContext{}, formats.Path{Path: "acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg"}, []byte("PK"), ln); err == nil {
		t.Error("a package was rewritten")
	}
}

func TestR_NUGET_015_TheFillSignalsTheEvictionOfMergedDocumentsExceptForTheServiceIndex(t *testing.T) {
	h, _, _ := fillHost(t)
	a := New().Remote()
	for path, signals := range map[string]int{".nuget/index.json": 0, "acme.widgets/index.json": 1, "acme.widgets/.registration/index.json": 1} {
		before := len(h.Notifications())
		err := h.Seed(context.Background(), func(w formats.PackageWriter) error {
			d, err := a.OnFill(context.Background(), nil, formats.FillEvent{Repo: h.Ref(), Path: formats.Path{Path: path}, Packages: w})
			if d != formats.FillByFetchTime {
				t.Errorf("%s: decision %v", path, d)
			}
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got := len(h.Notifications()) - before; got != signals {
			t.Errorf("%s: %d signals, want %d", path, got, signals)
		}
	}
}

// TestR_NUGET_004_ADependencyRangeWithASemVer2BoundMakesACachedVersionSemVer2Specific: the
// rows of a package a remote repository cached follow the rule of a pushed one: a dependency
// range with a SemVer 2.0.0-specific bound makes the version SemVer 2.0.0-specific, as the
// remote's own rendering of the upstream's leaves decides too.
func TestR_NUGET_004_ADependencyRangeWithASemVer2BoundMakesACachedVersionSemVer2Specific(t *testing.T) {
	h, f, st := fillHost(t)
	if err := commitFilled(t, h, f, st, "acme.widgets", "1.0.0", buildNupkg(t, "Acme.Widgets", "1.0.0", withDep("Acme.Core", "[1.0.0, 2.0.0)"))); err != nil {
		t.Fatal(err)
	}
	if err := commitFilled(t, h, f, st, "acme.widgets", "1.1.0", buildNupkg(t, "Acme.Widgets", "1.1.0", withDep("Acme.Core", "[1.0.0-beta.1, )"))); err != nil {
		t.Fatal(err)
	}
	if len(st.Cached) != 2 {
		t.Fatalf("%d NuGet rows, want 2", len(st.Cached))
	}
	for _, row := range st.Cached {
		want := row.LowerVersion == "1.1.0"
		if row.IsSemver2 != want {
			t.Errorf("%s: is_semver2 = %v, want %v", row.LowerVersion, row.IsSemver2, want)
		}
	}
}
