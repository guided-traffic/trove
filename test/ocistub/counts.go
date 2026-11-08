package ocistub

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Entry is the number of requests that had the same method, host, path, query, conditionality and credential scheme. Path is
// the path as the client sent it, Host the Host header, which tells the registry, the realm and the CDN apart. Name and
// Reference are the repository and the tag or digest the request addressed (for a CDN request, the repository the signed URL
// names and the digest). Statuses counts the answers by status code; an answer that was cut ("reset", a client that left)
// is counted under "0".
type Entry struct {
	Method      string         `json:"method"`
	Host        string         `json:"host"`
	Path        string         `json:"path"`
	Query       string         `json:"query,omitempty"`
	Conditional bool           `json:"conditional"`
	Auth        string         `json:"auth"`             // none, bearer, basic or other
	Accept      []string       `json:"accept,omitempty"` // the media types of the Accept header lines, sorted, without q values
	Kind        string         `json:"kind"`
	Name        string         `json:"name,omitempty"`
	Reference   string         `json:"reference,omitempty"`
	Count       int            `json:"count"`
	Statuses    map[string]int `json:"statuses,omitempty"`
}

// Counts is a snapshot of the counters. Control requests are not counted.
type Counts struct {
	Entries []Entry `json:"entries"`
}

type countKey struct {
	method, host, path, query, auth, accept string
	conditional                             bool
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

func (e Entry) status(codes ...int) int {
	n := 0
	for _, c := range codes {
		n += e.Statuses[strconv.Itoa(c)]
	}
	return n
}

// Requests is the number of requests with a method for a path as the client wrote it, on any host.
func (c Counts) Requests(method, path string) int {
	return c.sum(func(e Entry) bool { return e.Method == method && e.Path == path })
}

// OfKind is the number of requests of a kind with a method (empty: any), on any host.
func (c Counts) OfKind(kind, method string) int {
	return c.sum(func(e Entry) bool { return e.Kind == kind && (method == "" || e.Method == method) })
}

// answered is the number of requests of an entry that were not a 401 challenge. A client that runs the token handshake sends a
// request, is challenged and sends it again with the token; the manifest counters count the second one only, because a challenge
// costs the upstream no pull (the quota of Docker Hub runs after the authorization too).
func (e Entry) answered() int { return e.Count - e.status(401) }

// ManifestGets is the number of GET requests for a manifest by tag or digest of a repository that were not answered with a 401
// challenge; the quota of Docker Hub counts these and no other request. An empty reference sums them.
func (c Counts) ManifestGets(name, reference string) int {
	return c.manifestRequests(name, reference, "GET")
}

// ManifestHeads is the number of HEAD requests for a manifest of a repository that were not answered with a 401 challenge.
func (c Counts) ManifestHeads(name, reference string) int {
	return c.manifestRequests(name, reference, "HEAD")
}

func (c Counts) manifestRequests(name, reference, method string) int {
	n := 0
	for _, e := range c.Entries {
		if e.Kind == KindManifest && e.Method == method && e.Name == name && (reference == "" || e.Reference == reference) {
			n += e.answered()
		}
	}
	return n
}

// ManifestEntries are the counter entries of the manifest requests of a repository and reference (empty: any) with a method
// (empty: any), challenged or not, with the Accept types each carried: the means to show that a client sent the types R-OCI-022
// asks for.
func (c Counts) ManifestEntries(name, reference, method string) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if e.Kind == KindManifest && e.Name == name && (reference == "" || e.Reference == reference) && (method == "" || e.Method == method) {
			out = append(out, e)
		}
	}
	return out
}

// BlobFetches is the number of GET requests for a blob that delivered the bytes (status 200 or 206), on the registry host or on
// the CDN host; a redirect is not a fetch. A pull "records each layer once" when this is 1 for every layer of the image.
func (c Counts) BlobFetches(digest string) int {
	n := 0
	for _, e := range c.Entries {
		if (e.Kind == KindBlob || e.Kind == KindCDN) && e.Method == "GET" && e.Reference == digest {
			n += e.status(200, 206)
		}
	}
	return n
}

// BlobRequests is the number of requests with a method (empty: any) for a blob on the registry host of a repository (empty: any).
func (c Counts) BlobRequests(name, digest, method string) int {
	return c.sum(func(e Entry) bool {
		return e.Kind == KindBlob && (name == "" || e.Name == name) && e.Reference == digest && (method == "" || e.Method == method)
	})
}

// CDNRequests is the number of requests the CDN host saw for a digest (empty: any digest).
func (c Counts) CDNRequests(digest string) int {
	return c.sum(func(e Entry) bool { return e.Kind == KindCDN && (digest == "" || e.Reference == digest) })
}

// TagLists is the number of tags list requests of a repository (empty: any).
func (c Counts) TagLists(name string) int {
	return c.sum(func(e Entry) bool { return e.Kind == KindTags && (name == "" || e.Name == name) })
}

// Catalogs is the number of catalog requests.
func (c Counts) Catalogs() int { return c.OfKind(KindCatalog, "") }

// ReferrersRequests is the number of referrers API requests of a repository (empty: any).
func (c Counts) ReferrersRequests(name string) int {
	return c.sum(func(e Entry) bool { return e.Kind == KindReferrers && (name == "" || e.Name == name) })
}

// TokenRequests is the number of requests the realm saw, with a credential scheme (empty: any).
func (c Counts) TokenRequests(auth string) int {
	return c.sum(func(e Entry) bool { return e.Kind == KindToken && (auth == "" || e.Auth == auth) })
}

// RepositoryRequests is the number of requests for a repository, of any kind.
func (c Counts) RepositoryRequests(name string) int {
	return c.sum(func(e Entry) bool { return e.Name == name })
}

// WithStatus is the number of requests answered with a status code.
func (c Counts) WithStatus(code int) int {
	n := 0
	for _, e := range c.Entries {
		n += e.status(code)
	}
	return n
}

// OnHost is the number of requests whose Host header was host.
func (c Counts) OnHost(host string) int {
	return c.sum(func(e Entry) bool { return e.Host == host })
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

// AuthSchemes lists the credential schemes ("none", "bearer", "basic", "other") requests on a host carried, sorted.
func (c Counts) AuthSchemes(host string) []string {
	seen := map[string]bool{}
	for _, e := range c.Entries {
		if e.Host == host {
			seen[e.Auth] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Violations lists what breaks "at most maxManifests manifest GET requests per repository and reference and at most maxBlobs
// blob fetches per digest", one line each, sorted. A manifest request answered with a 401 challenge is not counted.
func (c Counts) Violations(maxManifests, maxBlobs int) []string {
	manifests, blobs := map[string]int{}, map[string]int{}
	for _, e := range c.Entries {
		switch {
		case e.Kind == KindManifest && e.Method == "GET":
			manifests[e.Name+" "+e.Reference] += e.answered()
		case (e.Kind == KindBlob || e.Kind == KindCDN) && e.Method == "GET":
			blobs[e.Reference] += e.status(200, 206)
		}
	}
	var out []string
	for k, n := range manifests {
		if n > maxManifests {
			out = append(out, fmt.Sprintf("manifest %s requested %d times, at most %d", k, n, maxManifests))
		}
	}
	for k, n := range blobs {
		if n > maxBlobs {
			out = append(out, fmt.Sprintf("blob %s fetched %d times, at most %d", k, n, maxBlobs))
		}
	}
	sort.Strings(out)
	return out
}

// Summary is a one-line description for test failures.
func (c Counts) Summary() string {
	m, b := c.OfKind(KindManifest, ""), c.OfKind(KindBlob, "")+c.OfKind(KindCDN, "")
	return fmt.Sprintf("%d requests: %d manifest, %d blob, %d token, %d other", c.Total(), m, b, c.OfKind(KindToken, ""), c.Total()-m-b-c.OfKind(KindToken, ""))
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
