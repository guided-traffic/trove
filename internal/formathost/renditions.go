package formathost

import (
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/formats"
)

// RenditionCacheName is the registered name of the rendition cache (R-ARCH-032).
const RenditionCacheName = "format_renditions"

// renditionSafetyTTL bounds the age of an entry. The key holds the package's
// revision, so an entry is never stale; the bound only keeps a document nobody asks
// for any more from living until the cache is full.
const renditionSafetyTTL = time.Hour

// renditions is the per-replica cache of rendered documents (R-ARCH-012, C-12).
// The key carries everything a rendition depends on - the package's revision, the
// variant, the base URL, the endpoint and its exposure fingerprint, the settings
// and the render version - so a change of any of them is a different key and no
// invalidation exists to miss. It is size-weighted by cache.rendition_bytes, and a
// document above an eighth of the budget is not stored.
type renditions struct {
	c      *cache.Cache[formats.RenditionKey, []byte]
	budget int64
}

var _ formats.RenditionCache = (*renditions)(nil)

// NewRenditionCache registers the rendition cache on reg with the given budget in
// bytes (cache.rendition_bytes). A budget of zero stores nothing.
func NewRenditionCache(reg *cache.Registry, budget int64) (formats.RenditionCache, error) {
	if reg == nil {
		return nil, fmt.Errorf("formathost: no cache registry for the rendition cache")
	}
	if budget <= 0 {
		return missCache{}, nil
	}
	c := cache.Register(reg, RenditionCacheName, cache.Options[formats.RenditionKey, []byte]{
		MaxBytes: budget, Weigh: func(_ formats.RenditionKey, v []byte) int64 { return int64(len(v)) + 256 },
		SafetyTTL: renditionSafetyTTL,
	})
	return &renditions{c: c, budget: budget}, nil
}

func (r *renditions) Get(k formats.RenditionKey) ([]byte, bool) { return r.c.Get(k) }

func (r *renditions) Put(k formats.RenditionKey, doc []byte) {
	if int64(len(doc)) > r.budget/8 {
		return
	}
	r.c.Set(k, doc)
}
