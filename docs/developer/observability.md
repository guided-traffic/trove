# Observability: logs, metrics, tracing, request IDs

Package [internal/observability](../../internal/observability) holds the primitives; the requirements are
R-OPS-006 (metrics endpoint), R-OPS-007 (naming and labels), R-OPS-009 (log record), R-OPS-010 (request log),
R-OPS-012 (component levels), R-OPS-013 (redaction) and R-OPS-018 (trace context) in
[operations-and-observability.md](../spec/operations-and-observability.md). It is a leaf package: every layer
imports it, it imports only `internal/config` and `internal/buildinfo`.

## Files

| File | Responsibility |
|---|---|
| [logger.go](../../internal/observability/logger.go) | `NewLogger`, `Logging` (streams, components, level override, ring), the slog handler |
| [redact.go](../../internal/observability/redact.go) | `Secret` type, key-based redaction, `Scrubber` (token, Bearer, URL user info, query parameters, poll secret) |
| [ring.go](../../internal/observability/ring.go) | in-memory ring buffer of the last records |
| [metrics.go](../../internal/observability/metrics.go) | `Metrics`: registry, `/metrics` handler, build info, HTTP families, guarded family constructors |
| [labels.go](../../internal/observability/labels.go) | the allowed label list, `CheckFamilies` |
| [capped.go](../../internal/observability/capped.go) | the `repo` / `subscription` label cap with `_other` |
| [dbgauges.go](../../internal/observability/dbgauges.go) | background evaluation of gauges read from PostgreSQL |
| [requestlog.go](../../internal/observability/requestlog.go), [requestinfo.go](../../internal/observability/requestinfo.go), [recorder.go](../../internal/observability/recorder.go) | request log middleware, the per-request facts handlers fill in |
| [tracing.go](../../internal/observability/tracing.go), [otlp.go](../../internal/observability/otlp.go) | `Tracer`, trace middleware, OTLP export, bounded span queue, span scrubbing |

## Logs

One JSON object per line on standard output (`log.format: text` switches to slog's text handler with the
same field names). Build the logger once in the composition root:

```go
logging := observability.NewLogging(cfg.Log, os.Stdout, observability.WithIdentity(instance, role))
log := logging.Logger()                 // log=app
proxyLog := logging.Component("proxy")  // log=app, component=proxy, level from log.levels
```

- **Streams.** `log` is `app`, `request`, `upstream` or `audit`. `logger.With("log", "audit")` replaces the
  stream; the field is never written twice.
- **Fixed fields.** `time` (RFC 3339 UTC, milliseconds), `level`, `msg`, `log`, `replica`, `role` always;
  `component` when the logger came from `Component`; `trace_id` and `span_id` when the call passes a context
  that carries a trace (`log.InfoContext(ctx, ...)`). Always pass the request context.
- **Keys** are `snake_case` and stable; renaming one is a breaking change that the release notes name.
- **Messages are constants.** Variable data goes into attributes.
- **Component levels** (R-OPS-012) are one `slog.LevelVar` per component. `Logging.SetOverride` /
  `ClearOverride` change them without a restart; the API that stores the override and propagates it is M1 work.
- **Ring buffer.** `logging.Ring().Records()` returns the last `log.ring_buffer_records` records, already
  redacted, for support bundles.

### Redaction (R-OPS-013)

Applied by the handler to every record, in every format, including the ring buffer:

| Rule | Where |
|---|---|
| attribute keys containing `password`, `secret`, `token`, `authorization`, `cookie`, `api_key`, `apikey`, `private_key`, `credential` (case-insensitive) get the value `[REDACTED]`; `token_id` and `token_type` are exempt | `SensitiveKey` |
| `trv_<type>_<body>` becomes `trv_<type>_[REDACTED]`; `Bearer x`, `Basic x`, URL user information, query parameters of `log.request.redact_query_params`, the segment after `/auth/cli/` | `Scrubber.String`, applied to `msg` and every string attribute and error text |
| `observability.Secret` and `config.Secret` print `[REDACTED]` through slog, `fmt` and JSON | `Secret`, `replaceAttr` |

Rules for code:

- Hold every secret in memory as `observability.Secret` (or the loader's `config.Secret`) so even a log call
  with an innocent key cannot print it.
- Errors that leave the process (client responses, support bundles) go through `observability.ScrubError` or
  `Scrubber.Error`.
- Never log bodies or headers other than `User-Agent`, `Content-Type`, `Content-Length`, `Accept`.
- Span attributes are scrubbed again right before export (`scrubExporter`), so a missed call site cannot leak.

## Metrics

`observability.NewMetrics(role, WithMetricsConfig(cfg.Metrics))` owns the registry. It exposes the Go runtime
and process collectors (`go_*`, `process_*`, the only families without the `trove_` prefix),
`trove_build_info{version,revision,goversion,role}`, the HTTP families, `trove_ready`,
`trove_metrics_label_overflow_total`, `trove_tracing_spans_dropped_total` and
`trove_metrics_db_gauges_last_success_timestamp_seconds`.

`m.Handler()` serves `GET /metrics` for the operations listener: Prometheus text 0.0.4, OpenMetrics when the
`Accept` header asks (histograms carry the trace ID of a sampled request as an exemplar), `404` with
`metrics.enabled: false`, `401` without the bearer value with `metrics.auth: static`. The token file is read on
every scrape, so a rotated Secret takes effect without a restart. A scrape reads the registry only.

`role` and `pool` of every other series are target labels the ServiceMonitor attaches (Q-OPS-13); the process
never emits them outside `trove_build_info`.

### How to add a metric

1. Find the family in R-OPS-008, or add it to the spec page of your subsystem under the rules of R-OPS-007.
2. Create it through the guarded constructors, once, where the owning component is built:

   ```go
   queued := m.NewCounterVec("trove_cache_flushes_total", "Cache flushes by cause.", "cache", "reason")
   ```

   `NewCounterVec`, `NewGaugeVec` and `NewHistogramVec` panic at start-up when the name lacks the `trove_`
   prefix, when a counter lacks `_total` (or a non-counter has it), or when a label is not in the allowed list
   of R-OPS-007 ([labels.go](../../internal/observability/labels.go)). If you need a new label, the spec table
   changes first.
3. Label values must be bounded: enumerations in code, handler names from the route table, `repo` and
   `subscription` through `m.Repos().Value(family, key)` / `m.Subscriptions().Value(...)` (cap, `_other`,
   overflow counter). Never a user name, token ID, IP, path, package name, version, trace ID or error text.
4. Take `*observability.Metrics` (or only a `prometheus.Registerer` from `m.Registry()`) in your constructor.
   A family a role does not run must not be registered on that role ("absent, not zero").
5. In your package test call `observability.CheckFamilies` over the gathered registry (or `m.Check()`), so a
   forbidden label fails the build.

### Gauges read from PostgreSQL (Q-OPS-18)

Inventory, queue, backup-run, registry and CI gauges are read by every `web` and `worker` replica in the
background. Register a source; the evaluator calls it once per `metrics.db_gauge_interval` with a deadline, and
a scrape returns the last values without touching PostgreSQL:

```go
desc := prometheus.NewDesc("trove_jobs_queued", "Queued jobs (max across replicas).", []string{"kind"}, nil)
m.DBGauges().Add(observability.GaugeSource{
    Name: "jobs", Descs: []*prometheus.Desc{desc},
    Collect: func(ctx context.Context) ([]prometheus.Metric, error) { /* query on the request pool */ },
})
go m.DBGauges().Run(ctx) // the composition root starts it once
```

A failing source keeps its previous values; `trove_metrics_db_gauges_last_success_timestamp_seconds` advances
only when every source succeeded and is absent until the first success. The help text of such a gauge says to
aggregate with `max`, not `sum`.

## Request log and request facts

`RequestLog(logger, m, opts...)` counts every request in `trove_http_*` and writes the R-OPS-010 record. Options:
`WithListener("client"|"ops")`, `WithRequestRecords(false)` (metrics only, for the ops listener without
`log.request.ops_listener`), `WithRemoteAddr(func)`, `WithRequestScrubber`. Absent values are JSON `null`,
an unknown request length is `-1`.

Handlers tell the middleware what only they know through the request context:

```go
ri := observability.RequestInfoFrom(r.Context()) // nil-safe: every setter ignores a nil receiver
ri.SetHandler("npm.packument")                    // metrics label, handler field, http.route of the span
ri.SetRepo("npm-all", "npm")
ri.SetPrincipal(name, "user", principalID)
ri.SetToken(tokenID, "bearer")                    // the ID, never the token
```

A handler never name-builds the handler label from request data; the label set is capped at 1000 values and
folds into `unmatched`.

## Tracing and request IDs

`observability.Tracing(cfg.Tracing, opts...)` always creates W3C trace IDs, whether or not export is enabled; the
trace ID is the request ID. Middleware order on a listener: `tracer.Middleware()` outermost, then `RequestLog`,
then the application.

| Question | Answer |
|---|---|
| Where does `X-Request-Id` come from? | the trace ID of the server span, set before the handler runs |
| Incoming `traceparent`? | continued per `tracing.incoming_context`: `trusted_proxies` (peer in `WithTrustedProxies(server.trusted_proxies)`), `all`, `none`. Otherwise a new trace starts and the incoming context becomes a span link. A malformed header is ignored. |
| Get the ID in code | `observability.RequestID(ctx)`, `SpanID(ctx)`, `Sampled(ctx)` |
| Start a span for a job or call | `tracer.Start(ctx, name, kind, attrs...)`; outbound calls get theirs from the outbound client |
| Export | `tracing.enabled: true`: OTLP over `http/protobuf` or `grpc`; unset trove keys fall back to `OTEL_EXPORTER_OTLP_*`; resource attributes from `WithTraceResource` plus `OTEL_RESOURCE_ATTRIBUTES` |
| Queue | bounded by `tracing.queue_size`; a full queue drops the span and counts `trove_tracing_spans_dropped_total`; handling never waits |
| Sampling | parent-based with `tracing.sample_ratio`; disabled tracing never samples |

Spans carry attributes under the `trove.` namespace and never user names, e-mail addresses or tokens. Database
spans carry statements with placeholders only.

## Outbound requests

All outbound HTTP goes through [internal/outbound](../../internal/outbound). Its factory adds, per request:
`User-Agent: trove/<version>`, a client span, a `traceparent` only with `tracing.propagate_to_upstreams`, one
`log: upstream` record (R-OPS-011: `purpose`, `repo`, `subscription`, scrubbed `url`, `status`, `error`,
`request_bytes`, `response_bytes`, `duration_ms` including the body, `attempt`, `on_behalf_of`; pass the last two
with `outbound.WithCall`, extra fields with `outbound.WithLogAttrs`) and the families
`trove_outbound_requests_total{kind,code}`, `trove_outbound_request_duration_seconds{kind}` and
`trove_outbound_egress_denied_total{kind}`, where `kind` is the purpose.

The factory (`outbound.New(cfg.Proxy.Outbound, opts...)`; call `Err()` at start-up) applies:

| Setting | Behaviour |
|---|---|
| trust store | system roots (if enabled) plus `proxy.outbound.tls.ca_files`; no switch turns verification off; read at start-up (reload within 60 s, R-REPO-035, is P1 and not built) |
| minimum TLS | `proxy.outbound.tls.min_version` |
| egress policy | `deny_cidrs` / `allow_cidrs` checked in the dialer's `Control` hook after resolution; IPv4-mapped IPv6 is unmapped first; applies to every purpose except `objectstore`; a refusal is `errors.Is(err, outbound.ErrEgressDenied)` |
| proxy | `WithProxyURL(url, bypass...)`, or `proxy.outbound.proxy_from_environment`; behind a proxy trove resolves and checks the target itself first (the proxy's own connection is unchecked, H-6) |
| timeouts | connect 10 s, TLS handshake 10 s, response headers 30 s; overall 30 s for `oidc_discovery`, `jwks`, `webhook`, none for `remote` and `objectstore`; `WithSocketTimeout` maps a remote's socket timeout |
| redirects | at most 5 hops (`WithMaxRedirects`), `https` to `http` refused, `WithRedirectCheck` for format rules |
| compression | not transparent: callers choose `Accept-Encoding` |

Build one client per purpose or per remote repository and keep it: a client owns a connection pool.
`outbound.NewDialer` and the alias `outbound.Dialer` serve drivers (PostgreSQL, Valkey) that need a socket
dialer, so no other package names the dial calls.

## Testing

Subtest names carry the requirement ID (`t.Run("R-OPS-013 …")`); the coverage generator reads them. Use
`tracetest.NewInMemoryExporter` with `WithSpanExporter` to assert spans, `httptest` for the middleware, and
`Metrics.Check()` for the label rules.
