//go:build e2e

package e2e

// npm across replicas (R-NPM-046, R-ARCH-012): what a client sees when consecutive requests land on different web
// replicas, and when Valkey is flushed or fails over underneath it. The clients are pinned to replicas through the
// forwarder of their pods (pinClient) where the criterion names a replica, and go through the scatter edge where it
// asks for scattered requests.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// webReplicas returns the ready web pods in name order: name and pod IP.
func webReplicas(t testing.TB, want int) []pod {
	t.Helper()
	var out []pod
	for _, p := range livePods(t, selWeb) {
		if p.Ready && p.IP != "" {
			out = append(out, p)
		}
	}
	if len(out) < want {
		t.Fatalf("%d ready web pods, the test needs %d", len(out), want)
	}
	return out
}

// pinnedClient is the client of row on client pod i, relayed to the web pod p until the returned function runs.
func pinnedClient(t *testing.T, fx *npmFixture, row string, i int, p pod) (*npmClient, func()) {
	t.Helper()
	pinClient(t, i, p.IP)
	if got := strings.TrimSpace(kn(t, "exec", npmClientPod(t, i), "--", "cat", "/tmp/target")); got != p.IP+":8080" {
		t.Fatalf("client pod %d is pinned to %q, want %s:8080", i, got, p.IP)
	}
	c := newNPMClient(t, fx, row, i)
	c.tag += fmt.Sprintf("-pin%d", i)
	return c, func() { unpinClient(t, i) }
}

// apiAtPod returns an API client that talks to the listener of one web pod through a port-forward.
func apiAtPod(t testing.TB, addr, token string) *api {
	a := newAPI(t, withRoundRobin(addr))
	return a.withToken(token)
}

func TestNPMReplicas(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)

	// E2.3: publish through replica A, view the version at once through replica B. R-ARCH-012: a committed write is
	// in force on every replica when its answer arrives; the next request of the client may land anywhere.
	t.Run("R-ARCH-012 E2.3 publish through replica A, view through replica B", func(t *testing.T) {
		webs := webReplicas(t, 2)
		a, undoA := pinnedClient(t, fx, "npm11", 0, webs[0])
		defer undoA()
		b, undoB := pinnedClient(t, fx, "npm11", 1, webs[1])
		defer undoB()
		name := fx.pkgName("npm11-"+fx.run, "e23", false)
		viewDir := b.project("e23-view", nil)
		b.configure(viewDir)
		const rounds = 20
		for i := 0; i < rounds; i++ {
			version := fmt.Sprintf("1.%d.0", i)
			a.publish(name, version)
			r := b.must(viewDir, "view", name+"@"+version, "version")
			if got := strings.TrimSpace(r.Out); got != version {
				t.Fatalf("round %d: npm view through replica B printed %q right after the publish through replica A, want %s", i, got, version)
			}
		}
		versions, tags, _ := fx.versionsOf(t, fx.registry(), name)
		if len(versions) != rounds || tags["latest"] != fmt.Sprintf("1.%d.0", rounds-1) {
			t.Fatalf("%d versions, dist-tags %v after %d publishes", len(versions), tags, rounds)
		}
	})

	// E2.10, R-NPM-046: a conditional request is answered 304 by every replica, and a publish through one replica
	// makes the validator of all the others stale at once. The validator is computed from PostgreSQL and the
	// configuration, so no replica holds it.
	t.Run("R-NPM-046 E2.10 a validator is answered 304 by every replica and changes with a publish", func(t *testing.T) {
		webs := webReplicas(t, 3)
		addrs, stop := podForwards(t)
		defer stop()
		if len(addrs) != len(webs) {
			t.Fatalf("%d forwards for %d web pods", len(addrs), len(webs))
		}
		name := fx.pkgName("npm11-"+fx.run, "etag", true)
		c, undo := pinnedClient(t, fx, "npm11", 2, webs[2])
		defer undo()
		c.publish(name, "1.0.0")
		path := "/api/npm/" + fx.registry() + "/" + url.PathEscape(name)
		at := func(i int) *api { return apiAtPod(t, addrs[i], fx.token) }
		first := at(0).get(path).want(t, "document through replica A", 200)
		etag := first.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("the document has no ETag: %v", first.Header)
		}
		for i := 1; i < len(addrs); i++ {
			r := at(i).get(path, "If-None-Match", etag)
			if r.Status != 304 {
				t.Fatalf("replica %d answered the conditional request with %s, want 304", i, r)
			}
		}
		// the date is not a validator (R-NPM-013): an If-Modified-Since in the future must not produce a 304 on its own
		if r := at(1).get(path, "If-Modified-Since", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat)); r.Status != 200 {
			t.Fatalf("a request with only If-Modified-Since answered %s, want 200: the date is ignored", r)
		}
		c.publish(name, "1.1.0")
		r := at(0).get(path, "If-None-Match", etag)
		if r.Status != 200 || r.Header.Get("ETag") == etag || r.Header.Get("ETag") == "" {
			t.Fatalf("after a publish through replica C, replica A answered the old validator with %s (ETag %q), want 200 and a new ETag", r, r.Header.Get("ETag"))
		}
		// the same for the local repository itself
		local := "/api/npm/" + npmLocalKey + "/" + url.PathEscape(name)
		e := at(0).get(local).want(t, "document of npm-local", 200).Header.Get("ETag")
		for i := 1; i < len(addrs); i++ {
			if r := at(i).get(local, "If-None-Match", e); r.Status != 304 {
				t.Fatalf("npm-local: replica %d answered %s, want 304", i, r)
			}
		}
		// and for a remote repository after an upstream change: the validator is the same on every replica, and a change
		// that the repository has taken over (an invalidation makes it revalidate) gives every replica a new one
		repos := fx.upstreamRepos(t, "e210etag", nil)
		s := stub(t)
		s.resetAll(t)
		defer s.resetAll(t)
		const remotePkg = "@cobalt/nimble-ledger"
		remotePath := "/api/npm/" + repos.remote + "/" + url.PathEscape(remotePkg)
		re := at(0).get(remotePath).want(t, "document of the remote", 200).Header.Get("ETag")
		for i := 1; i < len(addrs); i++ {
			if r := at(i).get(remotePath, "If-None-Match", re); r.Status != 304 {
				t.Fatalf("the remote: replica %d answered %s, want 304", i, r)
			}
		}
		s.publish(t, remotePkg, "99.0.0", nil, "latest")
		fx.adm.do(http.MethodPost, "/api/v1/repositories/"+repos.remote+"/cache/invalidate", map[string]any{"path": remotePkg, "scope": "metadata"}).want(t, "invalidate", 200)
		for i := range addrs {
			waitFor(t, fmt.Sprintf("replica %d to answer the old validator of the remote with 200", i), 10*time.Second, 200*time.Millisecond, func() bool {
				r := at(i).get(remotePath, "If-None-Match", re)
				return r.Status == 200 && r.Header.Get("ETag") != re && r.Header.Get("ETag") != ""
			})
		}
	})

	// E2.10, R-NPM-046: npm unpublish spans several requests (read for write, revisioned write, read, tarball
	// delete); in the scatter profile they reach different replicas and the command still succeeds.
	t.Run("R-NPM-046 E2.10 npm unpublish succeeds when its requests reach different replicas", func(t *testing.T) {
		c := newNPMClient(t, fx, "npm11", 0)
		c.tag += "-unpub"
		name := fx.pkgName("npm11-"+fx.run, "e210unpub", true)
		c.publish(name, "1.0.0")
		c.publish(name, "1.1.0")
		before := npmRequestsPerReplica(t)
		dir := c.project("unpublish", nil)
		c.configure(dir)
		c.must(dir, "unpublish", name+"@1.0.0")
		versions, _, _ := fx.versionsOf(t, fx.registry(), name)
		if len(versions) != 1 || versions[0] != "1.1.0" {
			t.Fatalf("after the unpublish of 1.0.0: versions %v", versions)
		}
		// R-NPM-046: the access of the requests is counted per replica (trove_npm_requests_total, R-NPM-044); the
		// requests of the unpublish must have reached more than one replica
		after, served := npmRequestsPerReplica(t), 0
		for p, n := range after {
			if n > before[p] {
				served++
			}
		}
		if served < 2 {
			t.Fatalf("the requests of the unpublish reached %d replica(s) (counters before %v, after %v); the scatter profile must spread them", served, before, after)
		}
	})

	// E2.10, R-NPM-046: a flush of Valkey and a Sentinel failover during the scenario list fail no client operation.
	t.Run("R-NPM-046 E2.10 valkey FLUSHALL during the scenario list", func(t *testing.T) {
		duringChaos(t, fx, "npm11", "flush", func(stop <-chan struct{}) {
			for {
				valkeyPrimaryCLI(t, "FLUSHALL")
				select {
				case <-stop:
					return
				case <-time.After(1500 * time.Millisecond):
				}
			}
		})
	})
	t.Run("R-NPM-046 E2.10 a Sentinel failover during the scenario list", func(t *testing.T) {
		duringChaos(t, fx, "npm11", "failover", func(stop <-chan struct{}) {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Second):
			}
			devScript(t, "valkey-failover.sh")
		})
	})

	// E2.10, R-NPM-046: the edge drops the answer of a committed revisioned write of an unpublish; npm's retry reaches
	// another replica, which acknowledges it, and the command exits 0.
	t.Run("R-NPM-046 E2.10 an unpublish whose answer the edge drops succeeds on its retry", func(t *testing.T) {
		c := newNPMClient(t, fx, "npm11", 0)
		c.tag += "-dropped"
		c.extraEnv = []string{"npm_config_fetch_timeout=20000", "npm_config_fetch_retry_mintimeout=1000", "npm_config_fetch_retry_maxtimeout=3000"}
		name := fx.pkgName("npm11-"+fx.run, "dropped", false)
		c.publish(name, "1.0.0")
		c.publish(name, "1.1.0")
		dir := c.project("unpublish", nil)
		c.configure(dir)
		defer dropOneAnswer(t)()
		r := c.run(dir, "unpublish", name+"@1.0.0", "--loglevel=http")
		if r.Code != 0 {
			t.Fatalf("npm unpublish exited %d after the edge dropped the answer of its write:\n%s", r.Code, tailLines(r.Out, 25))
		}
		// the write of the unpublish is the PUT below /-rev/; it appears twice in npm's request log when the first answer
		// was lost (the failed attempt is logged with its error, the retry with its status)
		writes := 0
		for _, l := range strings.Split(r.Out, "\n") {
			if strings.Contains(l, " PUT ") && strings.Contains(l, "/-rev/") {
				writes++
			}
		}
		if writes < 2 {
			t.Fatalf("npm logged %d write(s) of the unpublish, so the edge did not drop an answer and the test proves nothing:\n%s", writes, tailLines(r.Out, 25))
		}
		versions, _, _ := fx.versionsOf(t, fx.registry(), name)
		if len(versions) != 1 || versions[0] != "1.1.0" {
			t.Fatalf("after the unpublish of 1.0.0: versions %v", versions)
		}
	})

	// E2.10, R-OPS-021, R-REPO-045: a cache invalidation sent through one replica is in force on every replica within
	// cluster.epoch_poll_interval plus one second. It needs a remote repository.
	t.Run("R-OPS-021 E2.10 a cache invalidation takes effect on every replica", func(t *testing.T) {
		cacheInvalidationOnEveryReplica(t, fx)
	})
}

// duringChaos runs the scenario list of one gate row while chaos runs beside it, twice with fresh package names, and
// requires that no scenario fails: a failed client operation is a failed scenario.
func duringChaos(t *testing.T, fx *npmFixture, row, name string, chaos func(stop <-chan struct{})) {
	t.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); chaos(stop) }()
	defer func() { close(stop); wg.Wait() }()
	var ran atomic.Int32
	for round := 0; round < 2; round++ {
		c := newNPMClient(t, fx, row, round%npmClientCount(t))
		c.tag += fmt.Sprintf("-%s%d", name, round)
		for _, sc := range npmScenarios {
			if sc.na != nil && sc.na(c) != "" {
				continue
			}
			if strings.HasPrefix(sc.id, "install-remote") || strings.Contains(sc.id, "workspace") || strings.Contains(sc.id, "switch") {
				continue // needs the remote class; it has its own tests
			}
			t.Run(fmt.Sprintf("%s round %d %s", name, round, sc.id), func(t *testing.T) {
				c.t = t
				sc.run(t, c)
				ran.Add(1)
			})
		}
	}
	if ran.Load() == 0 {
		t.Fatal("no scenario ran during the chaos")
	}
}

// npmRequestsPerReplica is the sum of trove_npm_requests_total of each ready web pod.
func npmRequestsPerReplica(t testing.TB) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, p := range readyWebPods(t) {
		for series, v := range metricsOf(t, p) {
			if strings.HasPrefix(series, "trove_npm_requests_total{") {
				out[p] += v
			}
		}
	}
	return out
}

// cacheInvalidationOnEveryReplica changes a package at the test upstream, invalidates the cached metadata of the
// remote repository through the API (one replica takes the call) and requires every replica to serve the new version
// within cluster.epoch_poll_interval (1 s in the dev profile) plus one second (R-OPS-021, R-REPO-045). The measured
// time starts when the call answers and may exceed the bound by one probe's round trip, which the allowance covers.
func cacheInvalidationOnEveryReplica(t *testing.T, fx *npmFixture) {
	t.Helper()
	const (
		pkg       = "@cobalt/dusky-gauge"
		bound     = 2 * time.Second
		allowance = 250 * time.Millisecond
	)
	// a remote repository of its own: the change made at the upstream below must not leak into the shared npm-remote,
	// whose cached document the other tests read
	repos := fx.upstreamRepos(t, "e210inv", nil)
	s := stub(t)
	s.resetAll(t)
	defer s.resetAll(t)
	addrs, stop := podForwards(t)
	defer stop()
	path := "/api/npm/" + repos.remote + "/" + url.PathEscape(pkg)
	for i := range addrs {
		apiAtPod(t, addrs[i], fx.token).get(path).want(t, fmt.Sprintf("document through replica %d", i), 200)
	}
	version := fmt.Sprintf("99.%d.0", time.Now().Unix()%100000)
	s.publish(t, pkg, version, nil, "latest")
	fx.adm.do(http.MethodPost, "/api/v1/repositories/"+repos.remote+"/cache/invalidate", map[string]any{"path": pkg, "scope": "metadata"}).want(t, "invalidate", 200)
	start := time.Now()
	for i := range addrs {
		a := apiAtPod(t, addrs[i], fx.token)
		for {
			r := a.get(path)
			if r.Status == 200 && strings.Contains(string(r.Body), `"`+version+`"`) {
				break
			}
			if time.Since(start) > bound+allowance {
				t.Fatalf("replica %d still serves the old document %s after the invalidation (%s, bound %s)", i, time.Since(start).Round(time.Millisecond), r, bound)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// dropOneAnswer makes the edge drop the connection (silent-drop of HAProxy 2.8) instead of answering the first revisioned PUT it carries (the write
// of an unpublish), after the replica has committed it: HAProxy Ingress takes the rules through the config-backend
// annotation of trove's Ingress, a stick-table counter lets exactly one answer through the rule. The returned function
// restores the annotation the profile set.
func dropOneAnswer(t *testing.T) func() {
	t.Helper()
	const key = "haproxy-ingress.github.io/config-backend"
	was := kn(t, "get", "ingress", "trove", "-o", `jsonpath={.metadata.annotations.haproxy-ingress\.github\.io/config-backend}`)
	rules := was + `
stick-table type string size 1 expire 1m store gpc0
acl e2e_put method PUT
acl e2e_rev path_sub /-rev/
http-request track-sc0 str(e2e-drop) if e2e_put e2e_rev
http-request set-var(txn.e2e_rev) int(1) if e2e_put e2e_rev
http-response silent-drop if { var(txn.e2e_rev) -m found } { sc0_get_gpc0 eq 0 } { sc0_inc_gpc0 gt 0 }`
	kn(t, "annotate", "ingress", "trove", "--overwrite", key+"="+rules)
	time.Sleep(8 * time.Second) // the controller renders the backend and reloads HAProxy
	return func() {
		kn(t, "annotate", "ingress", "trove", "--overwrite", key+"="+was)
		time.Sleep(5 * time.Second)
	}
}
