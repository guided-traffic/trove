package nugetstub

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// PageSize is the number of leaves of a registration page.
	PageSize = 64
	// InlineBelow is the number of versions from which the pages of a registration index are not
	// inlined: 65 to 127 versions give two inlined pages, 128 and more give pages to fetch.
	InlineBelow = 128
)

// bases are the two hosts the stub's documents point to.
type bases struct{ api, content string }

// bases decides the addresses of the request's documents. With Config.PublicURL (and, for the
// second host, Config.ContentURL) they are fixed. Otherwise a request that reaches the stub under a
// loopback name gets the API host 127.0.0.1 and the content host localhost on the same port: two
// names for one listener, which is how a Go test runs two hosts without a resolver. Any other Host
// is used for both, so a deployment that wants two hosts sets Config.ContentURL.
func (s *Server) bases(r *http.Request) bases {
	api, content := strings.TrimSuffix(s.cfg.PublicURL, "/"), strings.TrimSuffix(s.cfg.ContentURL, "/")
	if api == "" {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil {
			host, port = r.Host, ""
		}
		host = strings.Trim(host, "[]")
		withPort := func(h string) string {
			if port == "" {
				return "https://" + h
			}
			return "https://" + net.JoinHostPort(h, port)
		}
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			api, content = withPort("127.0.0.1"), withPort("localhost")
		} else {
			api = "https://" + r.Host
		}
	}
	if content == "" {
		content = api
	}
	return bases{api: api, content: content}
}

// hostOf is the lowercase host:port of a base URL.
func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// wrongHost reports whether a request reaches its resource on the host that does not serve it.
func (b bases) wrongHost(kind, requestHost string) bool {
	api, content := hostOf(b.api), hostOf(b.content)
	if api == content {
		return false
	}
	host := strings.ToLower(requestHost)
	contentKind := kind == KindVersions || kind == KindNuspec || kind == KindNupkg || kind == KindSearch
	switch {
	case contentKind && host == api:
		return true
	case !contentKind && host == content:
		return true
	}
	return false
}

// resourceTypes are the @type values of each resource of the service index.
var (
	registrationPlainTypes = []string{"RegistrationsBaseUrl", "RegistrationsBaseUrl/3.0.0-beta", "RegistrationsBaseUrl/3.0.0-rc"}
	registrationGzTypes    = []string{"RegistrationsBaseUrl/3.4.0", "RegistrationsBaseUrl/3.6.0"}
	searchTypes            = []string{"SearchQueryService", "SearchQueryService/3.0.0-beta", "SearchQueryService/3.0.0-rc", "SearchQueryService/3.5.0"}
	flatTypes              = []string{"PackageBaseAddress/3.0.0"}
	catalogTypes           = []string{"Catalog/3.0.0"}
)

func (s *Server) indexDoc(b bases, m Mode) []byte {
	type resource struct {
		ID      string `json:"@id"`
		Type    string `json:"@type"`
		Comment string `json:"comment,omitempty"`
	}
	var res []resource
	add := func(base, path, comment string, types []string) {
		for _, t := range types {
			omitted := false
			for _, o := range m.OmitResources {
				if strings.HasPrefix(t, o) {
					omitted = true
				}
			}
			if !omitted {
				res = append(res, resource{ID: base + path, Type: t, Comment: comment})
			}
		}
	}
	add(b.api, registrationGz, "registrations, gzip", registrationGzTypes)
	add(b.api, registrationPath, "registrations, identity", registrationPlainTypes)
	add(b.content, flatPath, "package content", flatTypes)
	add(b.content, searchPath, "search", searchTypes)
	if m.Catalog {
		add(b.api, catalogIndexPath, "catalog", catalogTypes)
	}
	out, _ := json.Marshal(map[string]any{"version": "3.0.0", "resources": res})
	return out
}

func versionsDoc(p *Package) []byte {
	list := make([]string, len(p.Versions))
	for i, v := range p.Versions {
		list[i] = v.Version
	}
	out, _ := json.Marshal(map[string]any{"versions": list})
	return out
}

// registration documents -----------------------------------------------------------------------

type pageSpan struct{ lower, upper string }

func spans(p *Package) []pageSpan {
	var out []pageSpan
	for i := 0; i < len(p.Versions); i += PageSize {
		end := min(i+PageSize, len(p.Versions))
		out = append(out, pageSpan{p.Versions[i].Version, p.Versions[end-1].Version})
	}
	return out
}

func indexURL(b bases, hive string, p *Package) string {
	return b.api + hive + p.Lower() + "/index.json"
}

func leafURL(b bases, hive string, p *Package, version string) string {
	return b.api + hive + p.Lower() + "/" + version + ".json"
}

func nupkgURL(b bases, p *Package, version string) string {
	return b.content + flatPath + p.Lower() + "/" + version + "/" + p.Lower() + "." + version + ".nupkg"
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Server) catalogEntry(b bases, hive string, p *Package, v Version, badDigest bool) map[string]any {
	built := s.build(p, v)
	hash := built.hash
	if badDigest {
		hash = PackageHash(append(append([]byte(nil), built.nupkg...), 0))
	}
	entry := map[string]any{
		"@id":                      leafURL(b, hive, p, v.Version),
		"@type":                    "PackageDetails",
		"authors":                  "Contoso",
		"description":              p.Description,
		"id":                       p.ID,
		"version":                  v.Version,
		"listed":                   v.Listed,
		"published":                iso(v.Published),
		"packageContent":           nupkgURL(b, p, v.Version),
		"packageHash":              hash,
		"packageHashAlgorithm":     "SHA512",
		"packageSize":              len(built.nupkg),
		"requireLicenseAcceptance": false,
	}
	if len(v.Dependencies) > 0 {
		deps := make([]map[string]any, len(v.Dependencies))
		for i, d := range v.Dependencies {
			deps[i] = map[string]any{
				"@type": "PackageDependency", "id": d.ID, "range": d.Range,
				"registration": b.api + hive + strings.ToLower(d.ID) + "/index.json",
			}
		}
		entry["dependencyGroups"] = []map[string]any{{"@type": "PackageDependencyGroup", "targetFramework": ".NETStandard2.0", "dependencies": deps}}
	}
	addAdvisories(entry, b, hive, p, v)
	return entry
}

// addAdvisories sets the deprecation and the vulnerabilities of a version on a catalog entry or a
// search result, in the JSON-LD form nuget.org writes: each with an @id in the upstream's hive.
func addAdvisories(entry map[string]any, b bases, hive string, p *Package, v Version) {
	if d := v.Deprecation; d != nil {
		dep := map[string]any{
			"@id": leafURL(b, hive, p, v.Version) + "#deprecation", "@type": "deprecation", "reasons": d.Reasons, "message": d.Message,
		}
		if d.AlternateID != "" {
			dep["alternatePackage"] = map[string]any{
				"@id": b.api + hive + strings.ToLower(d.AlternateID) + "/index.json", "@type": "alternatePackage", "id": d.AlternateID, "range": d.AlternateRange,
			}
		}
		entry["deprecation"] = dep
	}
	if len(v.Vulnerabilities) > 0 {
		list := make([]map[string]any, len(v.Vulnerabilities))
		for i, x := range v.Vulnerabilities {
			list[i] = map[string]any{
				"@id": leafURL(b, hive, p, v.Version) + "#vulnerability/GitHub/" + strconv.Itoa(i+1), "@type": "Vulnerability",
				"advisoryUrl": x.AdvisoryURL, "severity": strconv.Itoa(x.Severity),
			}
		}
		entry["vulnerabilities"] = list
	}
}

func (s *Server) leafInPage(b bases, hive string, p *Package, v Version, badDigest bool) map[string]any {
	return map[string]any{
		"@id":             leafURL(b, hive, p, v.Version),
		"@type":           "Package",
		"commitId":        "00000000-0000-0000-0000-000000000000",
		"commitTimeStamp": iso(v.Published),
		"catalogEntry":    s.catalogEntry(b, hive, p, v, badDigest),
		"packageContent":  nupkgURL(b, p, v.Version),
		"registration":    indexURL(b, hive, p),
	}
}

func (s *Server) pageDoc(b bases, hive string, p *Package, sp pageSpan, withItems, badDigest bool) map[string]any {
	var items []map[string]any
	for _, v := range p.Versions {
		if compareVersions(v.Version, sp.lower) >= 0 && compareVersions(v.Version, sp.upper) <= 0 {
			items = append(items, s.leafInPage(b, hive, p, v, badDigest))
		}
	}
	id := b.api + hive + p.Lower() + "/page/" + sp.lower + "/" + sp.upper + ".json"
	doc := map[string]any{
		"@id": id, "@type": "catalog:CatalogPage", "count": len(items),
		"lower": sp.lower, "upper": sp.upper, "parent": indexURL(b, hive, p),
	}
	if withItems {
		doc["items"] = items
	}
	return doc
}

func (s *Server) registrationIndexDoc(b bases, hive string, p *Package, badDigest bool) []byte {
	inline := len(p.Versions) < InlineBelow
	pages := make([]map[string]any, 0, len(p.Versions)/PageSize+1)
	for _, sp := range spans(p) {
		page := s.pageDoc(b, hive, p, sp, inline, badDigest)
		if inline {
			page["@id"] = indexURL(b, hive, p) + "#page/" + sp.lower + "/" + sp.upper
		}
		pages = append(pages, page)
	}
	out, _ := json.Marshal(map[string]any{
		"@id": indexURL(b, hive, p), "@type": []string{"catalog:CatalogRoot", "PackageRegistration", "catalog:Permalink"},
		"count": len(pages), "items": pages,
	})
	return out
}

func (s *Server) registrationPageDoc(b bases, hive string, p *Package, sp pageSpan, badDigest bool) []byte {
	out, _ := json.Marshal(s.pageDoc(b, hive, p, sp, true, badDigest))
	return out
}

func (s *Server) registrationLeafDoc(b bases, hive string, p *Package, v Version) []byte {
	out, _ := json.Marshal(map[string]any{
		"@id": leafURL(b, hive, p, v.Version), "@type": "Package",
		"catalogEntry": leafURL(b, hive, p, v.Version), "listed": v.Listed,
		"packageContent": nupkgURL(b, p, v.Version), "published": iso(v.Published),
		"registration": indexURL(b, hive, p),
	})
	return out
}

// search ------------------------------------------------------------------------------------------

const maxTake = 1000

// exactIDField is the prefix of a query term that asks for one package ID.
const exactIDField = "packageid:" //nolint:misspell // the field name of the NuGet query syntax

// searchDoc answers a query of the search resource: q is matched as a case-insensitive substring of
// the ID or the description, the exact-ID field of the query syntax (exactIDField) restricts it to one ID. Packages with no listed
// version (no prerelease unless asked for) are left out.
func (s *Server) searchDoc(b bases, q url.Values) []byte {
	skip, _ := strconv.Atoi(q.Get("skip"))
	take, err := strconv.Atoi(q.Get("take"))
	if err != nil || take < 0 {
		take = 20
	}
	skip, take = max(skip, 0), min(take, maxTake)
	prerelease := q.Get("prerelease") == "true"
	term := strings.ToLower(strings.TrimSpace(q.Get("q")))
	exact := false
	if rest, ok := strings.CutPrefix(term, exactIDField); ok {
		term, exact = rest, true
	}

	var hits []map[string]any
	for _, k := range s.cfg.Corpus.IDs() {
		p, _, ok := s.view(k)
		if !ok || !matchesQuery(p, term, exact) {
			continue
		}
		if hit := searchHit(b, p, prerelease); hit != nil {
			hits = append(hits, hit)
		}
	}
	// packages published at runtime
	s.mu.Lock()
	var extra []string
	for k := range s.overlay {
		if _, ok := s.cfg.Corpus.Get(k); !ok {
			extra = append(extra, k)
		}
	}
	s.mu.Unlock()
	sort.Strings(extra)
	for _, k := range extra {
		if p, _, ok := s.view(k); ok && matchesQuery(p, term, exact) {
			if hit := searchHit(b, p, prerelease); hit != nil {
				hits = append(hits, hit)
			}
		}
	}
	total := len(hits)
	hits = hits[min(skip, total):]
	hits = hits[:min(take, len(hits))]
	out, _ := json.Marshal(map[string]any{
		"@context":  map[string]string{"@vocab": "http://schema.nuget.org/schema#", "@base": b.api + registrationGz},
		"totalHits": total, "data": hits,
	})
	return out
}

func matchesQuery(p *Package, term string, exact bool) bool {
	switch {
	case term == "":
		return true
	case exact:
		return p.Lower() == term
	}
	return strings.Contains(p.Lower(), term) || strings.Contains(strings.ToLower(p.Description), term)
}

// SearchOwner is the owner every search result of the stub names, and every result says verified,
// so that a test sees whether a remote keeps both (R-NUGET-016).
const SearchOwner = "contoso"

// VersionDownloads is the download count the stub's search gives the i-th version (from 0, in
// ascending order, unlisted versions included) of a package: 1000 times its position plus one.
// The total downloads of a result sum every version of the package, listed or not, as nuget.org's
// do, so the total is not the sum of the versions a result lists.
func VersionDownloads(i int) int64 { return int64(i+1) * 1000 }

func searchHit(b bases, p *Package, prerelease bool) map[string]any {
	var versions []map[string]any
	var latest *Version
	var total int64
	for i, v := range p.Versions {
		total += VersionDownloads(i)
		if !v.Listed || (!prerelease && isPrerelease(v.Version)) {
			continue
		}
		latest = &p.Versions[i]
		versions = append(versions, map[string]any{"version": v.Version, "downloads": VersionDownloads(i), "@id": leafURL(b, registrationGz, p, v.Version)})
	}
	if latest == nil {
		return nil
	}
	hit := map[string]any{
		"@id": indexURL(b, registrationGz, p), "@type": "Package", "registration": indexURL(b, registrationGz, p),
		"id": p.ID, "version": latest.Version, "description": p.Description, "summary": "", "title": p.ID,
		"authors": []string{"Contoso"}, "owners": []string{SearchOwner}, "tags": []string{}, "totalDownloads": total, "verified": true,
		"packageTypes": []map[string]string{{"name": "Dependency"}}, "versions": versions,
	}
	// The deprecation and the advisories of the version the result names, as nuget.org's.
	addAdvisories(hit, b, registrationGz, p, *latest)
	return hit
}
