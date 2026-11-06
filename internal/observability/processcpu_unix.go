//go:build unix

package observability

import (
	"syscall"
	"time"
)

// processCPUTime is the CPU time the process has used since it started, user
// and system, of every thread (getrusage RUSAGE_SELF): the time the container's
// CPU accounting charges. runtime/metrics' /cpu/classes are not used for it:
// the runtime updates them only at the end of a garbage collection, so a
// heartbeat period without one would read as idle and the next as a burst.
func processCPUTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
