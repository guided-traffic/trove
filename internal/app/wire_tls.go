package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability/health"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// tlsListener names a listener's TLS for the readiness check.
type tlsListener struct {
	name string
	l    *httpserver.ListenerTLS
}

// startTLS builds the TLS of the listeners this role opens (R-ARCH-035): one
// reloader per listener, the ops listener from server.ops_tls (inherit: the
// client listener's pairs without client certificates). A pair that does not
// load at start is a configuration error that holds the first readiness: the
// listener binds, refuses handshakes, and serves once the files are valid; a
// later rotation that does not load keeps the previous pair in service and only
// counts and logs (it never makes the replica unready).
func (a *App) startTLS() error {
	s := a.cfg.Server
	var err error
	if a.opsTLS, err = httpserver.NewListenerTLS("ops", s.OpsListenerTLS(), a.tlsOptions("ops")...); err != nil {
		return err
	}
	if a.servesDefault() {
		if a.clientTLS, err = httpserver.NewListenerTLS("client", s.TLS, a.tlsOptions("client")...); err != nil {
			return err
		}
	}
	if err := a.startEndpointTLS(); err != nil {
		return err
	}
	listeners := []tlsListener{{"ops", a.opsTLS}, {"client", a.clientTLS}}
	for _, rt := range a.eps {
		if !rt.cfg.Default {
			listeners = append(listeners, tlsListener{"endpoint " + rt.cfg.Name, rt.tls})
		}
	}
	if !slices.ContainsFunc(listeners, func(x tlsListener) bool { return x.l != nil }) {
		return nil
	}
	// The reloader has logged and counted a file that did not load.
	a.health.AddReadiness("tls", func(context.Context) error {
		var errs []error
		for _, x := range listeners {
			if x.l == nil {
				continue
			}
			if err := x.l.Loaded(); err != nil {
				errs = append(errs, fmt.Errorf("%s listener: %w", x.name, err))
			}
		}
		return health.ConfigError(errors.Join(errs...), "certificate")
	}, true)
	return nil
}

// tlsOptions are the reloader options of a listener.
func (a *App) tlsOptions(listener string) []tlsfiles.Option {
	return []tlsfiles.Option{
		tlsfiles.WithMetrics(a.tele.tls),
		tlsfiles.WithLogger(a.log.With("component", "tls", "listener", listener)),
	}
}
