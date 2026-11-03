//go:build e2e

package e2e

// The long legs of the reference pipeline (roadmap M4 E4.7, R-CI-028, R-CI-029, R-CI-018): the pipeline while the node of a web
// replica is drained, with Valkey stopped for the whole run (every replica stays ready and reports "degraded":["valkey"]),
// single-use exchanges across a forced Sentinel failover and FLUSHALL, and a web pod deleted gracefully while 50 jobs exchange
// in a loop. They take minutes, so they are the set ci-ha of the nightly HA and Performance workflow, not of a change
// (deploy/dev/e2e.sh, docs/developer/e2e.md).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pipelineOnce runs both jobs of the reference pipeline on a protected tag and requires that both pass, publish and leave no
// secret in the log. It returns the time span of the two jobs.
func (fx *ciFixture) pipelineOnce(t *testing.T, tag string, pod int) (start, end time.Time) {
	t.Helper()
	claims := protectedTag(t, tag)
	start = time.Now()
	npm := fx.npmJob(t, pod, "ha-npm-"+tag, claims, nil)
	nuget := fx.nugetJob(t, pod, "ha-nuget-"+tag, claims, nil)
	end = time.Now()
	for what, r := range map[string]ciJobResult{"npm job": npm, "NuGet job": nuget} {
		if r.Code != 0 {
			t.Fatalf("%s of %s exited %d:\n%s", what, tag, r.Code, tailLines(r.Out, 60))
		}
		noSecrets(t, what, r)
	}
	version := strings.TrimPrefix(tag, "v")
	if !contains(fx.versionsOfNPM(t, ciNpmInt, npmPackageName), version) || !contains(fx.versionsOfNuGet(t, ciNugetInt, nugetPackageID), version) {
		t.Fatalf("%s: the packages are not both published", tag)
	}
	return start, end
}

// TestCIHADrain is E4.7's first half: the pipeline passes while the node of one web replica is drained with kubectl drain during
// its run (R-CI-028).
func TestCIHADrain(t *testing.T) {
	fx := ciFx(t)
	node := drainNodeVariantA(t)
	var drainStart, drainEnd atomic.Int64
	done := make(chan error, 1)
	go func() {
		cmd := exec.Command(filepath.Join(cfg.repo, "deploy/dev", "drain.sh"), node)
		cmd.Dir = cfg.repo
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		drainStart.Store(time.Now().UnixNano())
		err := cmd.Run()
		drainEnd.Store(time.Now().UnixNano())
		if err != nil {
			err = fmt.Errorf("drain.sh %s: %w\n%s", node, err, tail(buf.String(), 2000))
		}
		done <- err
	}()
	finished := false
	defer func() {
		if !finished {
			<-done // never leave the cluster half drained
		}
	}()
	overlapped := 0
	for i := 0; ; i++ {
		s, e := fx.pipelineOnce(t, fmt.Sprintf("v1.7.%d", i), i%2)
		ds, de := drainStart.Load(), drainEnd.Load()
		if ds != 0 && s.UnixNano() < max(de, time.Now().UnixNano()*boolInt(de == 0)) && e.UnixNano() > ds {
			overlapped++
		}
		select {
		case err := <-done:
			finished = true
			if err != nil {
				t.Fatal(err)
			}
		default:
		}
		if finished && i >= 1 {
			break
		}
	}
	t.Logf("drain of %s: %s; %d pipeline runs overlapped it", node, time.Duration(drainEnd.Load()-drainStart.Load()).Round(time.Second), overlapped)
	if overlapped == 0 {
		t.Error("no run of the pipeline overlapped the drain")
	}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// TestCIHAValkeyStopped is E4.7's second half and R-CI-029's readiness criterion: the pipeline passes with Valkey stopped for the
// whole run, and every replica stays ready and reports "degraded":["valkey"].
func TestCIHAValkeyStopped(t *testing.T) {
	fx := ciFx(t)
	withValkeyStopped(t, func() {
		webReadyDegraded(t, true)
		fx.pipelineOnce(t, "v1.8.0", 0)
		fx.pipelineOnce(t, "v1.8.1", 1)
		webReadyDegraded(t, true)
		live, ready := livePods(t, selWeb), readyWebPods(t)
		if len(live) < 2 || len(ready) != len(live) {
			t.Errorf("R-CI-029: %d of %d web replicas are ready with Valkey stopped, want all", len(ready), len(live))
		}
		for _, p := range ready {
			code, body := ops(p, "/readyz")
			if code != 200 || !strings.Contains(body, `"degraded"`) || !strings.Contains(body, `"valkey"`) {
				t.Errorf("R-CI-029: %s /readyz = %d %s, want 200 with degraded valkey", p, code, strings.TrimSpace(body))
			}
		}
	})
	webReadyDegraded(t, false)
}

// formExchange is the body of an exchange request.
func formExchange(idToken string) string {
	return "grant_type=" + formEscape("urn:ietf:params:oauth:grant-type:token-exchange") +
		"&subject_token_type=" + formEscape("urn:ietf:params:oauth:token-type:id_token") + "&subject_token=" + formEscape(idToken)
}

// postWithRetry posts an exchange and repeats it after a 429 or 503 as the pipeline's `curl --retry 3` does (a refused
// request records no jti, so the repeat is not a replay); it returns the answer and the number of repeats.
func postWithRetry(a *api, form string) (reply, int, error) {
	var last reply
	for n := 0; n < 20; n++ {
		r, err := a.send(http.MethodPost, "/api/v1/auth/oidc/token", form, "Content-Type", "application/x-www-form-urlencoded")
		if err != nil {
			return reply{}, n, err
		}
		if r.Status != http.StatusTooManyRequests && r.Status != http.StatusServiceUnavailable {
			return r, n, nil
		}
		last = r
		wait := 1
		if v, err := strconv.Atoi(r.Header.Get("Retry-After")); err == nil && v > 0 && v < 30 {
			wait = v
		}
		time.Sleep(time.Duration(wait) * time.Second)
	}
	return last, 20, fmt.Errorf("still %d after 20 repeats", last.Status)
}

// TestCIHASingleUse is R-CI-018 and R-CI-029 under a forced Sentinel failover and FLUSHALL: on an issuer with
// single_use_subject_tokens (a second double, its own issuer URL) two concurrent exchanges of one ID token yield exactly one
// success and one subject_token_replayed, for every ID token, while the Valkey primary is failed over and flushed; no request
// answers 5xx, and the package requests with the issued tokens answer 200.
func TestCIHASingleUse(t *testing.T) {
	fx := ciFx(t)
	single := double(t, "oidcstub-single", "TROVE_E2E_OIDCSTUB_SINGLE_HOST")
	single.resetAll(t)
	const issuerName = "gitlab-single"
	fx.adm.do(http.MethodDelete, "/api/v1/ci/issuers/"+issuerName+"?cascade=true", nil)
	fx.adm.do(http.MethodPost, "/api/v1/ci/issuers", map[string]any{
		"name": issuerName, "kind": "gitlab", "issuer": single.issuer(), "caBundleRef": ciCABundleRef,
		"description": "end-to-end tests: single-use subject tokens", "singleUseSubjectTokens": true,
	}).want(t, "register the single-use issuer", 201)
	defer fx.adm.do(http.MethodDelete, "/api/v1/ci/issuers/"+issuerName+"?cascade=true", nil)
	fx.adm.do(http.MethodPost, "/api/v1/ci/issuers/"+issuerName+"/mappings", map[string]any{
		"name": "single-read", "priority": 10, "maxTtlSeconds": 600,
		"match":  map[string]any{"all": []any{map[string]any{"claim": "namespace_id", "equals": "4711"}}},
		"grants": map[string]any{"policies": []string{ciPolicyName}},
	}).want(t, "create the mapping of the single-use issuer", 201)

	// R-CI-029: 500 exchanges (250 ID tokens, each exchanged twice at once) and 500 package requests (two with each issued token). The
	// address budget of the exchange (ci.exchange.rate_limit.per_client_ip_per_minute, 600) is the test's pace: a gate lets one ID token
	// start every 600 ms, which is 200 exchanges a minute, so no 429 is expected and a retried one is counted.
	const tokens = 250
	var gate sync.Mutex
	var gateAt time.Time
	pace := func() {
		gate.Lock()
		defer gate.Unlock()
		if d := time.Until(gateAt); d > 0 {
			time.Sleep(d)
		}
		gateAt = time.Now().Add(600 * time.Millisecond)
	}
	var (
		mu       sync.Mutex
		problems []string
		okCount  atomic.Int64
		repeats  atomic.Int64
		next     atomic.Int64
	)
	fail := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(problems) < 20 {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}
	var events sync.WaitGroup
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				i := int(next.Add(1))
				if i > tokens {
					return
				}
				pace()
				pid := strconv.Itoa(5000 + i)
				idToken, err := single.tryMint(map[string]any{"aud": fx.base, "project_id": pid, "job_project_id": pid, "job_id": fmt.Sprint(fx.jobBase*1000 + int64(i))})
				if err != nil {
					fail("mint %d: %v", i, err)
					continue
				}
				type result struct {
					r   reply
					n   int
					err error
				}
				res := make(chan result, 2)
				for range 2 {
					go func() {
						r, n, err := postWithRetry(newAPI(t), formExchange(idToken))
						res <- result{r, n, err}
					}()
				}
				wins, replays := 0, 0
				var citoken string
				for range 2 {
					x := <-res
					repeats.Add(int64(x.n))
					switch {
					case x.err != nil:
						fail("token %d: %v", i, x.err)
					case x.r.Status == 200:
						wins++
						var out struct {
							AccessToken string `json:"access_token"`
						}
						_ = json.Unmarshal(x.r.Body, &out)
						citoken = out.AccessToken
					case x.r.Status == 400 && bytes.Contains(x.r.Body, []byte("subject_token_replayed")):
						replays++
					default:
						fail("token %d: unexpected answer %s", i, x.r)
					}
				}
				if wins != 1 || replays != 1 {
					fail("token %d: %d successes and %d replays, want 1 and 1", i, wins, replays)
					continue
				}
				okCount.Add(1)
				for range 2 {
					p, err := newAPI(t).withToken(citoken).send(http.MethodGet, "/api/npm/"+ciNpmAll+"/-/ping", nil)
					if err != nil || p.Status != 200 {
						fail("token %d: the package request answered %v %v", i, p, err)
					}
				}
			}
		}()
	}
	// the failover and FLUSHALL come while the workers run (the workload takes about 150 s)
	events.Add(1)
	go func() {
		defer events.Done()
		time.Sleep(6 * time.Second)
		script(t, "valkey-failover.sh")
		time.Sleep(2 * time.Second)
		t.Logf("FLUSHALL: %s", strings.TrimSpace(valkeyPrimaryCLI(t, "FLUSHALL")))
	}()
	events.Wait()
	workers.Wait()
	t.Logf("%d single-use tokens, %d exchanges repeated after a 429 or 503", okCount.Load(), repeats.Load())
	for _, p := range problems {
		t.Error(p)
	}
	if got := okCount.Load(); got < tokens {
		t.Errorf("only %d of %d tokens ended with exactly one success", got, tokens)
	}
	// R-CI-029: this page's only Valkey keys are the counters of the two limiters
	keys := valkeyPrimaryCLI(t, "--scan", "--pattern", "trove:*")
	for _, k := range strings.Fields(keys) {
		if strings.Contains(k, "ci") && !strings.Contains(k, "ci_exchange_address") && !strings.Contains(k, "ci_exchange_subject") && strings.Contains(k, "ci_") {
			t.Errorf("R-CI-029: Valkey key %q is not a counter of ci_exchange_address or ci_exchange_subject", k)
		}
	}
}

// TestCIHAPodDelete is R-CI-029: a web pod is deleted gracefully while 50 jobs exchange in a loop; every exchange answers 200 and no
// client sees a reset connection or a 5xx. The jobs have projects of their own, so the per-subject budget is not the subject of
// the test, and their pace keeps the address budget (600 a minute) unspent.
func TestCIHAPodDelete(t *testing.T) {
	fx := ciFx(t)
	const jobs = 50
	var (
		mu     sync.Mutex
		bad    []string
		total  atomic.Int64
		stopAt = time.Now().Add(100 * time.Second)
		wg     sync.WaitGroup
	)
	for j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pid := strconv.Itoa(6000 + j)
			tok, err := fx.dbl.tryMint(map[string]any{"aud": fx.base, "project_id": pid, "job_project_id": pid, "job_id": fmt.Sprint(fx.jobBase*1000 + 500 + int64(j))})
			if err != nil {
				mu.Lock()
				bad = append(bad, fmt.Sprintf("job %d mint: %v", j, err))
				mu.Unlock()
				return
			}
			time.Sleep(time.Duration(j) * 140 * time.Millisecond) // spread the jobs over the period
			for time.Now().Before(stopAt) {
				r, err := newAPI(t).send(http.MethodPost, "/api/v1/auth/oidc/token", formExchange(tok), "Content-Type", "application/x-www-form-urlencoded")
				total.Add(1)
				if err != nil || r.Status != 200 {
					mu.Lock()
					if len(bad) < 20 {
						bad = append(bad, fmt.Sprintf("job %d: %v %v", j, r, err))
					}
					mu.Unlock()
				}
				time.Sleep(8 * time.Second)
			}
		}()
	}
	time.Sleep(15 * time.Second)
	victim := readyWebPods(t)[0]
	t.Logf("deleting %s gracefully", victim)
	kn(t, "delete", "pod", victim, "--wait=false")
	waitWebSettled(t)
	wg.Wait()
	t.Logf("%d exchanges by %d jobs", total.Load(), jobs)
	for _, b := range bad {
		t.Error(b)
	}
	if total.Load() < 250 {
		t.Errorf("only %d exchanges ran", total.Load())
	}
}
