//go:build !linux && !darwin

package authz

import "time"

// threadCPU is the wall clock on a platform without a thread CPU clock.
func threadCPU() (now func() time.Duration, done func()) {
	start := time.Now()
	return func() time.Duration { return time.Since(start) }, func() {}
}

// cpuClock is what the performance test measures with.
const cpuClock = "wall clock"
