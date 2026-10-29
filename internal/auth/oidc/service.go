package oidc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The browser routes of the login (R-AUTH-005, R-AUTH-006, R-AUTH-013,
// R-AUTH-014). They lie outside /api/v1/ in the ui family of R-EP-005; the
// composition root registers them through httpserver.Router.
const (
	RouteStart      = "GET /auth/oidc/{providerId}/start"
	RouteCallback   = "GET /auth/oidc/{providerId}/callback"
	RouteLogout     = "POST /auth/logout"
	RouteLocalLogin = "POST /auth/local/login"
)

// LoggedOutPath is where every logout ends; the post-logout redirect URI is the
// base URL plus this path (R-AUTH-003, R-AUTH-013).
const LoggedOutPath = "/ui/logged-out"

// flowTTL is the lifetime of a login flow and of its binding cookie
// (R-AUTH-005).
const flowTTL = 10 * time.Minute

// pendingEvery is how old the pending-flow count may be (R-AUTH-038).
const pendingEvery = 5 * time.Second

// DefaultMaxPendingFlows is auth.rate_limit.max_pending_flows of R-AUTH-038
// while the composition root passes none.
const DefaultMaxPendingFlows = 100000

// Limiter is the cluster-wide limiter auth.flow_starts of R-AUTH-038, counted per
// client address (R-ARCH-034); the identity area builds it on the api area's
// cluster limiter. Allow reports whether one more request of the bucket may
// proceed and, if not, when to retry.
type Limiter interface {
	Allow(ctx context.Context, bucket string) (ok bool, retryAfter time.Duration)
}

// Options configure a [Service].
type Options struct {
	DB *postgres.DB
	// Sessions creates and ends the sessions.
	Sessions *session.Store
	// Provisioner is the seam to the identity area: just-in-time users and group
	// sync. Its refusals are the errors of package principal ([refusalReason]).
	Provisioner principal.Provisioner
	// Clients returns the outbound client of a purpose.
	Clients HTTPClients
	Audit   audit.Recorder
	Log     *slog.Logger
	// Metrics are the identity metrics of R-AUTH-041 (attempts and rate-limited
	// requests are families the identity area owns); nil counts nothing.
	Metrics *principal.Metrics
	// BaseURL is server.base_url, from which every URL sent to a provider is
	// derived (R-AUTH-003).
	BaseURL string
	Auth    config.Auth
	// Limiter is auth.flow_starts; nil limits nothing.
	Limiter Limiter
	// MaxPendingFlows is auth.rate_limit.max_pending_flows; zero takes the default.
	MaxPendingFlows int
	// Getenv reads the variable of client_secret_env; nil is os.Getenv.
	Getenv func(string) string

	// The edge functions of the composition root: problem+json with the request
	// ID, the client address the chain resolved (R-ARCH-006), the event source,
	// and the base URL's origin for the origin check (R-AUTH-012).
	Problem    func(http.ResponseWriter, *http.Request, error)
	ClientAddr func(*http.Request) string
	Source     session.SourceFunc
	Origin     func(*http.Request) string
}

// Service is the login of one replica that serves the client listener: the
// providers, the browser routes and the break-glass sign-in. Every step of a
// flow reads its state from PostgreSQL, so consecutive requests may reach any
// replica (R-AUTH-042).
type Service struct {
	o         Options
	log       *slog.Logger
	m         *principal.Metrics
	providers []*Provider // in login-page order
	byID      map[string]*Provider
	bg        *BreakGlass

	pmu      sync.Mutex
	pendAt   time.Time
	pendN    int64
	pendBusy bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// CheckBaseURL is the rule of R-AUTH-003 that spans two sections: a provider
// needs server.base_url, and the base URL is https except on a loopback host.
func CheckBaseURL(auth config.Auth, baseURL string) error {
	if len(auth.OIDC.Providers) == 0 {
		return nil
	}
	if baseURL == "" {
		return errors.New("server.base_url is required when auth.oidc.providers is not empty: every redirect URI is derived from it (R-AUTH-003)")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("server.base_url %q is not an absolute URL", baseURL)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !config.LoopbackHost(u.Hostname())) {
		return fmt.Errorf("server.base_url %q must be https for OIDC login (http only for a loopback host, R-AUTH-003)", baseURL)
	}
	return nil
}

// New builds the service: every provider (no network call yet) and the
// break-glass account. A configuration error stops the replica.
func New(ctx context.Context, o Options) (*Service, error) {
	if err := CheckBaseURL(o.Auth, o.BaseURL); err != nil {
		return nil, err
	}
	if o.DB == nil || o.Sessions == nil {
		return nil, errors.New("oidc: the service needs the database and the session store")
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.MaxPendingFlows <= 0 {
		o.MaxPendingFlows = DefaultMaxPendingFlows
	}
	if o.Problem == nil {
		o.Problem = func(w http.ResponseWriter, r *http.Request, err error) { errs.Write(w, r, err, "", nil) }
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{o: o, log: log.With("component", "login"), m: o.Metrics, byID: map[string]*Provider{}}
	for id, raw := range o.Auth.OIDC.Providers {
		p, err := newProvider(id, raw, o.BaseURL, o.Clients, o.Getenv, s.log)
		if err != nil {
			return nil, err
		}
		s.providers = append(s.providers, p)
		s.byID[id] = p
	}
	sortProviders(s.providers)
	bg, err := newBreakGlass(ctx, s)
	if err != nil {
		return nil, err
	}
	s.bg = bg
	return s, nil
}

// sortProviders puts the providers in login-page order: by order, then by ID
// (Q-AUTH-30).
func sortProviders(ps []*Provider) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].cfg.Order != ps[j].cfg.Order {
			return ps[i].cfg.Order < ps[j].cfg.Order
		}
		return ps[i].ID < ps[j].ID
	})
}

// Name implements the composition root's Component.
func (s *Service) Name() string { return "login" }

// Start starts the background loops of a web replica (R-AUTH-042): the
// discovery refresh of every provider and the rotation check of the
// break-glass password file. It returns at once; a provider that cannot be
// reached does not hold start-up or readiness back (R-AUTH-004).
func (s *Service) Start(ctx context.Context) error {
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	for _, p := range s.providers {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			p.run(lctx)
		}()
	}
	if s.bg != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.bg.watch(lctx)
		}()
	}
	return nil
}

// Stop ends the loops.
func (s *Service) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Providers returns the providers in login-page order.
func (s *Service) Providers() []*Provider { return slices.Clone(s.providers) }

// Provider returns a provider by ID.
func (s *Service) Provider(id string) (*Provider, bool) {
	p, ok := s.byID[id]
	return p, ok
}

// BreakGlass returns the break-glass account.
func (s *Service) BreakGlass() *BreakGlass { return s.bg }

// WaitDiscovered waits until every provider finished its first discovery
// attempt that succeeded, or ctx ends. Tests use it.
func (s *Service) WaitDiscovered(ctx context.Context) error {
	for _, p := range s.providers {
		if p.document() != nil {
			continue
		}
		select {
		case <-p.refreshed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// LoginOption is one button of the login page (R-AUTH-009). It holds no issuer,
// client ID or error detail.
type LoginOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	StartURL    string `json:"startUrl"`
	Available   bool   `json:"available"`
}

// LoginOptions is the response of GET /api/v1/auth/login-options (R-AUTH-009,
// wave 2). AutoRedirect is empty when the page shows buttons.
type LoginOptions struct {
	Providers       []LoginOption `json:"providers"`
	LocalLogin      bool          `json:"localLogin"`
	AnonymousAccess bool          `json:"anonymousAccess"`
	AutoRedirect    string        `json:"autoRedirect,omitempty"`
}

// LoginOptions renders the login page's options: providers in configuration
// order (Q-AUTH-30), the local form when the break-glass account is enabled and
// shown, anonymous access as the identity area's runtime setting says.
func (s *Service) LoginOptions(anonymousAccess bool) LoginOptions {
	out := LoginOptions{Providers: []LoginOption{}, AnonymousAccess: anonymousAccess}
	for _, p := range s.providers {
		out.Providers = append(out.Providers, LoginOption{
			ID: p.ID, DisplayName: p.cfg.DisplayName, StartURL: "/auth/oidc/" + p.ID + "/start", Available: p.document() != nil,
		})
	}
	if len(s.providers) == 1 && s.o.Auth.LoginPage.AutoRedirect {
		out.AutoRedirect = s.providers[0].ID
	}
	out.LocalLogin = s.bg != nil && s.bg.usable() && s.o.Auth.BreakGlass.ShowOnLoginPage
	return out
}

// SafeReturnTo is the open-redirect defense of R-AUTH-005: a relative path that
// starts with /ui/, without //, \, a scheme or control characters; anything
// else becomes /ui/.
func SafeReturnTo(v string) string {
	const fallback = "/ui/"
	if !strings.HasPrefix(v, "/ui/") || len(v) > 2048 || strings.Contains(v, "//") || strings.Contains(v, `\`) {
		return fallback
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return fallback
		}
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		return fallback
	}
	return v
}

// clientAddr is the address the chain resolved, "" outside the chain.
func (s *Service) clientAddr(r *http.Request) string {
	if s.o.ClientAddr == nil {
		return ""
	}
	return s.o.ClientAddr(r)
}

func (s *Service) source(r *http.Request) audit.Source {
	if s.o.Source != nil {
		return s.o.Source(r)
	}
	return audit.Source{UserAgent: r.UserAgent()}
}

// allow applies auth.flow_starts to the client address (R-AUTH-038). Without a
// limiter every request proceeds.
func (s *Service) allow(w http.ResponseWriter, r *http.Request) bool {
	if s.o.Limiter == nil {
		return true
	}
	ok, retry := s.o.Limiter.Allow(r.Context(), s.clientAddr(r))
	if ok {
		return true
	}
	s.m.RateLimited("flow_starts")
	s.o.Problem(w, r, errs.E(errs.RateLimited, errs.CodeRateLimited, "Too many login attempts from this address; retry later.", errs.WithRetryAfter(retry)))
	return false
}

// pendingFull reports whether max_pending_flows unexpired browser and
// command-line flows are pending (R-AUTH-038). The count is read from
// PostgreSQL at most every 5 s on this replica; the request that finds it older
// refreshes it, the others use the last value.
func (s *Service) pendingFull(ctx context.Context) bool {
	s.pmu.Lock()
	stale := !s.pendBusy && time.Since(s.pendAt) >= pendingEvery
	if stale {
		s.pendBusy = true
	}
	n := s.pendN
	s.pmu.Unlock()
	if stale {
		c, err := countPending(ctx, s.o.DB)
		s.pmu.Lock()
		s.pendBusy = false
		if err == nil {
			s.pendN, s.pendAt, n = c, time.Now(), c
		}
		s.pmu.Unlock()
	}
	return n >= int64(s.o.MaxPendingFlows)
}
