package nugetstub

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests of the stub's catalog and its search cap (R-REPO-051, R-REPO-059): the NuGet part of the counting stub upstream
// of the mirror's suites.

var catalogEpoch = time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC)

// acmeCorpus is the corpus of the criterion in nuget.md: Acme.A with two versions, Acme.B and Other.C, each committed on a
// day of its own.
func acmeCorpus() *Corpus {
	c := NewCorpus()
	for i, spec := range []struct {
		id       string
		versions []string
	}{{"Acme.A", []string{"1.0.0", "2.0.0"}}, {"Acme.B", []string{"1.0.0"}}, {"Other.C", []string{"1.0.0"}}} {
		p := &Package{ID: spec.id, Description: spec.id}
		for _, v := range spec.versions {
			p.AddVersion(v, catalogEpoch.Add(time.Duration(i)*24*time.Hour))
		}
		c.Add(p)
	}
	return c
}

type catalogIndexDoc struct {
	Count int `json:"count"`
	Items []struct {
		ID    string `json:"@id"`
		Stamp string `json:"commitTimeStamp"`
		Count int    `json:"count"`
	} `json:"items"`
}

type catalogPageDocT struct {
	Items []struct {
		ID      string `json:"@id"`
		Type    string `json:"@type"`
		Stamp   string `json:"commitTimeStamp"`
		NuID    string `json:"nuget:id"`
		Version string `json:"nuget:version"`
	} `json:"items"`
}

func (f *fixture) catalogURL() string {
	f.t.Helper()
	return f.resource("Catalog/3.0.0")
}

func TestR_REPO_059_TheServiceIndexListsTheCatalogOnlyWhileItIsSwitchedOn(t *testing.T) {
	f := newFixture(t, Config{Corpus: acmeCorpus()})
	var idx serviceIndex
	f.getJSON(f.api+"/v3/index.json", &idx)
	for _, r := range idx.Resources {
		if strings.HasPrefix(r.Type, "Catalog") {
			t.Fatalf("the default service index lists %s", r.Type)
		}
	}
	if r := f.get(f.api + "/v3/catalog/index.json"); r.status != http.StatusNotFound {
		t.Errorf("catalog index without the catalog: %d", r.status)
	}
	f.setMode(Mode{Catalog: true})
	if got, want := f.catalogURL(), f.api+"/v3/catalog/index.json"; got != want {
		t.Errorf("catalog resource %s, want %s", got, want)
	}
	if r := f.get(f.catalogURL()); r.status != http.StatusOK {
		t.Errorf("catalog index: %d", r.status)
	}
	f.setMode(Mode{Catalog: true, OmitResources: []string{"Catalog"}})
	f.getJSON(f.api+"/v3/index.json", &idx)
	if slices.ContainsFunc(idx.Resources, func(r struct {
		ID   string `json:"@id"`
		Type string `json:"@type"`
	},
	) bool {
		return strings.HasPrefix(r.Type, "Catalog")
	}) {
		t.Error("an omitted catalog resource is listed")
	}
}

func TestR_REPO_059_TheCatalogListsPagesCommitsAndLeavesInOrder(t *testing.T) {
	f := newFixture(t, Config{Corpus: acmeCorpus(), CatalogPageSize: 1})
	f.setMode(Mode{Catalog: true})
	var idx catalogIndexDoc
	f.getJSON(f.catalogURL(), &idx)
	if idx.Count != 3 || len(idx.Items) != 3 {
		t.Fatalf("index %+v: a page per commit at page size 1", idx)
	}
	var names []string
	var stamps []string
	for i, it := range idx.Items {
		var pg catalogPageDocT
		f.getJSON(it.ID, &pg)
		if len(pg.Items) != it.Count || len(pg.Items) == 0 {
			t.Fatalf("page %d: %d items, the index says %d", i, len(pg.Items), it.Count)
		}
		for _, item := range pg.Items {
			if item.Type != "nuget:PackageDetails" || item.Stamp != it.Stamp {
				t.Errorf("item %+v on a page of commit %s", item, it.Stamp)
			}
			names = append(names, item.NuID+" "+item.Version)
		}
		stamps = append(stamps, it.Stamp)
	}
	if want := []string{"Acme.A 1.0.0", "Acme.A 2.0.0", "Acme.B 1.0.0", "Other.C 1.0.0"}; !slices.Equal(names, want) {
		t.Errorf("items %v, want %v", names, want)
	}
	if !slices.IsSorted(stamps) || stamps[0] == stamps[1] {
		t.Errorf("commit stamps %v are not ascending", stamps)
	}
	// Two versions of Acme.A share one commit and one page: a commit never spans pages.
	var first catalogPageDocT
	f.getJSON(idx.Items[0].ID, &first)
	if len(first.Items) != 2 || first.Items[0].Stamp != first.Items[1].Stamp {
		t.Errorf("first page %+v", first.Items)
	}
}

func TestR_REPO_059_ACatalogPageHoldsManyCommitsAtTheConfiguredSize(t *testing.T) {
	f := newFixture(t, Config{Corpus: acmeCorpus(), CatalogPageSize: 3})
	f.setMode(Mode{Catalog: true})
	var idx catalogIndexDoc
	f.getJSON(f.catalogURL(), &idx)
	// Records 1 and 2 (Acme.A), 3 (Acme.B) fill a page; the commit of Other.C opens the next.
	if len(idx.Items) != 2 || idx.Items[0].Count != 3 || idx.Items[1].Count != 1 {
		t.Fatalf("%+v", idx)
	}
}

func TestR_REPO_059_ARuntimeChangeGrowsTheCatalogByAPageOfItsOwn(t *testing.T) {
	now := catalogEpoch.Add(-time.Hour) // before every commit of the corpus: the stamps still only get later
	f := newFixture(t, Config{Corpus: acmeCorpus(), Now: func() time.Time { return now }})
	f.setMode(Mode{Catalog: true})
	records, pages := f.stub.CatalogLen()
	if records != 4 || pages != 1 {
		t.Fatalf("%d records on %d pages", records, pages)
	}
	if err := f.stub.Publish("Acme.A", "3.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.stub.Drop("Acme.B", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	var idx catalogIndexDoc
	f.getJSON(f.catalogURL(), &idx)
	if len(idx.Items) != 3 {
		t.Fatalf("pages %+v: the corpus's page and one per change", idx.Items)
	}
	if !slices.IsSorted([]string{idx.Items[0].Stamp, idx.Items[1].Stamp, idx.Items[2].Stamp}) || idx.Items[1].Stamp == idx.Items[2].Stamp {
		t.Errorf("stamps %v are not strictly later", idx.Items)
	}
	var pub, del catalogPageDocT
	f.getJSON(idx.Items[1].ID, &pub)
	f.getJSON(idx.Items[2].ID, &del)
	if len(pub.Items) != 1 || pub.Items[0].Type != "nuget:PackageDetails" || pub.Items[0].NuID != "Acme.A" || pub.Items[0].Version != "3.0.0" {
		t.Errorf("publish page %+v", pub.Items)
	}
	if len(del.Items) != 1 || del.Items[0].Type != "nuget:PackageDelete" || del.Items[0].NuID != "Acme.B" || del.Items[0].Version != "1.0.0" {
		t.Errorf("delete page %+v", del.Items)
	}
	// The package of the dropped version is still there for the .nupkg: only the lists changed.
	if err := f.stub.Restore("Acme.B", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := f.stub.SetListed("Acme.A", "3.0.0", false); err != nil {
		t.Fatal(err)
	}
	if records, pages := f.stub.CatalogLen(); records != 8 || pages != 5 {
		t.Errorf("%d records on %d pages", records, pages)
	}
	if err := f.stub.Publish("Acme.A", "3.0.0", nil); err == nil {
		t.Error("a publish of a version that exists changed the catalog")
	} else if records, _ := f.stub.CatalogLen(); records != 8 {
		t.Errorf("a refused publish left %d records, want 8", records)
	}
}

func TestR_REPO_059_ALeafIsReadableAndCounted(t *testing.T) {
	f := newFixture(t, Config{Corpus: acmeCorpus(), CatalogPageSize: 1})
	f.setMode(Mode{Catalog: true})
	if err := f.stub.Drop("Acme.B", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	var idx catalogIndexDoc
	f.getJSON(f.catalogURL(), &idx)
	var details, deleted catalogPageDocT
	f.getJSON(idx.Items[0].ID, &details)
	f.getJSON(idx.Items[len(idx.Items)-1].ID, &deleted)

	var leaf struct {
		Type    []string `json:"@type"`
		ID      string   `json:"id"`
		Version string   `json:"version"`
		Size    int      `json:"packageSize"`
		Content string   `json:"packageContent"`
	}
	f.getJSON(details.Items[0].ID, &leaf)
	if leaf.Type[0] != "PackageDetails" || leaf.ID != "Acme.A" || leaf.Version != "1.0.0" || leaf.Size <= 0 || !strings.HasSuffix(leaf.Content, "/acme.a/1.0.0/acme.a.1.0.0.nupkg") {
		t.Errorf("details leaf %+v", leaf)
	}
	f.getJSON(deleted.Items[0].ID, &leaf)
	if leaf.Type[0] != "PackageDelete" || leaf.ID != "Acme.B" || leaf.Version != "1.0.0" {
		t.Errorf("delete leaf %+v", leaf)
	}
	if r := f.get(strings.Replace(details.Items[0].ID, "acme.a.1.0.0", "acme.a.9.9.9", 1)); r.status != http.StatusNotFound {
		t.Errorf("an unknown leaf: %d", r.status)
	}
	c := f.counts()
	if c.CatalogIndexes() != 1 || c.CatalogPages() != 2 || c.CatalogLeaves() != 3 {
		t.Errorf("catalog requests: %d index, %d pages, %d leaves", c.CatalogIndexes(), c.CatalogPages(), c.CatalogLeaves())
	}
	if c.Nupkgs("Acme.A") != 0 || c.sum(func(e Entry) bool { return e.Kind == KindCatalogLeaf && e.ID == "acme.b" && e.Version == "1.0.0" }) != 1 {
		t.Errorf("the leaf of Acme.B is not counted under its ID: %+v", c.Entries)
	}
}

func TestR_REPO_059_TheCatalogHonoursItsDelayClass(t *testing.T) {
	f := newFixture(t, Config{Corpus: acmeCorpus()})
	f.setMode(Mode{Catalog: true, DelayMS: map[string]int{ClassCatalog: 150}})
	start := time.Now()
	f.getJSON(f.catalogURL(), &catalogIndexDoc{})
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("answered after %v", d)
	}
}

// The search of the stub with a cap: a skip of the cap or more is refused; the hits stay the real number.
func TestR_REPO_059_TheSearchRefusesASkipFromItsCap(t *testing.T) {
	c := NewCorpus()
	for i := range 25 {
		p := &Package{ID: fmt.Sprintf("Acme.P%02d", i)}
		p.AddVersion("1.0.0", catalogEpoch)
		c.Add(p)
	}
	f := newFixture(t, Config{Corpus: c})
	f.setMode(Mode{SearchSkipCap: 10})
	search := f.resource("SearchQueryService")
	var res struct {
		TotalHits int `json:"totalHits"`
		Data      []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	f.getJSON(search+"?q=&take=5&skip=9", &res)
	if res.TotalHits != 25 || len(res.Data) != 5 {
		t.Errorf("skip 9: %d hits, %d rows", res.TotalHits, len(res.Data))
	}
	for _, skip := range []int{10, 24} {
		if r := f.get(fmt.Sprintf("%s?q=&take=5&skip=%d", search, skip)); r.status != http.StatusBadRequest {
			t.Errorf("skip %d: %d", skip, r.status)
		}
	}
	if err := f.stub.SetMode(Mode{SearchSkipCap: -1}); err == nil {
		t.Error("a negative cap was accepted")
	}
	if n := f.counts().Searches(); n != 3 {
		t.Errorf("searches %d: refused requests count too", n)
	}
}
