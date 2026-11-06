//go:build integration

package overload

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestBurstBesideEmptyCaches is the burst that Q-ARCH-240 shed (Q-ARCH-328): a
// replica with a 1 GiB container and the default caches, whose budgets add up
// to 648 MiB. It reserved them whole and admitted 77 downloads; the caches
// are empty here, so the replica admits what its memory holds, and 300 held
// downloads of 1 MiB (about 190 MiB) are all served. TestMemoryAdmission shows
// that a real exhaustion is still shed.
func TestBurstBesideEmptyCaches(t *testing.T) {
	in := newInstall(t)
	r := in.start(t, "burst", fmt.Sprintf("TROVE_RUNTIME_MEMORY_LIMIT_BYTES=%d", 1<<30))
	big := in.deploy(t, r, "big.bin", 1<<20)
	const path = "/files/" + repo + "/big.bin"
	const n = 300

	t.Run("R-ARCH-005 Q-ARCH-328 the slots are not reduced by the caches' budgets while the caches are empty", func(t *testing.T) {
		// 0.8 × 0.85 × 1 GiB over 640 KiB is 1,114; the budgets of the default caches would leave 77.
		if slots := r.metric(t, "trove_http_memory_slots"); slots < 1000 {
			t.Fatalf("trove_http_memory_slots = %.0f, want about 1,114 (77 when the budgets were reserved)", slots)
		}
	})

	t.Run("R-ARCH-005 Q-ARCH-328 300 downloads held at once are all served: nothing is shed although the budgets of the caches would have left 77 slots", func(t *testing.T) {
		const shedSeries = `trove_http_requests_shed_total{endpoint="default",reason="memory_slots"}`
		base := r.completed(t)
		downloads := make([]*slow, n)
		for i := range downloads {
			downloads[i] = slowGet(t, r.client, path, in.token)
		}
		testenv.WaitFor(t, "every download reached the replica", 60*time.Second, func() bool {
			return r.completed(t)-base+r.metric(t, `trove_http_requests_in_flight{endpoint="default",listener="client"}`) >= n
		})
		time.Sleep(time.Second)
		results := make([]result, n)
		var wg sync.WaitGroup
		for i := range downloads {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = downloads[i].finish()
			}()
		}
		wg.Wait()
		for i, res := range results {
			if res.err != nil || res.status != http.StatusOK || res.sum != big || res.n != 1<<20 {
				t.Errorf("download %d: status %d, %d bytes, err %v", i, res.status, res.n, res.err)
			}
		}
		if shed := r.metrics(t, shedSeries)[shedSeries]; shed != 0 {
			t.Errorf("%.0f requests were shed for the memory", shed)
		}
		if !r.alive() {
			t.Fatal("the replica exited")
		}
	})
}
