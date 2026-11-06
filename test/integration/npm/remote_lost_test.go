//go:build integration

package npm

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// The object of a cached item is lost behind trove's back (an object store that lost its data, an out-of-band deletion,
// ha-and-helm.md R-HA-024): the rows stay. A remote repository treats the item as a cache miss and fills it again from its
// upstream, whose commit writes the object again; while the upstream cannot serve it the answer is 503 content_unavailable
// with Retry-After, never a 500 (storage.md R-STOR-027, R-STOR-012, Q-REPO-202 to Q-REPO-204, finding F-62).

// entryBlob is the digest of the blob a cache entry of a repository references.
func (r *renv) entryBlob(repo, kind, path string) []byte {
	r.t.Helper()
	h := r.st.Text(`SELECT encode(e.blob_sha256, 'hex') FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id
		WHERE r.key = $1 AND e.kind = $2 AND e.path = $3`, repo, kind, path)
	d, err := hex.DecodeString(h)
	if err != nil || len(d) != 32 {
		r.t.Fatalf("the %s entry %s of %s: %q", kind, path, repo, h)
	}
	return d
}

// contentStatus is the content_status of a blob.
func (r *renv) contentStatus(d []byte) string {
	r.t.Helper()
	return r.st.Text(`SELECT content_status FROM blobs WHERE sha256 = $1`, d)
}

// lostAnswer checks the answer of a lost item that cannot be filled: 503 content_unavailable with Retry-After.
func lostAnswer(t *testing.T, what string, a testenv.Answer) {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(a.Body, &p)
	if a.Status != http.StatusServiceUnavailable || p.Code != "content_unavailable" || a.Header.Get("Retry-After") == "" {
		t.Fatalf("%s: %d %q Retry-After %q, want 503 content_unavailable with Retry-After: %.300s", what, a.Status, p.Code, a.Header.Get("Retry-After"), a.Body)
	}
}

func TestR_STOR_027_ACachedNPMItemWhoseObjectIsMissingIsFilledAgainFromTheUpstream(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	reps := r.start(2)
	p := upstreamPackage(t, 2)
	v := p.Versions[0]
	file := corpus.TarballFile(p.Name, v.Version)
	if a := r.view(reps[0], rRemote, p.Name, r.tokA); a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	if a := r.tarball(reps[0], rRemote, p.Name, v.Version, r.tokA); a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
		t.Fatalf("tarball: %d", a.Status)
	}
	tarballs := func() int { return r.stub.Counts().Tarball(p.Name, file) }

	t.Run("R-STOR-027 R-HA-024 the tarball is fetched again with its bytes, the object is written again and the next request is a hit", func(t *testing.T) {
		d := r.entryBlob(rRemote, "artifact", p.Name+"/-/"+file)
		r.st.DeleteObject(t, d)
		before := tarballs()
		a := r.tarball(reps[0], rRemote, p.Name, v.Version, r.tokA)
		if a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
			t.Fatalf("%d %.300s", a.Status, a.Body)
		}
		if got := tarballs(); got != before+1 {
			t.Errorf("%d upstream requests for the tarball, want 1", got-before)
		}
		if !r.st.ObjectExists(t, d) || r.contentStatus(d) != "ok" {
			t.Fatalf("object written again: %v, content_status %s", r.st.ObjectExists(t, d), r.contentStatus(d))
		}
		if a := r.tarball(reps[1], rRemote, p.Name, v.Version, r.tokA); a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
			t.Fatalf("the next request: %d", a.Status)
		}
		if got := tarballs(); got != before+1 {
			t.Errorf("the next request reached the upstream: %d requests", got-before)
		}
	})

	t.Run("R-STOR-027 Q-REPO-204 through the virtual repository the member's cache misses and its fetch tier fills it again", func(t *testing.T) {
		d := r.entryBlob(rRemote, "artifact", p.Name+"/-/"+file)
		r.st.DeleteObject(t, d)
		before := tarballs()
		a := r.tarball(reps[1], rAll, p.Name, v.Version, r.tokA)
		if a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
			t.Fatalf("%d %.300s", a.Status, a.Body)
		}
		if got := tarballs(); got != before+1 {
			t.Errorf("%d upstream requests for the tarball, want 1", got-before)
		}
		if !r.st.ObjectExists(t, d) {
			t.Fatal("the object was not written again")
		}
	})

	t.Run("R-STOR-027 Q-REPO-203 the package document is fetched again whole, on a replica that does not hold its bytes, and served", func(t *testing.T) {
		d := r.entryBlob(rRemote, "metadata", p.Name)
		r.st.DeleteObject(t, d)
		docs, conditional := r.upstreamDocs(p.Name), r.stub.Counts().ConditionalDocuments(p.Name)
		// The replicas that served the document keep its bytes in memory by digest and would answer from there.
		fresh := r.start(1)[0]
		a := r.view(fresh, rRemote, p.Name, r.tokA)
		if a.Status != 200 || len(versionsOf(t, a)) != len(p.Versions) {
			t.Fatalf("%d %.300s", a.Status, a.Body)
		}
		if got := r.upstreamDocs(p.Name); got != docs+1 {
			t.Errorf("%d upstream requests for the document, want 1", got-docs)
		}
		if got := r.stub.Counts().ConditionalDocuments(p.Name); got != conditional {
			t.Error("the document was revalidated: a 304 would have left the entry without its bytes")
		}
		if !r.st.ObjectExists(t, d) || r.contentStatus(d) != "ok" {
			t.Fatalf("object written again: %v, content_status %s", r.st.ObjectExists(t, d), r.contentStatus(d))
		}
		// Through the virtual repository, on another replica without the bytes.
		r.st.DeleteObject(t, d)
		if a := r.view(r.start(1)[0], rAll, p.Name, r.tokA); a.Status != 200 || len(versionsOf(t, a)) != len(p.Versions) {
			t.Fatalf("virtual: %d %.300s", a.Status, a.Body)
		}
		if !r.st.ObjectExists(t, d) {
			t.Fatal("the object was not written again through the virtual repository")
		}
	})
}

func TestR_STOR_027_ConcurrentRequestsForALostTarballOverTwoReplicasFillItOnce(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{Valkey: true})
	reps := r.start(2)
	p := upstreamPackage(t, 2)
	v := p.Versions[0]
	file := corpus.TarballFile(p.Name, v.Version)
	if a := r.tarball(reps[0], rRemote, p.Name, v.Version, r.tokA); a.Status != 200 {
		t.Fatalf("tarball: %d %s", a.Status, a.Body)
	}
	r.st.DeleteObject(t, r.entryBlob(rRemote, "artifact", p.Name+"/-/"+file))
	before := r.stub.Counts().Tarball(p.Name, file)
	// A slow upstream makes the refills overlap: the single-flight of each replica and the flight lock in Valkey keep them
	// to one fetch (R-REPO-026), whichever request found the object missing first.
	if err := r.stub.SetMode(npmstub.Mode{DelayMS: map[string]int{"tarball": 400}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan string, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a := r.tarball(reps[i%2], rRemote, p.Name, v.Version, r.tokA); a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
				failures <- fmt.Sprintf("%d %.200s", a.Status, a.Body)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Errorf("a request failed: %s", f)
	}
	if got := r.stub.Counts().Tarball(p.Name, file) - before; got != 1 {
		t.Errorf("%d upstream requests for the lost tarball, want 1", got)
	}
}

func TestR_STOR_027_ACachedNPMItemWhoseObjectIsMissingIs503WhileTheUpstreamCannotServeIt(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 2)
	v := p.Versions[0]
	file := corpus.TarballFile(p.Name, v.Version)
	if a := r.view(rep, rRemote, p.Name, r.tokA); a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	if a := r.tarball(rep, rRemote, p.Name, v.Version, r.tokA); a.Status != 200 {
		t.Fatalf("tarball: %d", a.Status)
	}
	tgz, doc := r.entryBlob(rRemote, "artifact", p.Name+"/-/"+file), r.entryBlob(rRemote, "metadata", p.Name)
	r.st.DeleteObject(t, tgz)
	r.st.DeleteObject(t, doc)
	if err := r.stub.SetMode(npmstub.Mode{Down: npmstub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	lostAnswer(t, "tarball", r.tarball(rep, rRemote, p.Name, v.Version, r.tokA))
	if got := r.contentStatus(tgz); got != "missing" {
		t.Errorf("the tarball's blob is %s, want missing: the read marks it (R-HA-024)", got)
	}
	cold := r.start(1)[0] // the first replica holds the document's bytes in memory
	lostAnswer(t, "document", r.view(cold, rRemote, p.Name, r.tokA))
	if got := r.contentStatus(doc); got != "missing" {
		t.Errorf("the document's blob is %s, want missing", got)
	}
	// The cache view never asks the upstream: the same answer, without a request.
	before := r.stub.Counts().Total()
	lostAnswer(t, "cache view", r.tarball(cold, rRemote+"~cache", p.Name, v.Version, r.tokA))
	if got := r.stub.Counts().Total(); got != before {
		t.Errorf("the cache view reached the upstream %d times", got-before)
	}

	// The upstream is back: both are filled again with their bytes.
	if err := r.stub.SetMode(npmstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if a := r.tarball(rep, rRemote, p.Name, v.Version, r.tokA); a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
		t.Fatalf("tarball after the upstream returned: %d %.300s", a.Status, a.Body)
	}
	if a := r.view(cold, rRemote, p.Name, r.tokA); a.Status != 200 || len(versionsOf(t, a)) != len(p.Versions) {
		t.Fatalf("document after the upstream returned: %d %.300s", a.Status, a.Body)
	}
	if !r.st.ObjectExists(t, tgz) || !r.st.ObjectExists(t, doc) || r.contentStatus(tgz) != "ok" || r.contentStatus(doc) != "ok" {
		t.Fatal("the objects were not written again")
	}
}

func TestR_HA_024_ALocalTarballWhoseObjectIsMissingIs503AndItsBlobIsMarkedMissing(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	pk := pkg{Name: "lost-local", Version: "1.0.0"}
	r.publishTo(rep, rLocal, r.tokA, pk)
	d := r.itemBlob(rLocal, "lost-local/-/lost-local-1.0.0.tgz")
	r.st.DeleteObject(t, d)
	// A local repository has no upstream: the loss is data loss, answered 503 (the object may come back through a restore),
	// and the blob is marked so that every replica answers it from PostgreSQL.
	lostAnswer(t, "first read", r.tarball(rep, rLocal, pk.Name, pk.Version, r.tokA))
	if got := r.contentStatus(d); got != "missing" {
		t.Fatalf("content_status %s, want missing", got)
	}
	lostAnswer(t, "second read", r.tarball(r.start(1)[0], rLocal, pk.Name, pk.Version, r.tokA))
	// The repair of R-STOR-027: an upload of identical content writes the object again.
	r.publishTo(rep, rLocal2, r.tokA, pk)
	if !r.st.ObjectExists(t, d) || r.contentStatus(d) != "ok" {
		t.Fatal("an upload of the same content did not repair the blob")
	}
	if a := r.tarball(rep, rLocal, pk.Name, pk.Version, r.tokA); a.Status != 200 {
		t.Fatalf("after the repair: %d %.300s", a.Status, a.Body)
	}
}

// itemBlob is the digest of the blob of an item.
func (r *renv) itemBlob(repo, path string) []byte {
	r.t.Helper()
	h := r.st.Text(`SELECT encode(i.blob_sha256, 'hex') FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2`, repo, path)
	d, err := hex.DecodeString(h)
	if err != nil || len(d) != 32 {
		r.t.Fatalf("the item %s of %s: %q", path, repo, h)
	}
	return d
}
