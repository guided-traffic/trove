package app

import (
	"context"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/api/login"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/outbound"
)

// The login area (WP1.1) is two areas, both on the replicas that serve the client
// listener (web and all): a worker enables neither, serves no login and reads no
// login Secret; its session-cleanup task is a registered job kind.
//
// wireSessions comes before the auth area: the session store is the
// session.Validator of the authentication stage (R-AUTH-001) and reads
// auth.revocation_bound and auth.tokens.cache_ttl, which the identity area owns.
// wireLogin comes after the identity area: the OIDC and break-glass login calls
// its provisioner, takes the limiter auth.flow_starts of the limits area
// (R-AUTH-038) and starts its background loops (R-AUTH-042).
func (a *App) wireSessions(_ context.Context, in infra, id identityArea) (sessionsArea, error) {
	cfg := a.cfg
	store, err := session.New(session.Options{
		DB: in.db, Changes: in.changes, Fresh: in.changes, Caches: in.caches, Audit: in.rec,
		Log: a.log, Metrics: a.tele.metrics.Registry(), AuthMetrics: id.metrics,
		Sessions: cfg.Auth.Sessions, BreakGlass: cfg.Auth.BreakGlass,
		RevocationBound: cfg.Auth.RevocationBound, CacheTTL: cfg.Auth.Tokens.CacheTTL,
		Origin: requestOrigin, Source: eventSource,
	})
	if err != nil {
		return sessionsArea{}, err
	}
	return sessionsArea{store: store}, nil
}

// wireLogin builds the OIDC and break-glass login on the session store and the
// handler of its browser routes, which every endpoint's router mounts.
func (a *App) wireLogin(ctx context.Context, in infra, id identityArea, ss sessionsArea, lim limitsArea) (loginArea, error) {
	if err := requireInputs(AreaLogin, given("provisioner", id.provisioner), given("session store", ss.store), given("flow-start limiter", lim.flowStarts)); err != nil {
		return loginArea{}, err
	}
	cfg := a.cfg
	svc, err := oidc.New(ctx, oidc.Options{
		DB: in.db, Sessions: ss.store, Provisioner: id.provisioner, Audit: in.rec,
		Clients: func(purpose string) *http.Client {
			return in.out.Client(purpose, outbound.WithTimeout(10*time.Second))
		},
		Log: a.log, Metrics: id.metrics,
		BaseURL: cfg.Server.BaseURL, Auth: cfg.Auth,
		Limiter: takeLimiter{lim.flowStarts}, MaxPendingFlows: cfg.Auth.RateLimit.MaxPendingFlows,
	})
	if err != nil {
		return loginArea{}, err
	}
	if err := a.startComponent(ctx, svc); err != nil {
		return loginArea{}, err
	}
	return loginArea{service: svc, handler: login.New(svc, ss.store, login.Options{})}, nil
}

// takeLimiter adapts the cluster limiter in the take form (it counts every
// request, admitted or not) to oidc.Limiter.
type takeLimiter struct{ l *cluster.Limiter }

func (t takeLimiter) Allow(ctx context.Context, bucket string) (bool, time.Duration) {
	v := t.l.Take(ctx, bucket)
	return v.Allowed, v.RetryAfter
}

// registerLogin puts the browser routes of the login on an endpoint's router
// (R-AUTH-005, R-AUTH-006, R-AUTH-013, R-AUTH-014). They belong to the ui
// family, so an endpoint whose surface lacks ui drops them (R-EP-005).
func (a *App) registerLogin(router *httpserver.Router) {
	part[loginArea](a.parts).handler.Register(router)
}

// requestOrigin is the origin of the base URL the chain resolved for the
// request's endpoint (R-AUTH-012).
func requestOrigin(r *http.Request) string {
	return session.NormalizeOrigin(httpserver.InfoFrom(r.Context()).BaseURL)
}

// eventSource is the source of an audit event (R-AUTH-041).
func eventSource(r *http.Request) audit.Source {
	return httpserver.SourceFrom(r.Context())
}
