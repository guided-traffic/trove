package app

import (
	"math"
	"runtime/debug"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func TestMemoryLimit(t *testing.T) {
	before := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(before) })

	t.Run("R-HA-036 without GOMEMLIMIT the Go memory limit is runtime.memory_limit_ratio times runtime.memory_limit_bytes", func(t *testing.T) {
		debug.SetMemoryLimit(math.MaxInt64)
		l, src := applyMemoryLimit(config.Runtime{MemoryLimitRatio: 0.85, MemoryLimitBytes: 1 << 30}, nil)
		ratio := 0.85
		if want := int64(ratio * (1 << 30)); l != want || debug.SetMemoryLimit(-1) != want || src != "runtime.memory_limit_bytes" {
			t.Fatalf("limit %d from %q, runtime %d", l, src, debug.SetMemoryLimit(-1))
		}
	})
	t.Run("R-HA-036 an explicit GOMEMLIMIT wins and is left as the runtime read it", func(t *testing.T) {
		debug.SetMemoryLimit(300 << 20)
		l, src := applyMemoryLimit(config.Runtime{MemoryLimitRatio: 0.85, MemoryLimitBytes: 1 << 30}, []string{"GOMEMLIMIT=300MiB"})
		if l != 300<<20 || debug.SetMemoryLimit(-1) != 300<<20 || src != "GOMEMLIMIT" {
			t.Fatalf("limit %d from %q", l, src)
		}
		debug.SetMemoryLimit(math.MaxInt64)
		if l, _ := applyMemoryLimit(config.Runtime{MemoryLimitRatio: 0.85, MemoryLimitBytes: 1 << 30}, []string{"GOMEMLIMIT=off"}); l != 0 {
			t.Fatalf("GOMEMLIMIT=off gave a limit of %d", l)
		}
	})
	t.Run("R-HA-036 without a known container limit nothing is set", func(t *testing.T) {
		debug.SetMemoryLimit(math.MaxInt64)
		if l, src := applyMemoryLimit(config.Runtime{MemoryLimitRatio: 0.85}, nil); l != 0 || src != "" || debug.SetMemoryLimit(-1) != math.MaxInt64 {
			t.Fatalf("limit %d from %q", l, src)
		}
	})
	t.Run("R-ARCH-005 a request may hold the download buffer, the object store's receive window and the overhead", func(t *testing.T) {
		c := &config.Config{}
		c.Storage.Download = config.StorageDownload{BufferSize: 256 << 10, HoldBack: 64 << 10}
		c.Storage.S3.HTTP.ReceiveWindow = 256 << 10
		if got := requestMemory(c); got != 640<<10 {
			t.Fatalf("request memory %d, want 640 KiB", got)
		}
	})
}
