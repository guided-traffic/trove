//go:build unix

package avatar

import (
	"syscall"
	"time"
)

// cpuTime is the CPU time the process has used, all threads (the garbage collector's included). It depends on how busy the host
// is (a decoder is bound by memory, which processes share), but not on how long the scheduler kept the process waiting.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
