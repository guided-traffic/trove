package outbound

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// roundTripper wraps the transport of one client with the User-Agent, trace
// propagation, the client span, the upstream log record and the metrics.
type roundTripper struct {
	f       *Factory
	purpose string
	attrs   []slog.Attr
	base    http.RoundTripper
	hop     HopPolicy
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the pool.
func (rt *roundTripper) CloseIdleConnections() {
	if c, ok := rt.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	ctx := req.Context()
	out := req.Clone(ctx)
	if rt.hop != nil {
		hop := hopOf(req)
		rt.hop.Decorate(out, hop)
		if cp, ok := rt.hop.(ClientCertPolicy); ok {
			out = out.WithContext(withCertAllowed(ctx, cp.AllowClientCertificate(out, hop)))
		}
	}
	if out.Header.Get("User-Agent") == "" {
		out.Header.Set("User-Agent", rt.f.userAgent())
	}
	endSpan := func(int, error) {}
	if rt.f.tracer != nil {
		var sctx context.Context
		sctx, endSpan = rt.f.tracer.StartClient(out.Context(), req.Method, req.URL)
		out = out.WithContext(sctx)
		rt.f.tracer.Inject(sctx, out.Header)
	}
	resp, err := rt.base.RoundTrip(out)
	rec := &record{rt: rt, req: req, start: start, endSpan: endSpan, call: callFrom(ctx)}
	if err != nil {
		rec.finish(0, 0, err)
		return nil, err
	}
	resp.Body = &trackedBody{ReadCloser: resp.Body, rec: rec, status: resp.StatusCode}
	return resp, nil
}

// record is the completion of one outbound request: log, metrics, span.
type record struct {
	rt      *roundTripper
	req     *http.Request
	start   time.Time
	endSpan func(int, error)
	call    Call
	once    sync.Once
}

func (r *record) finish(status int, respBytes int64, err error) {
	r.once.Do(func() {
		f := r.rt.f
		dur := time.Since(r.start)
		code := "error"
		if status > 0 {
			code = strconv.Itoa(status)
		}
		if f.requests != nil {
			f.requests.WithLabelValues(r.rt.purpose, code).Inc()
			f.duration.WithLabelValues(r.rt.purpose).Observe(dur.Seconds())
		}
		r.endSpan(status, err)
		if f.upstreamLog == nil {
			return
		}
		reqBytes := r.req.ContentLength
		if r.req.Body == nil || r.req.Body == http.NoBody {
			reqBytes = 0
		}
		attrs := []slog.Attr{
			slog.String("purpose", r.rt.purpose),
			slog.String("method", r.req.Method),
			slog.String("url", f.scrub.URL(r.req.URL)),
			slog.Int("status", status),
			slog.Int64("request_bytes", reqBytes),
			slog.Int64("response_bytes", respBytes),
			slog.Float64("duration_ms", float64(dur.Microseconds())/1000),
			slog.Int("attempt", r.call.Attempt),
			slog.String("on_behalf_of", r.call.OnBehalfOf),
		}
		if err != nil {
			attrs = append(attrs, slog.String("error", f.scrub.Error(err)))
		} else {
			attrs = append(attrs, slog.Any("error", nil))
		}
		have := map[string]bool{}
		for _, a := range r.rt.attrs {
			have[a.Key] = true
		}
		if r.call.Repo != "" {
			attrs = append(attrs, slog.String("repo", r.call.Repo))
			have["repo"] = true
		}
		attrs = append(attrs, nullable("collapsed_waiters", r.call.CollapsedWaiters != nil, func() any { return r.call.CollapsedWaiters() }))
		if r.rt.purpose == PurposeRemote {
			attrs = append(attrs,
				nullable("flight_role", r.call.FlightRole != "", func() any { return r.call.FlightRole }),
				nullable("cache_status", r.call.CacheStatus != nil && r.call.CacheStatus() != "", func() any { return r.call.CacheStatus() }))
		}
		for _, k := range []string{"repo", "subscription"} {
			if !have[k] {
				attrs = append(attrs, slog.Any(k, nil))
			}
		}
		for _, a := range r.rt.attrs {
			if a.Key != "repo" || r.call.Repo == "" {
				attrs = append(attrs, a)
			}
		}
		level := slog.LevelInfo
		if errors.Is(err, ErrEgressDenied) {
			level = slog.LevelWarn // a security event (R-REPO-038)
		}
		f.upstreamLog.With("component", componentOf(r.rt.purpose)).LogAttrs(r.req.Context(), level, "upstream request completed", attrs...)
	})
}

// trackedBody counts the response body and finishes the record when the body
// ends or is closed, because the duration includes the body.
type trackedBody struct {
	io.ReadCloser
	rec    *record
	status int
	n      int64
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if err != nil {
		if errors.Is(err, io.EOF) {
			b.rec.finish(b.status, b.n, nil)
		} else {
			b.rec.finish(b.status, b.n, err)
		}
	}
	return n, err
}

func (b *trackedBody) Close() error {
	err := b.ReadCloser.Close()
	b.rec.finish(b.status, b.n, nil)
	return err
}

// componentOf maps a purpose to the log component of R-OPS-012.
func componentOf(purpose string) string {
	switch purpose {
	case PurposeWebhook:
		return "webhooks"
	case PurposeOIDCDiscovery, PurposeJWKS:
		return "auth"
	case PurposeObjectStore:
		return "storage"
	}
	return "proxy"
}

// nullable is the attribute key with the value, or null when there is none.
func nullable(key string, ok bool, v func() any) slog.Attr {
	if !ok {
		return slog.Any(key, nil)
	}
	return slog.Any(key, v())
}
