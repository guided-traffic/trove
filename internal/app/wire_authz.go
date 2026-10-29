package app

import (
	"context"
	"net/http"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// withMinEpoch wraps the authentication stage: once a request authenticated, the
// header Trove-Authz-Min-Epoch is read into the principal (R-AUTHZ-032). The
// order matters: a request with an invalid credential got its 401 from the stage
// before the header is looked at, and an anonymous request ignores the header, so
// an anonymous caller cannot probe the epoch.
func withMinEpoch(next func(*http.Request) (httpserver.Principal, error)) func(*http.Request) (httpserver.Principal, error) {
	return func(r *http.Request) (httpserver.Principal, error) {
		p, err := next(r)
		if err != nil || p.Anonymous {
			return p, err
		}
		n, err := authz.ParseMinEpoch(r.Header.Get(authz.HeaderMinEpoch))
		if err != nil {
			return httpserver.Principal{}, err
		}
		p.MinEpoch = n
		return p, nil
	}
}

// wireAuthz builds the decision point (R-AUTHZ-028): the grant index is built
// from PostgreSQL before the client listener opens, and the engine follows the
// change areas authz and repo_config. Valkey is not an input; the engine reaches
// it only through the change log. A replica without PostgreSQL (the unit tests of
// the composition root) wires nothing and nobody holds a permission.
func (a *App) wireAuthz(ctx context.Context) error {
	db := a.database()
	if db == nil || a.changes == nil {
		return nil
	}
	cfg := a.cfg
	eng, err := authz.New(authz.Options{
		Source:            authz.NewPostgresSource(db, cfg.Authz.Cache.MaxStaleness),
		Changes:           a.changes,
		Config:            cfg.Authz,
		Audit:             cfg.Audit,
		EpochPollInterval: cfg.Cluster.EpochPollInterval,
		RetryBudget:       cfg.Database.Retry.MaxElapsed,
		AnonymousAccess:   a.m1.anonymousAccess,
		Ceiling:           a.exposureCeiling(), // the endpoints area (R-EP-013, wire_exposure.go)
		Recorder:          a.m1.recorder,
		Replica:           a.id.Instance,
		Log:               a.log.With("component", "authz"),
		Metrics:           a.tele.metrics.Registry(),
	})
	if err != nil {
		return err
	}
	if err := a.startComponent(ctx, eng); err != nil {
		return err
	}
	a.m1.authorizer, a.m1.authzEngine = eng, eng
	if a.m1.principals != nil && a.m1.repositories != nil {
		// The recorder is the one of step 4; a shell without infrastructure has none.
		var rec audit.Recorder = audit.Nop{}
		if a.m1.recorder != nil {
			rec = a.m1.recorder
		}
		svc := authz.NewService(authz.ServiceOptions{
			DB: db, Changes: a.changes, Principals: a.m1.principals, Repositories: a.m1.repositories, Recorder: rec,
		})
		a.mu.Lock()
		a.m1.authzService = svc
		a.mu.Unlock()
	}
	return nil
}

// authzServices are the services the permission operations of the management API
// use; a member is nil while the authz wiring has not run.
func (a *App) authzServices() v1.AuthzServices {
	a.mu.Lock()
	defer a.mu.Unlock()
	return v1.AuthzServices{Policies: a.m1.authzService, Engine: a.m1.authzEngine}
}
