//go:build e2e

package e2e

// A drain per role (R-HA-059 "A drain per role", E6a.6, Q-HA-94): for each of api, packages, the main worker and the worker pool
// vuln, the pods of that Deployment alone are evicted one after the other through the eviction API, each after the previous
// replacement is ready, under the ping load through the edge, the upload and download loop on generic-local, a loop of
// management API calls with a token, the driver's changes and jobs, and the npm load of TestNPMHA (npm_load_test.go: eight npm
// ci loops, a publish loop and k6's package documents and tarballs, with its pass conditions). The npm load runs in the scatter
// profile of the npm tests: three api, three packages, two worker and two vuln pods. The drain of packages replaces every
// replica, so every replica's caches of documents (the rendition cache, the merged documents of npm-all, the remote's stored
// documents) are cold afterwards. npm ci asks for tarballs only, so k6 asks for the document of every package the lockfile
// locks (npmLoadOpts.allDocuments), and the documents' p99 is held to twice the warm-up's on its own: the tarballs of 1 to 5
// MiB set the p99 of the mix. A long leg: the set drain-roles of the nightly workflow, on dispatch and locally (owner,
// 2026-11-01), not of a change.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type drainRole struct {
	name     string
	selector string // the Deployment's pods: the role and its pool label
	worker   bool   // the pods run jobs of the default queue
}

var drainRoles = []drainRole{
	{"api", selAPI + ",trove/pool=", false},
	{"packages", selPackages + ",trove/pool=", false},
	{"worker", selWorker + ",trove/pool=", true},
	{"worker-vuln", selWorker + ",trove/pool=vuln", false},
}

// evict asks the eviction API to remove a pod. A PodDisruptionBudget that does not allow it yet answers 429; the call is
// repeated until the budget does, as `kubectl drain` does.
func evict(t testing.TB, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":%q,"namespace":%q}}`, name, cfg.namespace)
	f := filepath.Join(cfg.out, "eviction-"+name+".json")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the eviction of "+name+" to be admitted", 3*time.Minute, 5*time.Second, func() bool {
		_, err := kubectl("create", "--raw", fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/eviction", cfg.namespace, name), "-f", f)
		if err != nil && !strings.Contains(err.Error(), "TooManyRequests") && !strings.Contains(err.Error(), "disruption budget") {
			t.Fatal(err)
		}
		return err == nil
	})
}

func TestDrainRoles(t *testing.T) {
	h := npmHAFx(t)
	t.Run("R-HA-059 R-HA-011 R-NPM-046 R-ARCH-024 E6a.6 each role evicted pod by pod under load: only 200 for the ping load, no failed transfer or management call, every npm command exits 0, k6 only 2xx and 304 for npm, its p99 and its documents' p99 at most twice the warm-up's, every replaced packages replica renders the documents again, every acknowledged publish installs, the pods of the other roles keep their names and restarts, no SIGKILL, nothing Pending for 60 s, the probe jobs of the worker resume from their cursor", func(t *testing.T) {
		const steps = 400
		// R-HA-059's load starts 5 minutes before the drain and runs until 10 minutes after; as in TestNPMHA's variant A the
		// nightly leg measures a warm-up of 2 minutes before the first eviction and runs 3 minutes after the last replacement
		// is ready. The 90 s before the warm-up fill the replicas' caches with k6's documents: k6 cycles through its 406 at 75
		// documents/s, 16 times in 90 s, so a replica has missed a given one with a chance of about (2/3)^16, 0.15 %.
		const prime, warmUp, after = 90 * time.Second, 2 * time.Minute, 3 * time.Minute
		troveSettled(t, 3, 3, 4)
		initialPackages := map[string]bool{}
		for _, p := range livePods(t, selPackages) {
			initialPackages[p.Name] = true
		}
		adm := adminAPI(t)
		watch := startPodWatch(t, selTrove)
		o := haFullLoad()
		o.allDocuments = true
		npm := startNPMLoad(t, h, "drain-roles", o)
		drv := startDriverLoad(t, "drain-roles", 90*time.Minute)
		k := startK6(t, "drain-roles", 90*time.Minute)
		kx := startK6Script(t, "drain-roles-xfer", "transfer.js", 90*time.Minute, k6Opts{rate: 4, prefix: "T ", token: transferToken(t)})

		// A token's management calls, one every 250 ms, through the edge; a refused call is a failure of the drain.
		var calls, bad atomic.Int64
		var firstBad atomic.Value
		stopCalls := make(chan struct{})
		var callsDone sync.WaitGroup
		callsDone.Add(1)
		go func() {
			defer callsDone.Done()
			for {
				select {
				case <-stopCalls:
					return
				case <-time.After(250 * time.Millisecond):
				}
				r, err := adm.send(http.MethodGet, "/api/v1/auth/whoami", nil)
				calls.Add(1)
				if err != nil || r.Status != 200 {
					bad.Add(1)
					firstBad.CompareAndSwap(nil, fmt.Sprintf("%v %v", r, err))
				}
			}
		}()
		time.Sleep(time.Until(npm.started.Add(prime + warmUp)))

		var ids []string
		var evicted []pod
		var firstEviction time.Time
		roleFrom, roleTo := map[string]time.Time{}, map[string]time.Time{}
		for _, role := range drainRoles {
			before := map[string]pod{}
			for _, p := range livePods(t, selTrove) {
				before[p.Name] = p
			}
			victims := livePods(t, role.selector)
			if len(victims) < 2 {
				t.Fatalf("%s: %d pods, the drain needs the replicas of a highly available role", role.name, len(victims))
			}
			if role.worker {
				for i := 0; i < 4; i++ {
					ids = append(ids, strings.TrimSpace(driver(t, "probe", "-steps", fmt.Sprint(steps), "-interval-ms", "1000")))
				}
				time.Sleep(10 * time.Second) // the jobs are claimed
			}
			t.Logf("%s: evicting %v one after the other", role.name, names(victims))
			for _, v := range victims {
				evict(t, v.Name)
				if firstEviction.IsZero() {
					firstEviction = time.Now()
				}
				if roleFrom[role.name].IsZero() {
					roleFrom[role.name] = time.Now()
				}
				evicted = append(evicted, v)
				waitFor(t, v.Name+" to be gone", 4*time.Minute, 2*time.Second, func() bool {
					for _, p := range podsOf(t, role.selector) {
						if p.Name == v.Name {
							return false
						}
					}
					return true
				})
				waitFor(t, "the replacement of "+v.Name+" to be ready", 5*time.Minute, 2*time.Second, func() bool {
					ps := podsOf(t, role.selector)
					if len(ps) != len(victims) {
						return false
					}
					for _, p := range ps {
						if !p.Ready || p.Deleting {
							return false
						}
					}
					return true
				})
			}
			roleTo[role.name] = time.Now()
			// The pods of the other roles keep their names, their UIDs and their restart counts.
			now := map[string]pod{}
			for _, p := range livePods(t, selTrove) {
				now[p.Name] = p
			}
			for name, was := range before {
				if hasPod(victims, name) {
					continue
				}
				if p, ok := now[name]; !ok || p.UID != was.UID || p.Restarts != was.Restarts {
					t.Errorf("%s drain: the pod %s of another role changed (was uid %s with %d restarts, now %+v)", role.name, name, was.UID, was.Restarts, p)
				}
			}
		}
		drainEnd := time.Now()
		t.Logf("%d evictions from %s to %s (%s)", len(evicted), firstEviction.Format("15:04:05"), drainEnd.Format("15:04:05"), drainEnd.Sub(firstEviction).Round(time.Second))

		finished := map[string]probeJob{}
		waitFor(t, "every probe job to end", 8*time.Minute, 5*time.Second, func() bool {
			for _, id := range ids {
				if _, ok := finished[id]; ok {
					continue
				}
				var j probeJob
				if err := jsonUnmarshal(driver(t, "job", id), &j); err != nil {
					t.Fatal(err)
				}
				if j.State == "succeeded" || j.State == "failed" || j.State == "cancelled" {
					finished[id] = j
				}
			}
			return len(finished) == len(ids)
		})
		moved := 0
		for _, id := range ids {
			j := finished[id]
			t.Logf("job %s: %s attempt %d/%d steps %d hops %d resumed at %v", id, j.State, j.Attempt, j.MaxAttempts, j.Result.Steps, len(j.Result.Hops), j.Result.Resumes)
			if j.State != "succeeded" || j.Result.Steps != steps {
				t.Errorf("job %s ended %s with %d steps, want succeeded with %d", id, j.State, j.Result.Steps, steps)
			}
			if j.Attempt != 1 {
				t.Errorf("job %s: attempt %d, want 1 (a release at shutdown does not count an attempt)", id, j.Attempt)
			}
			if len(j.Result.Hops) > 1 {
				moved++
				if len(j.Result.Resumes) == 0 || j.Result.Resumes[0] <= 0 || j.Result.Resumes[0] >= steps {
					t.Errorf("job %s moved but did not resume from a cursor inside the run: %v", id, j.Result.Resumes)
				}
			}
		}
		if moved == 0 {
			t.Errorf("no probe job moved to another worker although both main workers were evicted")
		}

		time.Sleep(max(10*time.Second, time.Until(drainEnd.Add(after))))
		close(stopCalls)
		callsDone.Wait()
		watch.stop()
		driver(t, "stop")
		ds := drv.wait(t)
		res := k.stop(t)
		xres := kx.stopTransfer(t)
		nres := npm.stop(t)
		nk := nres.K6
		warmFrom, windowTo := nk.First.Add(prime), drainEnd.Add(2*time.Minute)
		p99s := map[string]map[string]int64{}
		var warm, during, warmDoc, duringDoc time.Duration
		var nWarm, nDuring, nWarmDoc, nDuringDoc int
		for _, kind := range []string{"", "document", "tarball"} {
			w, nw := nk.p99Of(kind, warmFrom, firstEviction)
			d, nd := nk.p99Of(kind, firstEviction, windowTo)
			pk, npk := nk.p99Of(kind, roleFrom["packages"], roleTo["packages"].Add(2*time.Minute))
			label := kind
			switch kind {
			case "":
				label, warm, nWarm, during, nDuring = "all", w, nw, d, nd
			case "document":
				warmDoc, nWarmDoc, duringDoc, nDuringDoc = w, nw, d, nd
			}
			p99s[label] = map[string]int64{"warmup_ms": w.Milliseconds(), "warmup_n": int64(nw), "drain_ms": d.Milliseconds(), "drain_n": int64(nd), "packages_ms": pk.Milliseconds(), "packages_n": int64(npk)}
			t.Logf("npm k6 p99 of %s: warm-up %s over %d requests; from the first eviction until 2 minutes after the last replacement was ready %s over %d; from the first eviction of packages until 2 minutes after its last replacement was ready %s over %d",
				label, w, nw, d, nd, pk, npk)
		}
		served := nk.documentsServed(roleFrom["packages"])
		t.Logf("k6 asked for %d distinct documents of %d; distinct documents answered 200 per replica since the first eviction of packages: %v", nk.distinctDocuments(), h.allDocuments, served)
		noteResult(t, "e14-drain-roles", map[string]any{
			"k6_status": res.Status, "driver": ds, "pending_pods": watch.pendFor, "moved_jobs": moved, "transfers": xres.Status, "api_calls": calls.Load(), "api_calls_failed": bad.Load(),
			"evictions": len(evicted), "first_eviction": firstEviction, "drain_seconds": drainEnd.Sub(firstEviction).Seconds(), "role_from": roleFrom, "role_to": roleTo,
			"npm_k6": k6Summary(nk), "npm_p99": p99s, "npm_documents": h.allDocuments, "npm_documents_asked": nk.distinctDocuments(), "npm_documents_served": served,
			"npm_commands": len(nres.Commands), "npm_published": len(nres.Published),
		})

		res.only200(t)
		if len(xres.Failures) != 0 || xres.Bad != 0 || xres.Dropped != 0 {
			t.Errorf("the upload and download loop on generic-local failed during the drains: %d failures, %d wrong checksums, %v dropped iterations; first: %v", len(xres.Failures), xres.Bad, xres.Dropped, xres.Failures)
		}
		if xres.Status["put 201"]+xres.Status["put 200"] < 100 || xres.Status["get 200"] < 100 {
			t.Errorf("the transfer loop made too few requests to say anything: %v", xres.Status)
		}
		if bad.Load() != 0 {
			t.Errorf("%d of %d management API calls with a token failed during the drains; first: %v", bad.Load(), calls.Load(), firstBad.Load())
		}

		// the pass conditions of R-HA-059 for the npm load, as TestNPMHA's variant A applies them
		nres.allExitZero(t)
		nres.loopsSpan(t, firstEviction, drainEnd)
		nk.only2xx304(t)
		if nWarm < 1000 || nDuring < 1000 || nWarmDoc < 1000 || nDuringDoc < 1000 {
			t.Errorf("too few npm k6 requests for the latency conditions: %d (%d documents) in the warm-up, %d (%d documents) in the drain window", nWarm, nWarmDoc, nDuring, nDuringDoc)
		}
		if during > 2*warm {
			t.Errorf("R-HA-059: the p99 of k6's npm requests from the first eviction until 2 minutes after the last replacement was ready is %s, more than twice the warm-up's %s", during, warm)
		}
		// Q-HA-94: the documents on their own, which the tarballs would hide in the mix
		if duringDoc > 2*warmDoc {
			t.Errorf("R-HA-059 Q-HA-94: the p99 of k6's document requests from the first eviction until 2 minutes after the last replacement was ready is %s, more than twice the warm-up's %s", duringDoc, warmDoc)
		}
		// The load reached the cold caches: every packages replica that replaced one rendered at least half of the documents.
		replaced := 0
		for replica, n := range served {
			if !strings.HasPrefix(replica, "trove-packages-") || initialPackages[replica] {
				continue
			}
			replaced++
			if n < h.allDocuments/2 {
				t.Errorf("the replacement %s answered %d distinct documents with 200, fewer than half of the %d k6 asks for: its caches were not exercised", replica, n, h.allDocuments)
			}
		}
		if replaced != len(initialPackages) {
			t.Errorf("k6's documents were answered by %d replacement packages replicas, want %d (one per evicted pod)", replaced, len(initialPackages))
		}
		nres.installPublished(t, h)

		if len(watch.killed) != 0 {
			t.Errorf("containers were killed: %v", watch.killed)
		}
		if name, d := watch.longestPending(); d > 60*time.Second {
			t.Errorf("trove pod %s was Pending for %s", name, d.Round(time.Second))
		}
		checkEvents(t, evicted)
		checkReleaseServes(t, 3, 3, 4)
	})
}

func hasPod(ps []pod, name string) bool {
	for _, p := range ps {
		if p.Name == name {
			return true
		}
	}
	return false
}
