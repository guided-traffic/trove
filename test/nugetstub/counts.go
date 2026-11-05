package nugetstub

import (
	"fmt"
	"sort"
	"strings"
)

// Kinds of request the stub tells apart.
const (
	KindIndex    = "index"     // /v3/index.json
	KindRegIndex = "reg-index" // registration index of one ID
	KindRegPage  = "reg-page"  // a registration page fetched on its own
	KindRegLeaf  = "reg-leaf"  // a registration leaf
	KindVersions = "versions"  // flat container version list
	KindNuspec   = "nuspec"    // flat container .nuspec
	KindNupkg    = "nupkg"     // flat container .nupkg
	KindSearch   = "search"
	KindOther    = "other"

	KindCatalogIndex = "catalog-index" // /v3/catalog/index.json
	KindCatalogPage  = "catalog-page"  // /v3/catalog/page<N>.json
	KindCatalogLeaf  = "catalog-leaf"  // /v3/catalog/data/<commit>/<id>.<version>.json
)

// Entry is the number of requests that had the same method, host, path, conditionality and
// credential scheme. Path is the escaped path as the client sent it. Host is the Host header, which
// tells the two hosts of the service index apart.
type Entry struct {
	Method      string `json:"method"`
	Host        string `json:"host"`
	Path        string `json:"path"`
	Conditional bool   `json:"conditional"`
	Auth        string `json:"auth"` // none, bearer, basic or other
	Kind        string `json:"kind"`
	ID          string `json:"id,omitempty"` // lowercase package ID
	Version     string `json:"version,omitempty"`
	Count       int    `json:"count"`
}

// Counts is a snapshot of the counters. Control requests are not counted.
type Counts struct {
	Entries []Entry `json:"entries"`
}

// Total is the number of requests.
func (c Counts) Total() int { return c.sum(func(Entry) bool { return true }) }

func (c Counts) sum(match func(Entry) bool) int {
	n := 0
	for _, e := range c.Entries {
		if match(e) {
			n += e.Count
		}
	}
	return n
}

// Nupkgs is the number of requests for the .nupkg files of a package, of every version.
func (c Counts) Nupkgs(id string) int {
	id = strings.ToLower(id)
	return c.sum(func(e Entry) bool { return e.Kind == KindNupkg && e.ID == id })
}

// Nupkg is the number of requests for one .nupkg file.
func (c Counts) Nupkg(id, version string) int {
	id = strings.ToLower(id)
	return c.sum(func(e Entry) bool { return e.Kind == KindNupkg && e.ID == id && e.Version == version })
}

// VersionLists is the number of requests for the flat container version list of a package.
func (c Counts) VersionLists(id string) int {
	id = strings.ToLower(id)
	return c.sum(func(e Entry) bool { return e.Kind == KindVersions && e.ID == id })
}

// Registrations is the number of requests for registration documents (index, pages and leaves)
// of a package.
func (c Counts) Registrations(id string) int {
	id = strings.ToLower(id)
	return c.sum(func(e Entry) bool {
		return (e.Kind == KindRegIndex || e.Kind == KindRegPage || e.Kind == KindRegLeaf) && e.ID == id
	})
}

// ConditionalRequests is the number of requests for the package's documents (everything but
// .nupkg files) that carried If-None-Match or If-Modified-Since.
func (c Counts) ConditionalRequests(id string) int {
	id = strings.ToLower(id)
	return c.sum(func(e Entry) bool { return e.ID == id && e.Kind != KindNupkg && e.Conditional })
}

// Searches is the number of search requests.
func (c Counts) Searches() int { return c.sum(func(e Entry) bool { return e.Kind == KindSearch }) }

// Requests is the number of requests with a method for a path as the client wrote it, on any host.
func (c Counts) Requests(method, path string) int {
	return c.sum(func(e Entry) bool { return e.Method == method && e.Path == path })
}

// Hosts lists the Host headers the stub saw, sorted.
func (c Counts) Hosts() []string {
	seen := map[string]bool{}
	for _, e := range c.Entries {
		seen[e.Host] = true
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// Violations lists what breaks "at most maxDocuments requests per document path and at most
// maxNupkgs per .nupkg path" (criterion E3.2 states one .nupkg request per package), one line
// each, sorted. A path is counted over every host, method, condition and credential.
func (c Counts) Violations(maxDocuments, maxNupkgs int) []string {
	docs, nupkgs := map[string]int{}, map[string]int{}
	for _, e := range c.Entries {
		if e.Kind == KindNupkg {
			nupkgs[e.Path] += e.Count
		} else {
			docs[e.Path] += e.Count
		}
	}
	var out []string
	for path, n := range docs {
		if n > maxDocuments {
			out = append(out, fmt.Sprintf("document %s requested %d times, at most %d", path, n, maxDocuments))
		}
	}
	for path, n := range nupkgs {
		if n > maxNupkgs {
			out = append(out, fmt.Sprintf("package %s requested %d times, at most %d", path, n, maxNupkgs))
		}
	}
	sort.Strings(out)
	return out
}

// Summary is a one-line description for test failures.
func (c Counts) Summary() string {
	nupkgs := c.sum(func(e Entry) bool { return e.Kind == KindNupkg })
	return fmt.Sprintf("%d requests: %d .nupkg, %d other", c.Total(), nupkgs, c.Total()-nupkgs)
}

type countKey struct {
	method, host, path, auth string
	conditional              bool
}

// authScheme classifies an Authorization header without keeping the credential.
func authScheme(header string) string {
	scheme, _, _ := strings.Cut(header, " ")
	switch strings.ToLower(scheme) {
	case "":
		return "none"
	case "bearer":
		return "bearer"
	case "basic":
		return "basic"
	}
	return "other"
}

// CatalogIndexes is the number of requests for the catalog index.
func (c Counts) CatalogIndexes() int {
	return c.sum(func(e Entry) bool { return e.Kind == KindCatalogIndex })
}

// CatalogPages is the number of requests for catalog pages.
func (c Counts) CatalogPages() int {
	return c.sum(func(e Entry) bool { return e.Kind == KindCatalogPage })
}

// CatalogLeaves is the number of requests for catalog leaves; a mirror's discovery sends none.
func (c Counts) CatalogLeaves() int {
	return c.sum(func(e Entry) bool { return e.Kind == KindCatalogLeaf })
}
