package npm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// TestR_NPM_013_ConditionalRequestsAndCacheHeaders: the validator of a package document
// is derived from its inputs, so a matching request is a 304 that needs no rendering,
// a date never validates, and nothing is `public`.
func TestR_NPM_013_ConditionalRequestsAndCacheHeaders(t *testing.T) {
	rf := newRepo(t)
	rf.publish("@acme/ui", "1.0.0")
	first := rf.get("@acme%2fui")
	etag := first.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag %q is no weak validator", etag)
	}
	t.Run("a document carries the validator, Last-Modified, Vary and a private Cache-Control", func(t *testing.T) {
		h := first.Header()
		if h.Get("Last-Modified") == "" || h.Get("Vary") != "Accept, Accept-Encoding" {
			t.Fatalf("Last-Modified %q Vary %q", h.Get("Last-Modified"), h.Get("Vary"))
		}
		if cc := h.Get("Cache-Control"); cc != "private, max-age=0, must-revalidate" || strings.Contains(cc, "public") {
			t.Fatalf("Cache-Control %q", cc)
		}
	})
	t.Run("a matching If-None-Match is a 304 and reads no rows", func(t *testing.T) {
		before := rf.st.docReads.Load()
		for _, inm := range []string{etag, strings.TrimPrefix(etag, "W/"), `"other", ` + etag} {
			r := rf.get("@acme%2fui", "If-None-Match", inm)
			if r.Code != 304 || r.Body.Len() != 0 || r.Header().Get("ETag") != etag {
				t.Fatalf("If-None-Match %q: %d %q etag %q", inm, r.Code, r.Body.String(), r.Header().Get("ETag"))
			}
			if cc := r.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") {
				t.Fatalf("Cache-Control %q", cc)
			}
		}
		if rf.st.docReads.Load() != before {
			t.Fatal("a 304 read the rows of the document")
		}
	})
	t.Run("If-Modified-Since never produces a 304", func(t *testing.T) {
		lm := first.Header().Get("Last-Modified")
		for _, ims := range []string{lm, "Sat, 01 Jan 2050 00:00:00 GMT"} {
			if r := rf.get("@acme%2fui", "If-Modified-Since", ims); r.Code != 200 || r.Body.Len() == 0 {
				t.Fatalf("If-Modified-Since %q alone: %d", ims, r.Code)
			}
		}
		// a matching validator wins over a date in the past (RFC 9110 §13.1.3)
		if r := rf.get("@acme%2fui", "If-None-Match", etag, "If-Modified-Since", "Mon, 01 Jan 2001 00:00:00 GMT"); r.Code != 304 {
			t.Fatalf("matching If-None-Match with a date in the past: %d", r.Code)
		}
	})
	t.Run("a validator that does not match, and the wildcard, are answered with the document", func(t *testing.T) {
		for _, inm := range []string{`W/"0000"`, "*", ""} {
			if r := rf.get("@acme%2fui", "If-None-Match", inm); r.Code != 200 {
				t.Fatalf("If-None-Match %q: %d", inm, r.Code)
			}
		}
	})
	t.Run("a dist-tag change makes the old validator obsolete", func(t *testing.T) {
		rf.publish("@acme/ui", "1.1.0")
		if r := rf.do(http.MethodPut, "-/package/@acme%2fui/dist-tags/stable", `"1.0.0"`); r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		r := rf.get("@acme%2fui", "If-None-Match", etag)
		if r.Code != 200 || r.Header().Get("ETag") == etag {
			t.Fatalf("%d etag %q (old %q)", r.Code, r.Header().Get("ETag"), etag)
		}
	})
	t.Run("the variants, the base URL and the endpoint have validators of their own", func(t *testing.T) {
		full := rf.get("@acme%2fui").Header().Get("ETag")
		abbreviated := rf.get("@acme%2fui", "Accept", "application/vnd.npm.install-v1+json").Header().Get("ETag")
		version := rf.get("@acme%2fui/1.0.0").Header().Get("ETag")
		other := newRepo(t, func(o *formattest.HostOptions) { o.BaseURL = "https://mirror.example.org" })
		other.publish("@acme/ui", "1.0.0")
		other.publish("@acme/ui", "1.1.0")
		other.do(http.MethodPut, "-/package/@acme%2fui/dist-tags/stable", `"1.0.0"`)
		moved := other.get("@acme%2fui").Header().Get("ETag")
		rq, err := rf.h.NewRequest(context.Background(), http.MethodGet, "@acme%2fui", nil)
		if err != nil {
			t.Fatal(err)
		}
		rq.Subject = alice()
		rq.Endpoint.Name, rq.Endpoint.ExposureFingerprint = "public", "abc123"
		onEndpoint := rf.h.ServeRequest(rq, false).Header().Get("ETag")
		seen := map[string]string{"full": full, "abbreviated": abbreviated, "version": version, "base URL": moved, "endpoint": onEndpoint}
		distinct := map[string]bool{}
		for k, v := range seen {
			if v == "" || distinct[v] {
				t.Fatalf("validator of %s is %q, not distinct from the others: %v", k, v, seen)
			}
			distinct[v] = true
		}
	})
	t.Run("the gzip form has the validator of the identity form", func(t *testing.T) {
		plain := rf.get("@acme%2fui")
		zipped := rf.get("@acme%2fui", "Accept-Encoding", "gzip")
		if zipped.Header().Get("Content-Encoding") != "gzip" || zipped.Header().Get("ETag") != plain.Header().Get("ETag") {
			t.Fatalf("%q %q", zipped.Header().Get("Content-Encoding"), zipped.Header().Get("ETag"))
		}
		if string(gunzip(t, zipped.Body.Bytes())) != plain.Body.String() {
			t.Fatal("the gzip form decodes to other bytes")
		}
	})
	t.Run("no package document of any form is public", func(t *testing.T) {
		for _, p := range []string{"@acme%2fui", "@acme%2fui/latest", "@acme%2fui/1.0.0", "@acme%2fui?write=true"} {
			for _, accept := range []string{"", "application/vnd.npm.install-v1+json"} {
				r := rf.get(p, "Accept", accept)
				if strings.Contains(r.Header().Get("Cache-Control"), "public") {
					t.Fatalf("%s: %q", p, r.Header().Get("Cache-Control"))
				}
			}
		}
	})
}

// TestR_NPM_013_DocumentMaxAge: npm.document_max_age is the max-age of the document.
func TestR_NPM_013_DocumentMaxAge(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "1.0.0")
	rep := defaultReplica()
	rep.cfg.DocumentMaxAge = 90_000_000_000 // 90 s
	rf.f.replicas.Store(rf.h, rep)
	if cc := rf.get("lodash").Header().Get("Cache-Control"); cc != "private, max-age=90, must-revalidate" {
		t.Fatalf("Cache-Control %q", cc)
	}
}

// TestR_ARCH_012_RenditionCache: a rendering is reused for the revision it was made at
// and for nothing else, whatever the validator says; a commit is a new key.
func TestR_ARCH_012_RenditionCache(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "1.0.0")
	read := func() int64 { return rf.st.docReads.Load() }
	base := read()
	a := rf.get("lodash")
	b := rf.get("lodash")
	if a.Body.String() != b.Body.String() || read()-base != 1 {
		t.Fatalf("two requests of one revision made %d renderings", read()-base)
	}
	t.Run("another variant and encoding are keys of their own", func(t *testing.T) {
		before := read()
		rf.get("lodash", "Accept", "application/vnd.npm.install-v1+json")
		rf.get("lodash", "Accept", "application/vnd.npm.install-v1+json")
		if read()-before != 1 {
			t.Fatalf("%d renderings for one variant", read()-before)
		}
		before = read()
		rf.get("lodash", "Accept-Encoding", "gzip")
		rf.get("lodash", "Accept-Encoding", "gzip")
		if read()-before != 0 {
			t.Fatalf("the gzip form of a cached rendering read the rows again (%d)", read()-before)
		}
	})
	t.Run("a commit is a new key: the next request renders again and sees the commit", func(t *testing.T) {
		rf.publish("lodash", "1.1.0")
		before := read()
		doc := jsonOf(t, rf.get("lodash"))
		if read()-before != 1 || len(versionsOf(doc)) != 2 {
			t.Fatalf("%d renderings, versions %v", read()-before, versionsOf(doc))
		}
	})
	t.Run("a base URL change is a new key and new links", func(t *testing.T) {
		rq, err := rf.h.NewRequest(context.Background(), http.MethodGet, "lodash", nil)
		if err != nil {
			t.Fatal(err)
		}
		rq.Subject = alice()
		body := rf.h.ServeRequest(rq, false).Body.String()
		if !strings.Contains(body, "https://trove.example.com/api/npm/main/lodash/-/lodash-1.1.0.tgz") {
			t.Fatalf("links %s", body)
		}
		// the same package on a Host with another base URL: no entry is shared
		other := newRepo(t, func(o *formattest.HostOptions) { o.BaseURL = "https://mirror.example.org" })
		other.publish("lodash", "1.0.0")
		if got := other.get("lodash").Body.String(); strings.Contains(got, "trove.example.com") || !strings.Contains(got, "https://mirror.example.org/api/npm/main/lodash/-/") {
			t.Fatalf("links %s", got)
		}
	})
}
