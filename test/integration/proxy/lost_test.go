//go:build integration

package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
)

// The bounds of a lost item's refill at the engine (Q-REPO-205): an upstream that now serves other bytes for a lost
// artifact, a format that keeps a lost document, a mirror's pass that joined a client's refill, and a negative entry beside
// a lost document. The counting upstream says what was fetched, the recorder what was audited.

// itemDigest is the SHA-256 of the item of a repository at a path.
func (e *env) itemDigest(repo, path string) blob.Digest {
	e.t.Helper()
	return e.digestOf(e.text(`SELECT encode(i.blob_sha256, 'hex') FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2`, repo, path))
}

// entryDigest is the SHA-256 of the blob a cache entry references.
func (e *env) entryDigest(repo, path string) blob.Digest {
	e.t.Helper()
	return e.digestOf(e.text(`SELECT encode(e.blob_sha256, 'hex') FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1 AND e.path = $2`, repo, path))
}

func (e *env) digestOf(h string) blob.Digest {
	e.t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 {
		e.t.Fatalf("digest %q", h)
	}
	return blob.Digest(b)
}

// loseObject deletes the object of a blob behind the engine's back.
func (e *env) loseObject(d blob.Digest) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.obj.Delete(ctx, blob.Key(e.s3.S3.Prefix, d)); err != nil {
		e.t.Fatal(err)
	}
}

// lostAnswer checks a 503 content_unavailable with Retry-After.
func lostAnswer(t *testing.T, what string, s served) {
	t.Helper()
	ee, ok := errs.As(s.err)
	if !ok || ee.Status() != http.StatusServiceUnavailable || ee.Code != errs.CodeContentUnavailable || ee.RetryAfter <= 0 {
		t.Fatalf("%s: %d %v, want 503 content_unavailable with Retry-After", what, s.status, s.err)
	}
}

func TestR_STOR_027_ALostArtifactWhoseUpstreamServesOtherBytesIsFetchedOncePerReplicaAndBackoff(t *testing.T) {
	e := newEnv(t)
	const path = "pkg/-/pkg-1.0.0.tgz"
	recorded, other := bytes.Repeat([]byte("a"), 8192), bytes.Repeat([]byte("b"), 8192)
	up := e.upstream()
	up.Set("/"+path, proxytest.Route{Body: recorded, Type: "application/octet-stream"})
	rem := e.remote("lost-changed", up)
	a := e.newReplica("a", replicaOpts{})
	if s := a.artifact(rem, path); s.status != 200 {
		t.Fatalf("fill: %d (%v)", s.status, s.err)
	}
	d := e.itemDigest("lost-changed", path)
	e.loseObject(d)
	up.Set("/"+path, proxytest.Route{Body: other, Type: "application/octet-stream"}) // the upstream serves other bytes now

	lostAnswer(t, "the refill", a.artifact(rem, path))
	if n := up.Count("GET", "/"+path); n != 2 {
		t.Fatalf("%d upstream GETs, want the fill and one refill", n)
	}
	if got := e.itemDigest("lost-changed", path); got != d {
		t.Fatal("the recorded digest was replaced (R-REPO-041)")
	}
	// The backoff: further requests on the replica answer the 503 and fetch nothing, and the security event is written once.
	for range 3 {
		lostAnswer(t, "inside the backoff", a.artifact(rem, path))
	}
	if n := up.Count("GET", "/"+path); n != 2 {
		t.Errorf("%d upstream GETs inside the backoff, want 2", n)
	}
	if n := len(a.rec.ofType(audit.EventProxyUpstreamDigestChanged)); n != 1 {
		t.Errorf("%d proxy.upstream_digest_changed events, want 1", n)
	}
	// Another replica fetches it once for itself and then backs off as well: one fetch per replica and backoff.
	b := e.newReplica("b", replicaOpts{})
	lostAnswer(t, "another replica", b.artifact(rem, path))
	lostAnswer(t, "another replica, again", b.artifact(rem, path))
	if n := up.Count("GET", "/"+path); n != 3 {
		t.Errorf("%d upstream GETs after a second replica, want 3", n)
	}
	// A mirror's pass meets the backoff too: a failure of the item, no fetch.
	if _, err := a.fill(rem, path, 2); err == nil {
		t.Error("a pass reported the lost item filled")
	}
	if n := up.Count("GET", "/"+path); n != 3 {
		t.Errorf("a pass inside the backoff fetched: %d GETs", n)
	}
}

func TestR_STOR_027_APassThatJoinsAClientsRefillThatLeftTheItemLostIsNotPresent(t *testing.T) {
	e := newEnv(t)
	const path = "pkg/-/pkg-2.0.0.tgz"
	up := e.upstream()
	up.Set("/"+path, proxytest.Route{Body: bytes.Repeat([]byte("a"), 8192), Type: "application/octet-stream"})
	rem := e.remote("lost-joined", up)
	a := e.newReplica("a", replicaOpts{})
	if s := a.artifact(rem, path); s.status != 200 {
		t.Fatalf("fill: %d (%v)", s.status, s.err)
	}
	e.loseObject(e.itemDigest("lost-joined", path))
	// The upstream serves other bytes, slowly: the pass joins the client's refill while it runs.
	up.Set("/"+path, proxytest.Route{Body: bytes.Repeat([]byte("b"), 8192), Type: "application/octet-stream", Delay: 1500 * time.Millisecond})
	client := make(chan served, 1)
	go func() { client <- a.artifact(rem, path) }()
	time.Sleep(500 * time.Millisecond) // the client's refill holds the flight for 1.5 s
	out, err := a.fill(rem, path, 2)
	if err == nil || out.Present {
		t.Fatalf("the pass that joined the refill: %+v %v, want a failure of the item (Q-REPO-205)", out, err)
	}
	if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeContentUnavailable {
		t.Fatalf("the pass's error: %v", err)
	}
	lostAnswer(t, "the client", <-client)
	if n := up.Count("GET", "/"+path); n != 2 {
		t.Errorf("%d upstream GETs: the pass fetched for itself", n)
	}
}

func TestR_STOR_027_ADocumentTheFormatKeepsLostIsFetchedOncePerBackoff(t *testing.T) {
	e := newEnv(t)
	const path = "pkg/keep"
	up := e.upstream()
	up.Set("/"+path, proxytest.Route{Body: []byte(`{"v":1}`), Type: "application/json"})
	rem := e.remote("lost-kept", up)
	a := e.newReplica("a", replicaOpts{})
	if s := a.metadata(rem, path); s.status != 200 {
		t.Fatalf("fill: %d (%v)", s.status, s.err)
	}
	e.loseObject(e.entryDigest("lost-kept", path))
	// The upstream's copy differs and the format keeps the stored document (npm keeps one that is newer than the upstream's).
	up.Set("/"+path, proxytest.Route{Body: []byte(`{"v":0}`), Type: "application/json"})
	keep := func(rq *proxy.Request) { rq.Adapter = &proxytest.Adapter{Keep: true} }
	b := e.newReplica("b", replicaOpts{}) // holds no bytes of the document in memory
	if s := b.metadata(rem, path, keep); s.err == nil {
		t.Fatalf("the document whose object is lost: %d, want an error", s.status)
	} else if ee, ok := errs.As(s.err); !ok || ee.Code != errs.CodeContentUnavailable {
		t.Fatalf("the document whose object is lost: %v", s.err)
	}
	if n := up.Count("GET", "/"+path); n != 2 {
		t.Fatalf("%d upstream GETs, want the fill and one refill", n)
	}
	for range 3 {
		lostAnswer(t, "inside the backoff", b.metadata(rem, path, keep))
	}
	if n := up.Count("GET", "/"+path); n != 2 {
		t.Errorf("%d upstream GETs inside the backoff, want 2", n)
	}
}

func TestR_STOR_027_ALostDocumentBesideANegativeEntryIs503NotTheNegative404(t *testing.T) {
	e := newEnv(t)
	const path = "pkg/negative"
	up := e.upstream()
	up.Set("/"+path, proxytest.Route{Body: []byte(`{"v":1}`), Type: "application/json"})
	rem := e.remote("lost-negative", up)
	a := e.newReplica("a", replicaOpts{})
	if s := a.metadata(rem, path); s.status != 200 {
		t.Fatalf("fill: %d (%v)", s.status, s.err)
	}
	// A 404 of a flight that did not see the stored entry (no cross-replica lock while Valkey is unavailable, R-REPO-026)
	// leaves a negative entry beside an entry that is not marked gone: negative entries are never removed by a fill.
	ctx := context.Background()
	if err := cluster.NewNegativeStore(a.vk).Record(ctx, rem.ID(), cluster.NewFlightKey(rem.ID(), path, ""),
		cluster.NegativeEntry{Status: http.StatusNotFound, SentAt: time.Now()}, time.Hour); err != nil {
		t.Fatal(err)
	}
	e.loseObject(e.entryDigest("lost-negative", path))
	b := e.newReplica("b", replicaOpts{proxy: func(p *config.Proxy) { p.MetadataCacheBytes = 0 }})
	first := b.metadata(rem, path) // the read finds the object gone and marks the blob
	if first.err == nil {
		t.Fatalf("the first read: %d", first.status)
	}
	if got := e.text(`SELECT content_status FROM blobs WHERE sha256 = $1`, e.entryDigest("lost-negative", path).Bytes()); got != "missing" {
		t.Fatalf("content_status %s", got)
	}
	lostAnswer(t, "a lost document beside a negative entry", b.metadata(rem, path))
}
