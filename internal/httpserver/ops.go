package httpserver

import (
	"log/slog"
	"net/http"
)

// OpsOptions are the parts of the ops listener (R-ARCH-005): probes and metrics,
// nothing else, and none of the client listener's paths.
type OpsOptions struct {
	Live  http.Handler // GET /healthz
	Ready http.Handler // GET /readyz
	// Metrics serves GET /metrics, including its own authentication
	// (metrics.auth); nil leaves the route out.
	Metrics http.Handler
	// AccessLog is set when log.request.ops_listener is true.
	AccessLog func(http.Handler) http.Handler
	Log       *slog.Logger
}

// OpsHandler builds the ops listener's handler. It uses the same recovery and
// request-ID stages as the client listener, but no in-flight limit, client
// address, authentication or rate limit: a probe must never be refused because
// the request pool is busy.
func OpsHandler(o OpsOptions) http.Handler {
	r := NewOpsRouter()
	r.Handle("GET /healthz", o.Live)
	r.Handle("GET /readyz", o.Ready)
	if o.Metrics != nil {
		r.Handle("GET /metrics", o.Metrics)
	}
	return Chain{Router: r, Log: o.Log, AccessLog: o.AccessLog}.Handler()
}
