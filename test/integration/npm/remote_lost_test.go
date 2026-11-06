//go:build integration

package npm

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
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

// ageCheck moves the last existence check of a blob back by more than the recheck interval, as if the interval had passed
// (Q-STOR-153): the test does not wait 30 s.
func (r *renv) ageCheck(d []byte) {
	r.t.Helper()
	r.st.Exec(`UPDATE blobs SET checked_at = checked_at - make_interval(secs => $2) WHERE sha256 = $1`, d, blob.MissingRecheckInterval.Seconds()+1)
}

func TestR_STOR_027_AVirtualAnswers503ForALostCachedTarballOfAMemberTheCallerMayNotFill(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	first := p.Versions[0]
	// bob may fill npm-gated (cacheFillAction: deploy) and caches one version through the virtual repository; alice may read
	// both and fill neither.
	b := r.view(rep, rGatedV, p.Name, r.tokB)
	if b.Status != 200 {
		t.Fatalf("bob: %d %.200s", b.Status, b.Body)
	}
	if got := r.install(rep, tarballsOf(t, b)[first.Version], r.tokB); got.Status != 200 {
		t.Fatalf("bob's install: %d", got.Status)
	}
	tgz := r.entryBlob(rGated, "artifact", p.Name+"/-/"+corpus.TarballFile(p.Name, first.Version))
	r.st.DeleteObject(t, tgz)
	r.stub.Reset()

	// Q-REPO-206: alice's document still lists the version; a loss of the store does not change what the documents say.
	a := r.view(rep, rGatedV, p.Name, r.tokA)
	if vs := versionsOf(t, a); a.Status != 200 || len(vs) != 1 || vs[0] != first.Version {
		t.Fatalf("alice sees %v (%d)", vs, a.Status)
	}
	// Q-REPO-205: her download is the member's 503, on the read that finds the object gone and on every later one, never 404.
	for i := range 3 {
		lostAnswer(t, fmt.Sprintf("alice's download %d", i+1), r.tarball(rep, rGatedV, p.Name, first.Version, r.tokA))
	}
	if got := r.contentStatus(tgz); got != "missing" {
		t.Fatalf("content_status %s, want missing", got)
	}
	// The same for a corrupt object (the scrub's or the verification's mark).
	r.st.Exec(`UPDATE blobs SET content_status = 'corrupt' WHERE sha256 = $1`, tgz)
	lostAnswer(t, "a corrupt object", r.tarball(rep, rGatedV, p.Name, first.Version, r.tokA))
	// The remote addressed directly answers the same.
	lostAnswer(t, "the remote itself", r.tarball(rep, rGated, p.Name, first.Version, r.tokA))
	if n := r.stub.Counts().Total(); n != 0 {
		t.Fatalf("the upstream was asked %d times on behalf of a caller who may not fill", n)
	}
	// bob's download fills it again through the virtual repository, and then alice gets it too.
	if got := r.tarball(rep, rGatedV, p.Name, first.Version, r.tokB); got.Status != 200 || integrityOf(got.Body) != first.Dist.Integrity {
		t.Fatalf("bob's refill: %d %.200s", got.Status, got.Body)
	}
	if got := r.tarball(rep, rGatedV, p.Name, first.Version, r.tokA); got.Status != 200 || integrityOf(got.Body) != first.Dist.Integrity {
		t.Fatalf("alice after the refill: %d %.200s", got.Status, got.Body)
	}
	if got := r.contentStatus(tgz); got != "ok" {
		t.Fatalf("content_status %s after the refill", got)
	}
}

func TestR_HA_024_AMissingMarkHealsOnceTheObjectIsBack(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	reps := r.start(2)

	t.Run("Q-STOR-153 a local tarball: 503 while its object is away, still 503 inside the interval, 200 with its bytes after it", func(t *testing.T) {
		pk := pkg{Name: "moved-local", Version: "1.0.0"}
		r.publishTo(reps[0], rLocal, r.tokA, pk)
		want := r.tarball(reps[0], rLocal, pk.Name, pk.Version, r.tokA)
		if want.Status != 200 {
			t.Fatalf("%d", want.Status)
		}
		d := r.itemBlob(rLocal, "moved-local/-/moved-local-1.0.0.tgz")
		data := r.st.TakeObject(t, d) // a wrong storage.s3.prefix: the store answers 404 for an object that exists
		lostAnswer(t, "object away", r.tarball(reps[0], rLocal, pk.Name, pk.Version, r.tokA))
		if got := r.contentStatus(d); got != "missing" {
			t.Fatalf("content_status %s, want missing", got)
		}
		r.st.PutObject(t, d, data) // the prefix is corrected
		// Inside the interval the store is not asked: the object is back, and the answer is still the 503 from PostgreSQL.
		lostAnswer(t, "inside the interval", r.tarball(reps[1], rLocal, pk.Name, pk.Version, r.tokA))
		lostAnswer(t, "inside the interval, /files/", r.st.Do(reps[1], http.MethodGet, "/files/"+rLocal+"/moved-local/-/moved-local-1.0.0.tgz", r.tokA, nil))
		if got := r.contentStatus(d); got != "missing" {
			t.Fatalf("content_status %s inside the interval", got)
		}
		r.ageCheck(d)
		a := r.tarball(reps[1], rLocal, pk.Name, pk.Version, r.tokA)
		if a.Status != 200 || string(a.Body) != string(want.Body) {
			t.Fatalf("after the interval: %d %.300s", a.Status, a.Body)
		}
		if got := r.contentStatus(d); got != "ok" {
			t.Fatalf("content_status %s after the object came back", got)
		}
		if a := r.tarball(reps[0], rLocal, pk.Name, pk.Version, r.tokA); a.Status != 200 {
			t.Fatalf("the other replica: %d", a.Status)
		}
	})

	t.Run("Q-STOR-153 a remote's marked tarball and document are served again without an upstream fetch once their objects are back", func(t *testing.T) {
		p := upstreamPackage(t, 2)
		v := p.Versions[0]
		file := corpus.TarballFile(p.Name, v.Version)
		if a := r.tarball(reps[0], rRemote, p.Name, v.Version, r.tokA); a.Status != 200 {
			t.Fatalf("fill: %d %s", a.Status, a.Body)
		}
		tgz, doc := r.entryBlob(rRemote, "artifact", p.Name+"/-/"+file), r.entryBlob(rRemote, "metadata", p.Name)
		tgzData, docData := r.st.TakeObject(t, tgz), r.st.TakeObject(t, doc)
		// The cache view never fills: the reads mark both blobs and answer 503, and the upstream is not asked.
		cold := r.start(1)[0] // holds no document bytes in memory
		lostAnswer(t, "tarball, cache view", r.tarball(cold, rRemote+"~cache", p.Name, v.Version, r.tokA))
		lostAnswer(t, "document, cache view", r.view(cold, rRemote+"~cache", p.Name, r.tokA))
		if r.contentStatus(tgz) != "missing" || r.contentStatus(doc) != "missing" {
			t.Fatalf("content_status %s and %s, want missing", r.contentStatus(tgz), r.contentStatus(doc))
		}
		r.st.PutObject(t, tgz, tgzData)
		r.st.PutObject(t, doc, docData)
		r.ageCheck(tgz)
		r.ageCheck(doc)
		before := r.stub.Counts().Total()
		a := r.tarball(cold, rRemote, p.Name, v.Version, r.tokA)
		if a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
			t.Fatalf("tarball: %d %.300s", a.Status, a.Body)
		}
		if b := r.view(r.start(1)[0], rRemote, p.Name, r.tokA); b.Status != 200 || len(versionsOf(t, b)) != len(p.Versions) {
			t.Fatalf("document: %d %.300s", b.Status, b.Body)
		}
		if got := r.stub.Counts().Total(); got != before {
			t.Errorf("%d upstream requests: the objects were back, nothing had to be fetched", got-before)
		}
		if r.contentStatus(tgz) != "ok" || r.contentStatus(doc) != "ok" {
			t.Fatalf("content_status %s and %s after the objects came back", r.contentStatus(tgz), r.contentStatus(doc))
		}
	})
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
