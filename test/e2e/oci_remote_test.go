//go:build e2e

package e2e

// The OCI acceptance tests of a remote repository (R-OCI-021 to R-OCI-023, R-OCI-026, R-REPO-050): the real clients pull through a remote of
// the counting test upstream (deploy/dev/ocistub.yaml, test/ocistub), in mode pullThrough and in mode mirror. What the upstream saw is the
// evidence: the first pull fills the cache, the second is served from it, and six concurrent pulls on three replicas cost one request per
// layer.

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

// ociPlatformBlobs are the blobs a pull of the image on the clients' platform fetches: the configuration and the layers of its platform
// manifest.
func ociPlatformBlobs(t testing.TB, info ocistub.ImageInfo, arch string) []string {
	t.Helper()
	m, ok := info.ForPlatform("linux/" + arch)
	if !ok {
		t.Fatalf("the image %s has no manifest for linux/%s", info.Name, arch)
	}
	blobs := m.Blobs()
	if len(blobs) == 0 {
		t.Fatalf("the image %s has no blobs for linux/%s", info.Name, arch)
	}
	return blobs
}

func TestOCIRemote(t *testing.T) {
	fx := ociFx(t)
	s := ociStub(t)
	host := ociRegistryHost(t)
	_, _, cdnHost := ociStubHosts(t)
	arch := ociArch(t)
	repos := fx.upstreamRepos(t, "remote", map[string]any{"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 5}})
	s.resetAll(t)
	defer s.resetAll(t)
	for i := range 3 {
		fx.client(t, i, ociDocker, "remote-login").login(host)
	}
	pruneClients(t, 3, ociDocker, ociPodman)

	t.Run("R-OCI-021 R-OCI-022 R-OCI-023 docker pull through a remote fills the cache: one manifest GET, each layer once, the CDN host sees no credential", func(t *testing.T) {
		s.reset(t)
		info := s.ociImage(t, "library/alpine", "3")
		ref := fx.image(t, repos.remote, "library/alpine", "3")
		c := fx.client(t, 0, ociDocker, "remote-fill")
		c.login(host)
		c.pull(ref)
		if !c.holds(ref, info.Digest) {
			t.Fatalf("docker holds %q after the pull, the upstream's index is %s", c.digests(ref), info.Digest)
		}
		counts := s.ociCounts(t)
		if n := counts.ManifestGets("library/alpine", "3"); n != 1 {
			t.Fatalf("the upstream saw %d manifest GET requests for library/alpine:3, want 1: %s", n, counts.Summary())
		}
		for _, d := range ociPlatformBlobs(t, info, arch) {
			if n := counts.BlobFetches(d); n != 1 {
				t.Errorf("the upstream delivered the blob %s %d times, want once", d, n)
			}
		}
		if schemes := counts.AuthSchemes(cdnHost); !reflect.DeepEqual(schemes, []string{"none"}) {
			t.Errorf("the blob CDN host saw the credential schemes %v, want none only (R-OCI-023)", schemes)
		}
		if counts.TokenRequests("") < 1 {
			t.Errorf("the realm saw no token request: the handshake did not run")
		}
	})

	t.Run("R-OCI-022 R-OCI-023 R-OCI-032 a second pull on another pod and by podman is served from the cache", func(t *testing.T) {
		before := s.ociCounts(t)
		info := s.ociImage(t, "library/alpine", "3")
		ref := fx.image(t, repos.remote, "library/alpine", "3")
		for pod, tool := range map[int]string{1: ociDocker, 2: ociPodman} {
			c := fx.client(t, pod, tool, "remote-hit")
			c.login(host)
			c.pull(ref)
			if !c.holds(ref, info.Digest) {
				t.Fatalf("%s holds %q after the pull, the upstream's index is %s", tool, c.digests(ref), info.Digest)
			}
		}
		after := s.ociCounts(t)
		if n := after.ManifestGets("library/alpine", "3"); n != before.ManifestGets("library/alpine", "3") {
			t.Errorf("the upstream saw %d manifest GET requests for library/alpine:3, %d before the pulls of the cache", n, before.ManifestGets("library/alpine", "3"))
		}
		for _, d := range ociPlatformBlobs(t, info, arch) {
			if n := after.BlobFetches(d); n != 1 {
				t.Errorf("the upstream delivered the blob %s %d times after the cached pulls, want once", d, n)
			}
		}
	})

	t.Run("R-OCI-023 R-OCI-032 six concurrent pulls of one image through three replicas fetch each layer from the upstream once", func(t *testing.T) {
		webs := packagesReplicas(t, 3)
		for pod, web := range webs[:3] {
			ociPin(t, pod, web.IP)
			defer ociUnpin(t, pod)
		}
		// the upstream holds every blob for a second, so that the six pulls overlap on the replicas
		s.ociMode(t, ocistub.Mode{DelayMS: map[string]int{ocistub.ClassBlob: 1000, ocistub.ClassCDN: 1000}})
		defer s.ociMode(t, ocistub.Mode{})
		s.reset(t)
		info := s.ociImage(t, "library/nginx", "1")
		ref := fx.image(t, repos.remote, "library/nginx", "1")
		clients := make([]*ociClient, 6)
		for i := range clients {
			tool := []string{ociDocker, ociPodman}[i/3]
			clients[i] = fx.client(t, i%3, tool, fmt.Sprintf("flight-%d", i))
			clients[i].login(host)
		}
		results := runTogether(len(clients), func(i int) restoreResult {
			out, err := clients[i].try("pull", ref)
			return restoreResult{out: out, err: err}
		})
		for i, r := range results {
			if r.err != nil {
				t.Fatalf("pull %d (%s) failed: %v\n%s", i, clients[i].tool, r.err, tailLines(r.out, 15))
			}
		}
		counts := s.ociCounts(t)
		for _, d := range ociPlatformBlobs(t, info, arch) {
			if n := counts.BlobFetches(d); n != 1 {
				t.Errorf("the upstream delivered the blob %s %d times for six concurrent pulls on three replicas, want once (R-OCI-023)", d, n)
			}
		}
		t.Logf("six concurrent pulls on three replicas: %s", counts.Summary())
	})

	t.Run("R-OCI-022 a tag the upstream does not have answers 404 and no client pulls it", func(t *testing.T) {
		c := fx.client(t, 0, ociDocker, "remote-missing")
		c.login(host)
		out, err := c.try("pull", fx.image(t, repos.remote, "library/alpine", "no-such-tag"))
		if err == nil || !strings.Contains(strings.ToLower(out), "not found") {
			t.Fatalf("docker pull of a tag the upstream lacks: %v\n%s", err, tailLines(out, 15))
		}
	})
}

// TestOCIRemoteMirror is the mirror of an OCI remote with the real clients (R-OCI-026, R-REPO-050): a pass lists the catalog of the test
// upstream and stores the tags that match, then the upstream is stopped, and docker and podman still pull what the pass stored while a tag
// outside the patterns cannot be pulled.
func TestOCIRemoteMirror(t *testing.T) {
	fx := ociFx(t)
	s := ociStub(t)
	host := ociRegistryHost(t)
	s.resetAll(t)
	s.ociMode(t, ocistub.Mode{Catalog: true})
	t.Cleanup(func() { s.resetAll(t) })

	key := "oci-mirror-" + fx.run
	body := ociRemoteBody(t, key, map[string]any{
		"mode":   "mirror",
		"mirror": map[string]any{"concurrency": 2, "maxVersionsPerPackage": 2},
	})
	body["includePatterns"] = []string{"acme/*"}
	body["oci"] = map[string]any{"mirror": map[string]any{"tags": []string{"1.*", "latest"}}}
	fx.adm.do("POST", reposRoute, body).want(t, "create "+key, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	ensurePolicy(t, fx.adm, "e2e-oci-mirror-"+fx.run, []string{key}, [2]any{"sa:" + ociPublisher, []string{"read"}})
	ociReadableOnEveryReplicaFor(t, fx.token, "acme/zz-probe", key)

	e := &mirrorEnv{adm: fx.adm}
	e.run_(t, key, false)
	done := e.awaitComplete(t, key, 10*time.Minute)
	sum := done.LastCompletePass
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages < 2 {
		t.Fatalf("the pass ended %+v, want complete with at least acme/a and acme/b and no failed package", *sum)
	}
	t.Logf("the pass ended %s: %d packages, %d items, %d bytes, method %s", sum.Outcome, sum.Packages, sum.ItemsStored, sum.BytesStored, sum.Method)

	indexDigest := s.ociImage(t, "acme/b", "latest").Digest
	// The upstream goes away: its connections are cut at once (the switch `down: reset` of the stub). Scaling its Deployment to zero would leave
	// the Service without an endpoint, and a kind node drops those connections without an answer, so that every request waits for the upstream
	// client's connect timeout (H-751).
	s.ociMode(t, ocistub.Mode{Catalog: true, Down: ocistub.DownReset})
	defer s.resetAll(t)

	t.Run("R-OCI-024 R-OCI-026 the referrers of a mirrored image with the upstream down are not an answer that fails the pull", func(t *testing.T) {
		// docker asks for the referrers of every image it pulls, also of one a mirror holds completely
		start := time.Now()
		r := fx.registry(t).do(http.MethodGet, "/v2/"+repoRef(key)+"/acme/b/referrers/"+indexDigest, nil)
		t.Logf("GET referrers of acme/b@%s with the upstream down: %d after %s: %s", indexDigest[:19], r.Status, time.Since(start).Round(time.Millisecond), r.Body)
		if r.Status != http.StatusNotFound {
			t.Errorf("the referrers with the upstream down answered %s, want 404 so that the client falls back to the tag schema (Q-OCI-128)", r)
		}
	})

	t.Run("R-OCI-026 R-REPO-050 docker and podman pull the mirrored tags with the upstream stopped", func(t *testing.T) {
		for i, tc := range []struct{ tool, name, tag string }{
			{ociDocker, "acme/a", "1.2"}, {ociDocker, "acme/a", "latest"}, {ociPodman, "acme/b", "1.0"}, {ociDocker, "acme/b", "latest"},
		} {
			c := fx.client(t, i%3, tc.tool, fmt.Sprintf("mirror-%d", i))
			c.login(host)
			ref := fx.image(t, key, tc.name, tc.tag)
			c.pull(ref)
			if got := c.digests(ref); !strings.Contains(got, "sha256:") {
				t.Errorf("%s holds %q after pulling %s", tc.tool, got, ref)
			}
		}
	})

	t.Run("R-OCI-026 a tag outside the patterns was not mirrored and cannot be pulled with the upstream stopped", func(t *testing.T) {
		c := fx.client(t, 0, ociDocker, "mirror-outside")
		c.login(host)
		if out, err := c.try("pull", fx.image(t, key, "acme/a", "2.0")); err == nil {
			t.Fatalf("docker pulled acme/a:2.0, which no pattern selects, with the upstream stopped:\n%s", tailLines(out, 10))
		}
	})
}
