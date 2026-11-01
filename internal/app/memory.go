package app

import (
	"context"
	"math"
	"runtime/debug"
	"strings"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// requestOverhead is what a download holds besides its buffer and the object
// store's receive window: the connection, the goroutine stacks, the request and
// the object-store call. 400 downloads held open measured 81 KiB of heap and
// 50 KiB of stacks each (Q-ARCH-240).
const requestOverhead = 128 << 10

// applyMemoryLimit applies R-HA-036: unless GOMEMLIMIT is set, the Go runtime's
// limit is runtime.memory_limit_ratio × runtime.memory_limit_bytes. It returns
// the limit in effect, 0 when none is known, and where it came from.
func applyMemoryLimit(rt config.Runtime, environ []string) (int64, string) {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "GOMEMLIMIT="); ok && v != "" {
			if l := debug.SetMemoryLimit(-1); l != math.MaxInt64 {
				return l, "GOMEMLIMIT"
			}
			return 0, "GOMEMLIMIT"
		}
	}
	if rt.MemoryLimitBytes <= 0 {
		return 0, ""
	}
	l := int64(rt.MemoryLimitRatio * float64(rt.MemoryLimitBytes))
	debug.SetMemoryLimit(l)
	return l, "runtime.memory_limit_bytes"
}

// cacheBudgets is what the per-replica caches hold when they are full, which the
// memory slots leave aside (Q-ARCH-243): the rendition cache, the two metadata
// caches of the proxy and virtual engines and the cache of pass-through answers.
func cacheBudgets(c *config.Config) int64 {
	return int64(c.Cache.RenditionBytes) + int64(c.Proxy.MetadataCacheBytes) + int64(c.Proxy.MergedMetadataCacheBytes) + int64(c.Proxy.QueryCacheBytes)
}

// requestMemory is what one request may hold, for the memory slots
// (Q-ARCH-240): the buffer of a verified download, the object store's receive
// window and the overhead.
func requestMemory(c *config.Config) int64 {
	return blob.DownloadBufferBytes(c.Storage.Download) + int64(c.Storage.S3.HTTP.ReceiveWindow) + requestOverhead
}

// startMemoryGuard sets the Go memory limit and, on a process with client
// listeners, starts the guard of R-ARCH-005 step 4 that every endpoint's chain
// shares. Without a known limit the guard is nil and admits everything.
func (a *App) startMemoryGuard(ctx context.Context) *httpserver.MemoryGuard {
	limit, source := applyMemoryLimit(a.cfg.Runtime, a.o.Environ)
	if len(a.eps) == 0 {
		if limit > 0 {
			a.log.Info("memory limit", "go_memory_limit_bytes", limit, "source", source)
		}
		return nil
	}
	per, reserved := requestMemory(a.cfg), cacheBudgets(a.cfg)
	g := httpserver.NewMemoryGuard(httpserver.MemoryGuardConfig{
		Limit: limit, RequestBytes: per, Reserved: reserved,
		OnChange: func(shedding bool, heap uint64) {
			if shedding {
				a.log.Warn("the live heap reached the high-water mark: new requests answer 503 server_busy until it falls below the low-water mark",
					"heap_bytes", heap, "go_memory_limit_bytes", limit)
				return
			}
			a.log.Info("the live heap fell below the low-water mark: requests are admitted again", "heap_bytes", heap)
		},
	})
	a.tele.metrics.MemorySlots(g.Slots())
	if g == nil {
		a.log.Warn("no memory limit is known (runtime.memory_limit_bytes, GOMEMLIMIT): the client listeners admit requests up to max_in_flight only")
		return nil
	}
	a.tele.metrics.MemorySlotsInUse(g.InUse)
	if float64(reserved) >= 0.8*float64(limit) {
		a.log.Error("the caches' byte budgets take the memory the requests need: one request at a time is admitted; lower cache.rendition_bytes, proxy.metadata_cache_bytes, proxy.merged_metadata_cache_bytes and proxy.query_cache_bytes or raise the memory limit",
			"cache_budget_bytes", reserved, "go_memory_limit_bytes", limit)
	}
	a.log.Info("memory limit", "go_memory_limit_bytes", limit, "source", source, "memory_slots", g.Slots(), "request_bytes", per, "cache_budget_bytes", reserved)
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		g.Run(ctx, httpserver.MemorySampleInterval)
	}()
	return g
}
