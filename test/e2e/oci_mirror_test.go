//go:build e2e

package e2e

// R-REPO-055 and R-OCI-026 on kind for an OCI remote (set `oci`, R-OCI-035): a pass of a mirror of the counting OCI test upstream runs in the
// pool of two processes that claim the queue `mirror` alone, while docker and podman pull images through the same remote on the three
// client-serving replicas.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

// ociInflight reads the requests the OCI test upstream has open and the most open at once since the last reset.
func ociInflight(t testing.TB) (inflight struct{ Open, Max int }) {
	t.Helper()
	if err := json.Unmarshal(ociStub(t).send(t, http.MethodGet, "/-/stub/inflight", nil), &inflight); err != nil {
		t.Fatal(err)
	}
	return inflight
}

// ociMirrorBody is a mirror of the whole test upstream of the clients' images: library/* is what the clients pull while the pass runs, bulk/tags
// is the long part of the pass (a tag pattern that keeps `tags` tags of its 2,500).
func (e *mirrorEnv) ociMirrorBody(t testing.TB, key string, tags int) map[string]any {
	t.Helper()
	body := ociRemoteBody(t, key, map[string]any{
		"mode":   "mirror",
		"mirror": map[string]any{"concurrency": mirrorConcurrency, "maxVersionsPerPackage": tags},
		"limits": map[string]any{"maxConcurrentFetches": mirrorMaxFetches},
	})
	body["includePatterns"] = []string{"library/*", "bulk/tags"}
	body["oci"] = map[string]any{"mirror": map[string]any{"tags": []string{"t0*", "latest", "3", "1"}}}
	return body
}

// awaitPassRunning waits until the pass has discovered the names it covers and is still running: the first package of an OCI pass is the long
// one (bulk/tags), so a count of packages filled is no signal that the clients can start.
func (e *mirrorEnv) awaitPassRunning(t *testing.T, key string) mirrorStatus {
	t.Helper()
	var s mirrorStatus
	waitFor(t, "the pass of "+key+" to run", 5*time.Minute, time.Second, func() bool {
		s = e.status(t, key)
		if s.State == "failing" {
			t.Fatalf("the mirror of %s is failing: %s (%s)", key, s.Errors.LastMessage, s.Errors.LastCode)
		}
		return s.Pass != nil && s.Pass.Job != nil && s.Pass.NamesDiscovered > 0
	})
	return s
}

// TestOCIMirror_R_REPO_055_APassRunsWhileClientsPull: the pass of an OCI mirror (the catalog of the test upstream, `latest` and the tags
// t0000 to t0999 of bulk/tags, each held 100 ms by the upstream) runs in a process of the pool while docker and podman on three client pods pull
// library/alpine:3, library/busybox:latest and library/nginx:1 through the mirror. Every pull succeeds, the upstream never holds more requests
// open than the bound of R-REPO-055 (maxConcurrentFetches + n - 1 + B), and the pass ends complete.
func TestOCIMirror_R_REPO_055_APassRunsWhileClientsPull(t *testing.T) {
	e := mirrorFx(t)
	s := ociStub(t)
	host := ociRegistryHost(t)
	fx := &ociFixture{adm: e.adm, token: e.token, run: e.run} // the service account and the run of the mirror environment
	b := mirrorShare(mirrorConcurrency, mirrorMaxFetches)
	bound := mirrorBound(mirrorMaxFetches, mirrorServing, b)
	s.resetAll(t)
	s.ociMode(t, ocistub.Mode{Catalog: true, DelayMS: map[string]int{ocistub.ClassManifest: 100}})
	t.Cleanup(func() { s.resetAll(t) })

	key := e.create(t, e.ociMirrorBody(t, "oci-mirror-"+e.run, 1000))
	t.Cleanup(func() { e.drop(t, key); e.awaitNoPassRunning(t) })
	e.run_(t, key, false)
	st := e.awaitPassRunning(t, key)
	pod, node := poolPodOfPass(t, st.Pass.Job.ID)
	if !strings.HasPrefix(pod, "trove-worker-"+mirrorPoolName+"-") {
		t.Errorf("the pass runs in %q (node %s), want a process of the pool %s", pod, node, mirrorPoolName)
	}
	t.Logf("the pass runs in %s (%s, %d names discovered, %d packages filled) when the clients pull", pod, st.Pass.Method, st.Pass.NamesDiscovered, st.Pass.PackagesDone)
	s.reset(t)

	images := []string{"library/alpine:3", "library/busybox:latest", "library/nginx:1"}
	clients := make([]*ociClient, 6)
	for i := range clients {
		clients[i] = fx.client(t, i%3, []string{ociDocker, ociPodman}[i/3], fmt.Sprintf("mirror-pull-%d", i))
		clients[i].token = e.token
		clients[i].login(host)
	}
	results := runTogether(len(clients), func(i int) restoreResult {
		for _, img := range images {
			name, tag, _ := strings.Cut(img, ":")
			if out, err := clients[i].try("pull", fx.image(t, key, name, tag)); err != nil {
				return restoreResult{out: img + ":\n" + out, err: err}
			}
		}
		return restoreResult{}
	})
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("client %d (%s) failed to pull through the mirror while the pass ran: %v\n%s", i, clients[i].tool, r.err, tailLines(r.out, 20))
		}
	}
	open := ociInflight(t)
	t.Logf("the test upstream held at most %d requests open while the pass ran and 6 clients pulled (bound %d = %d + %d - 1 + %d)", open.Max, bound, mirrorMaxFetches, mirrorServing, b)
	if open.Max > bound {
		t.Errorf("R-REPO-055: the OCI test upstream held %d requests open at once, want at most maxConcurrentFetches + n - 1 + B = %d", open.Max, bound)
	}
	done := e.awaitComplete(t, key, 20*time.Minute)
	sum := done.LastCompletePass
	t.Logf("the pass ended %s: %d packages (%d failed), %d items, %d bytes, method %s", sum.Outcome, sum.Packages, sum.PackagesFailed, sum.ItemsStored, sum.BytesStored, sum.Method)
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 {
		t.Errorf("the pass ended %+v, want complete with no failed package", *sum)
	}
	e.jobs.check(t)
}
