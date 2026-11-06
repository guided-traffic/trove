//go:build e2e

package e2e

// The helpers of the NuGet tests that need the counting test upstream (test/nugetstub, deploy/dev/nugetstub.yaml): the
// control client, the remote and virtual repositories of one test, and the pinning of a clients pod to one packages replica.
// The scenarios are in nuget_remote_test.go, nuget_virtual_test.go and nuget_collapse_test.go.

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

var (
	nugetStubOnce sync.Once
	nugetStubVal  *stubControl
)

// nugetStubHosts returns the two hosts of the stub's service index: the API host (index, registrations) and the content
// host (flat container, search), as deploy/dev/e2e.sh exports them.
func nugetStubHosts(t testing.TB) (api, content string) {
	t.Helper()
	api, content = os.Getenv("TROVE_E2E_NUGETSTUB_HOST"), os.Getenv("TROVE_E2E_NUGETSTUB_CONTENT_HOST")
	if api == "" || content == "" {
		t.Fatal("TROVE_E2E_NUGETSTUB_HOST and TROVE_E2E_NUGETSTUB_CONTENT_HOST are not set; run `deploy/dev/e2e.sh nuget`")
	}
	return api, content
}

// nugetStub is the control client of the counting NuGet test upstream; the methods reset and resetAll of the npm stub's
// client work on it as well (the control API has the same shape), nugetCounts, nugetMode and nugetPublish are the NuGet ones.
func nugetStub(t testing.TB) *stubControl {
	t.Helper()
	host, _ := nugetStubHosts(t)
	nugetStubOnce.Do(func() { nugetStubVal = newStubControl(t, "svc/nugetstub", host, "trove-nugetstub", 30*time.Second) })
	return nugetStubVal
}

// nugetPublish offers a version of a package ID at the NuGet test upstream, without dependencies; a 409 of a repeat after
// a transport error is a success when the stub lists exactly that version, listed (publishVersion).
func (s *stubControl) nugetPublish(t testing.TB, id, version string) {
	t.Helper()
	s.publishVersion(t, id, version, map[string]any{"name": id, "version": version}, nugetPublished(id, version))
}

// nugetPublished compares the answer of GET /-/stub/version of the NuGet test upstream with a publish without dependencies.
func nugetPublished(id, version string) func([]byte) error {
	return func(served []byte) error {
		var v nugetstub.ServedVersion
		if err := json.Unmarshal(served, &v); err != nil {
			return fmt.Errorf("the answer of GET /-/stub/version is no version (%w): %s", err, served)
		}
		return v.Matches(id, version, nil)
	}
}

// restoreResult is the outcome of one command run on a goroutine of its own.
type restoreResult struct {
	out string
	err error
}

// runTogether starts every function at once and returns their results in order. The functions must not call the
// Fatal methods of the test (they run off the test's goroutine); a command's failure is its returned error.
func runTogether(n int, f func(i int) restoreResult) []restoreResult {
	out := make([]restoreResult, n)
	for i := range out {
		out[i].err = fmt.Errorf("the function of client %d did not finish (it stopped the test)", i)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = f(i)
		}()
	}
	close(start)
	wg.Wait()
	return out
}

// nugetMode replaces the failure switches of the NuGet test upstream; the zero Mode clears them.
func (s *stubControl) nugetMode(t testing.TB, m nugetstub.Mode) {
	t.Helper()
	s.send(t, http.MethodPost, "/-/stub/mode", m)
}

// nugetCounts reads the counters of the NuGet test upstream.
func (s *stubControl) nugetCounts(t testing.TB) nugetstub.Counts {
	t.Helper()
	var c nugetstub.Counts
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/counts", nil), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// nugetUpstreamRepos are a remote repository over the test upstream and a virtual one over nuget-e2e (the local member,
// the deployment target) and that remote, created for one test so that the cache a test starts with is empty.
type nugetUpstreamRepos struct{ remote, all string }

func (fx *nugetFixture) upstreamRepos(t testing.TB, purpose string, settings map[string]any) nugetUpstreamRepos {
	t.Helper()
	apiHost, contentHost := nugetStubHosts(t)
	u := nugetUpstreamRepos{remote: fmt.Sprintf("nuget-%s-%s-remote", purpose, fx.run), all: fmt.Sprintf("nuget-%s-%s-all", purpose, fx.run)}
	// the service index names a second host for the flat container and the search (R-NUGET-014)
	remote := map[string]any{"url": "https://" + apiHost + "/v3/index.json", "extraHosts": []map[string]any{{"pattern": contentHost, "sendCredentials": false}}}
	maps.Copy(remote, settings)
	fx.adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": u.remote, "class": "remote", "format": "nuget", "requireAuthentication": true, "remote": remote,
	}).want(t, "create "+u.remote, 201)
	fx.adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": u.all, "class": "virtual", "format": "nuget", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{nugetAuthKey, u.remote}, "deployTarget": nugetAuthKey},
	}).want(t, "create "+u.all, 201)
	ensurePolicy(t, fx.adm, "e2e-nuget-"+purpose+"-"+fx.run, []string{u.remote, u.all}, [2]any{"sa:" + nugetPubSA, []string{"read"}})
	nugetReadableOnEveryReplica(t, fx.token, u.remote, u.all)
	// A remote that stays behind is asked by the shadowing check of every first push into nuget-e2e (R-REPO-048), and one
	// whose upstream was stopped keeps its circuit open: the repositories go with the test.
	t.Cleanup(func() {
		for _, key := range []string{u.all, u.remote} {
			deleteRepository(t, fx.adm, key)
		}
	})
	return u
}

// nugetReadableOnEveryReplica waits until each packages replica lets the token read the new repositories (the bound of
// R-AUTHZ-028 plus one second for the poll and the request, as awaitReadableOnEveryReplica does for npm). A request that
// failed on the transport of the pod's forward (a forward to a pod that has just become ready may drop its first
// connection) goes to a new forward (getOverForward); the clock of the bound keeps running.
func nugetReadableOnEveryReplica(t testing.TB, token string, keys ...string) {
	t.Helper()
	addrs, stop := packagesForwards(t)
	defer stop()
	const within = 3 * time.Second
	for i, addr := range addrs {
		a := apiAtPod(t, addr, token)
		for _, key := range keys {
			start := time.Now()
			for {
				r := a.getOverForward(nugetBase + key + "/v3/index.json")
				if r.Status == 200 {
					break
				}
				if r.Status != 404 && r.Status != 429 || time.Since(start) > within {
					t.Fatalf("replica %d: repository %s answered %s %s after its creation, want 200 within %s (R-AUTHZ-028)", i, key, time.Since(start).Round(time.Millisecond), r, within)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// nugetPin makes the forwarder of NuGet clients pod i relay to one packages pod's listener (pod IP, port 8080) instead of the
// edge, through the target file of the forwarder (test/e2e/clients/fwd); the base URL and the certificate the client
// verifies stay those of the edge. nugetUnpin returns it to the edge.
func nugetPin(t testing.TB, i int, webPodIP string) {
	t.Helper()
	nugetForwarderTarget(t, i, webPodIP+":8080")
}
func nugetUnpin(t testing.TB, i int) { t.Helper(); nugetForwarderTarget(t, i, "") }

func nugetForwarderTarget(t testing.TB, i int, target string) {
	t.Helper()
	nugetClientExec(t, i, "sh", "-c", fmt.Sprintf("printf '%%s' '%s' > /tmp/target", target))
}
