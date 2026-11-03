package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/vuln"
)

// PagesSafetyTTL is the safety TTL of the cache vuln_nuget_page: the bound on how long a replica that missed every change
// message keeps a rendered page (R-ARCH-018). The page's key moves with the epochs, so a missed message leaves the old page
// unreachable as soon as the replica's applied epochs move, and a page of the current key is correct.
const PagesSafetyTTL = 15 * time.Minute

// pagesMaxEntries bounds the cache: one page per ecosystem that has one, with room for a second render version.
const pagesMaxEntries = 4

// Epochs gives the applied epoch of a change area (cluster.Changes implements it).
type Epochs interface {
	Applied(area string) int64
}

// Pages is the cache vuln_nuget_page (vuln.CacheNuGetPage): the rendered NuGet vulnerability page of R-VULN-018, kept per
// replica under a key made of the ecosystem's data epoch and the applied epochs of the claims and repo_config areas, which
// are the page's inputs (the advisories, the claims that make a name internal, and the repository settings that make a
// claim count). A change of any of the three areas flushes the cache, and a fill that raced a change is not inserted
// (the fill rule of R-ARCH-018, cache.Cache.Load). It implements formathost.PageCache.
type Pages struct {
	c      *cache.Cache[string, []byte]
	states vuln.States
	epochs Epochs
}

// NewPages registers the cache on the registry.
func NewPages(reg *cache.Registry, states vuln.States, epochs Epochs) (*Pages, error) {
	if reg == nil || epochs == nil {
		return nil, errors.New("vuln store: the page cache needs a cache registry and the change epochs")
	}
	c := cache.Register(reg, vuln.CacheNuGetPage, cache.Options[string, []byte]{
		MaxEntries: pagesMaxEntries, SafetyTTL: PagesSafetyTTL,
		Areas: []string{cluster.AreaVulnData, cluster.AreaClaims, cluster.AreaRepoConfig},
	})
	return &Pages{c: c, states: states, epochs: epochs}, nil
}

// Key is the page key of an ecosystem: "<data epoch>.<claims epoch>.<repo_config epoch>". Epochs only rise, so the key of a
// page differs whenever one of its inputs changed.
func (p *Pages) Key(ecosystem string) string {
	var data int64 // the source states are not wired: no data, epoch 0
	if p.states != nil {
		data = p.states.State(ecosystem).Epoch
	}
	return strconv.FormatInt(data, 10) + "." +
		strconv.FormatInt(p.epochs.Applied(cluster.AreaClaims), 10) + "." +
		strconv.FormatInt(p.epochs.Applied(cluster.AreaRepoConfig), 10)
}

// Page returns the page cached under key, or renders it and caches it under the fill rule. The key says which page: a
// format passes its render version in front, so that two renderings never share an entry.
func (p *Pages) Page(ctx context.Context, key string, render func(context.Context) ([]byte, error)) ([]byte, error) {
	return p.c.Load(ctx, key, render)
}
