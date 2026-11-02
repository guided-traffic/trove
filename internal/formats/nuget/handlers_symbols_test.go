package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

const (
	acmeSnupkg = "acme.widgets/1.2.3/acme.widgets.1.2.3.snupkg"

	portablePDB = "BSJB\x01\x00\x01\x00portable-pdb-body"
	windowsPDB  = "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00windows-pdb-body"
)

// snupkgSpec describes a symbol package to build for a test.
type snupkgSpec struct {
	types   []string
	entries map[string]string
}

// newSnupkg builds a symbol package: a .nuspec with the given package types (the
// SymbolsPackage type by default) and the entries.
func newSnupkg(id, version string, s snupkgSpec) ([]byte, error) {
	if s.types == nil {
		s.types = []string{"SymbolsPackage"}
	}
	spec := pkgSpec{id: id, version: version, description: "Symbols", types: s.types}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, body []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	if err := add(id+".nuspec", spec.nuspec()); err != nil {
		return nil, err
	}
	for name, body := range s.entries {
		if err := add(name, []byte(body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// makeSnupkg is newSnupkg for a test.
func makeSnupkg(t testing.TB, id, version string, s snupkgSpec) []byte {
	t.Helper()
	b, err := newSnupkg(id, version, s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func defaultSnupkg(t testing.TB, version string) []byte {
	return makeSnupkg(t, "Acme.Widgets", version, snupkgSpec{entries: map[string]string{"lib/net8.0/Acme.Widgets.pdb": portablePDB}})
}

// pushSymbols sends a symbol package the way a client does.
func (h *writeHost) pushSymbols(t testing.TB, pkg []byte) *formattest.Response {
	t.Helper()
	body, ct := multipartBody(t, pkg)
	resp, err := h.Serve(context.Background(), http.MethodPut, "v3/symbolpackage", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestR_NUGET_020_ASymbolPushStoresTheSnupkgAndQueuesItsIndexing: after the package, the symbol
// package answers 201, is stored byte for byte on its canonical path, linked to the version,
// recorded once with the item, and the record is what enqueues the indexing job.
func TestR_NUGET_020_ASymbolPushStoresTheSnupkgAndQueuesItsIndexing(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	snupkg := defaultSnupkg(t, "1.2.3")
	r := h.pushSymbols(t, snupkg)
	if r.Code != http.StatusCreated || r.Body.Len() != 0 {
		t.Fatalf("status %d, body %q, want 201 and none", r.Code, r.Body.String())
	}
	it, ok, err := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: acmeSnupkg})
	if err != nil || !ok {
		t.Fatalf("the symbol package item: found %v, err %v", ok, err)
	}
	if !bytes.Equal(it.Digests.SHA512, sha512Of(snupkg)) {
		t.Error("the stored symbol package is not the pushed one, byte for byte")
	}
	if len(h.st.SymbolJobs) != 1 {
		t.Fatalf("%d symbol packages recorded, want 1", len(h.st.SymbolJobs))
	}
	if got := h.st.SymbolJobs[0]; got.ItemID != it.Ref.ID || it.Version == 0 || got.VersionID == 0 || got.RepositoryID == 0 {
		t.Errorf("recorded = %+v, item %d", got, it.Ref.ID)
	}
	if len(h.st.Published) != 1 {
		t.Errorf("%d versions, want the one the package push made", len(h.st.Published))
	}
}

// TestR_NUGET_020_AWindowsPdbIsAccepted: both PDB formats pass.
func TestR_NUGET_020_AWindowsPdbIsAccepted(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	pkg := makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{
		"lib/net48/Acme.Widgets.pdb": windowsPDB, "lib/net8.0/Acme.Widgets.pdb": portablePDB, "docs/Acme.Widgets.xml": "<doc/>",
	}})
	if r := h.pushSymbols(t, pkg); r.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
}

// TestR_NUGET_020_ASymbolPushBeforeThePackageIs404: a symbol package of a version the repository
// does not hold is 404, stores nothing and queues nothing; a version that was removed is as
// good as one that never was.
func TestR_NUGET_020_ASymbolPushBeforeThePackageIs404(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	if r := h.pushSymbols(t, defaultSnupkg(t, "1.2.3")); r.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	h.mustPush(t, "Acme.Widgets", "1.0.0")
	if r := h.pushSymbols(t, defaultSnupkg(t, "1.2.3")); r.Code != http.StatusNotFound {
		t.Fatalf("another version of the ID: status %d: %s", r.Code, r.Body.String())
	}
	if h.hasItem(t, acmeSnupkg) || len(h.st.SymbolJobs) != 0 {
		t.Error("a refused symbol push stored or queued something")
	}
	h.deleteMode("delete")
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	if r := h.do(t, http.MethodDelete, "v3/package/acme.widgets/1.2.3"); r.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", r.Code, r.Body.String())
	}
	if r := h.pushSymbols(t, defaultSnupkg(t, "1.2.3")); r.Code != http.StatusNotFound {
		t.Fatalf("a deleted version: status %d: %s", r.Code, r.Body.String())
	}
}

// TestR_NUGET_020_TheSameSymbolPackageAgainIs201WithoutAChange: an acknowledged retry stores
// nothing and queues no second job.
func TestR_NUGET_020_TheSameSymbolPackageAgainIs201WithoutAChange(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	snupkg := defaultSnupkg(t, "1.2.3")
	for i := range 3 {
		if r := h.pushSymbols(t, snupkg); r.Code != http.StatusCreated {
			t.Fatalf("push %d: status %d: %s", i, r.Code, r.Body.String())
		}
	}
	if len(h.st.SymbolJobs) != 1 {
		t.Errorf("%d symbol packages recorded, want 1", len(h.st.SymbolJobs))
	}
}

// TestR_NUGET_020_OtherSymbolBytesForTheVersionAre409: symbols must match the binaries of the
// immutable package, so the stored symbol package is never replaced.
func TestR_NUGET_020_OtherSymbolBytesForTheVersionAre409(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	first := defaultSnupkg(t, "1.2.3")
	if r := h.pushSymbols(t, first); r.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	other := makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{"lib/net8.0/Acme.Widgets.pdb": portablePDB + "more"}})
	r := h.pushSymbols(t, other)
	if r.Code != http.StatusConflict || code(t, r) != "nuget_symbol_package_exists" {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	it, _, _ := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: acmeSnupkg})
	if !bytes.Equal(it.Digests.SHA512, sha512Of(first)) {
		t.Error("the stored symbol package changed")
	}
}

// TestR_NUGET_020_ARefusedSymbolPackageIs400: each rule of the validation, with what the
// message says. Nothing is stored or queued.
func TestR_NUGET_020_ARefusedSymbolPackageIs400(t *testing.T) {
	cases := map[string]struct {
		pkg  func(t testing.TB) []byte
		want string
	}{
		"a dll inside": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{
				"lib/net8.0/Acme.Widgets.pdb": portablePDB, "lib/net8.0/Acme.Widgets.dll": "MZ",
			}})
		}, "assemblies belong to the package"},
		"a legacy symbols nupkg": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{types: []string{"Dependency"}, entries: map[string]string{
				"lib/net8.0/Acme.Widgets.dll": "MZ", "lib/net8.0/Acme.Widgets.pdb": portablePDB, "src/A.cs": "class A {}",
			}})
		}, "Legacy .symbols.nupkg"},
		"no package type": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{types: []string{}, entries: map[string]string{"a.pdb": portablePDB}})
		}, "SymbolsPackage"},
		"a pdb that is no pdb": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{"lib/net8.0/Acme.Widgets.pdb": "this is text"}})
		}, "file signature"},
		"an empty pdb": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{"lib/net8.0/Acme.Widgets.pdb": ""}})
		}, "file signature"},
		"an entry without extension": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{"LICENSE": "x"}})
		}, "LICENSE"},
		"an upper-case dll": {func(t testing.TB) []byte {
			return makeSnupkg(t, "Acme.Widgets", "1.2.3", snupkgSpec{entries: map[string]string{"lib/A.DLL": "MZ"}})
		}, "A.DLL"},
		"not an archive": {func(testing.TB) []byte { return []byte("not a zip") }, "ZIP"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newWriteHost(t, formattest.HostOptions{})
			h.mustPush(t, "Acme.Widgets", "1.2.3")
			r := h.pushSymbols(t, c.pkg(t))
			if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), c.want) {
				t.Fatalf("status %d, want 400 naming %q: %s", r.Code, c.want, r.Body.String())
			}
			if h.hasItem(t, acmeSnupkg) || len(h.st.SymbolJobs) != 0 {
				t.Error("a refused symbol package was stored or queued")
			}
		})
	}
}

// TestR_NUGET_020_ASymbolPackageIsRefusedOnThePackageRoad: pushing the .snupkg to the package
// resource stays 400 and tells where it goes (R-NUGET-005 step 4).
func TestR_NUGET_020_ASymbolPackageIsRefusedOnThePackageRoad(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	r := h.push(t, defaultSnupkg(t, "1.2.3"))
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "symbolpackage") {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
}

// TestR_NUGET_020_ASymbolPackageGoesWithItsVersion: the hard delete of a version takes the
// symbol package with it (R-NUGET-007); an unlist leaves it.
func TestR_NUGET_020_ASymbolPackageGoesWithItsVersion(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	if r := h.pushSymbols(t, defaultSnupkg(t, "1.2.3")); r.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if r := h.do(t, http.MethodDelete, "v3/package/acme.widgets/1.2.3"); r.Code != http.StatusNoContent {
		t.Fatalf("unlist: status %d", r.Code)
	}
	if !h.hasItem(t, acmeSnupkg) {
		t.Error("an unlist removed the symbol package")
	}
	h.deleteMode("delete")
	if r := h.do(t, http.MethodDelete, "v3/package/acme.widgets/1.2.3"); r.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", r.Code)
	}
	if h.hasItem(t, acmeSnupkg) || h.hasItem(t, acmePath) {
		t.Error("the delete left the symbol package or the package")
	}
}

// TestR_NUGET_020_TheSymbolRouteTakesAnAPIKeyAndADeploy: the route is the symbol publish
// resource, an API-key endpoint that needs the deploy permission.
func TestR_NUGET_020_TheSymbolRouteTakesAnAPIKeyAndADeploy(t *testing.T) {
	for _, rt := range New().Routes() {
		if rt.Name == routeSymbolPush && (!rt.APIKey || rt.Method != http.MethodPut) {
			t.Errorf("route = %+v", rt)
		}
	}
}
