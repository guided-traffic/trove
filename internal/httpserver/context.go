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
)

// RequestInfo is what the chain resolves once per request (R-ARCH-005 step 5).
// No handler, format or job reads Host, X-Forwarded-* or Forwarded itself
// (R-ARCH-006).
type RequestInfo struct {
	// Peer is the TCP peer.
	Peer netip.Addr
	// Client is the address of the caller after the trusted-proxy rule.
	Client netip.Addr
	// BaseURL is the origin generated URLs are built from, without a trailing
	// slash (R-OPS-005).
	BaseURL string
	// BaseURLSource is "config" when BaseURL comes from server.base_url or an
	// additional base URL, "request" when it was derived from the request
	// (development only).
	BaseURLSource string
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

// Principal is the authenticated caller, or the anonymous one. It is the seam
// between the authentication stage (R-ARCH-005 step 6) and the handlers; the
// identity packages fill it in a later milestone.
type Principal struct {
	// Anonymous is true when the request carried no credentials.
	Anonymous bool
	// Name is the user, service-account or CI principal name; empty when anonymous.
	Name string
}

// PrincipalFrom returns the caller; anonymous outside the chain.
func PrincipalFrom(ctx context.Context) Principal {
	if p, ok := ctx.Value(keyPrincipal).(Principal); ok {
		return p
	}
	return Principal{Anonymous: true}
}

// WithPrincipal stores the caller; the authentication stage and tests use it.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// Problem renders err as problem+json with the request's ID and logger
// (R-ARCH-023, R-API-009).
func Problem(w http.ResponseWriter, r *http.Request, err error) {
	errs.Write(w, r, err, RequestID(r.Context()), Logger(r.Context()))
}

// CodeServerBusy is the problem code of the in-flight limit (R-ARCH-005 step 4).
const CodeServerBusy = errs.CodeServerBusy
