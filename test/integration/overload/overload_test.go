//go:build integration

package overload

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// budget is the replica's container memory limit in this test.
const budget = 256 << 20

func TestMemoryAdmission(t *testing.T) {
	in := newInstall(t)
	// The caches are off, so all of the memory the guard shares out goes to requests (Q-ARCH-243 leaves their
	// budgets aside): 0.8 × the Go limit / 640 KiB.
	r := in.start(t, "overload", fmt.Sprintf("TROVE_RUNTIME_MEMORY_LIMIT_BYTES=%d", budget),
		"TROVE_CACHE_RENDITION_BYTES=0", "TROVE_PROXY_METADATA_CACHE_BYTES=0", "TROVE_PROXY_MERGED_METADATA_CACHE_BYTES=0")
	big := in.deploy(t, r, "big.bin", 1<<20)
	const path = "/files/" + repo + "/big.bin"
	slots := r.metric(t, "trove_http_memory_slots")

	t.Run("R-HA-036 the Go memory limit is runtime.memory_limit_ratio of the container limit", func(t *testing.T) {
		ratio := 0.85
		if got, want := r.metric(t, "go_gc_gomemlimit_bytes"), float64(int64(ratio*budget)); got != want {
			t.Fatalf("go_gc_gomemlimit_bytes = %.0f, want %.0f", got, want)
		}
		// 0.8 × the Go limit over 640 KiB per request (internal/app/memory.go)
		if slots != 278 {
			t.Fatalf("trove_http_memory_slots = %.0f, want 278", slots)
		}
	})

	t.Run("R-ARCH-005 R-HA-036 far more slow downloads than the memory holds: the excess gets 503 server_busy with Retry-After, the heap stays within the limit, every admitted download completes with its bytes", func(t *testing.T) {
		const n = 1000
		var (
			mu                   sync.Mutex
			maxHeld, maxInFlight float64
			maxInUse             float64
			samples              int
			stop, sampled        = make(chan struct{}), make(chan struct{})
		)
		// What the runtime holds from the operating system (heap, stacks, its own structures,
		// less what it returned) is what a container's memory limit counts.
		held := func(m map[string]float64) float64 {
			return m["go_memstats_sys_bytes"] - m["go_memstats_heap_released_bytes"]
		}
		var stopOnce sync.Once
		stopSampler := func() { stopOnce.Do(func() { close(stop) }); <-sampled }
		t.Cleanup(stopSampler) // also when a check below ends the test early
		go func() {
			defer close(sampled)
			for {
				m := r.metrics(t, "go_memstats_sys_bytes", "go_memstats_heap_released_bytes", `trove_http_requests_in_flight{endpoint="default",listener="client"}`, "trove_http_memory_slots_in_use")
				mu.Lock()
				maxHeld, maxInFlight = max(maxHeld, held(m)), max(maxInFlight, m[`trove_http_requests_in_flight{endpoint="default",listener="client"}`])
				maxInUse = max(maxInUse, m["trove_http_memory_slots_in_use"])
				samples++
				mu.Unlock()
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}()

		const bySlots, byHeap = `trove_http_requests_shed_total{endpoint="default",reason="memory_slots"}`, `trove_http_requests_shed_total{endpoint="default",reason="heap"}`
		shed := func() float64 {
			m := r.metrics(t, bySlots, byHeap)
			return m[bySlots] + m[byHeap]
		}
		// Every request has reached the replica once the requests it completed (a shed 503 included) and the ones in
		// flight add up to the requests sent. Completed counts too: on Linux the kernel's send buffer takes a whole
		// 1 MiB answer, the handler returns before its client reads, and the request leaves the in-flight gauge.
		base := r.completed(t)
		downloads := make([]*slow, n)
		for i := range downloads {
			downloads[i] = slowGet(t, r.client, path, in.token)
		}
		testenv.WaitFor(t, "every download reached the replica", 60*time.Second, func() bool {
			return r.completed(t)-base+r.metric(t, `trove_http_requests_in_flight{endpoint="default",listener="client"}`) >= n
		})
		time.Sleep(2 * time.Second) // the held downloads sit in their writes; the sampler keeps reading
		if !r.alive() {
			t.Fatal("the replica exited under the load")
		}

		results := make([]result, n)
		var wg sync.WaitGroup
		next := make(chan int)
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					results[i] = downloads[i].finish()
				}
			}()
		}
		for i := range downloads {
			next <- i
		}
		close(next)
		wg.Wait()
		stopSampler()

		ok, busy := 0, 0
		for i, res := range results {
			switch {
			case res.err == nil && res.status == http.StatusOK && res.sum == big && res.n == 1<<20:
				ok++
			case res.err == nil && res.status == http.StatusServiceUnavailable && res.retryAfter == "1":
				busy++
			default:
				t.Errorf("download %d: status %d, %d bytes, Retry-After %q, err %v", i, res.status, res.n, res.retryAfter, res.err)
			}
		}
		// The in-flight gauge counts a request from the request log on, so it also holds the ones being shed.
		m := r.metrics(t, bySlots, byHeap)
		t.Logf("%d downloads: %d complete, %d answered 503 server_busy (%.0f for the slots, %.0f for the heap); in flight at most %.0f, slots in use at most %.0f of %.0f; the runtime held at most %.1f MiB of the %d MiB limit (%d samples)",
			n, ok, busy, m[bySlots], m[byHeap], maxInFlight, maxInUse, slots, maxHeld/(1<<20), budget>>20, samples)
		// The slots in use are read on the server: a download the kernel's socket buffers took whole ends before its
		// client reads and frees its slot for the next one, so the number of completed downloads can exceed the slots.
		if busy == 0 || ok == 0 || maxInUse > slots || maxInUse == 0 {
			t.Errorf("%d complete, %d shed, at most %.0f slots in use of %.0f: the excess must be shed, the admitted ones must complete, and no more than the slots at once", ok, busy, maxInUse, slots)
		}
		if got := shed(); got != float64(busy) {
			t.Errorf("trove_http_requests_shed_total counts %.0f, the clients saw %d", got, busy)
		}
		if maxHeld >= budget {
			t.Errorf("the runtime held %.1f MiB, not below the %d MiB limit", maxHeld/(1<<20), budget>>20)
		}
		if !r.alive() {
			t.Fatal("the replica exited")
		}
	})

	t.Run("R-ARCH-005 once the load is gone the replica admits again", func(t *testing.T) {
		testenv.WaitFor(t, "a download answered 200", 30*time.Second, func() bool {
			res := slowGet(t, r.client, path, in.token).finish()
			return res.status == http.StatusOK && res.sum == big
		})
	})
}

// metrics reads several series of one scrape; an absent one is missing from the map.
func (r *replica) metrics(t *testing.T, series ...string) map[string]float64 {
	t.Helper()
	code, body := get("http://"+r.ops+"/metrics", "")
	if code != http.StatusOK {
		t.Errorf("metrics: %d", code)
		return nil
	}
	out := map[string]float64{}
	for _, s := range series {
		if v := testenv.MetricValue(body, s); v >= 0 {
			out[s] = v
		}
	}
	return out
}

// completed sums trove_http_requests_total of the default endpoint's client listener over every handler, method and
// code.
func (r *replica) completed(t *testing.T) float64 {
	t.Helper()
	code, body := get("http://"+r.ops+"/metrics", "")
	if code != http.StatusOK {
		t.Fatalf("metrics: %d %s", code, body)
	}
	var sum float64
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "trove_http_requests_total{") || !strings.Contains(line, `endpoint="default"`) || !strings.Contains(line, `listener="client"`) {
			continue
		}
		if v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64); err == nil {
			sum += v
		}
	}
	return sum
}
