//go:build integration

package mirror

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

// NuGet discovery against the counting NuGet stub (E6b.3; docs/spec/formats/nuget.md "Mirror discovery", R-REPO-051).

var nugetEpoch = time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC)

// nugetCorpus is Acme.A (1.0.0, 2.0.0), Acme.B (1.0.0) and Other.C (1.0.0), one commit each.
func nugetCorpus() *nugetstub.Corpus {
	c := nugetstub.NewCorpus()
	for i, spec := range []struct {
		id       string
		versions []string
	}{{"Acme.A", []string{"1.0.0", "2.0.0"}}, {"Acme.B", []string{"1.0.0"}}, {"Other.C", []string{"1.0.0"}}} {
		p := &nugetstub.Package{ID: spec.id, Description: spec.id}
		for _, v := range spec.versions {
			p.AddVersion(v, nugetEpoch.Add(time.Duration(i)*24*time.Hour))
		}
		c.Add(p)
	}
	return c
}

// newNugetRig starts a rig whose upstream is the NuGet stub over the corpus: the service index and the registrations on
// 127.0.0.1, the flat container and the search on localhost, which the remote admits as an extra host.
func newNugetRig(t *testing.T, c *nugetstub.Corpus, mode nugetstub.Mode, o rigOptions) (*rig, *nugetstub.Server) {
	t.Helper()
	stub, err := nugetstub.New(nugetstub.Config{Corpus: c, CatalogPageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := stub.SetMode(mode); err != nil {
		t.Fatal(err)
	}
	o.format, o.handler, o.indexPath = "nuget", stub, "v3/index.json"
	remote := map[string]any{"extraHosts": []any{map[string]any{"pattern": "localhost", "sendCredentials": false}}}
	for k, v := range o.remote {
		remote[k] = v
	}
	o.remote = remote
	return newRig(t, o), stub
}

// nugetPath is the path of a version's .nupkg as the plan names it.
func nugetPath(id, version string) string {
	l := strings.ToLower(id)
	return l + "/" + version + "/" + l + "." + version + ".nupkg"
}

// nupkgsOf lists the .nupkg files the stub was asked for.
func nupkgsOf(hits []hit) (out []string) {
	for _, h := range hits {
		if h.tarball() {
			out = append(out, strings.TrimPrefix(h.name(), "/v3-flatcontainer/"))
		}
	}
	sort.Strings(out)
	return out
}

func countPath(hits []hit, sub string) (n int) {
	for _, h := range hits {
		if strings.Contains(h.name(), sub) {
			n++
		}
	}
	return n
}

func TestR_REPO_051_NugetCatalogFeed(t *testing.T) {
	r, stub := newNugetRig(t, nugetCorpus(), nugetstub.Mode{Catalog: true}, rigOptions{include: []string{"Acme.*"}})
	run, web := r.runner("runner"), r.web("web")

	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)
	full := r.traf.since(0)

	t.Run("R-REPO-051 the full pass reads the catalog index and the three pages and stores the .nupkg files of Acme.A and Acme.B", func(t *testing.T) {
		c := stub.Counts()
		if c.CatalogIndexes() != 1 || c.CatalogPages() != 3 || c.CatalogLeaves() != 0 {
			t.Errorf("catalog requests: %d index, %d pages, %d leaves; want the index and the three pages, no leaf", c.CatalogIndexes(), c.CatalogPages(), c.CatalogLeaves())
		}
		want := []string{"acme.a/1.0.0/acme.a.1.0.0.nupkg", "acme.a/2.0.0/acme.a.2.0.0.nupkg", "acme.b/1.0.0/acme.b.1.0.0.nupkg"}
		if got := nupkgsOf(full); !slices.Equal(got, want) {
			t.Errorf("nupkgs %v, want %v", got, want)
		}
		if st := r.summary(); st["method"] != "feed" || st["kind"] != "full" || st["itemsStored"] != float64(3) {
			t.Errorf("summary %v", st)
		}
	})

	t.Run("R-REPO-051 no request names Other.C", func(t *testing.T) {
		if n := countPath(full, "other.c"); n != 0 {
			t.Errorf("%d requests for Other.C", n)
		}
		if n := stub.Counts().Nupkgs("other.c") + stub.Counts().VersionLists("other.c") + stub.Counts().Registrations("other.c"); n != 0 {
			t.Errorf("the stub counted %d requests for Other.C", n)
		}
	})

	// The stub appends a page with Acme.A 3.0.0 and a PackageDelete of Acme.B 1.0.0.
	if err := stub.Publish("Acme.A", "3.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := stub.Drop("Acme.B", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	mark := r.traf.mark()
	stub.Reset()
	t1 := r.pass(run)
	r.waitPass(t1, "incremental", "complete", passWithin)
	inc := r.traf.since(mark)

	t.Run("R-REPO-051 the incremental pass reads only the new pages, stores acme.a.3.0.0.nupkg and refreshes the version list of Acme.B", func(t *testing.T) {
		c := stub.Counts()
		if c.CatalogIndexes() != 1 || c.CatalogPages() != 2 || c.CatalogLeaves() != 0 {
			t.Errorf("incremental catalog requests: %d index, %d pages, %d leaves; want the index and the two new pages", c.CatalogIndexes(), c.CatalogPages(), c.CatalogLeaves())
		}
		if got, want := nupkgsOf(inc), []string{"acme.a/3.0.0/acme.a.3.0.0.nupkg"}; !slices.Equal(got, want) {
			t.Errorf("nupkgs %v, want %v", got, want)
		}
		if n := countPath(inc, "/acme.b/index.json"); n == 0 {
			t.Error("the version list of Acme.B was not refreshed")
		}
		if !r.storedItem(nugetPath("Acme.A", "3.0.0")) {
			t.Error("acme.a 3.0.0 is not stored")
		}
	})

	t.Run("R-REPO-051 a mirrored version restores with the stub stopped: its nupkg and documents are served from the store", func(t *testing.T) {
		r.srv.Close()
		for _, path := range []string{
			"v3/flatcontainer/acme.a/index.json",
			"v3/registration-semver2/acme.a/index.json",
			"v3/flatcontainer/acme.a/1.0.0/acme.a.1.0.0.nupkg",
			"v3/flatcontainer/acme.a/3.0.0/acme.a.3.0.0.nupkg",
			"v3/flatcontainer/acme.a/2.0.0/acme.a.2.0.0.nupkg",
		} {
			if a := r.get(web, path); a.Status != http.StatusOK {
				t.Errorf("GET %s with the stub stopped: %d %.200s", path, a.Status, a.Body)
			}
		}
	})
}

func TestR_REPO_051_NugetSearchListingIsTruncatedAndSaysSo(t *testing.T) {
	// The cap of nuget.org scaled down: with take=1000 the search refuses a skip of 1000 or more for 1500 hits, so the listing
	// reads the rows 0 to 999. The include pattern keeps the number of packages that are filled small.
	c := nugetstub.NewCorpus()
	for i := range 1500 {
		p := &nugetstub.Package{ID: fmt.Sprintf("Acme.P%04d", i)}
		p.AddVersion("1.0.0", nugetEpoch.Add(time.Duration(i)*time.Minute))
		c.Add(p)
	}
	r, stub := newNugetRig(t, c, nugetstub.Mode{SearchSkipCap: 1000}, rigOptions{include: []string{"Acme.P000*"}})
	run := r.runner("runner")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-REPO-051 the pass takes the listing, completes with discoveryComplete false and a note naming 1000 of 1500", func(t *testing.T) {
		st := r.summary()
		if st["method"] != "listing" || st["discoveryComplete"] != false || st["discoveryNote"] != "listing truncated at 1000 of 1500" {
			t.Errorf("summary %v", st)
		}
		if n := stub.Counts().CatalogIndexes(); n != 0 {
			t.Errorf("%d catalog requests of a stub without a catalog", n)
		}
		if n := r.storedItems(); n != 10 {
			t.Errorf("%d artifacts stored, want the ten packages Acme.P0000 to Acme.P0009", n)
		}
	})

	t.Run("R-REPO-056 the status shows the truncation", func(t *testing.T) {
		web := r.web("web")
		a := r.api(web, http.MethodGet, "/api/v1/repositories/"+repoKey+"/mirror", nil)
		if a.Status != http.StatusOK {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		last, _ := a.JSON(t)["lastCompletePass"].(map[string]any)
		if last["discoveryComplete"] != false || last["discoveryNote"] != "listing truncated at 1000 of 1500" {
			t.Errorf("lastCompletePass %v", last)
		}
	})
}

func TestR_REPO_051_NugetExactIDsReadNoCatalogAndNoSearch(t *testing.T) {
	r, stub := newNugetRig(t, nugetCorpus(), nugetstub.Mode{Catalog: true}, rigOptions{
		include: []string{"Acme.A", "Acme.B"}, mirror: settings{"discovery": "names"},
	})
	run := r.runner("runner")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-REPO-051 the method names stores the exact IDs and sends no catalog and no search request", func(t *testing.T) {
		if st := r.summary(); st["method"] != "names" || st["packages"] != float64(2) {
			t.Errorf("summary %v", st)
		}
		c := stub.Counts()
		if c.CatalogIndexes()+c.CatalogPages()+c.Searches() != 0 {
			t.Errorf("%d catalog and %d search requests", c.CatalogIndexes()+c.CatalogPages(), c.Searches())
		}
		want := []string{"acme.a/1.0.0/acme.a.1.0.0.nupkg", "acme.a/2.0.0/acme.a.2.0.0.nupkg", "acme.b/1.0.0/acme.b.1.0.0.nupkg"}
		if got := nupkgsOf(r.traf.since(0)); !slices.Equal(got, want) {
			t.Errorf("nupkgs %v, want %v", got, want)
		}
	})
}
