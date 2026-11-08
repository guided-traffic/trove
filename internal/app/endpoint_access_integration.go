//go:build integration

package app

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The accessors the integration tests send requests through (the same values the listeners serve); they are built only with the
// integration tag, so the production binary carries none.

// EndpointHandler returns the whole handler of an endpoint's client listener, the chain with the router behind it, nil when this
// process does not serve the endpoint. Tests send requests to it without a connection; the listener serves the same value.
func (a *App) EndpointHandler(name string) http.Handler {
	for _, rt := range a.eps {
		if rt.cfg.Name == name {
			return rt.handler
		}
	}
	return nil
}

// EndpointRouter returns the router of an endpoint, nil when this process does not serve it: the route table the start-up check
// read. Tests read its patterns (registrytest.Walk).
func (a *App) EndpointRouter(name string) *httpserver.Router {
	for _, rt := range a.eps {
		if rt.cfg.Name == name {
			return rt.router
		}
	}
	return nil
}

// RegistryEnforcer returns the enforcement point of the registry tokens' grants of an endpoint, nil when the endpoint serves no
// registry root.
func (a *App) RegistryEnforcer(name string) *registry.Enforcer {
	for _, rt := range a.eps {
		if rt.cfg.Name == name {
			return rt.registry
		}
	}
	return nil
}

// FormatHost returns the format host of this process, nil when it has none (a worker builds its own for its passes). Tests call the
// host's background fetches (Host.BackgroundUpstream) through it.
func (a *App) FormatHost() *formathost.Host {
	h, _ := part[formatHostArea](a.parts).host.(*formathost.Host)
	return h
}
