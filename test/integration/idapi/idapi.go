//go:build integration

// Package idapi builds the HTTP side of the integration tests of the access core:
// the real management API handlers (internal/api/v1) behind the real
// authentication stage (or a stand-in for it) and the real decision point, on the
// production request chain (internal/httpserver) without the rate-limit stage. A
// test drives the operations over HTTP the way a client does, with the components
// of an in-process replica (pools, change log, caches) that it built itself. The
// identity and login areas use it with the real stage; the authorization and
// audit areas (test/integration/stack) use it with a stand-in that names the
// caller in a header, with a decision point of their own and the services of
// their areas added through Components.Options and Components.Routes. The package
// is test support: no production package imports it.
package idapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// Components are the parts of one replica the API needs.
type Components struct {
	Name     string
	DB       *postgres.DB
	Changes  *cluster.Changes
	Stage    auth.Authenticator
	Manager  *principal.Manager
	Tokens   *token.Service
	Settings *token.SettingsStore
	// Sessions and Login are nil on a replica without a login.
	Sessions v1.SessionService
	Login    v1.LoginService
	// FlowStarts is the limiter of the revocation by value; nil: none.
	FlowStarts v1.FlowLimiter
	Recorder   audit.Recorder
	Auth       config.Auth
	// BaseURL is server.base_url; the answers' Location headers and the cookies'
	// origin are built from it.
	BaseURL string
	// AcceptsSession lets the stage take the session cookie.
	AcceptsSession bool
	// Log receives the server's log lines; nil discards them.
	Log *slog.Logger
	// Validator, when not nil, validates every answer against the document.
	Validator *apitest.Validator

	// Authenticate, when set, replaces Stage as the authentication stage of the
	// chain: a stand-in for tests whose subject is not the identity area.
	Authenticate func(*http.Request) (httpserver.Principal, error)
	// Engine is a decision point the caller built and started; nil builds one from
	// the other components (the engine then needs Settings for the anonymous
	// switch) and stops it when the test ends.
	Engine *authz.Engine
	// Options, when set, adds the services of further areas to the API options
	// before the server is built (the policy service, the audit store).
	Options func(engine *authz.Engine, o *v1.Options)
	// Routes, when set, mounts further routes on the router (the raw file path).
	Routes func(engine *authz.Engine, r *httpserver.Router)
}

// APIConfig is the api section of the tests.
func APIConfig() config.API {
	return config.API{
		PageSizeDefault: 50, PageSizeMax: 500, IdempotencyRetention: time.Hour,
		MaxRequestBodyBytes: config.MiB, MaxConfigurationBodyBytes: 4 * config.MiB,
	}
}

// AuthConfig is the auth section the API reads: the windows, the break-glass name
// and one provider for pending identity links.
func AuthConfig(tokens config.AuthTokens) config.Auth {
	return config.Auth{
		Tokens:    tokens,
		Anonymous: config.AuthAnonymous{Allowed: true},
		OIDC: config.AuthOIDC{Providers: map[string]config.AuthOIDCProvider{
			"corp": {DisplayName: "Corporate SSO", Issuer: "https://login.example.com/realms/acme"},
		}},
		Sessions:   config.AuthSessions{IdleTimeout: time.Hour, AbsoluteTimeout: 12 * time.Hour, MaxPerUser: 20, RecentAuthWindow: time.Hour},
		BreakGlass: config.AuthBreakGlass{Enabled: true, Username: "breakglass"},
		Clients:    config.AuthClients{Basic: config.AuthClientBasic{Enabled: true}},
	}
}

// principalOf is the conversion of the composition root (app.principalFromResult),
// reduced to what the tests read.
func principalOf(res auth.Result) httpserver.Principal {
	if res.Anonymous {
		return httpserver.AnonymousPrincipal()
	}
	p := httpserver.Principal{
		Kind: httpserver.PrincipalKind(res.Principal.Kind), ID: res.Principal.ID, Name: res.Principal.Name,
		Credential: httpserver.Credential{
			Method: httpserver.AuthMethod(res.Credential.Method), SessionID: res.Credential.SessionID,
			TokenID: res.Credential.TokenID, TokenType: httpserver.TokenType(res.Credential.TokenType),
			Scope: res.Credential.Scope,
		},
	}
	for _, g := range res.Groups {
		p.Groups = append(p.Groups, httpserver.GroupRef{ID: g.ID, Name: g.Name})
	}
	return p
}

// logWriter writes the server's log lines to the test log.
type logWriter struct{ t testing.TB }

func (l logWriter) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// TestLogger logs to the test log.
func TestLogger(t testing.TB) *slog.Logger { return slog.New(slog.NewTextHandler(logWriter{t}, nil)) }

// Handler builds the handler of a replica and its decision point. The engine reads
// the role bindings when it starts, so a test binds the administrator before it
// calls Handler. Stop the engine when the test ends (Handler registers it with t).
func Handler(t testing.TB, c Components) http.Handler {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine := c.Engine
	if engine == nil {
		var err error
		engine, err = authz.New(authz.Options{
			Source: authz.NewPostgresSource(c.DB, 10*time.Second), Changes: c.Changes,
			Config:            config.Authz{HideExistence: true, Cache: config.AuthzCache{MaxStaleness: 10 * time.Second, SafetyTTL: time.Hour}},
			EpochPollInterval: time.Second, RetryBudget: 5 * time.Second, Recorder: c.Recorder, Replica: c.Name, Log: slog.New(slog.DiscardHandler),
			AnonymousAccess: func(ctx context.Context) (bool, error) {
				eff, err := c.Settings.Effective(ctx)
				return eff.AnonymousAccess, err
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Start(ctx); err != nil {
			t.Fatalf("engine.Start: %v", err)
		}
		t.Cleanup(func() {
			sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer scancel()
			_ = engine.Stop(sctx)
		})
	}
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	opts := v1.Options{
		Document: doc, Identity: v1.Identity{Replica: c.Name, Role: "web"}, Ready: func() bool { return true },
		Access: v1.NewAccess(engine),
		API:    v1.Services{Config: APIConfig(), Store: c.DB, Audit: c.Recorder},
		Auth: v1.AuthServices{
			Manager: c.Manager, Tokens: c.Tokens, Settings: c.Settings, Sessions: c.Sessions, Login: c.Login, FlowStarts: c.FlowStarts, Config: c.Auth,
		},
	}
	if c.Options != nil {
		c.Options(engine, &opts)
	}
	api := v1.New(opts)
	router := httpserver.NewClientRouter()
	api.Register(router)
	if c.Routes != nil {
		c.Routes(engine, router)
	}
	resolver, err := httpserver.NewResolver(nil, c.BaseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := auth.EndpointRules{
		Name: "default", AcceptsSession: c.AcceptsSession, Anonymous: true,
		TokenTypes: []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT},
		PublicRoute: func(req *http.Request) bool {
			op, ok := doc.Operations()[router.Pattern(req)]
			return ok && op.Requirement.Public
		},
	}
	log := c.Log
	if log == nil {
		log = TestLogger(t)
	}
	authenticate := c.Authenticate
	if authenticate == nil {
		authenticate = func(req *http.Request) (httpserver.Principal, error) {
			res, err := c.Stage.Authenticate(req, rules)
			if err != nil {
				return httpserver.Principal{}, err
			}
			return principalOf(res), nil
		}
	}
	chain := httpserver.Chain{
		Log: log, Router: router, Resolver: resolver, Replica: c.Name, ReplicaHeader: httpserver.ReplicaAlways,
		Authenticate: authenticate,
	}.Handler()
	if c.Validator != nil {
		return c.Validator.Handler(t, chain)
	}
	return chain
}
