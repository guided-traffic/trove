//go:build unix

package avatar

import (
	"context"
	"testing"
)

// BenchmarkProcessWorstCase measures Process on the slowest accepted images (worstcase_test.go) in wall time and in CPU time.
// The wall time is what DecodeTimeout bounds; the CPU time is what a replica with a CPU limit is charged. It is not independent of
// the host's load (a decoder is bound by memory, which processes share), so take the least of several runs, not the median. Run it without -race, in the cgroup of a replica:
//
//	docker run --rm --cpus=1 -v "$PWD":/src -v "$HOME/go/pkg/mod":/go/pkg/mod -w /src -e GOTOOLCHAIN=local golang:1.27.3 \
//	  go test -run xxx -bench ProcessWorstCase -benchtime 3x -count 5 ./internal/avatar/
func BenchmarkProcessWorstCase(b *testing.B) {
	for _, c := range worstCases(b) {
		b.Run(c.name, func(b *testing.B) {
			start := cpuTime()
			b.ResetTimer()
			for b.Loop() {
				if _, err := proc().Process(context.Background(), c.mediaType, c.data); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(cpuTime()-start)/float64(b.N)/1e6, "cpu-ms/op")
		})
	}
}
