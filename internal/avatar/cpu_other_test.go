//go:build !unix

package avatar

import "time"

// cpuTime is not available here: the readings are zero and the CPU bound judges nothing.
func cpuTime() time.Duration { return 0 }
