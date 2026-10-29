package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability"
)

// telemetry bundles logging, metrics and tracing, the first things a replica
// starts (R-ARCH-024 step 2).
type telemetry struct {
	logging *observability.Logging
	log     *slog.Logger
	metrics *observability.Metrics
	tracer  *observability.Tracer
	passing *prometheus.GaugeVec
}

func newTelemetry(cfg *config.Config, id Identity, w io.Writer) (*telemetry, error) {
	t := &telemetry{}
	t.logging = observability.NewLogging(cfg.Log, w, observability.WithIdentity(id.Instance, id.Role))
	t.log = t.logging.Logger()
	t.metrics = observability.NewMetrics(id.Role, observability.WithMetricsConfig(cfg.Metrics))
	t.passing = t.metrics.NewGaugeVec("trove_health_check_passing", "1 while the readiness check passes, 0 while it fails.", "name")
	var err error
	t.tracer, err = observability.Tracing(cfg.Tracing,
		observability.WithTrustedProxies(cfg.Server.TrustedProxies),
		observability.WithTraceResource(id.Instance, id.Role, buildinfo.Get().Version),
		observability.WithTraceMetrics(t.metrics),
		observability.WithTraceLogger(t.log),
	)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// checkObserver feeds trove_health_check_passing{name} (R-OPS-008).
func (t *telemetry) checkObserver(name string, passing bool) {
	v := 0.0
	if passing {
		v = 1
	}
	t.passing.WithLabelValues(name).Set(v)
}

// clientStages returns the injected stages of the client chain: trace context,
// request log and metrics (R-ARCH-005 steps 2 and 3).
func (t *telemetry) clientStages(cfg *config.Config) (trace, reqLog func(http.Handler) http.Handler) {
	reqLog = observability.RequestLog(t.log, t.metrics,
		observability.WithListener("client"), observability.WithRequestRecords(cfg.Log.Request.Enabled))
	return t.tracer.Middleware(), reqLog
}

// opsStages returns the stages of the ops listener. It logs requests only with
// log.request.ops_listener (R-OPS-010); its metrics are always counted.
func (t *telemetry) opsStages(cfg *config.Config) func(http.Handler) http.Handler {
	return observability.RequestLog(t.log, t.metrics,
		observability.WithListener("ops"), observability.WithRequestRecords(cfg.Log.Request.OpsListener))
}

// label names the handler of a request for the request log and the metrics: the
// ServeMux pattern, never the raw path (R-ARCH-005 step 3).
func label(r *http.Request) {
	name := r.Pattern
	if name == "" || name == httpserver.CatchAllPattern {
		name = "unmatched"
	}
	observability.RequestInfoFrom(r.Context()).SetHandler(name)
}

// annotate records the resolved client address for the request log.
func annotate(r *http.Request, info httpserver.RequestInfo) {
	if info.Client.IsValid() {
		observability.RequestInfoFrom(r.Context()).SetRemoteAddr(info.Client.String())
	}
}

func (t *telemetry) shutdown(ctx context.Context) {
	_ = t.tracer.Shutdown(ctx)
}
