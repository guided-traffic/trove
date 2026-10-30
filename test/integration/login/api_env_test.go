//go:build integration

package login

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/idapi"
)

// The management API of the identity and login areas behind the replica's session
// store: the real handlers, the real authentication stage with the session
// validator, and the real decision point (test/integration/idapi). A replica has
// pools, a change log and caches of its own and no Valkey, so the answers show
// what the replicas share through PostgreSQL (R-AUTH-042).

// apiHandler builds the /api/v1/ handler of one replica.
func (e *env) apiHandler(name string, db *postgres.DB, changes *cluster.Changes, caches *cache.Registry, reg *prometheus.Registry,
	store *session.Store, login *oidc.Service, metrics *principal.Metrics, authCfg config.Auth,
) http.Handler {
	t := e.t
	t.Helper()
	tokenCfg := config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT:       config.AuthTokenPAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		SAT:       config.AuthTokenSAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		CIT:       config.AuthTokenCIT{MaxTTL: 6 * time.Hour},
		MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20},
	}
	authCfg.Tokens = tokenCfg
	authCfg.Anonymous = config.AuthAnonymous{Allowed: true}
	authCfg.Clients = config.AuthClients{Basic: config.AuthClientBasic{Enabled: true}}
	gate := principal.Gate{Fresh: changes, Bound: 2 * time.Second, Metrics: metrics}
	principals := principal.NewStore(principal.StoreOptions{DB: db, Cache: caches, Gate: gate, SafetyTTL: tokenCfg.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{
		DB: db, Notifier: changes, Recorder: e.audit, Cache: caches, Gate: gate, Tokens: tokenCfg, Anonymous: authCfg.Anonymous,
	})
	tokens := token.NewService(token.ServiceOptions{
		DB: db, Notifier: changes, Recorder: e.audit, Principals: principals, Settings: settings, Cache: caches, Gate: gate,
		Tokens: tokenCfg, Metrics: metrics, Log: quiet, Scope: scope.Options{Action: func(string) bool { return true }, SystemItem: func(string) bool { return true }},
	})
	manager := principal.NewManager(principal.ManagerOptions{DB: db, Notifier: changes, Recorder: e.audit, BreakGlassName: authCfg.BreakGlass.Username})
	stage := auth.New(auth.Options{
		Tokens: tokens, Sessions: store, Principals: principals, Settings: settings,
		Limiter:      auth.NewLocalLimiter(300, time.Minute, nil, nil),
		ClientAddr:   func(r *http.Request) netip.Addr { return httpserver.InfoFrom(r.Context()).Client },
		BasicEnabled: true, Metrics: metrics, Log: quiet,
	})
	e.mu.Lock()
	e.components = append(e.components, apiParts{db: db, changes: changes, manager: manager, tokens: tokens})
	e.mu.Unlock()
	c := idapi.Components{
		Name: name, DB: db, Changes: changes, Stage: stage, Manager: manager, Tokens: tokens, Settings: settings,
		Sessions: store, Login: login, Recorder: e.audit, Auth: authCfg, BaseURL: e.baseURL, AcceptsSession: true,
		Validator: e.validator, Log: quiet,
	}
	if e.extend != nil {
		e.extend(&c, replicaParts{name: name, db: db, changes: changes, caches: caches, principals: principals})
	}
	return idapi.Handler(t, c)
}

// replicaParts are the components of one replica that a test adds services for.
type replicaParts struct {
	name       string
	db         *postgres.DB
	changes    *cluster.Changes
	caches     *cache.Registry
	principals principal.Store
}

// apiParts are the components of a replica a test uses directly (to create users
// and tokens with the domain calls).
type apiParts struct {
	db      *postgres.DB
	changes *cluster.Changes
	manager *principal.Manager
	tokens  *token.Service
}

// bindAdmin binds the role admin to a principal through the authorization
// service, so that every replica's decision point picks it up from the change log.
func (e *env) bindAdmin(principalID string) {
	e.t.Helper()
	p := e.components[0]
	svc := authz.NewService(authz.ServiceOptions{DB: p.db, Changes: p.changes, Recorder: audit.Nop{}})
	err := p.db.InTx(context.Background(), func(tx meta.Tx) error { return svc.BindAdmin(context.Background(), tx, principalID, "") })
	if err != nil {
		e.t.Fatalf("bind admin: %v", err)
	}
}

// adminToken creates a user "root", binds the role admin and returns a PAT that
// exercises it (scope admin: true). The decision points of all replicas follow the
// change within a poll.
func (e *env) adminToken() string {
	e.t.Helper()
	ctx := context.Background()
	p := e.components[0]
	caller := principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}
	u, err := p.manager.CreateUser(ctx, caller, principal.NewUser{Name: "root", Email: "root@example.com"})
	if err != nil {
		e.t.Fatal(err)
	}
	e.bindAdmin(u.ID)
	c, err := p.tokens.Create(ctx, token.CreateRequest{
		Caller: principal.Caller{Actor: audit.Actor{Kind: "user", ID: u.ID, Name: u.Name}}, Type: token.TypePAT, OwnerID: u.ID,
		Description: "admin", Scope: []byte(`{"inherit":true,"admin":true}`), Via: "api",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return c.Secret
}
