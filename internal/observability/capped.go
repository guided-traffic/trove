package observability

import (
	"sync"

	"github.com/guided-traffic/trove/internal/qname"
)

// OtherLabel is the value of every key beyond the cap (R-OPS-007).
const OtherLabel = "_other"

// RepoLabel is one repository of the snapshot as the label cap reads it: the qualified name the series is
// labelled with, and what the include selectors match against.
type RepoLabel struct {
	// Name is the qualified name "<project>/<key>" (Q-REPO-212), the label value.
	Name          string
	Project, Key  string
	Class, Format string
}

// CappedLabels decides which repository (or subscription) keys get their own
// series. A repository key is the qualified name qname.Repo.String(), "<project>/<key>"
// (Q-REPO-212). The include list holds selectors of an instance context (R-AUTHZ-004): a
// key, a pattern of project and key globs with an optional format, or a class such as
// @any-remote with an optional format, matched against the snapshot's repositories. The
// decision is a pure function of the snapshot, so every replica that holds the same snapshot
// decides the same (Q-OPS-10): repositories matching the include selectors first, in selector
// order, then the others in the snapshot order (creation order), up to the limit.
type CappedLabels struct {
	m       *Metrics
	label   string
	limit   int
	include []qname.Selector

	mu     sync.RWMutex
	set    map[string]struct{}
	known  map[string]struct{} // every name of the last snapshot, chosen or not
	forget []func(name string)
}

// newCappedLabels builds the cap. An include entry that is no selector of an instance context
// is dropped: the configuration refuses it at startup (config.Metrics.Validate).
func newCappedLabels(m *Metrics, label string, limit int, include []string) *CappedLabels {
	c := &CappedLabels{m: m, label: label, limit: limit, set: map[string]struct{}{}}
	for _, text := range include {
		if sel, err := qname.ParseSelector(text, qname.Context{}); err == nil {
			c.include = append(c.include, sel)
		}
	}
	return c
}

// Update installs a new snapshot of keys that no selector reads (the subscriptions): all keys in
// creation order.
func (c *CappedLabels) Update(keys []string) {
	repos := make([]RepoLabel, len(keys))
	for i, k := range keys {
		repos[i] = RepoLabel{Name: k}
	}
	c.install(repos)
}

// UpdateRepositories installs a new snapshot: all repositories in creation order. Call it whenever
// the repository snapshot is rebuilt.
func (c *CappedLabels) UpdateRepositories(repos []RepoLabel) { c.install(repos) }

// OnForget registers fn to be called, once per name, with each repository name that was in the previous snapshot and is not
// in the new one: a deleted repository, or a moved one under its old name. The owner of a family with a repo label deletes the
// series of that name, so that a gauge such as trove_remote_upstream_up does not keep reporting a repository that no longer
// exists (R-OPS-007). The functions run in the caller of UpdateRepositories and must not block.
func (c *CappedLabels) OnForget(fn func(name string)) {
	c.mu.Lock()
	c.forget = append(c.forget, fn)
	c.mu.Unlock()
}

func (c *CappedLabels) install(repos []RepoLabel) {
	chosen := make(map[string]struct{}, min(c.limit, len(repos)))
	add := func(k string) {
		if len(chosen) >= c.limit {
			return
		}
		chosen[k] = struct{}{}
	}
	for _, sel := range c.include {
		for _, r := range repos {
			if selects(sel, r) {
				add(r.Name)
			}
		}
	}
	for _, r := range repos {
		add(r.Name)
	}
	known := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		known[r.Name] = struct{}{}
	}
	c.mu.Lock()
	c.set = chosen
	previous := c.known
	c.known = known
	forget := c.forget
	c.mu.Unlock()
	for name := range previous {
		if _, ok := known[name]; ok {
			continue
		}
		for _, fn := range forget {
			fn(name)
		}
	}
}

// selects reports whether the selector of an instance context covers the repository.
func selects(sel qname.Selector, r RepoLabel) bool {
	if sel.Format != "" && sel.Format != r.Format {
		return false
	}
	switch sel.Kind {
	case qname.SelectorKey:
		return sel.Project == r.Project && sel.Key == r.Key
	case qname.SelectorPattern:
		return qname.Glob(sel.ProjectGlob, r.Project) && qname.Glob(sel.KeyGlob, r.Key)
	case qname.SelectorClass:
		return sel.Class == "any" || sel.Class == r.Class
	}
	return false
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
