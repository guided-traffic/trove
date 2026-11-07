//go:build integration

package mirror

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
)

// Clients first across processes (E6b.4; R-REPO-055): a client-serving replica and a process that claims the queue mirror, the real
// engine, the real pass, the signal mirror_yield on the stack's Valkey, and the stub that counts the fetches it is serving.

// fetchResult is the answer of a client fetch started by startFetch.
type fetchResult struct {
	path   string
	status int
	bytes  int
	err    error
}

// startFetch sends a client GET without failing the test from the goroutine; the answer arrives on the channel.
func (r *rig) startFetch(rep *testenv.Replica, path string, out chan<- fetchResult) {
	go func() {
		res := fetchResult{path: path}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+rep.Addr("default")+"/api/"+r.format+"/"+testenv.DefaultProject+"/"+repoKey+"/"+path, nil)
		if err != nil {
			res.err = err
			out <- res
			return
		}
		req.Host = testenv.DefaultHost
		req.Header.Set("Authorization", "Bearer "+r.reader)
		resp, err := testenv.Client.Do(req)
		if err != nil {
			res.err = err
			out <- res
			return
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		res.status, res.bytes, res.err = resp.StatusCode, len(body), err
		out <- res
	}()
}

func TestR_REPO_055_AClientFetchThatHasToWaitPreemptsThePassOfAnotherProcess(t *testing.T) {
	const (
		slots   = 8 // limits.maxConcurrentFetches: the client-serving replica's slots, and B = min(4, 8/4) = 2
		pass    = 10
		clients = 9
	)
	include := []string{"@acme/*"}
	for i := range pass {
		include = append(include, fmt.Sprintf("@acme/p%03d", i))
	}
	r := newRig(t, rigOptions{
		valkey: true, include: include, mirror: settings{"concurrency": 4, "discovery": "names"},
		remote: map[string]any{"limits": map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": slots}},
	})
	r.publishMany("@acme", pass)
	for i := range clients {
		r.publish(fmt.Sprintf("@acme/c%03d", i), "1.0.0")
	}
	r.setStub(npmstub.Mode{DelayMS: map[string]int{npmstub.KindTarball: 2000}})
	run, web := r.runner("runner"), r.web("web")
	r.pass(run)

	// The window of a pass starts at 1 and doubles after a success: after the first tarball (2 s) the pass fetches two at a time.
	testenv.WaitFor(t, "the pass to have two background fetches at the stub", passWithin, func() bool { return r.traf.inflight() >= 2 })
	time.Sleep(300 * time.Millisecond)
	if got := r.traf.inflight(); got != 2 {
		t.Fatalf("the pass has %d background fetches at the stub, want B = 2", got)
	}

	results := make(chan fetchResult, clients)
	t.Run("R-REPO-055 eight client fetches take the client-serving replica's eight slots without a wait", func(t *testing.T) {
		waited := r.metric(web, "trove_remote_fetch_queue_wait_seconds_sum")
		for i := range slots {
			r.startFetch(web, tarballPath(fmt.Sprintf("@acme/c%03d", i), "1.0.0"), results)
		}
		testenv.WaitFor(t, "the eight client fetches to reach the stub", 10*time.Second, func() bool { return r.traf.inflight() >= 2+slots })
		if got := r.traf.inflight(); got != 2+slots {
			t.Errorf("%d tarball fetches at the stub, want the 2 of the pass and the 8 of the clients", got)
		}
		if w := r.metric(web, "trove_remote_fetch_queue_wait_seconds_sum") - waited; w > 0.05 {
			t.Errorf("the eight client fetches waited %.3f s for their slots in all", w)
		}
	})

	var ninth time.Time
	t.Run("R-REPO-055 a ninth waits, the process that runs the pass pre-empts both background fetches, and 1 s later the stub has at most eight open fetches", func(t *testing.T) {
		r.traf.resetPeak()
		ninth = time.Now()
		r.startFetch(web, tarballPath(fmt.Sprintf("@acme/c%03d", slots), "1.0.0"), results)
		testenv.WaitFor(t, "the pass's process to pre-empt the background fetches", 5*time.Second, func() bool {
			return r.metric(run, "trove_mirror_preempted_total") >= 2
		})
		time.Sleep(time.Until(ninth.Add(time.Second)))
		if got := r.traf.inflight(); got > slots {
			t.Errorf("%d tarball fetches at the stub 1 s after the ninth began, want at most %d", got, slots)
		}
		if got := r.metric(run, "trove_mirror_preempted_total"); got != 2 {
			t.Errorf("trove_mirror_preempted_total = %v, want 2", got)
		}
		if got := r.metric(run, "trove_mirror_items_total", "result", "preempted"); got != 2 {
			t.Errorf("trove_mirror_items_total{result=preempted} = %v, want 2", got)
		}
	})

	t.Run("R-REPO-055 the pass starts no background fetch while the clients press, and every client fetch succeeds", func(t *testing.T) {
		time.Sleep(time.Until(ninth.Add(3 * time.Second)))
		if got := r.traf.inflight(); got > 1 {
			t.Errorf("%d tarball fetches at the stub 3 s after the ninth began: the pass went on although the signal said clients wait", got)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range clients {
				select {
				case res := <-results:
					if res.err != nil || res.status != http.StatusOK || res.bytes == 0 {
						t.Errorf("client fetch of %s: %d, %d bytes, %v", res.path, res.status, res.bytes, res.err)
					}
				case <-time.After(20 * time.Second):
					t.Error("a client fetch did not return")
					return
				}
			}
		}()
		wg.Wait()
		if r.traf.peak() > 2+slots {
			t.Errorf("the stub served %d fetches at once, more than the 8 slots and the share B", r.traf.peak())
		}
	})
}
