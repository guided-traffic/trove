// Package observability holds the logging, metrics and tracing primitives of a
// replica (R-OPS-006 to R-OPS-013, R-OPS-018): the slog logger with its secret
// redaction and ring buffer, the Prometheus registry with the trove_ naming
// and label rules, the request log middleware, the W3C trace context and the
// OpenTelemetry export.
//
// It is a leaf package: every layer may import it, and it imports only
// internal/config and internal/buildinfo.
package observability
