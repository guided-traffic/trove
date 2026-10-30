package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// AreaProxy is the upstream clients of the remote repositories.
const AreaProxy Area = "proxy"

// proxyArea holds the upstream clients of the remote repositories (milestone M2):
// one client per remote, built from its settings and the secrets stored with it.
type proxyArea struct{ clients *upstream.Clients }

// wireProxy builds the upstream clients and binds the remote settings of the
// repository service: it seals the inline secrets of a remote section, probes the
// upstream for the upstream-test operations (R-REPO-024, R-REPO-033) and records
// the scope-all invalidation of R-REPO-011 in the transaction of an update that
// changed what the cached answers are evidence of (the invalidation area). The
// engine that uses the clients is wired by its own area.
func (a *App) wireProxy(_ context.Context, in infra, repo repositoryArea, inv invalidationArea) (proxyArea, error) {
	if err := requireInputs(AreaProxy, given("repository service", repo.service), given("invalidation service", inv.svc)); err != nil {
		return proxyArea{}, err
	}
	clients, err := upstream.New(upstream.Options{
		Factory: in.out, Store: repository.NewSecretStore(in.db), Ring: in.ring, Caches: in.caches,
		Proxy: a.cfg.Proxy, SecretsDir: a.cfg.ConfigAsCode.SecretsDir, SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL,
		Log: a.log.With("component", "proxy"),
	})
	if err != nil {
		return proxyArea{}, err
	}
	repo.service.Bind(repository.RemoteDeps{
		Ring: in.ring, Prober: clients,
		OnUpstreamChanged: func(ctx context.Context, tx meta.Tx, c repository.Caller, r repository.Repository) error {
			_, err := inv.svc.InvalidateTx(ctx, tx, c, r, "", invalidation.ScopeAll)
			return err
		},
	})
	return proxyArea{clients: clients}, nil
}
