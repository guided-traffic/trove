package app

import (
	"context"
	"net/http"
	"net/netip"
	"strings"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
)

// TokenScopeOptions are the cross-area checks of token scope validation
// (R-AUTH-024): the actions and system capabilities of the authorization area,
// its validators for the three pattern dimensions, and the configured endpoint
// names. The administration commands that mint tokens use them too.
func TokenScopeOptions(cfg *config.Config) scope.Options {
	items := map[string]bool{}
	for _, c := range authz.Capabilities() {
		items[string(c)] = true
	}
	acts := map[string]bool{}
	for _, a := range authz.Actions() {
		acts[string(a)] = true
	}
	endpoints := map[string]bool{}
	for _, n := range endpointNames(cfg) {
		endpoints[n] = true
	}
	return scope.Options{
		Action:     func(s string) bool { return acts[s] },
		SystemItem: func(s string) bool { return items[s] },
		Endpoint:   func(n string) bool { return endpoints[n] },
		Pattern: func(dimension, p string) error {
			switch dimension {
			case "repositories":
				return authz.ValidateSelector(p)
			case "packages":
				return authz.ValidatePackagePattern(p)
			default:
				return authz.ValidatePathPattern(p)
			}
		},
	}
}

// wireIdentity is the identity area: the principal store, the settings store,
// the token service, the provisioner and the manager, on the metrics the session
// store and the login count into as well (R-AUTH-041). The authentication stage
// is the auth area, which also needs the sessions and the limiters of a
// replica with a client listener.
func (a *App) wireIdentity(_ context.Context, in infra) (identityArea, error) {
	cfg := a.cfg.Auth
	metrics, err := principal.NewMetrics(a.tele.metrics.Registry())
	if err != nil {
		return identityArea{}, err
	}
	gate := principal.Gate{Fresh: in.changes, Bound: cfg.RevocationBound, Metrics: metrics}

	principals := principal.NewStore(principal.StoreOptions{DB: in.db, Cache: in.caches, Gate: gate, SafetyTTL: cfg.Tokens.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{
		DB: in.db, Notifier: in.changes, Recorder: in.rec, Cache: in.caches, Gate: gate, Tokens: cfg.Tokens, Anonymous: cfg.Anonymous,
	})
	recent, err := cluster.NewTokenSeen(in.vk, cfg.RateLimit.RecentTokenWindow)
	if err != nil {
		return identityArea{}, err
	}
	tokens := token.NewService(token.ServiceOptions{
		DB: in.db, Notifier: in.changes, Recorder: in.rec, Principals: principals, Settings: settings, Cache: in.caches, Gate: gate,
		Tokens: cfg.Tokens, Recent: recent, Metrics: metrics, Log: a.log.With("component", "tokens"),
		Scope: TokenScopeOptions(a.cfg),
	})
	breakGlass := cfg.BreakGlass.Username // reserved: no OIDC login and no user can take the name (R-AUTH-007)
	manager := principal.NewManager(principal.ManagerOptions{
		DB: in.db, Notifier: in.changes, Recorder: in.rec, BreakGlassName: breakGlass, Authz: authz.NewPrincipalHook(in.changes),
	})
	provisioner := principal.NewProvisioner(principal.ProvisionerOptions{
		DB: in.db, Notifier: in.changes, Recorder: in.rec, BreakGlassName: breakGlass, Metrics: metrics, Authz: authz.NewPrincipalHook(in.changes),
	})
	a.tokenSvc.set(tokens)
	return identityArea{
		metrics: metrics, principals: principals, manager: manager, provisioner: provisioner, tokens: tokens, settings: settings,
		// The decision point reads the runtime anonymous switch (R-AUTH-037) from the
		// same cached settings row the stage reads.
		anonymousAccess: func(ctx context.Context) (bool, error) {
			eff, err := settings.Effective(ctx)
			return eff.AnonymousAccess, err
		},
	}, nil
}

// wireAuth is the auth area: the authentication stage of the client listeners'
// chains, on the stores of the identity area, the session store and the
// cluster-wide failure throttle (R-AUTH-001, R-AUTH-038).
func (a *App) wireAuth(_ context.Context, id identityArea, ss sessionsArea, lim limitsArea) (authArea, error) {
	if err := requireInputs(AreaAuth, given("token service", id.tokens), given("session store", ss.store), given("failure limiter", lim.failures)); err != nil {
		return authArea{}, err
	}
	cfg := a.cfg.Auth
	publicOps, err := publicOperations()
	if err != nil {
		return authArea{}, err
	}
	stage := auth.New(auth.Options{
		Tokens: id.tokens, Sessions: ss.store, Principals: id.principals, Settings: id.settings, Limiter: lim.failures,
		ExemptCIDRs: cfg.RateLimit.ExemptPrefixes(), ClientAddr: clientAddr, BasicEnabled: cfg.Clients.Basic.Enabled,
		Metrics: id.metrics, Log: a.log.With("component", "auth"),
	})
	return authArea{stage: stage, publicOps: publicOps}, nil
}

// wireShell is the shell area: the same stage over an empty token store, so the
// HTTP shell of the tests has one authentication code path.
func (a *App) wireShell(context.Context) (shellArea, error) {
	publicOps, err := publicOperations()
	if err != nil {
		return shellArea{}, err
	}
	stage := auth.New(auth.Options{
		Tokens: emptyTokens{}, Settings: anonymousOn{}, BasicEnabled: a.cfg.Auth.Clients.Basic.Enabled, ClientAddr: clientAddr, Log: a.log,
	})
	return shellArea{auth: authArea{stage: stage, publicOps: publicOps}}, nil
}

// publicOperations lists the patterns of the management API operations whose
// declaration is public, or public by a switch.
func publicOperations() (map[string]bool, error) {
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for pattern, op := range doc.Operations() {
		if op.Requirement.Public || op.Requirement.PublicSwitch != "" {
			out[pattern] = true
		}
	}
	return out, nil
}

// authStage is the authentication stage of this replica: the auth area's, or the
// shell's.
func (a *App) authStage() authArea {
	if au := part[authArea](a.parts); au.stage != nil {
		return au
	}
	return part[shellArea](a.parts).auth
}

// clientAddr is the client address after the trusted-proxy rule, resolved by the
// chain before the authentication stage (R-ARCH-005 step 5).
func clientAddr(r *http.Request) netip.Addr { return httpserver.InfoFrom(r.Context()).Client }

// anonymousOn is the settings of a replica without a database: the HTTP shell
// has no administrator to change the switch, and its stand-in routes are open.
type anonymousOn struct{}

func (anonymousOn) Effective(context.Context) (token.Effective, error) {
	return token.Effective{AnonymousAccess: true}, nil
}

// publicRoute tells the stage which requests match a management API operation
// that is declared public. The system.bootstrap_anonymous_read bridge of the M0
// authorization stand-in also lets a read of the management API reach it
// without a credential; the authorization area replaces both.
func (a *App) publicRoute(rt *endpointRuntime) func(*http.Request) bool {
	bridge := a.cfg.System.BootstrapAnonymousRead
	return func(r *http.Request) bool {
		safe := r.Method == http.MethodGet || r.Method == http.MethodHead
		if bridge && safe && strings.HasPrefix(r.URL.Path, "/api/v1/") {
			return true
		}
		if rt.router == nil {
			return false
		}
		return a.authStage().publicOps[rt.router.Pattern(r)]
	}
}

// emptyTokens is the token store of a replica without a database: no token is
// known.
type emptyTokens struct{}

func (emptyTokens) Validate(context.Context, string) (token.Info, error) {
	return token.Info{}, token.ErrUnknown
}

// registerIdentityJobs registers the kinds and tasks of the identity area
// (R-AUTH-043): token-cleanup and token-expiry-notice. Both are maintenance
// queue jobs, exclusive per task; the handlers find the token service at run time,
// because the kinds are registered before the components are wired. The schedule
// of maintenance.tasks wins over the fallback here.
func (a *App) registerIdentityJobs(r *jobs.Registry) {
	run := func(fn func(context.Context, *token.Service) (any, error)) jobs.Handler {
		return a.tokenSvc.handler("token service", func(ctx context.Context, svc *token.Service, run *jobs.Run) error {
			res, err := fn(ctx, svc)
			if err == nil {
				err = run.SetResult(res)
			}
			return err
		})
	}
	tasks := []struct {
		name, schedule string
		fn             func(context.Context, *token.Service) (any, error)
	}{
		{token.TaskCleanup, token.DefaultCleanupSchedule, func(ctx context.Context, s *token.Service) (any, error) {
			n, err := s.Cleanup(ctx)
			return map[string]int64{"deleted": n}, err
		}},
		{token.TaskExpiryNotice, token.DefaultExpiryNoticeSchedule, func(ctx context.Context, s *token.Service) (any, error) {
			n, err := s.NoticeExpiring(ctx)
			return map[string]int{"notified": n}, err
		}},
	}
	for _, t := range tasks {
		r.RegisterKind(t.name, jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Handler: run(t.fn)})
		spec := jobs.TaskSpec{Schedule: t.schedule}
		if c, ok := a.cfg.Maintenance.Tasks[t.name]; ok && c.Schedule != "" {
			spec.Schedule, spec.Disabled = c.Schedule, !c.Enabled
		}
		r.RegisterTask(t.name, spec)
	}
}

// authServices hands the identity and login components to the management API
// (internal/api/v1). A component a replica does not have stays nil, and the
// operations that need it answer 503 unavailable.
func (a *App) authServices() v1.AuthServices {
	id, ss, lg, lim := part[identityArea](a.parts), part[sessionsArea](a.parts), part[loginArea](a.parts), part[limitsArea](a.parts)
	svc := v1.AuthServices{
		Manager: id.manager, Tokens: id.tokens, Settings: id.settings, Config: a.cfg.Auth,
		EndpointConfigured: TokenScopeOptions(a.cfg).Endpoint,
	}
	// A nil *session.Store or *oidc.Service must not become a non-nil interface.
	if ss.store != nil {
		svc.Sessions = ss.store
	}
	if lg.service != nil {
		svc.Login = lg.service
	}
	if lim.flowStarts != nil {
		svc.FlowStarts = takeLimiter{lim.flowStarts}
	}
	return svc
}

// authenticate returns the authentication stage of an endpoint's chain: the
// stage of the auth area (or the shell's) behind the conversion to
// httpserver.Principal. The chain is built after every step has run, so the stage
// is there.
func (a *App) authenticate(rt *endpointRuntime) func(*http.Request) (httpserver.Principal, error) {
	rules := endpointRules(rt)
	rules.PublicRoute = a.publicRoute(rt)
	rules.Refused = a.credentialRefused(rt)
	stage := a.authStage().stage
	return withMinEpoch(func(r *http.Request) (httpserver.Principal, error) {
		res, err := stage.Authenticate(r, rules)
		if err != nil {
			return httpserver.Principal{}, err
		}
		p := principalFromResult(res)
		recordPrincipal(r, p)
		return p, nil
	})
}
