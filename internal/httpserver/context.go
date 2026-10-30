package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/guided-traffic/trove/internal/errs"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyLogger
	keyInfo
	keyPrincipal
	keyShapes
)

// RequestInfo is what the chain resolves once per request (R-ARCH-005 step 5).
// No handler, format or job reads Host, X-Forwarded-* or Forwarded itself
// (R-ARCH-006).
type RequestInfo struct {
	// Peer is the TCP peer.
	Peer netip.Addr
	// Client is the address of the caller after the trusted-proxy rule.
	Client netip.Addr
	// UserAgent is the User-Agent header of the request, for the source of an audit
	// event (R-OPS-015); the chain fills it where it resolves the client address.
	UserAgent string
	// BaseURL is the origin generated URLs are built from, without a trailing
	// slash (R-OPS-005).
	BaseURL string
	// BaseURLSource is "config" when BaseURL comes from server.base_url or an
	// additional base URL, "request" when it was derived from the request
	// (development only).
	BaseURLSource string
	// Host is the effective host: the request's own host, or a forwarded host
	// within the endpoint's host set (R-EP-003). Empty outside an endpoint chain.
	Host string
	// HostLabel is the label a wildcard host matched (R-EP-003), "" otherwise.
	HostLabel string
	// RootRepository is the repository the effective host's root is bound to
	// (R-EP-006), "" when unbound. No route reads it in the MVP.
	RootRepository string
	// ForwardedHostIgnored is true when a trusted peer sent a forwarded host
	// outside the endpoint's host set.
	ForwardedHostIgnored bool
	// Misdirected is true when the endpoint checks hosts and the effective host
	// is not in its set; the chain answers 421.
	Misdirected bool
}

// InfoFrom returns the resolved request information; the zero value outside the
// chain.
func InfoFrom(ctx context.Context) RequestInfo {
	v, _ := ctx.Value(keyInfo).(RequestInfo)
	return v
}

// RequestID returns the request's ID, the trace ID the client receives as
// X-Request-Id. It is empty outside the chain.
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(keyRequestID).(string)
	return v
}

// Logger returns the request-scoped logger, or a discarding one outside the chain.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(keyLogger).(*slog.Logger); ok {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

// PrincipalFrom returns the caller; anonymous outside the chain.
func PrincipalFrom(ctx context.Context) Principal {
	if p, ok := ctx.Value(keyPrincipal).(Principal); ok {
		return p
	}
	return AnonymousPrincipal()
}

// WithPrincipal stores the caller; the authentication stage and tests use it.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// Problem renders err as problem+json with the request's ID and logger
// (R-ARCH-023, R-API-009).
//
// A path that has an error shape of its own (Router.HandleErrorShape: a format's
// protocol answers in its own body and with its own challenge) is rendered by it,
// so a refusal of the chain's own stages - authentication, the host check, the
// in-flight limit, a rate limit - reaches a package client in the form it parses
// (R-ARCH-023, Q-ARCH-214).
func Problem(w http.ResponseWriter, r *http.Request, err error) {
	if sh := shapeFor(r); sh != nil {
		sh(w, r, err)
		return
	}
	errs.Write(w, r, err, RequestID(r.Context()), Logger(r.Context()))
}

// shapeFor returns the error shape of the request's path, nil for none.
func shapeFor(r *http.Request) ShapeFunc {
	sh, _ := r.Context().Value(keyShapes).(*shapeTable)
	if sh == nil {
		return nil
	}
	return sh.forPath(r.URL.Path)
}

// CodeServerBusy is the problem code of the in-flight limit (R-ARCH-005 step 4).
const CodeServerBusy = errs.CodeServerBusy
