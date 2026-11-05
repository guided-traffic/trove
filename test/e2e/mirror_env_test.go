//go:build e2e

package e2e

// The harness of the mirror tests (R-REPO-059, docs/developer/e2e.md#the-mirror-tests): the cluster in the `mirror` profile (three
// api and three packages replicas, the main worker, and a pool of two processes that claim the queue `mirror` alone), the two
// counting test upstreams, the repositories in mode mirror, the status and run operations, the counters of the pool's processes
// and of the stubs, the name of the public registry made to resolve to the npm test upstream, and a sampler of the job table.
// The tests are in mirror_*_test.go.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"
)

const (
	// The remote section of every mirror under test: maxConcurrentFetches 8 and a mirror concurrency of 4 give the share
	// B = min(4, max(1, floor(8 / 4))) = 2 of R-REPO-055, and with the three client-serving replicas the cluster-wide bound
	// maxConcurrentFetches + n - 1 + B = 12.
	mirrorMaxFetches  = 8
	mirrorConcurrency = 4
	mirrorServing     = 3 // the client-serving replicas: the packages pods of the profile
	mirrorPoolName    = "mirror"
	// the pool's pods: the label the chart puts on a pool's pods (R-HA-062)
	selMirrorPool = selWorker + ",trove/pool=" + mirrorPoolName

	mirrorSA = "e2e-mirror"
	// the extra npm packages of the load: published to the test upstream, so that the pass has work that npm ci does not do
	mirrorExtraPackages = 300
	// the public registry's names, which the npm test upstream answers to in the cluster (ensurePublicNPMNames)
	publicRegistryHost    = "registry.npmjs.org"
	publicReplicationHost = "replicate.npmjs.com"
	corednsMarker         = "# trove-e2e-mirror"
)

// mirrorShare is B of R-REPO-055.
func mirrorShare(concurrency, maxFetches int) int { return min(concurrency, max(1, maxFetches/4)) }

// mirrorBound is the cluster-wide bound of R-REPO-055 on the upstream's concurrent fetches while the pass runs beside the clients.
func mirrorBound(maxFetches, n, b int) int { return maxFetches + n - 1 + b }

// mirrorEnv is the installation the mirror tests share: created once per run.
type mirrorEnv struct {
	adm   *api
	run   string // six hex digits, part of every key and name of this run
	token string // the service account's token: read on the repositories of the run
	npm   *npmFixture
	nuget *nugetFixture
	// extras are the npm packages published for the load, in order; their only version is 1.0.0
	extras []string

	mu     sync.Mutex
	grants []string // the repositories the account reads, in order of creation
	repos  []string // the repositories this run created, removed by mirrorTeardown
	// the repositories the clients-first test left with a complete pass, for the tests after it
	npmMirror, nugetMirror string

	jobs *jobSampler
}

var (
	mirrorOnce sync.Once
	mirrorVal  *mirrorEnv
	mirrorErr  error
)

// mirrorFx puts the cluster into the mirror profile and builds the installation of the tests.
func mirrorFx(t testing.TB) *mirrorEnv {
	t.Helper()
	mirrorOnce.Do(func() {
		mirrorErr = fmt.Errorf("the environment did not finish (see the first failure)")
		mirrorVal, mirrorErr = buildMirrorEnv(t)
	})
	if mirrorErr != nil {
		t.Fatalf("mirror environment: %v", mirrorErr)
	}
	return mirrorVal
}

func buildMirrorEnv(t testing.TB) (*mirrorEnv, error) {
	if err := runUp("mirror"); err != nil {
		return nil, err
	}
	waitServingSettled(t)
	waitFor(t, "two ready worker pods (the main worker) and two of the pool mirror", 5*time.Minute, 2*time.Second, func() bool {
		ready := func(sel string) int {
			n := 0
			for _, p := range livePods(t, sel) {
				if p.Ready && !p.Deleting {
					n++
				}
			}
			return n
		}
		return ready(selMainWorker) == 2 && ready(selMirrorPool) == 2
	})
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	e := &mirrorEnv{run: hex.EncodeToString(b)}
	e.adm = adminAPI(t)

	e.removeStaleMirrors(t)
	stub(t).resetAll(t)
	if os.Getenv("TROVE_E2E_NUGETSTUB_HOST") != "" { // the set mirror-long deploys the npm test upstream only
		nugetStub(t).resetAll(t)
	}
	ensurePublicNPMNames(t)

	ensureSA(t, e.adm, mirrorSA)
	e.token = freshSAToken(t, e.adm, mirrorSA, 6*3600, map[string]any{"inherit": true}).Token
	e.npm = &npmFixture{adm: e.adm, run: e.run, stubHost: os.Getenv("TROVE_E2E_NPMSTUB_HOST"), token: e.token, rowTokens: map[string]string{}}
	e.nuget = &nugetFixture{adm: e.adm, run: e.run, token: e.token}
	e.seedNPM(t)
	e.jobs = startJobSampler()
	return e, nil
}

// removeStaleMirrors deletes the mirrors an earlier run of this suite left behind (a run that was killed): over a test upstream that
// has lost its dataset they would only fail passes, and the dispatch would keep starting them.
func (e *mirrorEnv) removeStaleMirrors(t testing.TB) {
	t.Helper()
	var l struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	e.adm.get("/api/v1/repositories?limit=500").want(t, "list the repositories", 200).JSON(t, &l)
	for _, it := range l.Items {
		if strings.HasPrefix(it.Key, "npm-mirror-") || strings.HasPrefix(it.Key, "nuget-mirror-") {
			deleteRepository(t, e.adm, it.Key)
		}
	}
}

// seedNPM publishes the extra packages of the load to the npm test upstream: one version each, 1.0.0.
func (e *mirrorEnv) seedNPM(t testing.TB) {
	t.Helper()
	s := stub(t)
	s.send(t, http.MethodGet, "/-/stub/mode", nil) // a read first: a forward that broke with the stub is renewed by it
	e.extras = e.extras[:0]
	for i := 0; i < mirrorExtraPackages; i++ {
		name := fmt.Sprintf("e2e-mirror-%s-%03d", e.run, i)
		s.publish(t, name, "1.0.0", nil)
		e.extras = append(e.extras, name)
	}
}

// grant lets the account read the repositories and waits until every replica agrees.
func (e *mirrorEnv) grant(t testing.TB, format string, keys ...string) {
	t.Helper()
	e.mu.Lock()
	e.grants = append(e.grants, keys...)
	all := append([]string(nil), e.grants...)
	e.mu.Unlock()
	ensurePolicy(t, e.adm, "e2e-mirror-"+e.run, all, [2]any{"sa:" + mirrorSA, []string{"read"}})
	if format == "nuget" {
		nugetReadableOnEveryReplica(t, e.token, keys...)
	} else {
		awaitReadableOnEveryReplica(t, e.token, keys...)
	}
}

// create makes a remote repository; the process removes it at its end (mirrorTeardown), because the repositories of one test are the
// input of the next. The keys carry the run.
func (e *mirrorEnv) create(t testing.TB, body map[string]any) string {
	t.Helper()
	key, _ := body["key"].(string)
	body["class"], body["requireAuthentication"] = "remote", true
	e.adm.do(http.MethodPost, "/api/v1/repositories", body).want(t, "create "+key, 201)
	e.mu.Lock()
	e.repos = append(e.repos, key)
	e.mu.Unlock()
	e.grant(t, fmt.Sprint(body["format"]), key)
	return key
}

// drop deletes repositories of the run and takes them out of the account's grant, so that the next policy lists what exists.
func (e *mirrorEnv) drop(t testing.TB, keys ...string) {
	t.Helper()
	for _, key := range keys {
		deleteRepository(t, e.adm, key)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	keep := func(list []string) []string {
		var out []string
		for _, k := range list {
			if !slices.Contains(keys, k) {
				out = append(out, k)
			}
		}
		return out
	}
	e.grants, e.repos = keep(e.grants), keep(e.repos)
}

// npmBody is a remote of the public registry's name (the feed is offered for it only): the include patterns bound nothing, the bound
// of R-REPO-052 is a byte limit. paused keeps its passes from starting (the repository of a baseline).
func (e *mirrorEnv) npmBody(key string, paused bool) map[string]any {
	return map[string]any{
		"key": key, "format": "npm",
		"remote": map[string]any{
			"url": "https://" + publicRegistryHost + "/", "mode": "mirror",
			"mirror": map[string]any{"paused": paused, "concurrency": mirrorConcurrency, "maxVersionsPerPackage": 1, "maxBytes": int64(4) << 30},
			"limits": map[string]any{"maxConcurrentFetches": mirrorMaxFetches},
		},
	}
}

// npmNamesBody is a remote of the private test upstream, which offers no feed: the pass takes the exact names of the include patterns.
func (e *mirrorEnv) npmNamesBody(key string, names []string) map[string]any {
	return map[string]any{
		"key": key, "format": "npm", "includePatterns": names,
		"remote": map[string]any{
			"url": "https://" + e.npm.stubHost + "/", "mode": "mirror",
			"mirror": map[string]any{"concurrency": mirrorConcurrency},
			"limits": map[string]any{"maxConcurrentFetches": mirrorMaxFetches},
		},
	}
}

func (e *mirrorEnv) nugetBody(t testing.TB, key string) map[string]any {
	t.Helper()
	apiHost, contentHost := nugetStubHosts(t)
	return map[string]any{
		"key": key, "format": "nuget",
		"remote": map[string]any{
			"url": "https://" + apiHost + "/v3/index.json", "mode": "mirror",
			"extraHosts": []map[string]any{{"pattern": contentHost, "sendCredentials": false}},
			"mirror":     map[string]any{"concurrency": mirrorConcurrency, "maxVersionsPerPackage": 2},
			"limits":     map[string]any{"maxConcurrentFetches": mirrorMaxFetches},
		},
	}
}

// ── the status and the run ───────────────────────────────────────────────────

type mirrorPassInfo struct {
	Kind            string `json:"kind"`
	Method          string `json:"method"`
	NamesDiscovered int64  `json:"namesDiscovered"`
	PackagesDone    int64  `json:"packagesDone"`
	PackagesPending int64  `json:"packagesPending"`
	PackagesFailed  int64  `json:"packagesFailed"`
	ItemsStored     int64  `json:"itemsStored"`
	DiscoveryDone   bool   `json:"discoveryDone"`
	Job             *struct {
		ID string `json:"id"`
	} `json:"job"`
}

type mirrorSummary struct {
	Kind              string `json:"kind"`
	Method            string `json:"method"`
	StartedAt         string `json:"startedAt"`
	FinishedAt        string `json:"finishedAt"`
	Outcome           string `json:"outcome"`
	Packages          int64  `json:"packages"`
	PackagesFailed    int64  `json:"packagesFailed"`
	ItemsStored       int64  `json:"itemsStored"`
	BytesStored       int64  `json:"bytesStored"`
	DiscoveryComplete bool   `json:"discoveryComplete"`
}

type mirrorStatus struct {
	Mode             string          `json:"mode"`
	State            string          `json:"state"`
	Pass             *mirrorPassInfo `json:"pass"`
	LastCompletePass *mirrorSummary  `json:"lastCompletePass"`
	Errors           struct {
		ConsecutiveFailures int    `json:"consecutiveFailures"`
		LastMessage         string `json:"lastMessage"`
		LastCode            string `json:"lastCode"`
	} `json:"errors"`
	Raw []byte `json:"-"`
}

// status reads the status through the API at a (the edge, or one api pod).
func mirrorStatusAt(t testing.TB, a *api, key string) mirrorStatus {
	t.Helper()
	r := a.get("/api/v1/repositories/"+key+"/mirror").want(t, "status of the mirror of "+key, 200)
	var s mirrorStatus
	r.JSON(t, &s)
	s.Raw = r.Body
	return s
}

func (e *mirrorEnv) status(t testing.TB, key string) mirrorStatus {
	return mirrorStatusAt(t, e.adm, key)
}

// run asks for a pass now; a pass that is queued or running is the answer 409 and is no error.
func (e *mirrorEnv) run_(t testing.TB, key string, full bool) {
	t.Helper()
	for range 20 {
		r := e.adm.do(http.MethodPost, "/api/v1/repositories/"+key+"/mirror/run", map[string]any{"full": full})
		if r.Status == http.StatusTooManyRequests {
			time.Sleep(2 * time.Second)
			continue
		}
		r.want(t, "run a pass of "+key, 202, 409)
		return
	}
	t.Fatalf("run a pass of %s: rate limited for 40 s", key)
}

// awaitComplete waits until the repository has no pass in progress and a last complete pass, and returns the status. A state
// `failing` is a failure of the test at once: the pass will not end by waiting.
func (e *mirrorEnv) awaitComplete(t testing.TB, key string, within time.Duration) mirrorStatus {
	t.Helper()
	var s mirrorStatus
	var last string
	waitFor(t, "the pass of "+key+" to complete", within, 2*time.Second, func() bool {
		s = e.status(t, key)
		if s.State == "failing" {
			t.Fatalf("the mirror of %s is failing: %s (%s)", key, s.Errors.LastMessage, s.Errors.LastCode)
		}
		if s.Pass != nil {
			if line := fmt.Sprintf("%d of %d packages", s.Pass.PackagesDone, s.Pass.NamesDiscovered); line != last {
				last = line
			}
		}
		return s.Pass == nil && s.LastCompletePass != nil
	})
	return s
}

// awaitPassStarted waits until the pass has filled at least n packages and is still running.
func (e *mirrorEnv) awaitPassStarted(t testing.TB, key string, n int64) mirrorStatus {
	t.Helper()
	var s mirrorStatus
	waitFor(t, fmt.Sprintf("the pass of %s to have filled %d packages", key, n), 5*time.Minute, time.Second, func() bool {
		s = e.status(t, key)
		if s.State == "failing" {
			t.Fatalf("the mirror of %s is failing: %s (%s)", key, s.Errors.LastMessage, s.Errors.LastCode)
		}
		return s.Pass != nil && s.Pass.PackagesDone >= n
	})
	return s
}

// ── the pool's processes ─────────────────────────────────────────────────────

// preempted is trove_mirror_preempted_total of the repository, added over the pool's processes.
func preempted(t testing.TB, key string) float64 {
	t.Helper()
	var sum float64
	for _, p := range readyPodsOf(t, selMirrorPool) {
		for s, v := range metricsOf(t, p) {
			if strings.HasPrefix(s, "trove_mirror_preempted_total{") && strings.Contains(s, `repo="`+key+`"`) {
				sum += v
			}
		}
	}
	return sum
}

// poolPodOfPass is the pool pod whose process holds the job of the repository's pass, read from the job table.
func poolPodOfPass(t testing.TB, jobID string) (podName, node string) {
	t.Helper()
	podName = sql(t, `SELECT coalesce(r.name, '') FROM trove.job j LEFT JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.id = '`+jobID+`' AND j.state = 'running'`)
	if podName == "" {
		return "", ""
	}
	return podName, strings.TrimSpace(kn(t, "get", "pod", podName, "-o", "jsonpath={.spec.nodeName}"))
}

// claimedBy is trove_jobs_claimed_total of the kind in each process of the pool.
func claimedBy(t testing.TB, kind string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, p := range readyPodsOf(t, selMirrorPool) {
		for s, v := range metricsOf(t, p) {
			if strings.HasPrefix(s, "trove_jobs_claimed_total{") && strings.Contains(s, `kind="`+kind+`"`) {
				out[p] += v
			}
		}
	}
	return out
}

// ── the upstreams ────────────────────────────────────────────────────────────

func npmInflight(t testing.TB) npmstub.Inflight {
	t.Helper()
	var v npmstub.Inflight
	if err := json.Unmarshal(stub(t).send(t, http.MethodGet, "/-/stub/inflight", nil), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func nugetInflight(t testing.TB) nugetstub.Inflight {
	t.Helper()
	var v nugetstub.Inflight
	if err := json.Unmarshal(nugetStub(t).send(t, http.MethodGet, "/-/stub/inflight", nil), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// tarballCounts is the number of requests of each tarball path of the npm test upstream whose package name starts with prefix.
func tarballCounts(t testing.TB, prefix string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, en := range stub(t).counts(t).Entries {
		if en.Kind == npmstub.KindTarball && strings.HasPrefix(en.Name, prefix) {
			out[en.Path] += en.Count
		}
	}
	return out
}

// stubScale scales a test upstream's Deployment and waits for its pods (or their end).
func stubScale(t testing.TB, name string, replicas int) {
	t.Helper()
	kn(t, "scale", "deployment/trove-"+name, fmt.Sprintf("--replicas=%d", replicas))
	if replicas == 0 {
		kn(t, "wait", "--for=delete", "pod", "-l", "app.kubernetes.io/name=trove-"+name, "--timeout=120s")
		return
	}
	kn(t, "rollout", "status", "deployment/trove-"+name, "--timeout=3m")
}

var stubsStopped struct {
	sync.Mutex
	names map[string]bool
}

func markStubStopped(name string, stopped bool) {
	stubsStopped.Lock()
	defer stubsStopped.Unlock()
	if stubsStopped.names == nil {
		stubsStopped.names = map[string]bool{}
	}
	stubsStopped.names[name] = stopped
}

// ── the name of the public registry ──────────────────────────────────────────

var publicNames struct {
	sync.Mutex
	on bool
}

// ensurePublicNPMNames makes registry.npmjs.org and replicate.npmjs.com resolve, inside the cluster, to the Service of the npm test
// upstream, whose certificate carries both names (deploy/dev/npmstub.yaml): a CoreDNS `rewrite`. The feed of the mirror is
// offered only when the remote's URL is exactly https://registry.npmjs.org/ (R-REPO-051), and a pass of 300 packages needs it, because
// the method `names` takes at most the 100 exact names an include list holds (R-REPO-005). The rewrite lasts for the test
// process: TestMain calls restorePublicNPMNames. A run that was killed leaves it; the next run finds the marker and the end of
// that run removes it. Nothing else in the sets that call this reaches the public registry.
func ensurePublicNPMNames(t testing.TB) {
	t.Helper()
	publicNames.Lock()
	defer publicNames.Unlock()
	if publicNames.on {
		return
	}
	corefile, err := kubectl("-n", "kube-system", "get", "configmap", "coredns", "-o", "jsonpath={.data.Corefile}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(corefile, corednsMarker) {
		rules := "    rewrite name exact " + publicRegistryHost + " npmstub." + cfg.namespace + ".svc.cluster.local " + corednsMarker + "\n" +
			"    rewrite name exact " + publicReplicationHost + " npmstub." + cfg.namespace + ".svc.cluster.local " + corednsMarker + "\n"
		withRules := regexp.MustCompile(`(?m)^(\s*ready\s*\n)`).ReplaceAllString(corefile, "${1}"+rules)
		if withRules == corefile {
			t.Fatalf("the Corefile has no `ready` line to put the rewrite after:\n%s", corefile)
		}
		setCorefile(t, withRules)
	}
	publicNames.on = true
	// the clients' pods ask the same resolver as trove's: wait until they see the Service's address
	want := strings.TrimSpace(kn(t, "get", "service", "npmstub", "-o", "jsonpath={.spec.clusterIP}"))
	waitFor(t, publicRegistryHost+" and "+publicReplicationHost+" to resolve to the npm test upstream ("+want+")", 3*time.Minute, 2*time.Second, func() bool {
		for _, host := range []string{publicRegistryHost, publicReplicationHost} {
			out, err := kubectl("-n", cfg.namespace, "exec", npmClientPod(t, 0), "--", "getent", "hosts", host)
			if err != nil || !strings.Contains(out, want) {
				return false
			}
		}
		return true
	})
	// the stub's certificate carries both names, and the dev CA signs it
	for _, host := range []string{publicRegistryHost, publicReplicationHost} {
		c := newStubControl(t, "svc/npmstub", host, "trove-npmstub", 30*time.Second)
		c.send(t, http.MethodGet, "/-/stub/mode", nil)
		stopForward(c.fwd)
	}
}

func setCorefile(t testing.TB, corefile string) {
	t.Helper()
	patch, _ := json.Marshal(map[string]any{"data": map[string]string{"Corefile": corefile}})
	if out, err := kubectl("-n", "kube-system", "patch", "configmap", "coredns", "--type", "merge", "-p", string(patch)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := kubectl("-n", "kube-system", "rollout", "restart", "deployment/coredns"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := kubectl("-n", "kube-system", "rollout", "status", "deployment/coredns", "--timeout=3m"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// restorePublicNPMNames removes the rewrite, and starts a test upstream that a test left stopped; TestMain's step.
func restorePublicNPMNames() {
	publicNames.Lock()
	on := publicNames.on
	publicNames.Unlock()
	stubsStopped.Lock()
	for name, stopped := range stubsStopped.names {
		if stopped {
			_, _ = kubectl("-n", cfg.namespace, "scale", "deployment/trove-"+name, "--replicas=1")
		}
	}
	stubsStopped.Unlock()
	if !on {
		return
	}
	corefile, err := kubectl("-n", "kube-system", "get", "configmap", "coredns", "-o", "jsonpath={.data.Corefile}")
	if err != nil || !strings.Contains(corefile, corednsMarker) {
		return
	}
	var kept []string
	for _, line := range strings.Split(corefile, "\n") {
		if !strings.Contains(line, corednsMarker) {
			kept = append(kept, line)
		}
	}
	patch, _ := json.Marshal(map[string]any{"data": map[string]string{"Corefile": strings.Join(kept, "\n")}})
	if out, err := kubectl("-n", "kube-system", "patch", "configmap", "coredns", "--type", "merge", "-p", string(patch)); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: removing the CoreDNS rewrite:", err, out)
		return
	}
	_, _ = kubectl("-n", "kube-system", "rollout", "restart", "deployment/coredns")
}

// ── the job table ────────────────────────────────────────────────────────────

// jobSampler reads the job table every 400 ms for as long as the tests run and keeps what R-REPO-058 forbids: two unfinished
// jobs `mirror-pass` that share a dedup key (one repository). It reads through kubectl on the primary of the database, from a
// goroutine that never calls a method of a test.
type jobSampler struct {
	stop chan struct{}
	done chan struct{}

	mu      sync.Mutex
	samples int
	failed  int
	worst   int
	bad     []string
}

func startJobSampler() *jobSampler {
	s := &jobSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *jobSampler) loop() {
	defer close(s.done)
	primary := ""
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		if primary == "" {
			p, err := kubectl("-n", cfg.namespace, "get", "cluster", "trove-db", "-o", "jsonpath={.status.currentPrimary}")
			if err != nil || strings.TrimSpace(p) == "" {
				s.note(func() { s.failed++ })
				continue
			}
			primary = strings.TrimSpace(p)
		}
		out, err := kubectl("-n", cfg.namespace, "exec", primary, "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|", "-c",
			`SELECT dedup_key, count(*) FROM trove.job WHERE kind = 'mirror-pass' AND state IN ('queued', 'running', 'cancelling') GROUP BY dedup_key`)
		if err != nil {
			primary = ""
			s.note(func() { s.failed++ })
			continue
		}
		s.note(func() {
			s.samples++
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				f := strings.Split(line, "|")
				if len(f) != 2 {
					continue
				}
				var n int
				_, _ = fmt.Sscan(f[1], &n)
				s.worst = max(s.worst, n)
				if n > 1 {
					s.bad = append(s.bad, fmt.Sprintf("%s: %d unfinished jobs at %s", f[0], n, time.Now().Format(time.TimeOnly)))
				}
			}
		})
	}
}

func (s *jobSampler) note(f func()) { s.mu.Lock(); f(); s.mu.Unlock() }

// check fails the test when a sample saw two unfinished passes of one repository.
func (s *jobSampler) check(t testing.TB) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Logf("job table: %d samples (%d reads failed), at most %d unfinished mirror-pass job(s) per repository", s.samples, s.failed, s.worst)
	if len(s.bad) > 0 {
		t.Errorf("R-REPO-058: the job table held two unfinished mirror-pass jobs for one repository: %v", s.bad)
	}
}

func (s *jobSampler) close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

// mirrorTeardown is TestMain's step: the sampler ends, the rewrite of the public registry's name is removed, a stopped test upstream
// is started.
func mirrorTeardown() {
	mirrorOnce.Do(func() {}) // a run that never used the mirror environment has nothing to undo
	if e := mirrorVal; e != nil {
		if e.jobs != nil {
			e.jobs.close()
		}
		// a mirror that stays behind keeps dispatching passes in the cluster's next run
		for _, key := range e.repos {
			for range 10 {
				r, err := e.adm.send(http.MethodDelete, "/api/v1/repositories/"+key, nil)
				if err != nil || r.Status != http.StatusTooManyRequests {
					break
				}
				time.Sleep(2 * time.Second)
			}
		}
	}
	restorePublicNPMNames()
}

// ── the clients ──────────────────────────────────────────────────────────────

// npmReference is the reference project of perf/npm/reference (a lockfile of 707 entries that the corpus of the npm test upstream
// resolves), with the lockfile's addresses pointed at the repository of the test.
func npmReference(t testing.TB, key string) (pkg, lock string) {
	t.Helper()
	root := filepath.Join(cfg.repo, "perf", "npm", "reference")
	p, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := os.ReadFile(filepath.Join(root, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	// the lockfile names the repository npm-all on the edge's default port; the same prefix of the repository of this test
	const committed = "https://localhost:8080/api/npm/npm-all/"
	if !strings.Contains(string(l), committed) {
		t.Fatalf("perf/npm/reference/package-lock.json no longer resolves from %s", committed)
	}
	return string(p), strings.ReplaceAll(string(l), committed, registryURL(t, "localhost", key))
}

// npmCI runs `npm ci` of the reference project through the repository on client pod i with an empty client cache, and returns the
// run and the number of packages it installed.
func (e *mirrorEnv) npmCI(t testing.TB, pod int, key, label string) (npmRun, int) {
	t.Helper()
	c := newNPMClient(t, e.npm, "npm11", pod)
	c.registryKey = key
	c.tag = "mirror-" + label + "-" + e.run
	pkg, lock := npmReference(t, key)
	dir := c.dir("ci")
	c.sh("/work", `rm -rf "`+dir+`" && mkdir -p "`+dir+`"`)
	c.write(dir+"/package.json", pkg)
	c.write(dir+"/package-lock.json", lock)
	c.configure(dir)
	r := c.run(dir, "ci", "--cache", dir+"/cache")
	n := 0
	if r.Code == 0 {
		if out := c.sh(dir, `find node_modules -name package.json -path '*node_modules/*' | wc -l`); out.Code == 0 {
			_, _ = fmt.Sscan(strings.TrimSpace(out.Out), &n)
		}
	}
	c.sh("/work", `rm -rf "`+dir+`"`)
	return r, n
}

// npmInstall installs name@version into a new project through the repository, with an empty client cache; it returns the run and
// whether node_modules holds the package.
func (e *mirrorEnv) npmInstall(t testing.TB, pod int, key, label, name, version string) (npmRun, bool) {
	t.Helper()
	c := newNPMClient(t, e.npm, "npm11", pod)
	c.registryKey = key
	c.tag = "mirror-" + label + "-" + e.run
	dir := c.project("install", nil)
	c.configure(dir)
	r := c.run(dir, "install", name+"@"+version, "--cache", dir+"/cache")
	has := false
	if r.Code == 0 {
		has = c.sh(dir, `test -f "node_modules/`+name+`/package.json"`).Code == 0
	}
	c.sh("/work", `rm -rf "`+dir+`"`)
	return r, has
}

// restoreReference runs `dotnet restore` of the reference solution on NuGet client pod i through the repository and returns the number
// of packages it extracted.
func (e *mirrorEnv) restoreReference(t testing.TB, pod int, key, label string) (string, int, error) {
	t.Helper()
	c := e.nuget.client(t, pod, "mirror-"+label)
	for rel, b := range referenceSolution(t) {
		c.write(rel, b)
	}
	c.config(nugetFeed(t, "localhost", key), true)
	out, err := c.try("dotnet", "restore", "Reference.sln", "--no-cache")
	n := 0
	if err == nil {
		n = c.restoredPackages()
	}
	return out, n, err
}
