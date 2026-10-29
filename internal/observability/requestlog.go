package observability

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Field limits of the request record (R-OPS-010). The values come from the
// client; without a bound, a 60 KiB User-Agent made every record and every
// ring-buffer slot 60 KiB (and six times that as escaped JSON).
const (
	MaxUserAgent = 512
	MaxPath      = 2 << 10
	maxMethod    = 32
	// TruncatedMarker ends a field that was cut.
	TruncatedMarker = "…[truncated]"
)

// clipField bounds client-supplied text for a log record or a span: invalid
// UTF-8 is replaced (a run by one U+FFFD), secrets are scrubbed, and text above
// max bytes is cut at a rune boundary and ends in TruncatedMarker. The text is
// scrubbed before the cut so that a secret is never cut into a fragment the
// patterns no longer recognise; to bound the work for a huge header, only the
// first 8×max bytes are scrubbed, and a secret would have to start in the kept
// part and be longer than 7×max to lose its redaction.
func clipField(s string, maxBytes int, scrub *Scrubber) string {
	precut := len(s) > 8*maxBytes
	if precut {
		s = s[:8*maxBytes]
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	if scrub != nil {
		s = scrub.String(s)
	}
	if len(s) <= maxBytes {
		if precut {
			return s + TruncatedMarker
		}
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + TruncatedMarker
}

// RequestOption configures RequestLog.
type RequestOption func(*requestOptions)

type requestOptions struct {
	listener   string
	records    bool
	scrub      *Scrubber
	remoteAddr func(*http.Request) string
}

// WithListener sets the listener label and field: "client" (default) or "ops".
func WithListener(name string) RequestOption { return func(o *requestOptions) { o.listener = name } }

// WithRequestRecords switches the log records off (log.request.enabled false,
// or the ops listener without log.request.ops_listener). Metrics are still
// counted.
func WithRequestRecords(enabled bool) RequestOption {
	return func(o *requestOptions) { o.records = enabled }
}

// WithRequestScrubber sets the scrubber for the path field.
func WithRequestScrubber(s *Scrubber) RequestOption { return func(o *requestOptions) { o.scrub = s } }

// WithRemoteAddr sets the function that resolves the client address after the
// trusted-proxy rule (R-OPS-005). A handler may also call RequestInfo.SetRemoteAddr.
func WithRemoteAddr(f func(*http.Request) string) RequestOption {
	return func(o *requestOptions) { o.remoteAddr = f }
}

// RequestLog returns the middleware that counts every request in m and writes
// the log=request record of R-OPS-010 at INFO. Install it inside Tracing's
// middleware so that the record carries the trace ID; without Tracing it makes
// up a request ID and sends it as X-Request-Id itself.
func RequestLog(log *slog.Logger, m *Metrics, opts ...RequestOption) func(http.Handler) http.Handler {
	o := requestOptions{listener: "client", records: true, scrub: defaultScrubber}
	for _, f := range opts {
		f(&o)
	}
	rlog := log.With("log", StreamRequest, "component", "http")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ctx, ri := WithRequestInfo(r.Context())
			if RequestID(ctx) == "" {
				id := randomHex(16)
				ctx = context.WithValue(ctx, requestIDKey{}, id)
				w.Header().Set("X-Request-Id", id)
			}
			var body *countingBody
			if r.Body != nil && r.Body != http.NoBody {
				body = &countingBody{ReadCloser: r.Body}
				r.Body = body
			}
			rec := recorderFor(w)
			g := m.inFlight.WithLabelValues(o.listener)
			g.Inc()
			defer g.Dec()

			next.ServeHTTP(rec, r.WithContext(ctx))

			dur := time.Since(start)
			status := rec.statusOrOK()
			facts := ri.get()
			recv := requestBytes(r, body)
			m.observeRequest(o.listener, facts.handler, r.Method, status, dur, recv, rec.bytes, RequestID(ctx), Sampled(ctx))
			if !o.records {
				return
			}
			handler := facts.handler
			if handler == "" {
				handler = "unmatched"
			}
			remote := facts.remoteAddr
			if remote == "" && o.remoteAddr != nil {
				remote = o.remoteAddr(r)
			}
			peer := peerHost(r.RemoteAddr)
			if remote == "" {
				remote = peer
			}
			principal, ptype, auth := facts.principal, facts.principalType, facts.authMethod
			if principal == "" {
				principal = "anonymous"
			}
			if ptype == "" {
				ptype = "anonymous"
			}
			if auth == "" {
				auth = "none"
			}
			rlog.LogAttrs(ctx, slog.LevelInfo, "request completed",
				slog.String("trace_id", RequestID(ctx)),
				nullable("span_id", SpanID(ctx)),
				slog.String("listener", o.listener),
				slog.String("remote_addr", remote),
				slog.String("peer_addr", peer),
				slog.String("method", clipField(r.Method, maxMethod, nil)),
				slog.String("path", clipField(r.URL.RequestURI(), MaxPath, o.scrub)),
				slog.String("handler", handler),
				slog.Int("status", status),
				slog.Int64("request_bytes", recv),
				slog.Int64("response_bytes", rec.bytes),
				slog.Float64("duration_ms", float64(dur.Microseconds())/1000),
				slog.String("user_agent", clipField(r.UserAgent(), MaxUserAgent, o.scrub)),
				slog.String("principal", principal),
				slog.String("principal_type", ptype),
				nullable("principal_id", facts.principalID),
				nullable("token_id", facts.tokenID),
				slog.String("auth_method", auth),
				nullable("repo", facts.repo),
				nullable("format", facts.format),
				nullable("cache", facts.cache),
				nullable("deny_reason", facts.denyReason),
				nullable("limiter", facts.limiter),
				nullable("limit_mode", facts.limitMode),
			)
		})
	}
}

// requestBytes reports the body bytes received: the count of bytes the handler
// read, or -1 when the length is unknown (a chunked body nobody read).
func requestBytes(r *http.Request, body *countingBody) int64 {
	if body == nil {
		return 0
	}
	n := body.n.Load()
	if n == 0 && r.ContentLength < 0 {
		return -1
	}
	return n
}

// nullable renders an empty value as JSON null so every key is always present.
func nullable(key, v string) slog.Attr {
	if v == "" {
		return slog.Any(key, nil)
	}
	return slog.String(key, v)
}
