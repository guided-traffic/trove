package ansiblestub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The documents of the Galaxy v3 API, in the shapes the public Galaxy answers with (verified against it on 2026-11-10): relative
// links that start with "/", a download_url that is absolute on the host of the request, ascending creation times, newest version
// first in a list, and a version document that carries the plugin listing a client never reads (metadata.contents and the like),
// so that a remote's rewriting has something to drop.

const timeFormat = "2006-01-02T15:04:05.000000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeFormat) }

func collectionHref(ns, name string) string { return pluginRoot + "index/" + ns + "/" + name + "/" }
func versionsHref(ns, name string) string   { return collectionHref(ns, name) + "versions/" }
func versionHref(ns, name, v string) string { return versionsHref(ns, name) + v + "/" }

// collectionID is the stable UUID-shaped identifier of a collection.
func collectionID(ns, name string) string {
	h := sha256.Sum256([]byte("ansiblestub/" + ns + "." + name))
	x := hex.EncodeToString(h[:16])
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}

func notFound() map[string]any {
	return map[string]any{"errors": []map[string]string{{"status": "404", "code": "not_found", "title": "Not found."}}}
}

func unauthenticated() map[string]any {
	return map[string]any{"errors": []map[string]string{{"status": "401", "code": "not_authenticated", "title": "Authentication credentials were not provided."}}}
}

func forbidden() map[string]any {
	return map[string]any{"errors": []map[string]string{{"status": "403", "code": "permission_denied", "title": "You do not have permission to perform this action."}}}
}

// rootDocument is the API root.
func rootDocument(noV1 bool) map[string]any {
	versions := map[string]string{"v3": "v3/", "v1": "v1/"}
	if noV1 {
		delete(versions, "v1")
	}
	return map[string]any{"available_versions": versions, "current_version": "v3", "description": "trove counting stub of the Galaxy API", "server_version": "stub"}
}

// limitOffset reads the paging parameters: limit defaults to 100 and is clamped to 1..100, offset to 0 and up.
func limitOffset(q url.Values) (limit, offset int) {
	limit = 100
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = max(1, min(v, 100))
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

// pageLinks renders the four links of a v3 list. Each is relative to the host, with the same limit.
func pageLinks(path string, extra url.Values, count, limit, offset int) map[string]any {
	link := func(off int) string {
		q := url.Values{}
		for k, v := range extra {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(off))
		return path + "?" + q.Encode()
	}
	var prev, next any
	if offset > 0 {
		prev = link(max(0, offset-limit))
	}
	if offset+limit < count {
		next = link(offset + limit)
	}
	last := 0
	if count > 0 {
		last = (count - 1) / limit * limit
	}
	return map[string]any{"first": link(0), "previous": prev, "next": next, "last": link(last)}
}

func window(count, limit, offset int) (lo, hi int) {
	lo = min(offset, count)
	hi = min(lo+limit, count)
	return lo, hi
}

func collectionDocument(c *collectionDef) map[string]any {
	hv := c.highest()
	first, last := c.versions[0], c.versions[len(c.versions)-1]
	return map[string]any{
		"href": collectionHref(c.ns, c.name), "namespace": c.ns, "name": c.name, "deprecated": false,
		"versions_url":    versionsHref(c.ns, c.name),
		"highest_version": map[string]any{"href": versionHref(c.ns, c.name, hv.version), "version": hv.version},
		"created_at":      stamp(first.created()), "updated_at": stamp(last.created()),
	}
}

// indexDocument is a page of the collection index, ordered by name.
func indexDocument(cols []*collectionDef, q url.Values) map[string]any {
	limit, offset := limitOffset(q)
	lo, hi := window(len(cols), limit, offset)
	data := make([]any, 0, hi-lo)
	for _, c := range cols[lo:hi] {
		data = append(data, collectionDocument(c))
	}
	return map[string]any{
		"meta": map[string]any{"count": len(cols)}, "links": pageLinks(pluginRoot+"index/", nil, len(cols), limit, offset), "data": data,
	}
}

// versionsDocument is a page of a collection's version list, newest first.
func versionsDocument(c *collectionDef, q url.Values) map[string]any {
	limit, offset := limitOffset(q)
	lo, hi := window(len(c.versions), limit, offset)
	data := make([]any, 0, hi-lo)
	for i := lo; i < hi; i++ {
		v := c.versions[len(c.versions)-1-i]
		data = append(data, map[string]any{
			"version": v.version, "href": versionHref(c.ns, c.name, v.version), "created_at": stamp(v.created()), "updated_at": stamp(v.created()),
			"requires_ansible": v.requiresAnsible, "marks": []string{},
		})
	}
	return map[string]any{
		"meta": map[string]any{"count": len(c.versions)}, "links": pageLinks(versionsHref(c.ns, c.name), nil, len(c.versions), limit, offset), "data": data,
	}
}

// announced is what a version document says of its archive.
type announced struct {
	filename string
	sha256   string
	size     int64
}

// versionDocument is the document of one version. base is "https://host[:port]" of the request, so that download_url is absolute.
func versionDocument(base string, c *collectionDef, v *versionDef, a announced) map[string]any {
	deps := v.deps
	if deps == nil {
		deps = map[string]string{}
	}
	signatures := []map[string]any{}
	if v.signed {
		signatures = append(signatures, map[string]any{
			"signature":          "-----BEGIN PGP SIGNATURE-----\n\nopaque-signature-of-" + c.fullName() + "-" + v.version + "\n-----END PGP SIGNATURE-----\n",
			"pubkey_fingerprint": "0123456789ABCDEF0123456789ABCDEF01234567",
			"signing_service":    "ansible-default",
		})
	}
	return map[string]any{
		"version": v.version, "href": versionHref(c.ns, c.name, v.version),
		"created_at": stamp(v.created()), "updated_at": stamp(v.created()),
		"requires_ansible": v.requiresAnsible, "marks": []string{},
		"artifact":     map[string]any{"filename": a.filename, "sha256": a.sha256, "size": a.size},
		"collection":   map[string]any{"id": collectionID(c.ns, c.name), "name": c.name, "href": collectionHref(c.ns, c.name)},
		"download_url": base + pluginRoot + "artifacts/" + a.filename,
		"name":         c.name, "namespace": map[string]any{"name": c.ns},
		"signatures": signatures,
		"metadata": map[string]any{
			"authors": []string{"trove test <ansiblestub@trove.invalid>"}, "dependencies": deps,
			"description":   "trove test collection " + c.fullName(),
			"documentation": "https://example.invalid/" + c.ns + "/" + c.name + "/docs", "homepage": "https://example.invalid/" + c.ns + "/" + c.name,
			"issues": "https://example.invalid/" + c.ns + "/" + c.name + "/issues", "license": []string{"GPL-3.0-or-later"}, "license_file": nil,
			"readme": "README.md", "repository": "https://example.invalid/" + c.ns + "/" + c.name, "tags": []string{"test"},
			// What the public Galaxy adds and no client reads.
			"contents": []map[string]string{{"content_type": "role", "name": "hello", "description": "hello"}},
			"docs_blob": map[string]any{
				"collection_readme": map[string]string{"name": "README.md", "html": "<h1>" + c.fullName() + "</h1>"},
				"contents":          []any{}, "doc_strs": map[string]any{},
			},
			"manifest": map[string]any{"format": 1, "collection_info": map[string]string{"namespace": c.ns, "name": c.name, "version": v.version}},
			"files":    []map[string]string{{"name": "README.md", "ftype": "file"}},
		},
	}
}

// parseArtifactFile splits "<ns>-<name>-<version>.tar.gz".
func parseArtifactFile(file string) (ns, name, version string, ok bool) {
	base, found := strings.CutSuffix(file, ".tar.gz")
	if !found {
		return "", "", "", false
	}
	parts := strings.SplitN(base, "-", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// signedAddress is where the public Galaxy sends the download of an archive: the same host, a path of its content service and a
// signed query.
func signedAddress(file string) string {
	return fmt.Sprintf("/api/pulp/content/published/%s?expires=1893456000&validate_token=stub", url.PathEscape(file))
}

func isRedirectableShort(kind string) bool {
	switch kind {
	case kIndex, kCollection, kVersions, kVersion:
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(enc, "gzip") {
			return true
		}
	}
	return false
}
