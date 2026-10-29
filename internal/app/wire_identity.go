package app

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
)

// identityRecorder is the audit recorder of the identity events: the one built
// at step 4, or a discarding one in a shell without infrastructure.
func (a *App) identityRecorder() audit.Recorder {
	if a.m1.recorder != nil {
		return a.m1.recorder
	}
	return audit.Nop{}
}

// systemScopeItems are the system capabilities a token scope may name
// (R-AUTH-025: the capability names of R-AUTHZ-010).
func systemScopeItems() map[string]bool {
	caps := []authz.Capability{
		authz.ContentBypass, authz.IdentitiesRead, authz.IdentitiesWrite, authz.PoliciesRead, authz.PoliciesWrite,
		authz.RolesRead, authz.RolesWrite, authz.CIRead, authz.CIWrite, authz.TokensRead, authz.TokensRevoke,
		authz.RepositoriesRead, authz.RepositoriesWrite, authz.ProjectsRead, authz.ProjectsWrite, authz.WebhooksRead,
		authz.WebhooksWrite, authz.SettingsRead, authz.SettingsWrite, authz.SecuritySettingsWrite, authz.AuditRead,
		authz.EndpointsRead, authz.EndpointsWrite,
	}
	out := make(map[string]bool, len(caps))
	for _, c := range caps {
		out[string(c)] = true
	}
	return out
}

// wireIdentity builds the identity components at the WIRING POINT of step 8
// (docs/developer/m1-contracts.md): the principal store, the settings store, the
// token service, the provisioner and manager, and the authentication stage. A
// replica without PostgreSQL (the HTTP shell of the tests) gets the same stage
// over an empty token store, so there is one authentication code path.
func (a *App) wireIdentity() error {
	cfg := a.cfg.Auth
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		return err
	}
	a.m1.publicOps = map[string]bool{}
	for pattern, op := range doc.Operations() {
		if op.Requirement.Public || op.Requirement.PublicSwitch != "" {
			a.m1.publicOps[pattern] = true
		}
	}
	db, changes, caches := a.database(), a.changesOrNil(), a.caches
	if db == nil || changes == nil || caches == nil {
		a.m1.authenticator = auth.New(auth.Options{
			Tokens: emptyTokens{}, Settings: anonymousOn{}, BasicEnabled: cfg.Clients.Basic.Enabled, ClientAddr: clientAddr, Log: a.log,
		})
		return nil
	}
	metrics, err := principal.NewMetrics(a.tele.metrics.Registry())
	if err != nil {
		return err
	}
	gate := principal.Gate{Fresh: changes, Bound: cfg.RevocationBound, Metrics: metrics}
	rec := a.identityRecorder()

	principals := principal.NewStore(principal.StoreOptions{DB: db, Cache: caches, Gate: gate, SafetyTTL: cfg.Tokens.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{
		DB: db, Notifier: changes, Recorder: rec, Cache: caches, Gate: gate, Tokens: cfg.Tokens, Anonymous: cfg.Anonymous,
	})
	items := systemScopeItems()
	endpoints := map[string]bool{}
	for _, n := range endpointNames(a.cfg) {
		endpoints[n] = true
	}
	recent, err := cluster.NewTokenSeen(a.valkey(), cfg.RateLimit.RecentTokenWindow)
	if err != nil {
		return err
	}
	tokens := token.NewService(token.ServiceOptions{
		DB: db, Notifier: changes, Recorder: rec, Principals: principals, Settings: settings, Cache: caches, Gate: gate,
		Tokens: cfg.Tokens, Recent: recent, Metrics: metrics, Log: a.log.With("component", "tokens"),
		Scope: token.ScopeOptions{
			SystemItem: func(s string) bool { return items[s] },
			Endpoint:   func(n string) bool { return endpoints[n] },
		},
	})
	breakGlass := "" // WIRING POINT: auth.break_glass.username once the login area adds the key
	manager := principal.NewManager(principal.ManagerOptions{DB: db, Notifier: changes, Recorder: rec, BreakGlassName: breakGlass})
	provisioner := principal.NewProvisioner(principal.ProvisionerOptions{
		DB: db, Notifier: changes, Recorder: rec, BreakGlassName: breakGlass, Metrics: metrics,
	})

	limiter := a.m1.failureLimiter
	if limiter == nil {
		// WIRING POINT: the api area's cluster limiter (auth.credential_failures,
		// count-only form) replaces the per-replica one at the merge.
		limiter = auth.NewLocalLimiter(cfg.RateLimit.FailuresPerAddress, time.Minute, nil, nil)
	}
	stage := auth.New(auth.Options{
		Tokens: tokens, Sessions: a.m1.sessions, Principals: principals, Settings: settings, Limiter: limiter,
		ExemptCIDRs: cfg.RateLimit.ExemptPrefixes(), ClientAddr: clientAddr, BasicEnabled: cfg.Clients.Basic.Enabled,
		Metrics: metrics, Log: a.log.With("component", "auth"),
	})

	a.mu.Lock()
	a.m1.principals, a.m1.principalManager, a.m1.provisioner = principals, manager, provisioner
	a.m1.tokens, a.m1.authSettings, a.m1.authenticator = tokens, settings, stage
	// The decision point reads the runtime anonymous switch (R-AUTH-037) from the
	// same cached settings row the stage reads.
	a.m1.anonymousAccess = func() bool {
		eff, err := settings.Effective(context.Background())
		return err == nil && eff.AnonymousAccess
	}
	a.mu.Unlock()
	return nil
}

func (a *App) changesOrNil() *cluster.Changes { a.mu.Lock(); defer a.mu.Unlock(); return a.changes }

func (a *App) tokenService() *token.Service { a.mu.Lock(); defer a.mu.Unlock(); return a.m1.tokens }

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
		return a.m1.publicOps[rt.router.Pattern(r)]
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
		return func(ctx context.Context, run *jobs.Run) error {
			svc := a.tokenService()
			if svc == nil {
				return jobs.Permanent(errors.New("this replica has no token service"))
			}
			res, err := fn(ctx, svc)
			if err == nil {
				err = run.SetResult(res)
			}
			return err
		}
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
