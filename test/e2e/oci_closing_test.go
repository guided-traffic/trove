//go:build e2e

package e2e

// The additions of the full run of Test and Release on the commit that closes the phase (R-OCI-032, R-OCI-035): the registry with Valkey
// stopped, and a read-only subset against Docker Hub. The workflow starts them only when it is dispatched with closing=true; a run of them
// without TROVE_E2E_OCI_CLOSING=1 is a failure, never a skip, so they cannot go missing from the closing run (deploy/dev/e2e.sh oci-closing).

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

func requireOCIClosingRun(t testing.TB) {
	t.Helper()
	if os.Getenv("TROVE_E2E_OCI_CLOSING") != "1" {
		t.Fatal("this test belongs to the closing run: start it with deploy/dev/e2e.sh oci-closing (the closing=true dispatch of the workflow)")
	}
}

// TestOCIClosing: with Valkey stopped, docker push and docker pull through local, remote and virtual repositories succeed (R-OCI-032). The
// registry tokens are self-contained, the upload sessions are in PostgreSQL and S3, the manifests and tags are rows, and the flight of a remote
// blob falls back to one upstream request per replica (R-ARCH-033).
func TestOCIClosing(t *testing.T) {
	requireOCIClosingRun(t)
	fx := ociFx(t)
	s := ociStub(t)
	host := ociRegistryHost(t)
	arch := ociArch(t)
	repos := fx.upstreamRepos(t, "closing", nil)
	s.resetAll(t)
	defer s.resetAll(t)

	pruneClients(t, 3, ociDocker, ociPodman)
	withValkeyStopped(t, func() {
		servingReadyDegraded(t, true)

		t.Run("R-OCI-032 docker push and pull through a local repository with Valkey stopped, the pull by podman on another pod", func(t *testing.T) {
			name := fx.name("closing-local")
			ref := fx.image(t, repos.local, name, "1")
			a := fx.client(t, 0, ociDocker, "closing-push")
			a.login(host)
			a.build(ref, "closing local "+fx.run)
			a.push(ref)
			digest := fx.digestOf(t, repos.local, name, "1")
			b := fx.client(t, 1, ociPodman, "closing-pull")
			b.login(host)
			b.pull(ref)
			if !b.holds(ref, digest) {
				t.Fatalf("podman holds %q after the pull, want the digest %s", b.digests(ref), digest)
			}
		})

		t.Run("R-OCI-032 R-OCI-023 six concurrent pulls through a remote repository with Valkey stopped cost at most one request per layer and replica", func(t *testing.T) {
			webs := packagesReplicas(t, 3)
			for pod, web := range webs[:3] {
				ociPin(t, pod, web.IP)
				defer ociUnpin(t, pod)
			}
			s.ociMode(t, ocistub.Mode{DelayMS: map[string]int{ocistub.ClassBlob: 1000, ocistub.ClassCDN: 1000}})
			defer s.ociMode(t, ocistub.Mode{})
			s.reset(t)
			info := s.ociImage(t, "library/busybox", "latest")
			ref := fx.image(t, repos.remote, "library/busybox", "latest")
			clients := make([]*ociClient, 6)
			for i := range clients {
				clients[i] = fx.client(t, i%3, []string{ociDocker, ociPodman}[i/3], fmt.Sprintf("closing-flight-%d", i))
				clients[i].login(host)
			}
			for i, r := range runTogether(len(clients), func(i int) restoreResult {
				out, err := clients[i].try("pull", ref)
				return restoreResult{out: out, err: err}
			}) {
				if r.err != nil {
					t.Fatalf("pull %d (%s) failed: %v\n%s", i, clients[i].tool, r.err, tailLines(r.out, 15))
				}
			}
			counts := s.ociCounts(t)
			for _, d := range ociPlatformBlobs(t, info, arch) {
				if n := counts.BlobFetches(d); n < 1 || n > 3 {
					t.Errorf("the upstream delivered the blob %s %d times for six pulls on three replicas without Valkey, want one to three (one per replica)", d, n)
				}
			}
		})

		t.Run("R-OCI-032 docker push and pull through a virtual repository with Valkey stopped", func(t *testing.T) {
			name := fx.name("closing-virtual")
			ref := fx.image(t, repos.all, name, "1")
			a := fx.client(t, 2, ociDocker, "closing-virtual-push")
			a.login(host)
			a.build(ref, "closing virtual "+fx.run)
			a.push(ref)
			digest := fx.digestOf(t, repos.local, name, "1")
			b := fx.client(t, 0, ociDocker, "closing-virtual-pull")
			b.login(host)
			b.pull(ref)
			if !b.holds(ref, digest) {
				t.Fatalf("docker holds %q after the pull, want the digest %s", b.digests(ref), digest)
			}
			// the remote member's image through the same repository
			c := fx.client(t, 1, ociDocker, "closing-virtual-remote")
			c.login(host)
			c.pull(fx.image(t, repos.all, "library/alpine", "3"))
		})
	})
	servingReadyDegraded(t, false)
}

// TestOCIClosingPublic is the read-only subset against Docker Hub (R-OCI-035): a pull-through remote of the registry's default upstream pulls
// library/busybox:latest anonymously with docker, and nothing is pushed or changed upstream. Docker Hub counts manifest GET requests against a
// limit, so the subset pulls one tag once; the number of requests Docker Hub received cannot be read from outside, and the test does not claim
// it (docs/developer/e2e.md#the-oci-tests).
func TestOCIClosingPublic(t *testing.T) {
	requireOCIClosingRun(t)
	fx := ociFx(t)
	host := ociRegistryHost(t)
	key := "oci-hub-" + fx.run
	fx.adm.do("POST", reposRoute, map[string]any{
		"key": key, "class": "remote", "format": "oci", "requireAuthentication": true,
		"remote": map[string]any{
			"url":        "https://registry-1.docker.io/",
			"extraHosts": []map[string]any{{"pattern": "auth.docker.io", "sendCredentials": false}},
		},
	}).want(t, "create "+key, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	ensurePolicy(t, fx.adm, "e2e-oci-hub-"+fx.run, []string{key}, [2]any{"sa:" + ociPublisher, []string{"read"}})
	time.Sleep(3 * time.Second) // the bound of R-AUTHZ-028; a probe would ask Docker Hub for a manifest that does not exist

	t.Run("R-OCI-025 R-OCI-035 docker pulls library/busybox:latest from Docker Hub through the remote, anonymously, as busybox and as library/busybox", func(t *testing.T) {
		c := fx.client(t, 0, ociDocker, "hub")
		c.login(host)
		short, long := fx.image(t, key, "busybox", "latest"), fx.image(t, key, "library/busybox", "latest")
		c.pull(short)
		id := func(ref string) string { return strings.Fields(c.digests(ref))[0] } // the image ID; the repo digests name the spelling
		d1 := id(short)
		c.pull(long) // the one cached copy: library/ maps the one-component name (Q-OCI-14)
		if d2 := id(long); d1 != d2 {
			t.Errorf("busybox and library/busybox are %q and %q: one cached copy is expected", d1, d2)
		}
	})

	t.Run("R-OCI-026 R-OCI-035 a mirror of library/busybox with tags [latest] stores the tag, and docker pulls it from the mirror", func(t *testing.T) {
		mirror := "oci-hub-mirror-" + fx.run
		fx.adm.do("POST", reposRoute, map[string]any{
			"key": mirror, "class": "remote", "format": "oci", "requireAuthentication": true,
			"includePatterns": []string{"library/busybox"},
			"remote": map[string]any{
				"url": "https://registry-1.docker.io/", "mode": "mirror",
				"extraHosts": []map[string]any{{"pattern": "auth.docker.io", "sendCredentials": false}},
				"mirror":     map[string]any{"concurrency": 1, "maxVersionsPerPackage": 1},
			},
			"oci": map[string]any{"mirror": map[string]any{"tags": []string{"latest"}}},
		}).want(t, "create "+mirror, 201)
		t.Cleanup(func() { deleteRepository(t, fx.adm, mirror) })
		ensurePolicy(t, fx.adm, "e2e-oci-hub-mirror-"+fx.run, []string{mirror}, [2]any{"sa:" + ociPublisher, []string{"read"}})
		time.Sleep(3 * time.Second) // the bound of R-AUTHZ-028
		e := &mirrorEnv{adm: fx.adm}
		e.run_(t, mirror, false)
		done := e.awaitComplete(t, mirror, 10*time.Minute)
		if sum := done.LastCompletePass; sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages != 1 {
			t.Fatalf("the pass ended %+v, want complete with one package", *sum)
		}
		c := fx.client(t, 1, ociDocker, "hub-mirror")
		c.login(host)
		c.pull(fx.image(t, mirror, "library/busybox", "latest"))
	})
}
