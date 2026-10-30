package app

import (
	"context"
	"net/http"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats/catalog"
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

// wireAuthz is the authz area: the decision point (R-AUTHZ-028) and the policy
// service. The grant index is built from PostgreSQL before the client listener
// opens, and the engine follows the change areas authz and repo_config. Valkey
// is not an input; the engine reaches it only through the change log.
func (a *App) wireAuthz(ctx context.Context, in infra, repo repositoryArea, id identityArea, ex exposureArea) (authzArea, error) {
	if err := requireInputs(AreaAuthz, given("repository reader", repo.reader), given("principal store", id.principals),
		given("anonymous-access switch", id.anonymousAccess), given("exposure ceiling", ex.ceiling)); err != nil {
		return authzArea{}, err
	}
	cfg := a.cfg
	eng, err := authz.New(authz.Options{
		Source:            authz.NewPostgresSource(in.db, cfg.Authz.Cache.MaxStaleness),
		Changes:           in.changes,
		Config:            cfg.Authz,
		Audit:             cfg.Audit,
		EpochPollInterval: cfg.Cluster.EpochPollInterval,
		RetryBudget:       cfg.Database.Retry.MaxElapsed,
		AnonymousAccess:   id.anonymousAccess,
		Repositories:      repo.reader,       // the lookup and catch-up of a repository the index lacks (R-API-052)
		Ceiling:           ex.ceiling,        // the exposure area (R-EP-013, wire_exposure.go)
		Formats:           catalog.Default(), // the name rule that folds the names and patterns of a format (R-AUTHZ-005)
		Recorder:          in.rec,
		Replica:           a.id.Instance,
		Log:               a.log.With("component", "authz"),
		Metrics:           a.tele.metrics.Registry(),
	})
	if err != nil {
		return authzArea{}, err
	}
	if err := a.startComponent(ctx, eng); err != nil {
		return authzArea{}, err
	}
	svc := authz.NewService(authz.ServiceOptions{
		DB: in.db, Changes: in.changes, Principals: id.principals, Repositories: repo.reader, Recorder: in.rec,
		Formats: catalog.Default(),
	})
	return authzArea{engine: eng, service: svc}, nil
}

// decider returns the decision point the request paths decide through, nil on a
// replica without the authz area (the shell profile). The nil is explicit
// because a nil *authz.Engine in the interface would not be nil.
func (a *App) decider() authz.Decider {
	e := part[authzArea](a.parts).engine
	if e == nil {
		return nil
	}
	return e
}

// authzServices are the services the permission operations of the management API
// use; a member is nil on a replica without the authz area.
func (a *App) authzServices() v1.AuthzServices {
	az := part[authzArea](a.parts)
	return v1.AuthzServices{Policies: az.service, Engine: az.engine, Owners: part[ownershipArea](a.parts).service}
}
