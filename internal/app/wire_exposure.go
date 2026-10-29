package app

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The wiring of the endpoints area (WP1.9): the exposure ceiling the decision
// point applies (R-EP-013), the exposure-rule service of the management API
// (R-EP-016), the per-endpoint stage 7 with the endpoint's rate-limit class
// (R-EP-015), and the counting and audit of credentials an endpoint refuses
// (R-EP-009, R-EP-010).

// exposureCeiling returns the endpoint exposure, built once from the configured
// endpoints and their exposure modes. The authz wiring calls it at step 8 before
// the endpoints marker; the value is set before the client listener starts.
func (a *App) exposureCeiling() *authz.ExposureCeiling {
	if a.m1.exposureCeiling == nil {
		modes := map[string]string{}
		for _, e := range a.cfg.Server.ResolveEndpoints() {
			modes[e.Name] = e.Exposure
		}
		a.m1.exposureCeiling = authz.NewExposureCeiling(modes, nil)
	}
	return a.m1.exposureCeiling
}

// wireEndpoints runs at the endpoints marker of step 8: the rule service on a
// replica with PostgreSQL, the start-up warning for inert rules (H-58), and the
// rate-limit classes of the endpoints this process serves. Nothing here can
// fail: the limiter set exists from the identity step on every process with a
// client listener (wireFailureLimiter), and a failed count read only loses the
// warning.
func (a *App) wireEndpoints(ctx context.Context) {
	db, changes := a.database(), a.changesOrNil()
	if db != nil && changes != nil {
		a.m1.exposure = authz.NewExposureService(authz.ExposureServiceOptions{
			DB: db, Changes: changes, Recorder: a.auditRecorder(), Ceiling: a.exposureCeiling(),
		})
		a.warnInertEndpoints(ctx)
	}
	if a.sub.ClientListener && a.m1.limiters != nil {
		a.wireEndpointLimits(ctx, a.m1.limiters)
	}
}

// warnInertEndpoints logs one warning per endpoint name that holds rules but is
// not configured: its rules are stored and have no effect, and they would apply
// at once to an endpoint configured later under that name (R-EP-012, H-58).
func (a *App) warnInertEndpoints(ctx context.Context) {
	counts, err := a.m1.exposure.Counts(ctx)
	if err != nil {
		a.log.Warn("exposure rules: the rule counts could not be read at start-up", "err", err)
		return
	}
	for name, c := range counts {
		if !a.m1.exposure.Configured(name) {
			a.log.Warn("exposure rules of an endpoint that is not configured are inert; review or delete them before configuring an endpoint of that name",
				"endpoint", name, "rules", c.Rules)
		}
	}
}

// wireEndpointLimits builds one cluster limiter per rate-limit class that an
// endpoint of this process names (endpoint_<class>, R-EP-015) and the stage of
// each such endpoint. While Valkey is unavailable the per-replica share divides
// by the live replicas that serve this process's endpoints: the replicas of its
// pool, read from the replica registry.
func (a *App) wireEndpointLimits(ctx context.Context, ls *cluster.LimiterSet) {
	share := a.startServingReplicaCount(ctx)
	limiters := map[string]*cluster.Limiter{}
	a.m1.endpointLimits = map[string]func(http.Handler) http.Handler{}
	for _, rt := range a.eps {
		class := rt.cfg.RateLimitClass
		if class == "" {
			continue
		}
		l, ok := a.cfg.Server.EndpointRateLimits[class]
		if !ok {
			continue // configuration validation refuses a class without an entry
		}
		lim := limiters[class]
		if lim == nil {
			lim = ls.NewLimiterShared(v1.EndpointLimiterName(class), cluster.ScopeCluster, l.RequestsPerSecond, l.Burst, share.Get)
			limiters[class] = lim
		}
		a.m1.endpointLimits[rt.cfg.Name] = v1.NewEndpointRateLimit(v1.EndpointRateLimitOptions{
			Endpoint: rt.cfg.Name, Class: class, Limit: l, Limiter: lim,
		})
	}
}

// servingReplicas is the n of the endpoint classes' fallback share.
type servingReplicas struct{ n atomic.Int32 }

func (s *servingReplicas) Get() int { return max(1, int(s.n.Load())) }

// startServingReplicaCount refreshes the number of live replicas that serve this
// process's endpoints every heartbeat interval: web replicas of the same pool
// and replicas of the role all (EndpointsCountServingReplicas). A failed read
// keeps the last value; it is 1 until the first read.
func (a *App) startServingReplicaCount(ctx context.Context) *servingReplicas {
	s := &servingReplicas{}
	db := a.database()
	if db == nil {
		return s
	}
	stale := a.cfg.System.HeartbeatStaleAfter.Seconds()
	refresh := func() {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_ = db.Read(rctx, func(c q.DBTX) error {
			n, err := q.New(c).EndpointsCountServingReplicas(rctx, q.EndpointsCountServingReplicasParams{StaleSecs: stale, Pool: a.id.Pool})
			if err == nil {
				s.n.Store(n)
			}
			return err
		})
	}
	refresh()
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		t := time.NewTicker(a.cfg.System.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
	return s
}

// endpointStage is stage 7 of an endpoint's chain (R-ARCH-005): it puts the
// endpoint's name into the request context for the decision point, so every
// Decide and Readable of the request applies that endpoint's exposure whatever
// a coordinate names (R-EP-013, R-EP-014); then the endpoint's rate-limit class
// (R-EP-015); then the stage of the management API (CORS, the classes of
// R-API-016). The parts are read when the chain is built, after step 8.
func (a *App) endpointStage(rt *endpointRuntime) func(http.Handler) http.Handler {
	api, limit, name := a.m1.rateLimit, a.m1.endpointLimits[rt.cfg.Name], rt.cfg.Name
	return func(next http.Handler) http.Handler {
		h := next
		if api != nil {
			h = api(h)
		}
		if limit != nil {
			h = limit(h)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(authz.WithEndpoint(r.Context(), name)))
		})
	}
}

// credentialRefused counts a request the endpoint's credential rules refused
// (trove_endpoint_credentials_refused_total) and records a refused token as
// token.rejected with reason endpoint_not_accepted (R-EP-009, R-EP-010). The
// audit writer deduplicates token.rejected per token, or per client address for
// a token refused by its type before any lookup (R-OPS-015).
func (a *App) credentialRefused(rt *endpointRuntime) func(*http.Request, auth.Refusal) {
	name := rt.cfg.Name
	return func(r *http.Request, f auth.Refusal) {
		a.tele.metrics.EndpointCredentialsRefused(name, f.Reason)
		if f.Reason == auth.RefusedAnonymous {
			return
		}
		details, _ := json.Marshal(map[string]string{"reason": "endpoint_not_accepted", "refusal": f.Reason, "tokenType": string(f.TokenType)})
		ctx := r.Context()
		a.auditRecorder().Record(ctx, audit.Event{
			Type: audit.EventTokenRejected, Outcome: audit.Failure, Reason: "endpoint_not_accepted",
			Actor: audit.Actor{Kind: "anonymous"}, Endpoint: name,
			Source: audit.Source{
				ClientAddr: httpserver.InfoFrom(ctx).Client.String(), UserAgent: r.UserAgent(), TraceID: httpserver.RequestID(ctx),
			},
			Subject: audit.Subject{Kind: "token", ID: f.TokenID},
			Details: details,
		})
	}
}

// Authorizer returns the decision point of the replica, nil before step 8 or on
// a replica without PostgreSQL. The content routes of the files and format areas
// decide through it; the integration tests of the endpoints area use it to stand
// in for a content route.
func (a *App) Authorizer() authz.Authorizer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m1.authorizer
}

// endpointServices are the services of the endpoint operations.
func (a *App) endpointServices() v1.EndpointServices {
	return v1.EndpointServices{
		Endpoints: a.cfg.Server.ResolveEndpoints(), RateLimits: a.cfg.Server.EndpointRateLimits,
		Exposure: a.m1.exposure, Authorizer: a.m1.authorizer,
	}
}
