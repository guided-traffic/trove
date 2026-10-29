package observability

import (
	"path"
	"sync"
)

// OtherLabel is the value of every key beyond the cap (R-OPS-007).
const OtherLabel = "_other"

// CappedLabels decides which repository (or subscription) keys get their own
// series. The decision is a pure function of the snapshot, so every replica
// that holds the same snapshot decides the same (Q-OPS-10): keys matching the
// include patterns first, in pattern order, then the others in the snapshot
// order (creation order), up to the limit.
type CappedLabels struct {
	m       *Metrics
	label   string
	limit   int
	include []string

	mu  sync.RWMutex
	set map[string]struct{}
}

func newCappedLabels(m *Metrics, label string, limit int, include []string) *CappedLabels {
	return &CappedLabels{m: m, label: label, limit: limit, include: include, set: map[string]struct{}{}}
}

// Update installs a new snapshot: all keys in creation order. Call it whenever
// the repository snapshot changes.
func (c *CappedLabels) Update(keys []string) {
	chosen := make(map[string]struct{}, min(c.limit, len(keys)))
	order := make([]string, 0, min(c.limit, len(keys)))
	add := func(k string) {
		if len(order) >= c.limit {
			return
		}
		if _, dup := chosen[k]; dup {
			return
		}
		chosen[k] = struct{}{}
		order = append(order, k)
	}
	for _, pat := range c.include {
		for _, k := range keys {
			if ok, _ := path.Match(pat, k); ok {
				add(k)
			}
		}
	}
	for _, k := range keys {
		add(k)
	}
	c.mu.Lock()
	c.set = chosen
	c.mu.Unlock()
}

// Value returns the label value for key in the family: the key itself when it
// has a series, otherwise "_other" and one count on
// trove_metrics_label_overflow_total{family,label}.
func (c *CappedLabels) Value(family, key string) string {
	c.mu.RLock()
	_, ok := c.set[key]
	c.mu.RUnlock()
	if ok {
		return key
	}
	c.m.overflow.WithLabelValues(family, c.label).Inc()
	return OtherLabel
}
