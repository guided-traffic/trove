package app

import (
	"context"
	"encoding/json"
	"net/http"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The wiring of the endpoints area (WP1.9): the exposure ceiling the decision
// point applies (R-EP-013), the exposure-rule service of the management API
// (R-EP-016), the per-endpoint stage 7 with the endpoint's rate-limit class
// (R-EP-015), and the counting and audit of credentials an endpoint refuses
// (R-EP-009, R-EP-010).

// wireExposure is the exposure area: the ceiling built once from the configured
// endpoints and their exposure modes, and the rule service on it. The decision
// point applies the ceiling, and the repository's delete hook records the rules a
// deletion takes with it (H-164), so both come before them.
func (a *App) wireExposure(_ context.Context, in infra) (exposureArea, error) {
	modes := map[string]string{}
	for _, e := range a.cfg.Server.ResolveEndpoints() {
		modes[e.Name] = e.Exposure
	}
	ceiling := authz.NewExposureCeiling(modes, catalog.Default())
	rules := authz.NewExposureService(authz.ExposureServiceOptions{
		DB: in.db, Changes: in.changes, Recorder: in.rec, Ceiling: ceiling,
	})
	return exposureArea{ceiling: ceiling, rules: rules}, nil
}

// wireEndpoints is the endpoints area: the start-up warning for inert rules
// (H-58) and the rate-limit classes of the endpoints this process serves. Only
// a failed count read can go wrong, and that only loses the warning.
func (a *App) wireEndpoints(ctx context.Context, ex exposureArea, lim limitsArea) (endpointsArea, error) {
	if err := requireInputs(AreaEndpoints, given("exposure rule service", ex.rules), given("limiter set", lim.set)); err != nil {
		return endpointsArea{}, err
	}
	a.warnInertEndpoints(ctx, ex.rules)
	return endpointsArea{limits: a.endpointLimits(lim.set)}, nil
}

// warnInertEndpoints logs one warning per endpoint name that holds rules but is
// not configured: its rules are stored and have no effect, and they would apply
// at once to an endpoint configured later under that name (R-EP-012, H-58).
func (a *App) warnInertEndpoints(ctx context.Context, rules *authz.ExposureService) {
	counts, err := rules.Counts(ctx)
	if err != nil {
		a.log.Warn("exposure rules: the rule counts could not be read at start-up", "err", err)
		return
	}
	for name, c := range counts {
		if !rules.Configured(name) {
			a.log.Warn("exposure rules of an endpoint that is not configured are inert; review or delete them before configuring an endpoint of that name",
				"endpoint", name, "rules", c.Rules)
		}
	}
}

// endpointLimits builds one cluster limiter per rate-limit class that an
// endpoint of this process names (endpoint_<class>, R-EP-015) and the stage of
// each such endpoint. While Valkey is unavailable the per-replica share divides
// by the live replicas that serve this process's endpoints, the replicas of its
// pool: the count the limiter set holds, shared with every other limiter of the
// client listener (replicaCount).
func (a *App) endpointLimits(ls *cluster.LimiterSet) map[string]func(http.Handler) http.Handler {
	limiters := map[string]*cluster.Limiter{}
	out := map[string]func(http.Handler) http.Handler{}
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
			lim = ls.NewLimiter(v1.EndpointLimiterName(class), cluster.ScopeCluster, l.RequestsPerSecond, l.Burst)
			limiters[class] = lim
		}
		out[rt.cfg.Name] = v1.NewEndpointRateLimit(v1.EndpointRateLimitOptions{
			Endpoint: rt.cfg.Name, Class: class, Limit: l, Limiter: lim,
		})
	}
	return out
}

// endpointStage is stage 7 of an endpoint's chain (R-ARCH-005): the endpoint's
// rate-limit class (R-EP-015), then the stage of the management API (CORS, the
// classes of R-API-016). It does not hand the endpoint to the decision point:
// the subject of a request carries it from the listener's endpoint, which the
// chain stored at step 1 (edge.SubjectFrom, R-EP-013, Q-EP-20). The parts are
// read when the chain is built, after step 8.
func (a *App) endpointStage(rt *endpointRuntime) func(http.Handler) http.Handler {
	api := part[apiArea](a.parts).rateLimit
	limit := part[endpointsArea](a.parts).limits[rt.cfg.Name]
	return func(next http.Handler) http.Handler {
		h := next
		if api != nil {
			h = api(h)
		}
		if limit != nil {
			h = limit(h)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(withRequestPath(r.Context(), r.URL.Path)))
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
			Source:  httpserver.SourceFrom(ctx),
			Subject: audit.Subject{Kind: "token", ID: f.TokenID},
			Details: details,
		})
	}
}

// endpointServices are the services of the endpoint operations.
func (a *App) endpointServices() v1.EndpointServices {
	return v1.EndpointServices{
		Endpoints: a.cfg.Server.ResolveEndpoints(), RateLimits: a.cfg.Server.EndpointRateLimits,
		Exposure: part[exposureArea](a.parts).rules, Authorizer: a.decider(),
	}
}
