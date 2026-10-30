package app

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability"
)

// endpointRuntime is one client endpoint this process serves (R-EP-002): its
// snapshot, its TLS, its router and its listener. Every endpoint has all four.
type endpointRuntime struct {
	cfg    config.ResolvedEndpoint
	ep     *httpserver.Endpoint
	tls    *httpserver.ListenerTLS
	router *httpserver.Router
	srv    *httpserver.Server
}

// servesEndpoint reports whether this process opens the listener of an endpoint
// (R-EP-002): the development role all opens every one; a web process opens the
// endpoints of its pool, and a process in a pool that no endpoint names serves
// the endpoints without a pool, like the main Deployment.
func servesEndpoint(role, pool string, e config.ResolvedEndpoint, all []config.ResolvedEndpoint) bool {
	if role == "all" {
		return true
	}
	if e.Pool == pool {
		return true
	}
	if pool == "" || e.Pool != "" {
		return false
	}
	for _, o := range all {
		if o.Pool == pool {
			return false
		}
	}
	return true
}

// planEndpoints builds the snapshot of every endpoint this process opens and
// logs the configurations worth a warning (R-EP-001, R-EP-005). A worker opens
// no client listener.
func (a *App) planEndpoints() error {
	if !a.sub.ClientListener {
		return nil
	}
	all := a.cfg.Server.ResolveEndpoints()
	for _, r := range all {
		if !servesEndpoint(a.id.Role, a.id.Pool, r, all) {
			continue
		}
		ep, err := httpserver.NewEndpoint(r, a.cfg.Server.AdditionalBaseURLs)
		if err != nil {
			return err
		}
		a.eps = append(a.eps, &endpointRuntime{cfg: r, ep: ep})
		a.warnEndpoint(r)
	}
	if len(a.eps) == 0 {
		return fmt.Errorf("pool %q serves no endpoint: no server.endpoints entry names it and the endpoints without a pool belong to the main web Deployment", a.id.Pool)
	}
	return nil
}

// warnEndpoint logs what the configuration validation allows but an operator
// should know.
func (a *App) warnEndpoint(r config.ResolvedEndpoint) {
	if r.Default {
		return
	}
	if u, err := url.Parse(r.BaseURL); err == nil && u.Scheme == "http" && !loopbackName(u.Hostname()) {
		a.log.Warn("endpoint base_url uses http on a host that is not a loopback name; clients would send credentials in the clear",
			"endpoint", r.Name, "base_url", r.BaseURL)
	}
	if r.Exposure == config.ExposureRules && (r.Serves(config.SurfaceAPI) || r.Serves(config.SurfaceCIExchange)) {
		a.log.Warn("endpoint serves api or ci_exchange with exposure: rules; the management API's content operations are subject to the exposure rules",
			"endpoint", r.Name, "surface", r.Surface)
	}
}

func loopbackName(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
}

// servesDefault reports whether this process opens the default endpoint's listener.
func (a *App) servesDefault() bool {
	for _, rt := range a.eps {
		if rt.cfg.Default {
			return true
		}
	}
	return false
}

// startEndpointTLS builds the TLS of every further endpoint this process opens,
// each with its own reloader (R-EP-004): its own pairs, or the pair of
// server.tls. The default endpoint uses the client listener's TLS. The reloader's
// listener label is client for the default endpoint and client:<name> for a
// further one.
func (a *App) startEndpointTLS() error {
	for _, rt := range a.eps {
		if rt.cfg.Default {
			rt.tls = a.clientTLS
			continue
		}
		name := "client:" + rt.cfg.Name
		l, err := httpserver.NewListenerTLS(name, rt.cfg.TLS, a.tlsOptions(name)...)
		if err != nil {
			return fmt.Errorf("endpoint %s: %w", rt.cfg.Name, err)
		}
		rt.tls = l
	}
	return nil
}

// startEndpoint builds the chain of one endpoint around its router, binds its
// listener and serves it (R-EP-002). The chain is constructed with the endpoint
// bound; its resolver checks hosts for it (R-EP-003).
func (a *App) startEndpoint(rt *endpointRuntime, router *httpserver.Router) error {
	cfg := a.cfg
	var additional []string
	if rt.cfg.Default {
		additional = cfg.Server.AdditionalBaseURLs
	}
	resolver, err := httpserver.NewResolver(rt.cfg.TrustedProxies, rt.cfg.BaseURL, additional,
		httpserver.WithForwardedHeader(httpserver.ForwardedHeader(cfg.Server.ForwardedHeader)), httpserver.ForEndpoint(rt.ep))
	if err != nil {
		return fmt.Errorf("endpoint %s: %w", rt.cfg.Name, err)
	}
	trace, reqLog := a.tele.clientStages(cfg, rt.cfg.Name)
	handler := httpserver.Chain{
		Router: router, Log: a.log, Endpoint: rt.ep, Events: endpointEvents{m: a.tele.metrics},
		Trace: trace, TraceID: observabilityRequestID, AccessLog: reqLog,
		OnRequest: label, MaxInFlight: rt.ep.MaxInFlight(), Resolver: resolver, Annotate: annotate,
		Authenticate: a.authenticate(rt), Preflight: part[apiArea](a.parts).preflight, RateLimit: a.endpointStage(rt), Replica: a.id.Instance, ReplicaHeader: rt.ep.ReplicaHeader(),
		Draining: a.health.Draining,
	}.Handler()

	name := "client"
	if !rt.cfg.Default {
		name = "client:" + rt.cfg.Name
	}
	srv, err := a.newServer(name, rt.cfg.Listen, handler, rt.tls)
	if err != nil {
		return err
	}
	if err := srv.Bind(); err != nil {
		return fmt.Errorf("endpoint %s: %w", rt.cfg.Name, err)
	}
	rt.router, rt.srv = router, srv
	a.tele.metrics.EndpointListenerUp(rt.cfg.Name, true)
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		if err := srv.Serve(); err != nil {
			a.fail(fmt.Errorf("endpoint %s: %w", rt.cfg.Name, err))
		}
	}()
	a.log.Info("client listener bound", "addr", srv.Addr().String(), "endpoint", rt.cfg.Name)
	return nil
}

// clientServers returns the client listeners this process opened.
func (a *App) clientServers() []*httpserver.Server {
	var out []*httpserver.Server
	for _, rt := range a.eps {
		if rt.srv != nil {
			out = append(out, rt.srv)
		}
	}
	return out
}

// EndpointAddr returns the bound address of an endpoint's listener, "" when this
// process does not serve the endpoint.
func (a *App) EndpointAddr(name string) string {
	for _, rt := range a.eps {
		if rt.cfg.Name == name && rt.srv != nil && rt.srv.Addr() != nil {
			return rt.srv.Addr().String()
		}
	}
	return ""
}

// EndpointNames returns the endpoints this process serves, default first.
func (a *App) EndpointNames() []string {
	var out []string
	for _, rt := range a.eps {
		out = append(out, rt.cfg.Name)
	}
	return out
}

// endpointNames lists every configured endpoint, for the bounded metric label.
func endpointNames(cfg *config.Config) []string {
	var out []string
	for _, e := range cfg.Server.ResolveEndpoints() {
		out = append(out, e.Name)
	}
	slices.Sort(out)
	return out
}

// endpointEvents feeds the host check's observations into the metrics (R-EP-009).
type endpointEvents struct{ m *observability.Metrics }

func (e endpointEvents) Misdirected(endpoint string) { e.m.EndpointMisdirected(endpoint) }
func (e endpointEvents) ForwardedHostIgnored(endpoint string) {
	e.m.EndpointForwardedHostIgnored(endpoint)
}
