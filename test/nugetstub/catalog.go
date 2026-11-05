package nugetstub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The catalog (Catalog/3.0.0): an append-only record of every package operation, for the mirror's discovery (R-REPO-051,
// R-REPO-059). It starts with one PackageDetails record per version of the corpus, at the time the version was published, and
// grows with every runtime change: Publish, Restore and SetListed add PackageDetails, Drop adds PackageDelete. A runtime
// commit opens a page of its own, so that a test can tell the page a pass has read from the one that is new; the records of the
// corpus are cut into pages of Config.CatalogPageSize records at commit boundaries, and a commit never spans two pages.
//
// The service index lists the catalog only while Mode.Catalog is set, and every catalog path answers 404 otherwise.

// DefaultCatalogPageSize is the number of records of a catalog page of the corpus, the size nuget.org uses.
const DefaultCatalogPageSize = 550

const (
	catalogPath      = "/v3/catalog/"
	catalogIndexPath = catalogPath + "index.json"
	catalogPagePath  = catalogPath + "page"
	catalogDataPath  = catalogPath + "data/"
)

// catalogRecord is one record: an operation on a version at a commit.
type catalogRecord struct {
	stamp   time.Time
	id      string // the ID as the corpus has it
	version string
	deleted bool
	page    int
}

// catalogLog is the catalog of a server, in the order of its commits.
type catalogLog struct {
	records []catalogRecord
	pages   int
}

func (l *catalogLog) last() time.Time {
	if len(l.records) == 0 {
		return time.Time{}
	}
	return l.records[len(l.records)-1].stamp
}

// initialCatalog is the catalog of a corpus that was never changed.
func initialCatalog(c *Corpus, pageSize int) *catalogLog {
	var recs []catalogRecord
	for _, k := range c.IDs() {
		p, _ := c.Get(k)
		for _, v := range p.Versions {
			recs = append(recs, catalogRecord{stamp: v.Published.UTC(), id: p.ID, version: v.Version})
		}
	}
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		switch {
		case !a.stamp.Equal(b.stamp):
			return a.stamp.Before(b.stamp)
		case !strings.EqualFold(a.id, b.id):
			return strings.ToLower(a.id) < strings.ToLower(b.id)
		}
		return compareVersions(a.version, b.version) < 0
	})
	l := &catalogLog{}
	inPage := 0
	for i := range recs {
		newCommit := i == 0 || !recs[i].stamp.Equal(recs[i-1].stamp)
		if i == 0 || newCommit && inPage >= pageSize {
			l.pages++
			inPage = 0
		}
		recs[i].page = l.pages - 1
		inPage++
	}
	l.records = recs
	return l
}

// appendCommitLocked adds the records of one commit on a page of their own. The stamp is the later of the wanted time and one
// millisecond after the last commit, so that commits only ever get later.
func (s *Server) appendCommitLocked(want time.Time, recs ...catalogRecord) {
	l := s.catalog
	stamp := want.UTC().Truncate(time.Millisecond)
	if last := l.last(); !stamp.After(last) {
		stamp = last.Add(time.Millisecond)
	}
	l.pages++
	for _, r := range recs {
		r.stamp, r.page = stamp, l.pages-1
		l.records = append(l.records, r)
	}
}

// CatalogLen is the number of records and pages of the catalog now.
func (s *Server) CatalogLen() (records, pages int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.catalog.records), s.catalog.pages
}

func catalogStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.0000000Z") }

func catalogFile(r catalogRecord) string {
	return r.stamp.UTC().Format("2006.01.02.15.04.05.000") + "/" + strings.ToLower(r.id) + "." + r.version + ".json"
}

func commitID(t time.Time) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", t.UnixMilli()) }

func (s *Server) catalogIndexDoc(b bases) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.catalog
	last := make([]time.Time, l.pages)
	count := make([]int, l.pages)
	for _, r := range l.records {
		last[r.page] = r.stamp
		count[r.page]++
	}
	items := make([]map[string]any, l.pages)
	for i := range items {
		items[i] = map[string]any{
			"@id": fmt.Sprintf("%s%s%d.json", b.api, catalogPagePath, i), "@type": "CatalogPage",
			"commitId": commitID(last[i]), "commitTimeStamp": catalogStamp(last[i]), "count": count[i],
		}
	}
	doc := map[string]any{
		"@id": b.api + catalogIndexPath, "@type": []string{"CatalogRoot", "AppendOnlyCatalog", "Permalink"},
		"commitId": commitID(l.last()), "commitTimeStamp": catalogStamp(l.last()), "count": l.pages, "items": items,
	}
	out, _ := json.Marshal(doc)
	return out
}

func (s *Server) catalogPageDoc(b bases, n int) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 || n >= s.catalog.pages {
		return nil, false
	}
	var items []map[string]any
	var last time.Time
	for _, r := range s.catalog.records {
		if r.page != n {
			continue
		}
		kind := "nuget:PackageDetails"
		if r.deleted {
			kind = "nuget:PackageDelete"
		}
		items = append(items, map[string]any{
			"@id": b.api + catalogDataPath + catalogFile(r), "@type": kind, "commitId": commitID(r.stamp),
			"commitTimeStamp": catalogStamp(r.stamp), "nuget:id": r.id, "nuget:version": r.version,
		})
		last = r.stamp
	}
	out, _ := json.Marshal(map[string]any{
		"@id": fmt.Sprintf("%s%s%d.json", b.api, catalogPagePath, n), "@type": "CatalogPage",
		"commitId": commitID(last), "commitTimeStamp": catalogStamp(last), "count": len(items),
		"parent": b.api + catalogIndexPath, "items": items,
	})
	return out, true
}

// leafOfLocked finds the record a leaf path ("<stamp>/<lower id>.<version>.json") names.
func (s *Server) leafOfLocked(rest string) (catalogRecord, bool) {
	for _, r := range s.catalog.records {
		if catalogFile(r) == rest {
			return r, true
		}
	}
	return catalogRecord{}, false
}

func (s *Server) catalogLeafDoc(b bases, rest string) ([]byte, bool) {
	s.mu.Lock()
	r, ok := s.leafOfLocked(rest)
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	leaf := map[string]any{
		"@id": b.api + catalogDataPath + rest, "catalog:commitId": commitID(r.stamp), "catalog:commitTimeStamp": catalogStamp(r.stamp),
		"id": r.id, "published": iso(r.stamp), "version": r.version,
	}
	if r.deleted {
		leaf["@type"] = []string{"PackageDelete", "catalog:Permalink"}
	} else {
		leaf["@type"] = []string{"PackageDetails", "catalog:Permalink"}
		leaf["verbatimVersion"] = r.version
		leaf["isPrerelease"] = isPrerelease(r.version)
		if p, _, ok := s.view(r.id); ok {
			if i := p.find(r.version); i >= 0 {
				v := p.Versions[i]
				leaf["listed"] = v.Listed
				leaf["packageContent"] = nupkgURL(b, p, v.Version)
				leaf["packageSize"] = len(s.build(p, v).nupkg)
				leaf["authors"] = "Contoso"
				leaf["description"] = p.Description
			}
		}
	}
	out, _ := json.Marshal(leaf)
	return out, true
}

// serveCatalog answers the catalog routes; they are 404 while the service index does not list the catalog.
func (s *Server) serveCatalog(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) {
	if !m.Catalog {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var (
		body []byte
		ok   bool
	)
	switch rt.kind {
	case KindCatalogIndex:
		body, ok = s.catalogIndexDoc(b), true
	case KindCatalogPage:
		body, ok = s.catalogPageDoc(b, rt.page)
	default:
		body, ok = s.catalogLeafDoc(b, rt.version)
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.writeDocument(w, r, body, s.indexTime(), false, false)
}

// NewCorpus returns an empty package set, to be filled with Add: a test builds the catalog it needs from it.
func NewCorpus() *Corpus { return &Corpus{packages: map[string]*Package{}} }

// Add adds a package, replacing one of the same ID.
func (c *Corpus) Add(p *Package) { c.add(p) }

// AddVersion adds a listed version to the package, published at the given time; it reports false for a version that is there.
func (p *Package) AddVersion(version string, published time.Time) bool {
	return p.insert(Version{Version: version, Published: published, Listed: true})
}
