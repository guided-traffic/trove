package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// AreaVirtual is the routing of virtual repositories and their claims.
const AreaVirtual Area = "virtual"

// virtualArea is the caller-independent routing of virtual repositories (members,
// pin rules, claims; R-REPO-019 steps 2 and 3, R-REPO-023). The virtual engine and
// the formats' publish transaction take it.
type virtualArea struct{ routing *repository.Routing }

// wireVirtual builds the routing on the repository snapshot that the repository
// area built (R-REPO-023, docs/developer/virtual.md): the claim index, registered
// on the cache registry and invalidated by the change area claims, and the auto
// claim of a first publish. It needs the concrete snapshot reader, so the
// repository area hands it on.
func (a *App) wireVirtual(_ context.Context, in infra, repo repositoryArea) (virtualArea, error) {
	if err := requireInputs(AreaVirtual, given("repository snapshot", repo.snapshot)); err != nil {
		return virtualArea{}, err
	}
	rt, err := repository.NewRouting(repository.RoutingOptions{
		Runner: in.db, Reader: repo.snapshot, Catalog: catalog.Default(), Caches: in.caches, Notifier: in.changes, Recorder: in.rec,
		RemoteRefusal: func() bool { return a.cfg.Proxy.Claims.RemoteRefusal },
		SafetyTTL:     a.cfg.Cache.SnapshotSafetyTTL,
		Log:           a.log.With("component", "routing"),
	})
	if err != nil {
		return virtualArea{}, err
	}
	return virtualArea{routing: rt}, nil
}
