package webui

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Shared is the store of the hashed files of every release whose replicas served the UI of this installation (R-HA-020,
// Q-HA-120). During a rolling upgrade a page whose index.html came from a replica of one release asks for its hashed files
// on replicas of both; a replica that lacks one asks the store, so the page loads whichever replica answers. The store is
// filled by the replicas themselves at start-up, with nothing but their own embedded build; no request writes to it.
type Shared interface {
	// Asset returns the content of the hashed file at path under the locale's directory that a replica of any release
	// published. An error that wraps ErrNotShared says no replica did; any other error says the store could not be asked.
	Asset(ctx context.Context, locale, path string) ([]byte, error)
}

// ErrNotShared is the answer of Shared.Asset for a file no replica published.
var ErrNotShared = errors.New("webui: no release published the asset")

const (
	// sharedTimeout bounds what a file from the shared store may cost a request: a slow store turns into a 503 the
	// browser shows as a failed load, never into a page that hangs (Q-UI-141).
	sharedTimeout = 10 * time.Second
	// sharedCacheBytes bounds the files a replica keeps in memory after it read them from the store. A file of a hashed
	// name never changes its code, so a kept copy is never stale; the files of one release's bundle are about 5 MiB in
	// both locales.
	sharedCacheBytes = 32 << 20
	// sharedMissTTL and sharedMissEntries bound the memory of names the store did not know: a burst of requests for one
	// unknown name costs one read of the store in the TTL, and a replica of a release that publishes later is found
	// within it.
	sharedMissTTL     = 10 * time.Second
	sharedMissEntries = 4096
)

// sharedCache is a replica's memory of the shared store: the files it read (bounded, the oldest first out), the names
// the store did not know (bounded, for sharedMissTTL), and the reads in flight, so that concurrent requests for one file
// read the store once.
type sharedCache struct {
	store Shared
	now   func() time.Time

	mu     sync.Mutex
	files  map[string][]byte
	order  []string
	bytes  int
	misses map[string]time.Time
	flight map[string]*sharedFlight
}

type sharedFlight struct {
	done chan struct{}
	data []byte
	err  error
}

func newSharedCache(store Shared) *sharedCache {
	return &sharedCache{
		store: store, now: time.Now,
		files: map[string][]byte{}, misses: map[string]time.Time{}, flight: map[string]*sharedFlight{},
	}
}

// get returns the file from memory or reads the store once for every request that waits for it.
func (c *sharedCache) get(ctx context.Context, locale, path string) ([]byte, error) {
	key := locale + "/" + path
	c.mu.Lock()
	if data, ok := c.files[key]; ok {
		c.mu.Unlock()
		return data, nil
	}
	if until, ok := c.misses[key]; ok {
		if c.now().Before(until) {
			c.mu.Unlock()
			return nil, ErrNotShared
		}
		delete(c.misses, key)
	}
	f, running := c.flight[key]
	if !running {
		f = &sharedFlight{done: make(chan struct{})}
		c.flight[key] = f
	}
	c.mu.Unlock()

	if running {
		select {
		case <-f.done:
			return f.data, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// The read does not end with the first request's context: the requests that wait for it would fail with it.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedTimeout)
	f.data, f.err = c.store.Asset(rctx, locale, path)
	cancel()
	c.mu.Lock()
	delete(c.flight, key)
	switch {
	case f.err == nil:
		c.keep(key, f.data)
	case errors.Is(f.err, ErrNotShared):
		if len(c.misses) >= sharedMissEntries {
			clear(c.misses)
		}
		c.misses[key] = c.now().Add(sharedMissTTL)
	}
	c.mu.Unlock()
	close(f.done)
	return f.data, f.err
}

// keep adds a file and drops the oldest ones over the bound; c.mu is held. A file larger than the bound is served and
// not kept.
func (c *sharedCache) keep(key string, data []byte) {
	if len(data) > sharedCacheBytes {
		return
	}
	for c.bytes+len(data) > sharedCacheBytes && len(c.order) > 0 {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= len(c.files[old])
		delete(c.files, old)
	}
	c.files[key] = data
	c.order = append(c.order, key)
	c.bytes += len(data)
}

// serveShared answers a hashed file that the replica's own bundle lacks from the shared store (R-HA-020, Q-HA-120,
// Q-UI-141): the file with the headers of a file of the bundle; 404 when no release published it; 503 with Retry-After
// when the store could not be asked, never cached, because the file may well exist.
func (h *Handler) serveShared(w http.ResponseWriter, r *http.Request, locale, path string) {
	data, err := h.shared.get(r.Context(), locale, path)
	switch {
	case errors.Is(err, ErrNotShared):
		problem(w, http.StatusNotFound, "Not Found")
		return
	case err != nil:
		// a client that went away while it waited for the read is no failure of the store
		if h.onSharedError != nil && r.Context().Err() == nil {
			h.onSharedError(locale, path, err)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "1")
		problem(w, http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	serveFile(w, r, path, bytes.NewReader(data))
}
