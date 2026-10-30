//go:build integration

package npm

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// documentURL is the path of a tarball below the registry URL of a repository.
func tarballRoute(repo, name, version string) string {
	return "/api/npm/" + repo + "/" + name + "/-/" + corpus.TarballFile(name, version)
}

func (r *renv) tarball(rep *testenv.Replica, repo, name, version, token string) testenv.Answer {
	r.t.Helper()
	return r.st.Do(rep, http.MethodGet, tarballRoute(repo, name, version), token, nil)
}

// listedVersions counts the versions the stored document of a package lists, and the unlisted ones.
func (r *renv) listedVersions(repo, name string) (listed, unlisted int64) {
	r.t.Helper()
	q := `SELECT count(*) FROM npm_remote_versions v JOIN repositories r ON r.id = v.repo_id WHERE r.key = $1 AND v.name = $2 AND v.listed = $3`
	return r.st.Number(q, repo, name, true), r.st.Number(q, repo, name, false)
}

func TestR_NPM_024_TwoReplicasThatFillOneDocumentAtOnceKeepTheNewerOne(t *testing.T) {
	// Valkey is stopped: the cross-replica lock does not exist, and both replicas fetch. The upstream
	// answers the first request with the current document and the second with an older copy, held
	// back so that it commits last; and the other way round: the older answer arrives first.
	for _, c := range []struct {
		name string
		mode func(string) npmstub.Mode
	}{
		{"the older copy commits last", func(n string) npmstub.Mode {
			return npmstub.Mode{StaleCopy: []string{n}, StaleCopySkip: 1, StaleCopyDelayMS: 700}
		}},
		{"the older copy arrives first", func(n string) npmstub.Mode {
			return npmstub.Mode{StaleCopy: []string{n}, DelayMS: map[string]int{"document": 100}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRemoteEnv(t, remoteOptions{})
			reps := r.start(2)
			p := upstreamPackage(t, 4)
			newest := p.Versions[len(p.Versions)-1].Version
			if err := r.stub.SetMode(c.mode(p.Name)); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			answers := make([]testenv.Answer, 2)
			for i := range reps {
				wg.Add(1)
				go func() {
					defer wg.Done()
					answers[i] = r.view(reps[i], rRemote, p.Name, r.tokA)
				}()
			}
			wg.Wait()
			for i, a := range answers {
				if a.Status != 200 {
					t.Fatalf("replica %d: %d %s", i, a.Status, a.Body)
				}
			}
			if got := r.upstreamDocs(p.Name); got != 2 {
				t.Fatalf("%d upstream requests: both replicas should have fetched", got)
			}
			if err := r.stub.SetMode(npmstub.Mode{}); err != nil {
				t.Fatal(err)
			}
			// Afterwards the stored document is the newer one, on every replica.
			for i, rep := range reps {
				a := r.view(rep, rRemote, p.Name, r.tokA)
				vs := versionsOf(t, a)
				if len(vs) != len(p.Versions) {
					t.Errorf("replica %d lists %d versions, the newer document has %d", i, len(vs), len(p.Versions))
				}
				// every tarball it lists installs
				addr := tarballsOf(t, a)[newest]
				if got := r.install(rep, addr, r.tokA); got.Status != 200 {
					t.Errorf("replica %d: the newest version does not install: %d %.200s", i, got.Status, got.Body)
				}
			}
			listed, unlisted := r.listedVersions(rRemote, p.Name)
			if listed != int64(len(p.Versions)) || unlisted != 0 {
				t.Errorf("npm_remote_versions: %d listed, %d unlisted; want %d and 0", listed, unlisted, len(p.Versions))
			}
			if n := r.events("npm.remote.version_removed", p.Name, ""); n != 0 {
				t.Errorf("%d npm.remote.version_removed events", n)
			}
		})
	}
}

func TestR_NPM_024_WithTheUpstreamDownAnExpiredDocumentAndCachedTarballsStillInstall(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	v := p.Versions[0].Version
	a := r.view(rep, rRemote, p.Name, r.tokA)
	if a.Status != 200 || r.tarball(rep, rRemote, p.Name, v, r.tokA).Status != 200 {
		t.Fatal("the first requests failed")
	}
	time.Sleep(2200 * time.Millisecond)
	for _, down := range []string{npmstub.DownUnavailable, npmstub.DownReset} {
		if err := r.stub.SetMode(npmstub.Mode{Down: down}); err != nil {
			t.Fatal(err)
		}
		stale := r.view(rep, rRemote, p.Name, r.tokA)
		if stale.Status != 200 || len(versionsOf(t, stale)) != len(p.Versions) {
			t.Fatalf("%s: %d %.200s", down, stale.Status, stale.Body)
		}
		if cs := stale.Header.Get("Cache-Status"); !strings.Contains(cs, "stale") {
			t.Errorf("%s: Cache-Status = %q", down, cs)
		}
		if got := r.tarball(rep, rRemote, p.Name, v, r.tokA); got.Status != 200 {
			t.Errorf("%s: the cached tarball: %d %.200s", down, got.Status, got.Body)
		}
	}
}

func TestR_NPM_024_ADocumentThatNamesAnotherPackageIs502AndTheStoredOneStaysServed(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p, q := upstreamPackage(t, 3), scopedPackage(t)
	if err := r.stub.SetMode(npmstub.Mode{WrongName: []string{p.Name, q.Name}}); err != nil {
		t.Fatal(err)
	}
	// Nothing is stored: the answer is 502 with the code of R-NPM-024.
	a := r.view(rep, rRemote, p.Name, r.tokA)
	if a.Status != 502 || code(t, a) != "npm_invalid_upstream_document" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := r.st.Number(`SELECT count(*) FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1 AND e.path = $2`, rRemote, p.Name); n != 0 {
		t.Errorf("%d cache entries of the refused document", n)
	}
	// A stored document survives a later refusal.
	if err := r.stub.SetMode(npmstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if ok := r.view(rep, rRemote, p.Name, r.tokA); ok.Status != 200 {
		t.Fatalf("%d", ok.Status)
	}
	time.Sleep(2200 * time.Millisecond)
	if err := r.stub.SetMode(npmstub.Mode{WrongName: []string{p.Name}}); err != nil {
		t.Fatal(err)
	}
	kept := r.view(rep, rRemote, p.Name, r.tokA)
	if kept.Status != 200 || len(versionsOf(t, kept)) != len(p.Versions) {
		t.Errorf("the stored document stopped being served: %d %.200s", kept.Status, kept.Body)
	}
	if r.upstreamDocs(p.Name) < 3 {
		t.Errorf("the refresh was not attempted")
	}
}

func TestR_NPM_025_AnInstallFromALockfileNeedsNoDocumentRequest(t *testing.T) {
	// npm ci asks for tarballs by the addresses of its lockfile, without documents; the repository
	// is empty. Each package is one document request and one tarball request of the upstream.
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	var picked []*corpus.Package
	for _, p := range corpus.Default().Packages {
		if len(p.Versions) >= 2 && len(p.Versions) < 30 {
			picked = append(picked, p)
		}
		if len(picked) == 8 {
			break
		}
	}
	for round := 0; round < 2; round++ {
		for _, p := range picked {
			v := p.Versions[len(p.Versions)-1]
			a := r.tarball(rep, rRemote, p.Name, v.Version, r.tokA)
			if a.Status != 200 || integrityOf(a.Body) != v.Dist.Integrity {
				t.Fatalf("%s@%s: %d, integrity %s want %s", p.Name, v.Version, a.Status, integrityOf(a.Body), v.Dist.Integrity)
			}
		}
	}
	if v := r.stub.Counts().Violations(1, 1); len(v) != 0 {
		t.Errorf("the upstream was asked more than once: %v", v)
	}
	if got := r.stub.Counts().Total(); got != 2*len(picked) {
		t.Errorf("%d upstream requests for %d packages, want %d", got, len(picked), 2*len(picked))
	}
}

func TestR_NPM_025_ATarballThatDoesNotMatchItsIntegrityIs502AndLeavesNoItem(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	v := p.Versions[0].Version
	if err := r.stub.SetMode(npmstub.Mode{BadIntegrity: []string{p.Name}}); err != nil {
		t.Fatal(err)
	}
	a := r.tarball(rep, rRemote, p.Name, v, r.tokA)
	if a.Status != 502 || code(t, a) != "upstream_integrity_mismatch" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	items := `SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2`
	if n := r.st.Number(items, rRemote, p.Name+"/-/"+corpus.TarballFile(p.Name, v)); n != 0 {
		t.Errorf("%d items of the refused tarball", n)
	}
	// Fixed: once the document is revalidated, a retry succeeds.
	if err := r.stub.SetMode(npmstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	if b := r.tarball(rep, rRemote, p.Name, v, r.tokA); b.Status != 200 || integrityOf(b.Body) != p.Versions[0].Dist.Integrity {
		t.Errorf("the retry: %d %.200s", b.Status, b.Body)
	}
}

func TestR_NPM_026_AVersionTheUpstreamDroppedIsGoneFromDocumentsAndTarballs(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	reps := r.start(2)
	rep := reps[0]
	p := upstreamPackage(t, 4)
	evil := p.Versions[1].Version
	if a := r.view(rep, rRemote, p.Name, r.tokA); a.Status != 200 {
		t.Fatal(a.Status)
	}
	if a := r.tarball(rep, rRemote, p.Name, evil, r.tokA); a.Status != 200 {
		t.Fatalf("the tarball is not cached: %d", a.Status)
	}
	if err := r.stub.Drop(p.Name, evil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	r.stub.Reset()

	// A lockfile install pins the version and asks no document: 404, one conditional document request.
	a := r.tarball(rep, rRemote, p.Name, evil, r.tokA)
	if a.Status != 404 {
		t.Fatalf("the dropped version answers %d", a.Status)
	}
	if docs, cond := r.upstreamDocs(p.Name), r.stub.Counts().ConditionalDocuments(p.Name); docs != 1 || cond != 1 {
		t.Errorf("%d document requests, %d conditional; want one conditional request", docs, cond)
	}
	if listed, unlisted := r.listedVersions(rRemote, p.Name); unlisted != 1 || listed != int64(len(p.Versions)-1) {
		t.Errorf("npm_remote_versions: %d listed, %d unlisted", listed, unlisted)
	}
	if n := r.events("npm.remote.version_removed", p.Name, ""); n != 1 {
		t.Errorf("%d npm.remote.version_removed events", n)
	}
	// Repeated within the period, the upstream records no request.
	for _, rp := range reps {
		if got := r.tarball(rp, rRemote, p.Name, evil, r.tokA); got.Status != 404 {
			t.Errorf("another request: %d", got.Status)
		}
	}
	if got := r.stub.Counts().Total(); got != 1 {
		t.Errorf("the upstream saw %d requests, want the one conditional request", got)
	}

	// Documents no longer list it, and neither the files address nor the cache view serves it.
	if vs := versionsOf(t, r.view(reps[1], rRemote, p.Name, r.tokA)); len(vs) != len(p.Versions)-1 || contains(strings.Join(vs, ","), evil+",") {
		t.Errorf("the document lists %v", vs)
	}
	files := r.st.Do(reps[1], http.MethodGet, "/files/"+rRemote+"/"+p.Name+"/-/"+corpus.TarballFile(p.Name, evil), r.tokA, nil)
	if files.Status != 404 {
		t.Errorf("/files/: %d", files.Status)
	}
	if cv := r.st.Do(reps[1], http.MethodGet, strings.Replace(tarballRoute(rRemote, p.Name, evil), rRemote, rRemote+"~cache", 1), r.tokA, nil); cv.Status != 404 {
		t.Errorf("cache view: %d", cv.Status)
	}
	// The marks are PostgreSQL state: a replica that holds nothing from Valkey or memory (the state
	// after FLUSHALL and a restart) answers alike.
	fresh := r.replica("web-fresh")
	if got := r.tarball(fresh, rRemote, p.Name, evil, r.tokA); got.Status != 404 {
		t.Errorf("a replica without any cache: %d", got.Status)
	}
	// Another version still installs.
	other := p.Versions[0].Version
	if got := r.tarball(fresh, rRemote, p.Name, other, r.tokA); got.Status != 200 {
		t.Errorf("an unaffected version: %d", got.Status)
	}
}

func TestR_NPM_026_ServeRemovedVersionsKeepsTheCachedTarballServed(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2, NPM: map[string]any{"serveRemovedVersions": true}})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 4)
	evil := p.Versions[1].Version
	r.view(rep, rRemote, p.Name, r.tokA)
	if a := r.tarball(rep, rRemote, p.Name, evil, r.tokA); a.Status != 200 {
		t.Fatalf("%d", a.Status)
	}
	if err := r.stub.Drop(p.Name, evil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	if a := r.tarball(rep, rRemote, p.Name, evil, r.tokA); a.Status != 200 {
		t.Errorf("the withdrawn version is not served although the repository serves removed versions: %d", a.Status)
	}
	// D-16: the document does not list a version the upstream's document stopped listing.
	if vs := versionsOf(t, r.view(rep, rRemote, p.Name, r.tokA)); len(vs) != len(p.Versions)-1 {
		t.Errorf("the document lists %d versions", len(vs))
	}
	if _, unlisted := r.listedVersions(rRemote, p.Name); unlisted != 1 {
		t.Errorf("%d unlisted", unlisted)
	}
}

func TestR_NPM_026_AnOlderCopyOfTheDocumentMarksNothingRemoved(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 4)
	newest := p.Versions[len(p.Versions)-1].Version
	r.view(rep, rRemote, p.Name, r.tokA)
	if err := r.stub.SetMode(npmstub.Mode{StaleCopy: []string{p.Name}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	a := r.view(rep, rRemote, p.Name, r.tokA)
	if a.Status != 200 || len(versionsOf(t, a)) != len(p.Versions) {
		t.Fatalf("%d: the newest version disappeared: %.200s", a.Status, a.Body)
	}
	if got := r.tarball(rep, rRemote, p.Name, newest, r.tokA); got.Status != 200 {
		t.Errorf("the newest version no longer installs: %d", got.Status)
	}
	if n := sumSeries(rep.Metrics(t), "trove_npm_stale_upstream_documents_total{"); n < 1 {
		t.Errorf("trove_npm_stale_upstream_documents_total = %v", n)
	}
	if listed, unlisted := r.listedVersions(rRemote, p.Name); unlisted != 0 || listed != int64(len(p.Versions)) {
		t.Errorf("%d listed, %d unlisted", listed, unlisted)
	}
	if n := r.events("npm.remote.version_removed", p.Name, ""); n != 0 {
		t.Errorf("%d removal events", n)
	}
}

func TestR_NPM_025_AGonePackageIs404UnlessRemovedVersionsAreServed(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
	rep := r.start(1)[0]
	p := upstreamPackage(t, 3)
	v := p.Versions[0].Version
	r.view(rep, rRemote, p.Name, r.tokA)
	if a := r.tarball(rep, rRemote, p.Name, v, r.tokA); a.Status != 200 {
		t.Fatal(a.Status)
	}
	if err := r.stub.SetMode(npmstub.Mode{Gone: []string{p.Name}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	if a := r.view(rep, rRemote, p.Name, r.tokA); a.Status != 404 {
		t.Errorf("the document of a gone package: %d", a.Status)
	}
	if a := r.tarball(rep, rRemote, p.Name, v, r.tokA); a.Status != 404 {
		t.Errorf("the tarball of a gone package: %d", a.Status)
	}
	if n := r.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'proxy.upstream_gone' AND repository = $1`, rRemote); n < 1 {
		t.Errorf("%d proxy.upstream_gone events", n)
	}
}
