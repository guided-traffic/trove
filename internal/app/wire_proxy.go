package app

import (
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// The wiring of the pull-through proxy (milestone M2). The upstream clients of
// the remote repositories are built here; the engine that uses them is wired
// inside its own marker below.

// wireProxy builds the proxy components of a replica at step 8 of the bootstrap,
// after the repository snapshot and service it reads. Nothing is built on a
// replica without PostgreSQL or the change log (a unit-test build).
func (a *App) wireProxy() error {
	db := a.database()
	a.mu.Lock()
	caches, svc := a.caches, a.m1.repositoryService
	a.mu.Unlock()
	if db == nil || caches == nil || svc == nil || a.ring == nil {
		return nil
	}
	log := a.log.With("component", "proxy")

	// >>> M2 upstream
	clients, err := upstream.New(upstream.Options{
		Factory: a.out, Store: repository.NewSecretStore(db), Ring: a.ring, Caches: caches,
		Proxy: a.cfg.Proxy, SecretsDir: a.cfg.ConfigAsCode.SecretsDir, SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL, Log: log,
	})
	if err != nil {
		return err
	}
	// The service seals the inline secrets of a remote section and probes the upstream
	// for the upstream-test operations (R-REPO-024, R-REPO-033).
	svc.Bind(repository.RemoteDeps{Ring: a.ring, Prober: clients})
	// <<< M2 upstream
	return nil
}
