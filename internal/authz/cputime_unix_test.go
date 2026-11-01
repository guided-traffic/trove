//go:build linux || darwin

package authz

import (
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// threadCPU returns a reading of the CPU time the calling thread has used, and
// the function that ends the measurement. The thread stays locked between the two
// calls. The difference of two readings is the time the code ran on a processor,
// without the time the scheduler gave the processor to another process: on a
// machine shared with other work, the wall-clock tail of a microsecond-scale call
// is that scheduling and not the code.
func threadCPU() (now func() time.Duration, done func()) {
	runtime.LockOSThread()
	return func() time.Duration {
		var ts unix.Timespec
		if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &ts); err != nil {
			panic(err)
		}
		return time.Duration(ts.Nano())
	}, runtime.UnlockOSThread
}

// cpuClock is what the performance test measures with.
const cpuClock = "thread CPU time"
