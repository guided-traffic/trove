//go:build integration

package overload

import (
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestHeldDownloadCost measures what a download held open costs the replica
// through the real serving path (Q-ARCH-328): n slow downloads of one size are
// held in their writes, and the heap in use, the goroutine stacks and the
// resident set are read against the idle replica. The replica has memory to
// spare and a tight GOGC, so the heap in use is close to the live heap.
func TestHeldDownloadCost(t *testing.T) {
	in := newInstall(t)
	const n = 100
	// A download is held only while the replica's write waits for the client. The client reads through a 4 KiB receive
	// buffer, but the replica's send buffer on a loopback connection takes a whole 1 MiB file on Linux (autotuned up to
	// tcp_wmem's 4 MiB; Test and Release run 42146436982 held 0 of 100), so the sizes start above that.
	for _, size := range []int{2 << 20, 5 << 20, 8 << 20} {
		name := fmt.Sprintf("f%d.bin", size)
		t.Run(fmt.Sprintf("Q-ARCH-328 R-ARCH-005 %d KiB held downloads", size>>10), func(t *testing.T) {
			// A replica of its own per size, so the pooled buffers of another size are not counted.
			r := in.start(t, fmt.Sprintf("cost%d", size>>10), fmt.Sprintf("TROVE_RUNTIME_MEMORY_LIMIT_BYTES=%d", 8<<30), "GOGC=10",
				"TROVE_CACHE_RENDITION_BYTES=0", "TROVE_PROXY_METADATA_CACHE_BYTES=0", "TROVE_PROXY_MERGED_METADATA_CACHE_BYTES=0",
				"TROVE_PROXY_QUERY_CACHE_BYTES=0")
			in.deploy(t, r, name, size)
			cost := func() (heap, stacks, rss float64) {
				m := r.metrics(t, "go_memstats_heap_inuse_bytes", "go_memstats_stack_inuse_bytes")
				out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(r.cmd.Process.Pid)).Output()
				if err != nil {
					t.Fatal(err)
				}
				kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
				return m["go_memstats_heap_inuse_bytes"], m["go_memstats_stack_inuse_bytes"], kb * 1024
			}
			path := "/files/" + repo + "/" + name
			time.Sleep(500 * time.Millisecond)
			h0, s0, r0 := cost()
			downloads := make([]*slow, n)
			for i := range downloads {
				downloads[i] = slowGet(t, r.client, path, in.token)
			}
			var held float64
			testenv.WaitFor(t, "downloads held", 30*time.Second, func() bool {
				held = r.metric(t, "trove_http_memory_slots_in_use")
				return held >= n/2
			})
			time.Sleep(2 * time.Second)
			held = r.metric(t, "trove_http_memory_slots_in_use")
			h1, s1, r1 := cost()
			if held < n/2 {
				t.Fatalf("only %.0f of %d downloads of %d KiB were still held when the cost was read: the replica's socket buffers took the rest, so this size measures nothing here", held, n, size>>10)
			}
			t.Logf("size %4d KiB: %3.0f of %d held; per held download: heap in use %6.0f KiB, stacks %5.0f KiB, resident %6.0f KiB",
				size>>10, held, n, (h1-h0)/held/1024, (s1-s0)/held/1024, (r1-r0)/held/1024)
			// The guard charges 640 KiB for a download of a large object (internal/app/memory.go): a held
			// download that costs more than 10 % above it means the charge is no longer true.
			if per := (h1 - h0 + s1 - s0) / held; per > 1.1*640*1024 {
				t.Errorf("a held download costs %.0f KiB of heap and stacks, more than 10 %% above the 640 KiB the guard charges", per/1024)
			}
			for _, d := range downloads {
				if res := d.finish(); res.status != http.StatusOK {
					t.Errorf("status %d, err %v", res.status, res.err)
				}
			}
		})
	}
}
