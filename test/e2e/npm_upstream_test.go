//go:build e2e

package e2e

// The criteria that are read off what the counting test upstream (test/npmstub) recorded: cold-cache collapse across
// replicas (E2.2), a down upstream and the negative cache (E2.4), namespace claims and the shadowing check (E2.5), and
// the request collapsing of R-ARCH-033 with its failure cases (E2.9). Each test makes the repositories it needs (upstreamRepos)
// and removes them again.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// ── repositories of one test ─────────────────────────────────────────────────

// upstreamRepos are a remote repository over the test upstream and a virtual one over npm-local and that remote,
// created for one test so that the cache a test starts with is empty. settings are merged into the remote's `remote`
// section.
type upstreamRepos struct {
	remote, all string
}

func (fx *npmFixture) upstreamRepos(t testing.TB, purpose string, settings map[string]any) upstreamRepos {
	t.Helper()
	u := upstreamRepos{remote: fmt.Sprintf("npm-%s-%s-remote", purpose, fx.run), all: fmt.Sprintf("npm-%s-%s-all", purpose, fx.run)}
	remote := map[string]any{"url": "https://" + fx.stubHost + "/"}
	for k, v := range settings {
		remote[k] = v
	}
	fx.adm.do(http.MethodPost, "/api/v1/repositories", map[string]any{"key": u.remote, "class": "remote", "format": "npm", "remote": remote}).want(t, "create "+u.remote, 201)
	fx.adm.do(http.MethodPost, "/api/v1/repositories", map[string]any{
		"key": u.all, "class": "virtual", "format": "npm", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{npmLocalKey, u.remote}, "deployTarget": npmLocalKey},
	}).want(t, "create "+u.all, 201)
	ensurePolicy(t, fx.adm, "e2e-npm-"+purpose+"-"+fx.run, []string{u.remote, u.all}, [2]any{"sa:" + npmPublisher, []string{"read"}})
	awaitReadableOnEveryReplica(t, fx.token, u.remote, u.all)
	// A remote that stays behind keeps being asked by the shadowing check of every first publish into npm-local (R-REPO-048
	// asks each remote of each virtual repository that contains the target); one whose upstream was stopped keeps its
	// circuit open for circuitOpenSeconds and makes every such publish a 503. The repositories go with the test.
	t.Cleanup(func() {
		for _, key := range []string{u.all, u.remote} {
			deleteRepository(t, fx.adm, key)
		}
	})
	return u
}

// awaitReadableOnEveryReplica waits until each web replica lets the token read the new repositories: a repository and
// the policy that grants it reach the other replicas within the bound of R-AUTHZ-028 (1 s with Valkey,
// epoch_poll_interval of 1 s plus 1 s without), and a replica that has not seen them yet answers 404. The tests allow one
// more second for the poll and the request, as E1.14 does; a replica that takes longer fails the test.
func awaitReadableOnEveryReplica(t testing.TB, token string, keys ...string) {
	t.Helper()
	addrs, stop := podForwards(t)
	defer stop()
	const within = 3 * time.Second
	for i, addr := range addrs {
		a := apiAtPod(t, addr, token)
		for _, key := range keys {
			start := time.Now()
			for {
				r := a.get("/api/npm/" + key + "/-/ping") // action read: the repository and the grant on that replica
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

// deleteRepository deletes a repository, waiting out the admin API's rate limit (R-API-016).
func deleteRepository(t testing.TB, adm *api, key string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		r, err := adm.send(http.MethodDelete, "/api/v1/repositories/"+key, nil)
		if err != nil {
			t.Errorf("delete %s: %v", key, err)
			return
		}
		switch r.Status {
		case 429:
			time.Sleep(2 * time.Second)
		case 200, 202, 204, 404:
			return
		default:
			t.Errorf("delete %s: %s", key, r)
			return
		}
	}
	t.Errorf("delete %s: still rate limited after 40 s", key)
}

// clientOn returns a client of the row on pod i that is configured for the repository key.
func (fx *npmFixture) clientOn(t *testing.T, row string, i int, key, suffix string) *npmClient {
	c := newNPMClient(t, fx, row, i)
	c.registryKey = key
	c.tag += "-" + suffix
	return c
}

// referenceLockfile returns the committed reference project (perf/npm/reference) with the registry key rewritten to
// key, and the number of packages the lockfile resolves.
func referenceLockfile(t testing.TB, key string) (pkgJSON, lock string, packages int) {
	t.Helper()
	dir := filepath.Join(cfg.repo, "perf", "npm", "reference")
	pj, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	lk, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Packages map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(lk, &parsed); err != nil {
		t.Fatal(err)
	}
	// one tarball per distinct address: a package that the tree holds at two places is one download
	seen := map[string]bool{}
	for _, p := range parsed.Packages {
		if p.Resolved != "" && !seen[p.Resolved] {
			seen[p.Resolved] = true
			packages++
		}
	}
	// the lockfile is resolved for https://localhost:8080/api/npm/npm-all/ (perf/README.md); the run's edge port and the
	// repository of the test are the only things that change in it
	return string(pj), strings.ReplaceAll(string(lk), "https://localhost:8080/api/npm/npm-all/", registryURL(t, "localhost", key)), packages
}

// ── E2.2 ─────────────────────────────────────────────────────────────────────

// coldInstall is E2.2: three clients run `npm ci` at once with empty client caches, each against another web replica,
// from the reference lockfile, resolving through a virtual repository over npm-local and a remote that points at the
// counting test upstream. valkeyStopped says that Valkey is down, which relaxes "one request" to "one per replica".
func coldInstall(t *testing.T, fx *npmFixture, valkeyStopped bool) {
	t.Helper()
	repos := fx.upstreamRepos(t, "e22", nil)
	s := stub(t)
	s.resetAll(t)
	pkgJSON, lock, packages := referenceLockfile(t, repos.all)
	if packages < 500 {
		t.Fatalf("the reference lockfile resolves %d packages, E2.2 needs at least 500", packages)
	}
	webs := webReplicas(t, 3)
	clients := make([]*npmClient, 3)
	dirs := make([]string, 3)
	for i := range clients {
		c, undo := pinnedClient(t, fx, "npm11", i, webs[i])
		defer undo()
		c.registryKey = repos.all
		c.tag += "-e22"
		clients[i] = c
		dirs[i] = c.project("cold-ci", nil)
		c.configure(dirs[i])
		c.write(dirs[i]+"/package.json", pkgJSON)
		c.write(dirs[i]+"/package-lock.json", lock)
		c.sh(dirs[i], `rm -rf "`+c.home()+`/npm-cache" node_modules`)
	}
	var wg sync.WaitGroup
	results := make([]npmRun, 3)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = clients[i].exec(dirs[i], nil, "npm11", "ci")
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.Code != 0 {
			t.Errorf("client %d (replica %s): npm ci exited %d after %s:\n%s", i, webs[i].Name, r.Code, r.Dur.Round(time.Millisecond), tailLines(r.Out, 25))
		}
	}
	counts := s.counts(t)
	maxEach := 1
	if valkeyStopped {
		maxEach = len(webs)
	}
	for _, v := range counts.Violations(maxEach, maxEach) {
		t.Errorf("the test upstream recorded %s", v)
	}
	if !valkeyStopped {
		tarballs := 0
		for _, e := range counts.Entries {
			if e.Kind == npmstub.KindTarball {
				tarballs += e.Count
			}
		}
		if tarballs != packages {
			t.Errorf("the test upstream served %d tarball requests for a lockfile of %d packages, want exactly one each", tarballs, packages)
		}
	}
	t.Logf("test upstream: %s", counts.Summary())
}

// ── the tests ────────────────────────────────────────────────────────────────

func TestNPMCollapse(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)

	t.Run("R-NPM-045 R-REPO-026 E2.2 three clients run npm ci at once against three replicas", func(t *testing.T) {
		coldInstall(t, fx, false)
	})

	// E2.9, R-ARCH-033: 100 concurrent misses over two replicas cause one upstream GET, hold no transaction open and
	// take no advisory lock.
	t.Run("R-ARCH-033 E2.9 100 concurrent misses over two replicas cause one upstream request", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e29", nil)
		s := stub(t)
		s.resetAll(t)
		s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 1500}})
		defer s.resetAll(t)
		webs := webReplicas(t, 2)
		const name, misses = "@cobalt/eager-lantern", 100
		path := "/api/npm/" + repos.remote + "/" + url.PathEscape(name)
		// the system is quiet when the flight starts: a replica that came up a moment ago (the profile switch) may still
		// be settling its sessions, which is not what the criterion measures
		waitFor(t, "no session idle in transaction", 60*time.Second, time.Second, func() bool {
			return strings.TrimSpace(sql(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%' AND now() - state_change > interval '700 ms'`)) == "0"
		})
		var wg sync.WaitGroup
		var statuses []int
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses = burstOver(t, webs, path, fx.token, misses/2)
		}()
		// The replicas' pollers run short transactions of a few milliseconds all the time (ListClusterEpochs and the like),
		// which a sample catches "idle in transaction" now and then. What the criterion forbids is a transaction held
		// across the upstream wait, which here is 1.5 s, so only a session idle in a transaction for longer than 700 ms
		// counts. The advisory locks the installation holds by itself (the lease sessions of the replicas) are the
		// baseline; the flight must add none.
		advisory := func() string {
			return strings.TrimSpace(sql(t, `SELECT count(*) FILTER (WHERE state LIKE 'idle in transaction%' AND now() - state_change > interval '700 ms') || ' ' || (SELECT count(*) FROM pg_locks WHERE locktype = 'advisory') FROM pg_stat_activity WHERE datname = current_database()`))
		}
		baseline := advisory()
		if !strings.HasPrefix(baseline, "0 ") {
			t.Fatalf("a session was idle in transaction before the test: %s", baseline)
		}
		var bad []string
		for deadline := time.Now().Add(1200 * time.Millisecond); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if out := advisory(); out != baseline {
				// name the session, so that a finding is about a connection and not about a count
				who := sql(t, `SELECT coalesce(string_agg(format('%s/%s since %s: %s', usename, application_name, now() - state_change, left(query, 120)), '; '), 'none') FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%' AND now() - state_change > interval '700 ms'`)
				bad = append(bad, out+" ("+who+")")
			}
		}
		wg.Wait()
		for i, st := range statuses {
			if st != 200 {
				t.Fatalf("request %d answered %d", i, st)
			}
		}
		if got := s.counts(t).Documents(name); got != 1 {
			t.Fatalf("the test upstream recorded %d document requests for %s, want 1 (100 misses over two replicas)", got, name)
		}
		if len(bad) > 0 {
			t.Fatalf("while the flight ran, pg_stat_activity/pg_locks showed %v where the baseline is %q (idle in transaction, advisory locks)", bad, baseline)
		}
	})

	// E2.9: with Valkey stopped the upstream sees at most one GET per replica.
	t.Run("R-ARCH-033 E2.9 with valkey stopped the upstream sees at most one request per replica", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e29v", nil)
		s := stub(t)
		s.resetAll(t)
		s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 1500}})
		defer s.resetAll(t)
		webs := webReplicas(t, 2)
		const name = "@cobalt/early-nozzle"
		path := "/api/npm/" + repos.remote + "/" + url.PathEscape(name)
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			for i, st := range burstOver(t, webs, path, fx.token, 30) {
				if st != 200 {
					t.Errorf("request %d answered %d", i, st)
				}
			}
		})
		if got := s.counts(t).Documents(name); got < 1 || got > 2 {
			t.Fatalf("the test upstream recorded %d document requests for %s, want between 1 and 2 (one per replica of the two that were asked)", got, name)
		}
	})

	// E2.9: a flight lock lost with Valkey's data (FLUSHALL stands for a Sentinel failover that drops the acknowledged
	// write, R-ARCH-030) costs at most one duplicate fetch and leaves one cache entry. The second replica takes the lock
	// that vanished, fetches the same document, and the duplicate fill is harmless (R-NPM-024 duplicate-fill rule).
	t.Run("R-ARCH-033 E2.9 a flight lock lost with valkey's data costs at most one duplicate fetch", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e29l", nil)
		s := stub(t)
		s.resetAll(t)
		s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 4000}})
		defer s.resetAll(t)
		webs := webReplicas(t, 2)
		const name = "@cobalt/lucid-dial"
		path := "/api/npm/" + repos.remote + "/" + url.PathEscape(name)
		var statuses []int
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses = burstOver(t, webs, path, fx.token, 10)
		}()
		time.Sleep(2 * time.Second) // one replica leads the flight and waits for the upstream, the others wait for it
		valkeyPrimaryCLI(t, "FLUSHALL")
		wg.Wait()
		for i, st := range statuses {
			if st != 200 {
				t.Fatalf("request %d answered %d", i, st)
			}
		}
		if got := s.counts(t).Documents(name); got < 1 || got > 2 {
			t.Fatalf("the test upstream recorded %d document requests for %s, want at most 2 (the leader's and one duplicate)", got, name)
		}
		r := fx.adm.get("/api/v1/repositories/"+repos.remote+"/cache/entries?pathPrefix="+url.QueryEscape(name)).want(t, "cache entries", 200)
		var entries struct {
			Items []json.RawMessage `json:"items"`
		}
		r.JSON(t, &entries)
		if len(entries.Items) != 1 {
			t.Fatalf("the cache holds %d entries for %s after the duplicate fetch, want 1: %s", len(entries.Items), name, r)
		}
	})

	// E2.9: a killed holder's flight is taken over within cluster.flight_lock_ttl plus the poll interval.
	t.Run("R-ARCH-033 E2.9 a killed holder's flight is taken over", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e29k", nil)
		s := stub(t)
		s.resetAll(t)
		s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 4000}})
		defer s.resetAll(t)
		webs := webReplicas(t, 2)
		addrs, stop := podForwards(t)
		defer stop()
		const name = "@cobalt/ivory-ember"
		path := "/api/npm/" + repos.remote + "/" + url.PathEscape(name)
		held := make(chan reply, 1)
		go func() { r, _ := apiAtPod(t, addrs[0], fx.token).send(http.MethodGet, path, nil); held <- r }()
		time.Sleep(1200 * time.Millisecond) // the first replica holds the flight lock and waits for the upstream
		kn(t, "delete", "pod", webs[0].Name, "--grace-period=0", "--force", "--wait=false")
		start := time.Now()
		r := apiAtPod(t, addrs[1], fx.token).get(path)
		if r.Status != 200 {
			t.Fatalf("the second replica answered %s after the holder was killed", r)
		}
		const bound = 40 * time.Second // flight_lock_ttl 30 s in the dev profile plus the poll interval, with the upstream delay
		if d := time.Since(start); d > bound {
			t.Fatalf("takeover took %s, want within %s", d.Round(time.Millisecond), bound)
		}
		if got := s.counts(t).Documents(name); got > 2 {
			t.Fatalf("the test upstream recorded %d document requests, want at most 2 (the killed holder's and the takeover's)", got)
		}
		<-held
		waitWebSettled(t)
	})
}

func TestNPMUpstream(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)

	// E2.4, R-REPO-029: with the upstream stopped, an install on a warm cache succeeds from stale metadata.
	t.Run("R-REPO-029 E2.4 an install on a warm cache succeeds from stale metadata while the upstream is down", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e24", map[string]any{"cache": map[string]any{"metadataTtlSeconds": 1}})
		s := stub(t)
		s.resetAll(t)
		defer s.resetAll(t)
		c := fx.clientOn(t, "npm11", 0, repos.all, "e24")
		dir := c.project("warm", nil)
		c.configure(dir)
		const pkg = "@cobalt/dusky-gauge"
		c.mustInstall(dir, pkg)
		c.wipeClientState(dir)
		time.Sleep(2500 * time.Millisecond) // past the metadata lifetime of 1 s
		s.setMode(t, npmstub.Mode{Down: npmstub.DownUnavailable})
		dir2 := c.project("stale", nil)
		c.configure(dir2)
		c.mustInstall(dir2, pkg)
		if got := s.counts(t).Documents(pkg); got < 1 {
			t.Fatalf("the test upstream saw no request for %s: the metadata was not stale, the test proves nothing", pkg)
		}
		if r := c.exec(dir2, nil, "node", "-e", fmt.Sprintf("require(%q)", pkg)); r.Code != 0 {
			t.Fatalf("the installed package does not load: %s", r.Out)
		}
	})

	// E2.4, R-REPO-028: a name the upstream answers 404 for is not asked again inside the negative cache period.
	t.Run("R-REPO-028 E2.4 repeated requests for a missing name cause one upstream request", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e24n", nil)
		s := stub(t)
		s.resetAll(t)
		defer s.resetAll(t)
		name := "e2e-no-such-" + fx.run
		path := "/api/npm/" + repos.remote + "/" + name
		for i := 0; i < 12; i++ { // scattered over the replicas by the edge
			fx.publisherGet(t, path).want(t, "document of a missing name", 404)
		}
		if got := s.counts(t).Documents(name); got != 1 {
			t.Fatalf("the test upstream recorded %d requests for the missing name, want 1 inside the negative cache period", got)
		}
	})

	// E2.5, R-REPO-023, R-NPM-033: a name under a namespace claimed by npm-local is never requested from the upstream,
	// even when the upstream offers a higher version.
	t.Run("R-REPO-023 R-NPM-033 E2.5 a claimed name is never requested from the upstream", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e25", nil)
		s := stub(t)
		s.resetAll(t)
		defer s.resetAll(t)
		scope := "claimed" + fx.run
		name := "@" + scope + "/sdk"
		setClaims(t, fx.adm, npmLocalKey, "@"+scope+"/*")
		s.publish(t, name, "99.0.0", nil, "latest")
		c := fx.clientOn(t, "npm11", 0, repos.all, "e25")
		c.publish(name, "1.0.0")
		dir := c.project("claimed", nil)
		c.configure(dir)
		if got := strings.TrimSpace(c.must(dir, "view", name, "version").Out); got != "1.0.0" {
			t.Fatalf("npm view %s printed %q, want the local 1.0.0 and not the upstream's 99.0.0", name, got)
		}
		c.mustInstall(dir, name)
		if n := s.counts(t).Documents(name) + s.counts(t).Tarballs(name); n != 0 {
			t.Fatalf("the test upstream recorded %d requests for the claimed name %s, want none", n, name)
		}
	})

	// E2.5, R-REPO-048: the shadowing check of the first publish of a name the upstream serves.
	t.Run("R-REPO-048 E2.5 the first publish of a name the upstream serves is refused to a deploy holder", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e25s", nil)
		s := stub(t)
		s.resetAll(t)
		defer s.resetAll(t)
		deployer := serviceToken(t, fx, "e2e-npm-deployer", repos.all, []string{"read", "deploy"}, []string{"read"})
		manager := serviceToken(t, fx, "e2e-npm-manager", repos.all, []string{"read", "deploy", "manage"}, []string{"read", "manage"})
		c := fx.clientOn(t, "npm11", 0, repos.all, "e25s")

		c.token = deployer
		// a name the upstream serves, new to this run: a rerun on the same cluster must not meet the version of the last
		name := "shadowed-" + fx.run
		s.publish(t, name, "1.0.0", nil, "latest")
		dir := c.publishDir(name, "0.0.1", "marker:shadow")
		r := c.run(dir, "publish")
		if r.Code == 0 || !strings.Contains(r.Out, "403") {
			t.Fatalf("a deploy holder published %s, which the upstream serves (exit %d):\n%s", name, r.Code, tailLines(r.Out, 15))
		}
		// npm prints the message of the refusal, not its code
		if !strings.Contains(r.Out, "would replace a package that a remote repository serves") {
			t.Fatalf("the refusal does not say why:\n%s", tailLines(r.Out, 15))
		}

		c.token = manager
		c.must(dir, "publish")
		if !auditMentions(t, fx.adm, name) {
			t.Fatalf("the manage holder's publish of %s left no audit event that names it", name)
		}

		// the upstream cannot be asked: a deploy holder gets 503 shadow_check_unavailable
		s.setMode(t, npmstub.Mode{Down: npmstub.DownUnavailable})
		c.token = deployer
		other := c.publishDir("shadow-unknown-"+fx.run, "0.0.1", "marker:unavailable")
		r = c.run(other, "publish")
		if r.Code == 0 || !strings.Contains(r.Out, "503") {
			t.Fatalf("with the upstream down a deploy holder's first publish of an upstream name exited %d, want a 503:\n%s", r.Code, tailLines(r.Out, 15))
		}
	})
}

// serviceToken makes a service account that holds localActions on npm-local and virtualActions on the virtual
// repository (which accepts read and manage only, R-AUTHZ-009) and returns a token.
func serviceToken(t *testing.T, fx *npmFixture, sa, virtualKey string, localActions, virtualActions []string) string {
	t.Helper()
	ensureSA(t, fx.adm, sa)
	ensurePolicy(t, fx.adm, sa+"-local-"+fx.run, []string{npmLocalKey}, [2]any{"sa:" + sa, localActions})
	ensurePolicy(t, fx.adm, sa+"-virtual-"+fx.run, []string{virtualKey}, [2]any{"sa:" + sa, virtualActions})
	return freshSAToken(t, fx.adm, sa, 3600, map[string]any{"inherit": true}).Token
}

// setClaims writes the manual claims of a local repository (R-REPO-023): the complete set, with the version of the
// claims document the read returned.
func setClaims(t *testing.T, adm *api, repo string, patterns ...string) {
	t.Helper()
	g := adm.get("/api/v1/repositories/"+repo+"/claims").want(t, "read claims", 200)
	var cur struct {
		Claims []struct {
			Pattern, Source, Scope string
		} `json:"claims"`
	}
	g.JSON(t, &cur)
	var all []map[string]string
	for _, c := range cur.Claims {
		if c.Source == "manual" {
			all = append(all, map[string]string{"pattern": c.Pattern, "scope": c.Scope})
		}
	}
	for _, p := range patterns {
		all = append(all, map[string]string{"pattern": p, "scope": "members"})
	}
	adm.do(http.MethodPut, "/api/v1/repositories/"+repo+"/claims", map[string]any{"claims": all}, "If-Match", g.Header.Get("ETag")).want(t, "write claims", 200)
}

// auditMentions says whether any recent audit event names the package.
func auditMentions(t *testing.T, adm *api, pkg string) bool {
	t.Helper()
	r := adm.get("/api/v1/audit/events?limit=200").want(t, "audit events", 200)
	return strings.Contains(string(r.Body), pkg)
}
