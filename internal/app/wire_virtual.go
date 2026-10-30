package app

import (
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// wireVirtual builds the routing of virtual repositories (R-REPO-023,
// docs/developer/virtual.md) on the repository snapshot that wireRepository has
// just built: the claim index, registered on the cache registry and invalidated
// by the change area claims, and the auto claim of a first publish. It is called
// by wireRepository, because it needs the concrete snapshot reader and the same
// infrastructure.
func (a *App) wireVirtual(db meta.Runner, reader *repository.SnapshotReader, caches *cache.Registry, changes *cluster.Changes, rec audit.Recorder) error {
	rt, err := repository.NewRouting(repository.RoutingOptions{
		Runner: db, Reader: reader, Catalog: catalog.Default(), Caches: caches, Notifier: changes, Recorder: rec,
		RemoteRefusal: func() bool { return a.cfg.Proxy.Claims.RemoteRefusal },
		SafetyTTL:     a.cfg.Cache.SnapshotSafetyTTL,
		Log:           a.log.With("component", "routing"),
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.m1.routing = rt
	a.mu.Unlock()
	return nil
}
