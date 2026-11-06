//go:build e2e

package e2e

// The long scenarios of R-BAS-049 (set bastion-long: nightly, on dispatch and locally; a red long leg is a finding, not a
// blocker, owner 2026-11-01): the drain of a node under a ten-minute client loop and the flood of anonymous requests for
// random names.

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// basLoopDuration is the length of the client loop of the drain: ten minutes (R-BAS-043), TROVE_E2E_BASTION_LOOP shortens it
// while the test is developed, never in CI.
func basLoopDuration(t testing.TB) time.Duration {
	if v := os.Getenv("TROVE_E2E_BASTION_LOOP"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("TROVE_E2E_BASTION_LOOP %q: %v", v, err)
		}
		return d
	}
	return 10 * time.Minute
}

// TestBastionLong_R_BAS_049_Drain (R-BAS-043): `kubectl drain` of the node of one Bastion replica during a ten-minute loop of
// `npm ci` and `dotnet restore` through the Bastion: zero failed runs.
func TestBastionLong_R_BAS_049_Drain(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	setN := env.npmSets["npm"]
	cn := env.npmClient(t, "npm", 0, "drain", env.full.Token)
	dir := env.basNPMProject(t, cn, setN, "drain", site)
	nc := env.nugetClient(t, 0, "drain", env.full.Token)
	env.configureNuGet(t, nc, site.reg)
	project := env.nuget.project(nc, "consumer")
	// one run of each first: the cache and the answers are warm, and the clients work
	cn.basInstall(t, dir, setN)
	env.nuget.restore(t, nc, project)

	node := basDrainNode(t, site)
	t.Logf("draining %s, which hosts a replica of %s", node, site.release)
	loop := basLoopDuration(t)
	deadline := time.Now().Add(loop)
	var runs, failed atomic.Int64
	var mu sync.Mutex
	var failures []string
	var wg sync.WaitGroup
	one := func(what string, f func() (string, bool)) {
		defer wg.Done()
		for time.Now().Before(deadline) {
			out, ok := f()
			runs.Add(1)
			if !ok {
				failed.Add(1)
				mu.Lock()
				failures = append(failures, what+": "+tailLines(out, 8))
				mu.Unlock()
			}
		}
	}
	wg.Add(2)
	go one("npm ci", func() (string, bool) {
		cn.wipeClientState(dir)
		r := cn.run(dir, cn.frozenInstall()...)
		return r.Out, r.Code == 0
	})
	go one("dotnet restore", func() (string, bool) {
		if out, err := nc.try("sh", "-c", "rm -rf packages httpcache obj plugins"); err != nil {
			return out, false
		}
		out, err := nc.try("dotnet", "restore", project, "--nologo")
		return out, err == nil
	})

	time.Sleep(2 * time.Minute)
	t.Cleanup(func() {
		if out, err := kubectl("uncordon", node); err != nil {
			t.Errorf("uncordon %s: %v %s", node, err, out)
		}
	})
	start := time.Now()
	out, err := kubectl("drain", node, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=5m")
	t.Logf("kubectl drain %s took %s (err %v)", node, time.Since(start).Round(time.Second), err)
	if err != nil {
		t.Errorf("kubectl drain: %v\n%s", err, tail(out, 1500))
	}
	waitBastionReady(t, site, 2)
	for _, p := range basReadyPods(t, site) {
		if p.Node == node {
			t.Errorf("replica %s is still on the drained node", p.Name)
		}
	}
	wg.Wait()
	t.Logf("%d client runs in %s through the drain, %d failed", runs.Load(), loop, failed.Load())
	if runs.Load() < 10 {
		t.Errorf("only %d runs in the loop: the loop did not run", runs.Load())
	}
	for _, f := range failures {
		t.Errorf("failed run: %s", f)
	}
}

// basDrainNode picks the node of a replica of the release that is not the node of the PostgreSQL primary, whose
// PodDisruptionBudget refuses an eviction (deploy/dev/drain.sh picks the same way).
func basDrainNode(t testing.TB, s *basSite) string {
	t.Helper()
	primary := strings.TrimSpace(kn(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}"))
	primaryNode := strings.TrimSpace(kn(t, "get", "pod", primary, "-o", "jsonpath={.spec.nodeName}"))
	for _, p := range basReadyPods(t, s) {
		if p.Node != primaryNode {
			return p.Node
		}
	}
	t.Fatalf("every replica of %s is on the node of the PostgreSQL primary %s", s.release, primaryNode)
	return ""
}

// TestBastionLong_R_BAS_049_AnonymousMissFlood (R-BAS-035, R-BAS-025): anonymous requests for 10,000 random names within a
// minute send at most the anonymous miss budget to the main installation, and no more than the anonymous share of
// max_concurrent at a time.
func TestBastionLong_R_BAS_049_AnonymousMissFlood(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	const openRepo = "bas-npm-open"
	env.adm.do(http.MethodPost, reposRoute, map[string]any{"key": openRepo, "class": "local", "format": "npm"}).want(t, "create "+openRepo, 201, 409)
	ensurePolicy(t, env.adm, "e2e-bas-anonymous", []string{openRepo}, [2]any{"anonymous", []string{"read"}})
	setAnonymous := func(on bool) {
		g := env.adm.get("/api/v1/settings/auth").want(t, "read the authentication settings", 200)
		env.adm.do(http.MethodPatch, "/api/v1/settings/auth", map[string]any{"anonymousAccess": on}, "If-Match", g.Header.Get("ETag"),
			"Content-Type", "application/merge-patch+json").want(t, "set anonymousAccess", 200)
	}
	setAnonymous(true)
	t.Cleanup(func() { setAnonymous(false) })
	known := "bas" + env.run + "-anon-known"
	ensurePolicy(t, env.adm, basPubSA+"-open", []string{openRepo}, [2]any{"sa:" + basPubSA, []string{"read", "deploy"}})
	time.Sleep(6 * time.Second) // the grant reaches every replica of the main installation within the revocation bound
	env.publishNPM(t, openRepo, known, "1.0.0", nil)

	anon := site.api(t, "")
	waitFor(t, "the Bastion to admit an anonymous request", 90*time.Second, 2*time.Second, func() bool {
		return anon.get(npmBase+openRepo+"/"+known).Status == 200
	})
	window := openMainWindow(t)
	inflightMax := int64(0)
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var n float64
			for _, p := range basReadyPods(t, site) {
				n += msum(basMetrics(t, p.Name), "trove_bastion_main_in_flight", `class="anonymous"`)
			}
			if int64(n) > atomic.LoadInt64(&inflightMax) {
				atomic.StoreInt64(&inflightMax, int64(n))
			}
			time.Sleep(300 * time.Millisecond)
		}
	}()
	res := site.flood(t, 10000, 50, 90, func(i int) string { return npmBase + openRepo + "/nonexistent-" + env.run + "-" + itoa(i) }, func(int) string { return "" })
	close(stop)
	sampler.Wait()
	reqs := window.done()
	var toMain []mainReq
	for _, r := range reqs {
		if strings.Contains(r.Path, "/"+openRepo+"/") {
			toMain = append(toMain, r)
		}
	}
	t.Logf("%d anonymous requests in %s: statuses %v (Bastion's 429 %v, ingress %d); %d reached the main installation; at most %d in flight",
		res.sent, res.took.Round(time.Second), res.codes, res.problemCode, res.edge429, len(toMain), atomic.LoadInt64(&inflightMax))
	budget := 30 // limits.misses.anonymous_per_minute, cluster-wide with Valkey
	minutes := int(res.took/time.Minute) + 1
	if len(toMain) > budget*minutes+2 {
		t.Errorf("%d anonymous misses reached the main installation in %s, want at most %d (the budget of %d a minute)", len(toMain), res.took.Round(time.Second), budget*minutes, budget)
	}
	if share := int64(64 * 0.25); atomic.LoadInt64(&inflightMax) > share {
		t.Errorf("%d anonymous requests were in flight to the main installation at once, want at most the anonymous share %d of max_concurrent", atomic.LoadInt64(&inflightMax), share)
	}
	if res.codes[200] != 0 {
		t.Errorf("%d requests for random names answered 200", res.codes[200])
	}
}
