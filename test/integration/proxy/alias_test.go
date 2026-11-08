//go:build integration

package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
)

// TestFillAliasesAndMediaType: a metadata document is stored with the media type the upstream sent, and a path the format names as
// an alias (Prepared.Aliases) is entered in the fill transaction as an item for the same blob and an artifact entry, so a second
// path serves the document with no request to the upstream (ADR-161, Q-OCI-25).
func TestFillAliasesAndMediaType(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	doc := []byte("alias pkg/by-digest\n{\"schemaVersion\":2}")
	const mt = "application/vnd.docker.distribution.manifest.v2+json"
	up.Set("/pkg/by-tag", proxytest.Route{Body: doc, Type: mt + "; charset=utf-8"})
	rem := e.remote("alias", up)
	r := e.newReplica("a", replicaOpts{})

	d, err := r.engine.Metadata(context.Background(), r.request(rem, "pkg/by-tag", proxy.Metadata))
	if err != nil || d.MediaType != mt {
		t.Fatalf("the fetched document: media type %q, %v", d.MediaType, err)
	}
	if got := e.text("SELECT media_type FROM remote_cache_entries WHERE path = 'pkg/by-tag'"); got != mt {
		t.Errorf("the cache entry keeps the media type %q", got)
	}
	// A hit reads the stored type back.
	if d, err = r.engine.Metadata(context.Background(), r.request(rem, "pkg/by-tag", proxy.Metadata)); err != nil || d.MediaType != mt || !d.Status.Hit {
		t.Fatalf("the stored document: %q, hit %v, %v", d.MediaType, d.Status.Hit, err)
	}
	if n := e.number("SELECT count(*) FROM remote_cache_entries a JOIN remote_cache_entries m ON m.blob_sha256 = a.blob_sha256 " +
		"WHERE a.path = 'pkg/by-digest' AND a.kind = 'artifact' AND a.item_id IS NOT NULL AND m.path = 'pkg/by-tag' AND m.kind = 'metadata'"); n != 1 {
		t.Fatalf("the alias entry shares the blob of the document: %d", n)
	}
	if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/by-digest' AND blob_sha256 = $1", proxytest.SHA256(doc)); n != 1 {
		t.Fatalf("the alias item: %d", n)
	}
	before := up.Total()
	s := r.artifact(rem, "pkg/by-digest")
	if s.status != http.StatusOK || string(s.body) != string(doc) {
		t.Fatalf("the alias: %d %q", s.status, s.body)
	}
	if up.Total() != before {
		t.Errorf("the alias cost %d upstream requests", up.Total()-before)
	}
}
