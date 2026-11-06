//go:build !unix

package observability

import (
	"runtime/metrics"
	"time"
)

// processCPUTime is, without getrusage, the Go runtime's own estimate: the CPU
// time of /cpu/classes/total:cpu-seconds that was not idle. The runtime updates
// it at the end of each garbage collection only, so it is coarse over a
// heartbeat period. trove's images are Linux; this keeps other builds working.
func processCPUTime() time.Duration {
	s := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/idle:cpu-seconds"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindFloat64 || s[1].Value.Kind() != metrics.KindFloat64 {
		return 0
	}
	return time.Duration((s[0].Value.Float64() - s[1].Value.Float64()) * float64(time.Second))
}
