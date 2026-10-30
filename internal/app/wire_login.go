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

// The login area (WP1.1) is wired in two steps of the bootstrap, both on the
// replicas that serve the client listener (web and all): a worker builds
// neither, serves no login and reads no login Secret; its session-cleanup task
// is a registered job kind.
//
// wireSessions runs before the identity area: the session store is the
// session.Validator of the authentication stage (R-AUTH-001) and reads
// auth.revocation_bound and auth.tokens.cache_ttl, which the identity area owns.
// wireLogin runs after it: the OIDC and break-glass login calls the identity
// area's provisioner, takes the limiter auth.flow_starts it registered
// (R-AUTH-038) and starts its background loops (R-AUTH-042).
func (a *App) wireSessions() error {
	if !a.sub.ClientListener {
		return nil
	}
	cfg := a.cfg
	metrics, err := a.authMetrics()
	if err != nil {
		return err
	}
	store, err := session.New(session.Options{
		DB: a.database(), Changes: a.Changes(), Fresh: a.Changes(), Caches: a.Caches(), Audit: a.auditRecorder(),
		Log: a.log, Metrics: a.tele.metrics.Registry(), AuthMetrics: metrics,
		Sessions: cfg.Auth.Sessions, BreakGlass: cfg.Auth.BreakGlass,
		RevocationBound: cfg.Auth.RevocationBound, CacheTTL: cfg.Auth.Tokens.CacheTTL,
		Origin: requestOrigin, Source: eventSource,
	})
	if err != nil {
		return err
	}
	a.m1.sessions = store
	return nil
}

// wireLogin builds the OIDC and break-glass login on the session store.
func (a *App) wireLogin(ctx context.Context) error {
	if !a.sub.ClientListener || a.m1.sessions == nil {
		return nil
	}
	cfg := a.cfg
	var flowStarts oidc.Limiter
	if a.m1.flowStarts != nil {
		flowStarts = takeLimiter{a.m1.flowStarts}
	}
	svc, err := oidc.New(ctx, oidc.Options{
		DB: a.database(), Sessions: a.m1.sessions, Provisioner: a.m1.provisioner, Audit: a.auditRecorder(),
		Clients: func(purpose string) *http.Client {
			return a.out.Client(purpose, outbound.WithTimeout(10*time.Second))
		},
		Log: a.log, Metrics: a.m1.authMetrics,
		BaseURL: cfg.Server.BaseURL, Auth: cfg.Auth,
		Limiter: flowStarts, MaxPendingFlows: cfg.Auth.RateLimit.MaxPendingFlows,
	})
	if err != nil {
		return err
	}
	if err := a.startComponent(ctx, svc); err != nil {
		return err
	}
	a.m1.login = svc
	return nil
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
	login.New(a.m1.login, a.m1.sessions, login.Options{}).Register(router)
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
