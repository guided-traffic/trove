package cache

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Flush reasons that the cache package itself raises. The cluster package adds
// change_log_gap, unknown_payload and epoch_regressed (R-ARCH-018 step 5).
const (
	ReasonAdmin  = "admin"
	ReasonChange = "change"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Epochs gives the applied epoch of a change area. cluster.Changes implements
// it. A nil Epochs makes every area report 0, which is what a replica without a
// change log (a unit test) sees.
type Epochs interface {
	Applied(area string) int64
}

// Registry holds the caches of one replica. The composition root creates one,
// hands it to the cluster's change log as its listener and passes it to every
// package that registers a cache. Tests create one per simulated replica.
type Registry struct {
	epochs  Epochs
	metrics *metrics

	mu     sync.RWMutex
	caches map[string]flusher
	order  []string
}

// flusher is the part of a cache that the registry drives, without its type
// parameters.
type flusher interface {
	areas() []string
	onChange(area string, version int, payload json.RawMessage)
	flush(reason string)
	stats() Stats
}

// NewRegistry creates a registry. epochs may be nil; reg may be nil for no
// metrics. A second registry on one prometheus.Registerer fails, so tests use a
// registerer per registry.
func NewRegistry(epochs Epochs, reg prometheus.Registerer) (*Registry, error) {
	m, err := newMetrics(reg)
	if err != nil {
		return nil, err
	}
	return &Registry{epochs: epochs, metrics: m, caches: map[string]flusher{}}, nil
}

// SetEpochs sets the epoch source after the change log exists. It must be
// called before any cache is filled.
func (r *Registry) SetEpochs(e Epochs) {
	r.mu.Lock()
	r.epochs = e
	r.mu.Unlock()
}

func (r *Registry) applied(area string) int64 {
	r.mu.RLock()
	e := r.epochs
	r.mu.RUnlock()
	if e == nil {
		return 0
	}
	return e.Applied(area)
}

func (r *Registry) add(name string, c flusher) {
	if !namePattern.MatchString(name) {
		panic(fmt.Sprintf("cache: invalid cache name %q", name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.caches[name]; dup {
		panic(fmt.Sprintf("cache: %q is registered twice", name))
	}
	r.caches[name] = c
	r.order = append(r.order, name)
	sort.Strings(r.order)
}

func (r *Registry) forArea(area string) []flusher {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []flusher
	for _, name := range r.order {
		c := r.caches[name]
		for _, a := range c.areas() {
			if a == area {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// ApplyChange hands a change of an area to the caches registered for it. It
// implements cluster.Listener. A cache without an OnChange hook is flushed
// whole, the coarse and always safe reaction. A version the cache does not
// understand is the caller's business: the cluster package flushes with
// FlushArea instead of calling this.
func (r *Registry) ApplyChange(area string, version int, payload json.RawMessage) {
	for _, c := range r.forArea(area) {
		c.onChange(area, version, payload)
	}
}

// FlushArea empties every cache registered for the area and runs their OnFlush
// hooks. It implements cluster.Listener.
func (r *Registry) FlushArea(area, reason string) {
	for _, c := range r.forArea(area) {
		c.flush(reason)
	}
}

// FlushAll empties every cache (the installation-wide flush, reason "admin").
func (r *Registry) FlushAll(reason string) {
	r.mu.RLock()
	all := make([]flusher, 0, len(r.caches))
	for _, name := range r.order {
		all = append(all, r.caches[name])
	}
	r.mu.RUnlock()
	for _, c := range all {
		c.flush(reason)
	}
}

// Stats is a snapshot of one cache.
type Stats struct {
	Name    string
	Entries int
	Bytes   int64
}

// Stats lists the caches by name.
func (r *Registry) Stats() []Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Stats, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.caches[name].stats())
	}
	return out
}
