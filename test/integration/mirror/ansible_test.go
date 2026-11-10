//go:build integration

package mirror

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

// Discovery against the counting Ansible Galaxy stub (R-ANS-021; docs/spec/formats/ansible.md "Mirror discovery", R-REPO-051). The
// stub answers on the loopback address of the rig; the Galaxy host is that name, and the archives are on the same host.

// newAnsibleRig starts a rig whose upstream is the API root of the Galaxy stub.
func newAnsibleRig(t *testing.T, cfg ansiblestub.Config, o rigOptions) (*rig, *ansiblestub.Server) {
	t.Helper()
	stub, err := ansiblestub.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	o.format, o.handler, o.indexPath = "ansible", stub, "api/"
	return newRig(t, o), stub
}

// ansibleArchives is the number of collection archives the repository holds.
func (r *rig) ansibleArchives() int64 {
	return r.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE 'collections/%.tar.gz' AND i.trashed_at IS NULL`, repoKey)
}

// ansibleStored reports whether the archive of a collection version is an item of the repository.
func (r *rig) ansibleStored(ns, name, version string) bool {
	return r.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2 AND i.trashed_at IS NULL`,
		repoKey, "collections/"+ns+"/"+name+"/"+ns+"-"+name+"-"+version+".tar.gz") == 1
}

func TestR_ANS_021_APassOfTheCollectionIndexStoresTheCollectionsThePatternsSelect(t *testing.T) {
	r, stub := newAnsibleRig(t, ansiblestub.Config{Fillers: 120}, rigOptions{include: []string{"acme.*"}})
	run, web := r.runner("runner"), r.web("web")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-ANS-021 every version of every acme collection is stored, nothing of another", func(t *testing.T) {
		// acme.base 2, acme.tools 4 (a pre-release among them), acme.signed 1.
		if n := r.ansibleArchives(); n != 7 {
			t.Errorf("%d archives stored, want 7", n)
		}
		for _, v := range [][3]string{{"acme", "base", "1.0.0"}, {"acme", "base", "1.2.0"}, {"acme", "tools", "2.1.0-rc.1"}, {"acme", "signed", "1.0.0"}} {
			if !r.ansibleStored(v[0], v[1], v[2]) {
				t.Errorf("%s.%s %s is not stored", v[0], v[1], v[2])
			}
		}
		if r.ansibleStored("other", "stuff", "1.0.0") {
			t.Error("other.stuff was stored")
		}
		if n := stub.Counts().Prefixed("/api/v3/collections/many/", http.MethodGet) + stub.Counts().Prefixed("/api/v3/collections/other/", http.MethodGet); n != 0 {
			t.Errorf("%d requests for collections the patterns refuse", n)
		}
		if st := r.summary(); st["method"] != "listing" || st["kind"] != "full" || st["itemsStored"] != float64(7) {
			t.Errorf("summary %v", st)
		}
	})

	t.Run("R-ANS-021 the index is read by offset, 100 entries a request, to its end", func(t *testing.T) {
		// 125 collections: the five of the corpus and the 120 fillers, two index pages.
		if n := stub.Counts().Requests(http.MethodGet, "/api/v3/collections/"); n != 2 {
			t.Errorf("%d index requests, want 2", n)
		}
	})

	t.Run("R-ANS-021 an install afterwards makes no request to the stub", func(t *testing.T) {
		before := stub.Counts().Total()
		for _, p := range []string{"v3/collections/acme/tools/versions/", "v3/collections/acme/tools/versions/2.0.0/", "v3/artifacts/collections/acme-tools-2.0.0.tar.gz", "v3/artifacts/collections/acme-base-1.2.0.tar.gz"} {
			if a := r.get(web, p); a.Status != http.StatusOK {
				t.Fatalf("%s: %d %s", p, a.Status, a.Body)
			}
		}
		if after := stub.Counts().Total(); after != before {
			t.Errorf("the installs made %d requests to the stub", after-before)
		}
	})
}

func TestR_ANS_021_TheMirrorKeepsWhatTheUpstreamDropsAndTakesWhatItPublishes(t *testing.T) {
	r, stub := newAnsibleRig(t, ansiblestub.Config{}, rigOptions{include: []string{"acme.base", "acme.tools"}, mirror: settings{"discovery": "names"}})
	run := r.runner("runner")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)
	if st := r.summary(); st["method"] != "names" || st["packages"] != float64(2) {
		t.Fatalf("summary %v", st)
	}
	if n := stub.Counts().Requests(http.MethodGet, "/api/v3/collections/"); n != 0 {
		t.Errorf("the method names read the index %d times", n)
	}
	if n := r.ansibleArchives(); n != 6 {
		t.Fatalf("%d archives stored, want 6", n)
	}

	if err := stub.DropVersion("acme.tools", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := stub.AddVersion("acme.base", "1.3.0"); err != nil {
		t.Fatal(err)
	}
	// The stored documents are fresh for their period: expire them, as a period that passed would.
	r.st.Exec(`UPDATE remote_cache_entries SET expires_at = now() - interval '1 hour', validated_at = now() - interval '2 hours' WHERE repo_id = $1 AND kind = 'metadata'`, r.repoN)
	t1 := r.pass(run)
	r.waitPass(t1, "full", "complete", passWithin)
	if !r.ansibleStored("acme", "base", "1.3.0") {
		t.Error("the version the upstream published is not stored")
	}
	if !r.ansibleStored("acme", "tools", "1.0.0") {
		t.Error("the archive of the version the upstream dropped was deleted: the mirror deletes nothing")
	}
}

func TestR_ANS_021_TheVersionBoundsOfTheMirrorLeaveVersionsOutBeforeTheirDocumentsAreRead(t *testing.T) {
	r, stub := newAnsibleRig(t, ansiblestub.Config{}, rigOptions{
		include: []string{"acme.tools"}, mirror: settings{"discovery": "names", "maxVersionsPerPackage": 1, "includePrereleases": false},
	})
	run := r.runner("runner")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)
	if n := r.ansibleArchives(); n != 1 || !r.ansibleStored("acme", "tools", "2.0.0") {
		t.Errorf("%d archives stored; want 2.0.0 alone (the pre-release 2.1.0-rc.1 is out, one version is kept)", n)
	}
	// The documents of the versions that are out are not read.
	for _, v := range []string{"2.1.0-rc.1", "1.1.0", "1.0.0"} {
		if n := stub.Counts().Requests(http.MethodGet, "/api/v3/collections/acme/tools/versions/"+v+"/"); n != 0 {
			t.Errorf("the document of %s was read %d times", v, n)
		}
	}
}

func TestR_ANS_021_ARateLimitedIndexSlowsThePassAndItCompletes(t *testing.T) {
	r, stub := newAnsibleRig(t, ansiblestub.Config{}, rigOptions{include: []string{"acme.*"}})
	if err := stub.SetThrottle(ansiblestub.Throttle{Mode: ansiblestub.ThrottleTooManyRequests, Class: ansiblestub.ClassIndex, RetryAfterSeconds: 1, Nth: 1, Count: 2}); err != nil {
		t.Fatal(err)
	}
	run := r.runner("runner")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)
	if st := stub.ThrottleState(); st.Throttled != 2 {
		t.Errorf("the stub throttled %d index requests, want 2", st.Throttled)
	}
	if n := r.ansibleArchives(); n != 7 {
		t.Errorf("%d archives stored after the throttled pass, want 7", n)
	}
}
