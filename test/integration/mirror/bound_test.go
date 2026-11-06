//go:build integration

package mirror

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
)

// The byte bound and the release at shutdown against the counting npm stub (E6b.4; R-REPO-052, R-REPO-053). The npm registry
// announces no tarball size (Q-NPM-74), so the bound is checked after each fetch: the case the fake host of the engine's own suite
// does not have. What the bound does across passes needs the fold of repository_usage (H-527) and is not tested here.

const (
	maxBytes10MiB = 10485760
	maxBytes64MiB = 67108864
)

func TestR_REPO_052_TheByteBoundEndsThePassNearTenMiBAndTheClientsAreUnaffected(t *testing.T) {
	const packages, pad, concurrency = 30, 1 << 20, 4
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, mirror: settings{"maxBytes": maxBytes10MiB, "concurrency": concurrency}})
	for i := range packages {
		if err := r.stub.PublishPadded(fmt.Sprintf("@acme/p%03d", i), "1.0.0", pad); err != nil {
			t.Fatal(err)
		}
	}
	run, web := r.runner("runner"), r.web("web")
	t0 := r.pass(run)
	r.waitPass(t0, "full", "bound_reached", passWithin)

	t.Run("R-REPO-052 the pass stores at most 10 MiB plus concurrency times the largest tarball", func(t *testing.T) {
		stored := r.number(`SELECT COALESCE(sum(i.size), 0) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE '%.tgz'`, repoKey)
		largest := r.number(`SELECT COALESCE(max(i.size), 0) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE '%.tgz'`, repoKey)
		files := r.storedItems()
		if largest < pad {
			t.Fatalf("the largest stored tarball is %d bytes", largest)
		}
		if limit := int64(maxBytes10MiB) + concurrency*largest; stored > limit {
			t.Errorf("%d bytes stored in %d tarballs, want at most %d", stored, files, limit)
		}
		if files >= packages || stored < maxBytes10MiB-largest {
			t.Errorf("%d tarballs, %d bytes stored: the pass did not run up to the bound", files, stored)
		}
		if st := r.summary(); st["outcome"] != "bound_reached" {
			t.Errorf("summary %v", st)
		}
	})

	t.Run("R-REPO-052 one mirror.bound_reached event is written and the status shows the bound", func(t *testing.T) {
		if n := r.waitAudit("mirror.bound_reached", 1); n != 1 {
			t.Errorf("%d mirror.bound_reached events, want 1", n)
		}
		a := r.api(web, http.MethodGet, "/api/v1/projects/it/repositories/"+repoKey+"/mirror", nil)
		doc := a.JSON(t)
		bound, _ := doc["bound"].(map[string]any)
		if a.Status != http.StatusOK || doc["state"] != "bound_reached" || bound["reached"] != "bytes" || bound["maxBytes"] != float64(maxBytes10MiB) {
			t.Errorf("status %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-REPO-052 a client fetch of a version the pass did not store succeeds through the same remote", func(t *testing.T) {
		pending := r.st.Text(`SELECT name FROM mirror_pending WHERE repo_id = $1 ORDER BY name LIMIT 1`, r.repoN)
		if pending == "" || r.storedItem(tarballPath(pending, "1.0.0")) {
			t.Fatalf("no unstored package is pending: %q", pending)
		}
		if a := r.get(web, tarballPath(pending, "1.0.0")); a.Status != http.StatusOK || len(a.Body) < pad {
			t.Errorf("GET %s: %d, %d bytes", pending, a.Status, len(a.Body))
		}
	})

	t.Run("R-REPO-052 after maxBytes is raised to 64 MiB the next pass stores the rest", func(t *testing.T) {
		doc := remoteDocAt(r.upstream, "mirror", []string{"@acme/*"}, settings{"maxBytes": maxBytes64MiB, "concurrency": concurrency}, nil)
		r.setDocument(run, doc)
		r.waitMirror(run, "maxBytes 64 MiB", func(s repository.RemoteSettings) bool {
			return s.Mirror.MaxBytes != nil && *s.Mirror.MaxBytes == maxBytes64MiB
		})
		at := r.pass(run)
		r.waitPass(at, "", "complete", passWithin)
		if n := r.storedItems(); n != packages {
			t.Errorf("%d tarballs stored, want %d", n, packages)
		}
		if n := r.number(`SELECT count(*) FROM mirror_pending WHERE repo_id = $1`, r.repoN); n != 0 {
			t.Errorf("%d names are still pending", n)
		}
	})
}

func TestR_REPO_053_AShutdownReleasesThePassAndASecondProcessFetchesEveryTarballOnce(t *testing.T) {
	const packages, concurrency = 200, 2
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, mirror: settings{"concurrency": concurrency}})
	names := r.publishMany("@acme", packages)
	r.setStub(npmstub.Mode{DelayMS: map[string]int{npmstub.KindTarball: 60}})
	a := r.runner("runner-a", "TROVE_SERVER_SHUTDOWN_TIMEOUT=8s")
	t0 := r.pass(a)
	testenv.WaitFor(t, "50 packages to be done", passWithin, func() bool {
		n, err := r.st.TryNumber(`SELECT pass_packages_done FROM mirror_state WHERE repo_id = $1`, r.repoN)
		return err == nil && n >= 50
	})
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}

	t.Run("R-REPO-053 the job is queued again with its attempt unchanged and the pass is kept", func(t *testing.T) {
		if got := r.st.Text(`SELECT state::text || '|' || attempt::text FROM job WHERE kind = 'mirror-pass'`); got != "queued|0" {
			t.Errorf("the released job is %q, want queued|0", got)
		}
		if n := r.number(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND pass_started_at IS NOT NULL AND consecutive_failures = 0`, r.repoN); n != 1 {
			t.Error("the release cleared the pass or counted a failure")
		}
		if done := r.number(`SELECT pass_packages_done FROM mirror_state WHERE repo_id = $1`, r.repoN); done < 50 || done >= packages {
			t.Errorf("%d packages done at the release", done)
		}
	})

	r.runner("runner-b")
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-REPO-053 the second process ends the pass and the stub saw every tarball once, apart from the packages in progress", func(t *testing.T) {
		if n := r.storedItems(); n != packages {
			t.Errorf("%d tarballs stored, want %d", n, packages)
		}
		twice := 0
		for _, n := range names {
			base := n[len("@acme/"):]
			switch got := r.stub.Counts().Tarball(n, base+"-1.0.0.tgz"); {
			case got == 0:
				t.Errorf("%s was never fetched", n)
			case got == 2:
				twice++
			case got > 2:
				t.Errorf("%s was fetched %d times", n, got)
			}
		}
		if twice > concurrency {
			t.Errorf("%d tarballs were fetched twice, at most the %d that were in flight at the shutdown may be", twice, concurrency)
		}
		if got := r.st.Text(`SELECT state::text FROM job WHERE kind = 'mirror-pass' ORDER BY created_at DESC LIMIT 1`); got != "succeeded" {
			t.Errorf("the job is %s", got)
		}
	})
}
