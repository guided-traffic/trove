//go:build integration

package nuget

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

const (
	portablePDB = "BSJB\x01\x00\x01\x00portable-pdb-body"
	windowsPDB  = "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00windows-pdb-body"
)

// snupkg builds a symbol package of a version: the .nuspec with the SymbolsPackage type and the
// entries. The body makes two builds of one version differ.
func snupkg(t testing.TB, id, version, body string, entries map[string]string) []byte {
	t.Helper()
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>%s</id><version>%s</version><authors>Acme</authors><description>Symbols</description><packageTypes><packageType name="SymbolsPackage"/></packageTypes></metadata></package>`,
		id, version)
	all := map[string]string{id + ".nuspec": nuspec}
	if entries == nil {
		entries = map[string]string{"lib/net8.0/" + id + ".pdb": portablePDB + body}
	}
	for k, v := range entries {
		all[k] = v
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range all {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pushSymbols sends PUT v3/symbolpackage.
func (e *wenv) pushSymbols(r *testenv.Replica, repo, token string, pkg []byte) testenv.Answer {
	e.t.Helper()
	body, ct := form(e.t, pkg)
	return e.st.Do(r, "PUT", "/api/nuget/it/"+repo+"/v3/symbolpackage", token, body, "Content-Type", ct)
}

func (e *wenv) symbolPackages(repo string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM nuget_symbol_package sp JOIN package_versions pv ON pv.id = sp.version_id
		JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1`, repo)
}

func (e *wenv) indexJobs(state string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'nuget-symbol-index' AND ($1 = '' OR state::text = $1)`, state)
}

// TestR_NUGET_020_ASymbolPushIsStoredLinkedAndQueuedForIndexing: after the package, the symbol
// package answers 201 and is stored byte for byte on its canonical path as an item of the
// version, with its row; the same transaction queued one indexing job on the index queue,
// exclusive per symbol package, and no worker has touched the package (Q-NUGET-11).
func TestR_NUGET_020_ASymbolPushIsStoredLinkedAndQueuedForIndexing(t *testing.T) {
	e := newWriteEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	e.mustPush(a, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3"}))
	rev := e.rev("nuget-local", "acme.widgets")
	sym := snupkg(t, "Acme.Widgets", "1.2.3", "", map[string]string{
		"lib/net8.0/Acme.Widgets.pdb": portablePDB, "lib/net48/Acme.Widgets.pdb": windowsPDB, "docs/Acme.Widgets.xml": "<doc/>",
	})
	// the other replica takes the symbol push: the package is looked up in PostgreSQL
	if ans := e.pushSymbols(b, "nuget-local", e.tokDev, sym); ans.Status != 201 || len(ans.Body) != 0 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	if got := e.st.Text(`SELECT encode(b.sha512, 'hex') FROM items i JOIN blobs b ON b.sha256 = i.blob_sha256 WHERE i.path = 'acme.widgets/1.2.3/acme.widgets.1.2.3.snupkg'`); got != sha512hex(sym) {
		t.Errorf("the stored symbol package differs from the pushed one: %s", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM items i JOIN package_versions pv ON pv.id = i.version_id WHERE i.path = 'acme.widgets/1.2.3/acme.widgets.1.2.3.snupkg' AND NOT i.generated`); got != 1 {
		t.Errorf("the symbol package is not an item of the version (%d)", got)
	}
	if got := e.st.Text(`SELECT (sp.item_id = i.id AND sp.indexed_at IS NULL AND sp.index_error IS NULL)::text FROM nuget_symbol_package sp
		JOIN items i ON i.path = 'acme.widgets/1.2.3/acme.widgets.1.2.3.snupkg'`); got != "true" {
		t.Errorf("symbol package row: %q", got)
	}
	if e.symbolPackages("nuget-local") != 1 || e.files("nuget-local", "acme.widgets", "1.2.3") != 3 {
		t.Errorf("rows %d, files %d", e.symbolPackages("nuget-local"), e.files("nuget-local", "acme.widgets", "1.2.3"))
	}
	if e.rev("nuget-local", "acme.widgets") <= rev {
		t.Error("the symbol push did not raise the package revision (generic writer rule, Q-ARCH-212)")
	}
	if got := e.st.Text(`SELECT j.queue || '|' || j.dedup_mode || '|' || j.state::text || '|' || (j.dedup_key = 'nuget-symbol-index:' || sp.version_id)::text || '|' || (j.args->>'version_id' = sp.version_id::text)::text
		FROM job j, nuget_symbol_package sp WHERE j.kind = 'nuget-symbol-index'`); got != "index|exclusive|queued|true|true" {
		t.Errorf("job = %q", got)
	}
	// R-NUGET-020: the push extracts nothing
	if got := e.st.Number(`SELECT count(*) FROM nuget_symbol_file`); got != 0 {
		t.Errorf("%d symbol files after the push", got)
	}
	if got := e.events("package.published", "nuget-local", "acme.widgets/1.2.3"); got != 1 {
		t.Errorf("%d package.published events: the symbol push is no publication", got)
	}
}

// TestR_NUGET_020_TheSymbolPushIsListedInTheServiceIndex: the resource is listed on a local
// repository now that it works.
func TestR_NUGET_020_TheSymbolPushIsListedInTheServiceIndex(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	ans := e.st.Do(a, "GET", "/api/nuget/it/nuget-local/v3/index.json", e.tokDev, nil)
	if ans.Status != 200 || !strings.Contains(string(ans.Body), `"SymbolPackagePublish/4.9.0"`) || !strings.Contains(string(ans.Body), `/api/nuget/it/nuget-local/v3/symbolpackage"`) {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
}

// TestR_NUGET_020_ASymbolPushNeedsThePackageFirst: before the .nupkg the symbol package is 404 and
// leaves nothing; after it, 201; the same bytes again are 201 on any replica and change nothing;
// other bytes are 409 nuget_symbol_package_exists and the stored package stays.
func TestR_NUGET_020_ASymbolPushNeedsThePackageFirst(t *testing.T) {
	e := newWriteEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	sym := snupkg(t, "Acme.Widgets", "1.5.1", "", nil)
	if ans := e.pushSymbols(a, "nuget-local", e.tokDev, sym); ans.Status != 404 {
		t.Fatalf("before the package: %d %s", ans.Status, ans.Body)
	}
	if e.symbolPackages("nuget-local") != 0 || e.indexJobs("") != 0 || e.st.Number(`SELECT count(*) FROM items WHERE kind = 'file'`) != 0 ||
		e.st.Number(`SELECT count(*) FROM packages`) != 0 {
		t.Fatal("a symbol push before the package left rows behind")
	}
	e.mustPush(a, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.5.1"}))
	for i, r := range []*testenv.Replica{a, b, a} {
		if ans := e.pushSymbols(r, "nuget-local", e.tokDev, sym); ans.Status != 201 {
			t.Fatalf("push %d: %d %s", i, ans.Status, ans.Body)
		}
	}
	if e.symbolPackages("nuget-local") != 1 || e.indexJobs("") != 1 {
		t.Errorf("rows %d, jobs %d after a replay", e.symbolPackages("nuget-local"), e.indexJobs(""))
	}
	other := snupkg(t, "Acme.Widgets", "1.5.1", "-rebuilt", nil)
	if ans := e.pushSymbols(b, "nuget-local", e.tokDev, other); ans.Status != 409 || code(t, ans) != "nuget_symbol_package_exists" {
		t.Fatalf("other bytes: %d %s", ans.Status, ans.Body)
	}
	if got := e.st.Text(`SELECT encode(b.sha512, 'hex') FROM items i JOIN blobs b ON b.sha256 = i.blob_sha256 WHERE i.path = 'acme.widgets/1.5.1/acme.widgets.1.5.1.snupkg'`); got != sha512hex(sym) {
		t.Error("the stored symbol package changed")
	}
	// another version of the ID has no package: 404
	if ans := e.pushSymbols(a, "nuget-local", e.tokDev, snupkg(t, "Acme.Widgets", "1.5.2", "", nil)); ans.Status != 404 {
		t.Errorf("another version: %d %s", ans.Status, ans.Body)
	}
}

// TestR_NUGET_020_ARefusedSymbolPackageStoresNothing: a .dll inside, a legacy .symbols.nupkg, a .pdb
// that is no PDB and a package without the SymbolsPackage type are 400 nuget_invalid_package
// and leave no symbol row, no symbol item and no job.
func TestR_NUGET_020_ARefusedSymbolPackageStoresNothing(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	e.mustPush(a, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	items := e.st.Number(`SELECT count(*) FROM items`)
	for name, c := range map[string]struct {
		pkg  []byte
		want string
	}{
		"a dll inside": {snupkg(t, "Acme.Widgets", "1.0.0", "", map[string]string{
			"lib/net8.0/Acme.Widgets.pdb": portablePDB, "lib/net8.0/Acme.Widgets.dll": "MZ",
		}), "assemblies belong to the package"},
		"a legacy symbols nupkg": {nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0", description: "legacy"}), "Legacy .symbols.nupkg"},
		"a pdb that is none": {snupkg(t, "Acme.Widgets", "1.0.0", "", map[string]string{
			"lib/net8.0/Acme.Widgets.pdb": "plain text",
		}), "file signature"},
		"no archive": {[]byte("not a package"), "ZIP"},
	} {
		t.Run(name, func(t *testing.T) {
			ans := e.pushSymbols(a, "nuget-local", e.tokDev, c.pkg)
			if ans.Status != 400 || code(t, ans) != "nuget_invalid_package" || !strings.Contains(string(ans.Body), c.want) {
				t.Fatalf("%d %s", ans.Status, ans.Body)
			}
			line(t, ans)
		})
	}
	if e.symbolPackages("nuget-local") != 0 || e.indexJobs("") != 0 || e.st.Number(`SELECT count(*) FROM items`) != items {
		t.Fatal("a refused symbol package left rows behind")
	}
}

// TestR_NUGET_020_ASymbolPushNeedsDeployOnTheDeclaredID: a reader and a person without a grant are
// 403, an anonymous caller 401 with the Basic challenge; none of them learns whether the version
// exists, and nothing is stored.
func TestR_NUGET_020_ASymbolPushNeedsDeployOnTheDeclaredID(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	e.mustPush(a, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	for name, c := range map[string]struct {
		token  string
		status int
	}{"reader": {e.tokReader, 403}, "nobody": {e.tokNobody, 403}, "anonymous": {"", 401}} {
		for _, version := range []string{"1.0.0", "9.9.9"} {
			ans := e.pushSymbols(a, "nuget-local", c.token, snupkg(t, "Acme.Widgets", version, "", nil))
			if ans.Status != c.status {
				t.Errorf("%s, version %s: %d %s", name, version, ans.Status, ans.Body)
			}
			if c.status == 401 && ans.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
				t.Errorf("%s: challenge %q", name, ans.Header.Get("WWW-Authenticate"))
			}
		}
	}
	if e.symbolPackages("nuget-local") != 0 || e.indexJobs("") != 0 {
		t.Fatal("a refused caller stored symbols")
	}
	if ans := e.pushSymbols(a, "nuget-local", e.tokPeer, snupkg(t, "Acme.Widgets", "1.0.0", "", nil)); ans.Status != 201 {
		t.Errorf("the peer who holds deploy: %d %s", ans.Status, ans.Body)
	}
}

// TestR_NUGET_020_ASymbolPackageGoesWithItsVersion: unlisting leaves the symbol package, the hard
// delete of a version removes its item and its row, and the number answers 404 for symbols
// afterwards (R-NUGET-007).
func TestR_NUGET_020_ASymbolPackageGoesWithItsVersion(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	for _, repo := range []string{"nuget-local", "nuget-hard"} {
		e.mustPush(a, repo, e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
		if ans := e.pushSymbols(a, repo, e.tokDev, snupkg(t, "Acme.Widgets", "1.0.0", "", nil)); ans.Status != 201 {
			t.Fatalf("%s: %d %s", repo, ans.Status, ans.Body)
		}
		if ans := e.send(a, "DELETE", repo, "acme.widgets/1.0.0", e.tokDev); ans.Status != 204 {
			t.Fatalf("%s delete: %d %s", repo, ans.Status, ans.Body)
		}
	}
	if e.symbolPackages("nuget-local") != 1 || e.files("nuget-local", "acme.widgets", "1.0.0") != 3 {
		t.Errorf("unlist: rows %d, files %d, want the symbol package to stay", e.symbolPackages("nuget-local"), e.files("nuget-local", "acme.widgets", "1.0.0"))
	}
	if e.symbolPackages("nuget-hard") != 0 || e.files("nuget-hard", "acme.widgets", "1.0.0") != 0 {
		t.Errorf("hard delete: rows %d, files %d", e.symbolPackages("nuget-hard"), e.files("nuget-hard", "acme.widgets", "1.0.0"))
	}
	if ans := e.pushSymbols(a, "nuget-hard", e.tokDev, snupkg(t, "Acme.Widgets", "1.0.0", "", nil)); ans.Status != 404 {
		t.Errorf("symbols of a deleted version: %d %s", ans.Status, ans.Body)
	}
}

// TestR_NUGET_020_TheIndexingJobRunsOnAWorkerAndRecordsWhatItFound: with a worker the queued job
// succeeds on the index queue and records the state of the package; the package stays pending
// (indexed_at unset) because the symbol server that serves keys is R-NUGET-021. The job of a
// package whose version was deleted meanwhile succeeds with state gone (Q-NUGET-60).
func TestR_NUGET_020_TheIndexingJobRunsOnAWorkerAndRecordsWhatItFound(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	e.mustPush(a, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	e.mustPush(a, "nuget-hard", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"}))
	for _, repo := range []string{"nuget-local", "nuget-hard"} {
		if ans := e.pushSymbols(a, repo, e.tokDev, snupkg(t, "Acme.Widgets", "1.0.0", "", nil)); ans.Status != 201 {
			t.Fatalf("%s: %d %s", repo, ans.Status, ans.Body)
		}
	}
	// With the web replica alone nothing claims the jobs.
	if e.indexJobs("queued") != 2 {
		t.Fatalf("%d queued jobs, want 2", e.indexJobs("queued"))
	}
	// The hard repository's version goes before the worker starts: its symbol rows cascade away.
	if ans := e.send(a, "DELETE", "nuget-hard", "acme.widgets/1.0.0", e.tokDev); ans.Status != 204 {
		t.Fatalf("delete: %d %s", ans.Status, ans.Body)
	}
	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-a", Role: "worker"})
	testenv.WaitFor(t, "the indexing jobs", 30*time.Second, func() bool { return e.indexJobs("succeeded") == 2 })
	if got := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'nuget-symbol-index' AND result->>'state' = 'stored' AND (result->>'files')::int = 0 AND args->>'repository_id' = (SELECT id::text FROM repositories WHERE key = 'nuget-local')`); got != 1 {
		t.Errorf("%d jobs recorded the stored package", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM job WHERE kind = 'nuget-symbol-index' AND result->>'state' = 'gone'`); got != 1 {
		t.Errorf("%d jobs recorded the gone package", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM nuget_symbol_package WHERE indexed_at IS NULL`); got != 1 {
		t.Errorf("%d pending symbol packages, want the stored one to stay pending", got)
	}
}
