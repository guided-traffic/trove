package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// HeaderRequestID carries the request's trace ID on every client-listener
// response (R-OPS-002, R-OPS-018).
const HeaderRequestID = "X-Request-Id"

// HeaderReplica names the replica that answered, according to
// server.replica_header (R-OPS-002).
const HeaderReplica = "X-Trove-Replica"

// Replica header modes (server.replica_header).
const (
	ReplicaAuthenticated = "authenticated"
	ReplicaAlways        = "always"
	ReplicaNever         = "never"
)

// EndpointEvents receives what the host check of an endpoint chain observes, for
// the endpoint metrics (R-EP-009).
type EndpointEvents interface {
	// Misdirected is called for every 421 answered by the host check.
	Misdirected(endpoint string)
	// ForwardedHostIgnored is called when a trusted peer sent a forwarded host
	// outside the endpoint's host set.
	ForwardedHostIgnored(endpoint string)
}

// Chain is the middleware chain of the client listener. Handler wraps the
// router in the order R-ARCH-005 fixes, outermost first:
//
//  1. panic recovery
//  2. trace context and request ID
//  3. request log and metrics
//  4. in-flight limit
//  5. client address and base URL
//  6. authentication
//  7. rate limits
//  8. the handler (the router)
//
// Every stage but 1, 4 and 5 is injected, so the observability and the identity
// packages plug in without this package importing them. A nil stage is skipped.
type Chain struct {
	Router *Router
	Log    *slog.Logger

	// Endpoint binds the chain to one client endpoint (R-EP-002): step 1 places it
	// in the request context, and the host check of step 5 answers for it. Nil
	// for the ops listener and for tests of the bare chain.
	Endpoint *Endpoint
	// Events receives the endpoint events of step 5; nil discards them.
	Events EndpointEvents

	// Trace is stage 2: it continues or starts the trace context. TraceID returns
	// the trace ID that Trace stored in the context; the chain sends it as
	// X-Request-Id. Without TraceID (or with an empty result) the chain mints a
	// 32-digit hex ID itself, so every response carries one.
	Trace   func(http.Handler) http.Handler
	TraceID func(context.Context) string
	// OnRequest runs right after stage 2, so the trace context and whatever the
	// injected stage keeps in the context exist. r.Pattern is already the route
	// label (R-ARCH-005 step 3). The composition root names the handler for the
	// request log and the metrics here.
	OnRequest func(*http.Request)

	// AccessLog is stage 3.
	AccessLog func(http.Handler) http.Handler

	// MaxInFlight is stage 4: requests beyond it get 503 server_busy with
	// Retry-After: 1. Zero means no limit.
	MaxInFlight int

	// Resolver is stage 5. Annotate, if set, receives the resolved information so
	// the request log can record the client address.
	Resolver *Resolver
	Annotate func(*http.Request, RequestInfo)

	// Authenticate is stage 6. An error is rendered as problem+json (a credential
	// that is present but invalid gives 401, R-ARCH-005). Nil means every caller is
	// anonymous.
	Authenticate func(*http.Request) (Principal, error)

	// RateLimit is stage 7.
	RateLimit func(http.Handler) http.Handler

	// Replica and ReplicaHeader implement X-Trove-Replica (R-OPS-002).
	Replica       string
	ReplicaHeader string

	// Draining, when it returns true, adds Connection: close to every HTTP/1.1
	// response so clients reconnect through the Service to another replica
	// (R-ARCH-024 step 2).
	Draining func() bool
}

// Handler builds the chain around the router.
func (c Chain) Handler() http.Handler {
	var h http.Handler = c.Router
	if c.RateLimit != nil {
		h = c.RateLimit(h)
	}
	h = c.authStage(h)
	h = c.addrStage(h)
	h = c.inflightStage(h)
	if c.AccessLog != nil {
		h = c.AccessLog(h)
	}
	h = c.idStage(h)
	if c.Trace != nil {
		h = c.Trace(h)
	}
	return c.recoverStage(h)
}

// recoverStage is step 1. It also puts the logger in the context, labels the
// request with its pattern for the outer stages, and applies Connection: close.
func (c Chain) recoverStage(next http.Handler) http.Handler {
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), keyLogger, log)
		if c.Endpoint != nil {
			ctx = WithEndpoint(ctx, c.Endpoint)
		}
		r = r.WithContext(ctx)
		// Label the request before any stage copies it: stages 2 and 3 read r.Pattern
		// (the route label), and the mux only sets it on the copy it receives.
		r.Pattern = c.Router.Pattern(r)

		ww := &writer{ResponseWriter: w}
		// Set once for every response of the listener, before any stage can write:
		// an API body, a problem or a package file must never be sniffed into
		// another content type.
		ww.Header().Set("X-Content-Type-Options", "nosniff")
		if c.Draining != nil && c.Draining() && r.ProtoMajor == 1 {
			ww.Header().Set("Connection", "close")
		}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			id := ww.Header().Get(HeaderRequestID)
			log.Error("panic in handler", "request_id", id, "method", r.Method, "path", r.URL.Path,
				"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			if ww.wrote {
				// The status line is out; the only honest signal left is a broken connection.
				panic(http.ErrAbortHandler)
			}
			errs.Write(ww, r, errs.E(errs.Internal, errs.CodeInternal, ""), id, log)
		}()
		next.ServeHTTP(ww, r)
	})
}

// idStage is the second half of step 2: whatever trace context the injected
// stage established, every response carries X-Request-Id.
func (c Chain) idStage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id string
		if c.TraceID != nil {
			id = c.TraceID(r.Context())
		}
		if id == "" {
			id = newTraceID()
		}
		w.Header().Set(HeaderRequestID, id)
		if c.ReplicaHeader == ReplicaAlways && c.Replica != "" {
			w.Header().Set(HeaderReplica, c.Replica)
		}
		r = r.WithContext(context.WithValue(r.Context(), keyRequestID, id))
		if c.OnRequest != nil {
			c.OnRequest(r)
		}
		next.ServeHTTP(w, r)
	})
}

// newTraceID returns 16 random bytes in hex, the shape of a W3C trace ID.
func newTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// inflightStage is step 4. Probes live on the ops listener, so this limit never
// starves them.
func (c Chain) inflightStage(next http.Handler) http.Handler {
	if c.MaxInFlight <= 0 {
		return next
	}
	sem := make(chan struct{}, c.MaxInFlight)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			Problem(w, r, errs.E(errs.Unavailable, CodeServerBusy,
				"The replica is at its concurrent request limit; retry.", errs.WithRetryAfter(time.Second)))
		}
	})
}

// addrStage is step 5.
func (c Chain) addrStage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var info RequestInfo
		if c.Resolver != nil {
			info = c.Resolver.Resolve(r)
		} else {
			info = RequestInfo{Peer: Peer(r), Client: Peer(r)}
		}
		ctx := context.WithValue(r.Context(), keyInfo, info)
		r = r.WithContext(ctx)
		if c.Annotate != nil {
			c.Annotate(r, info)
		}
		if info.ForwardedHostIgnored && c.Events != nil {
			c.Events.ForwardedHostIgnored(c.Endpoint.Name())
		}
		if info.Misdirected {
			// The answer does not depend on the path, so it tells nothing about content.
			if c.Events != nil {
				c.Events.Misdirected(c.Endpoint.Name())
			}
			Problem(w, r, errs.E(errs.Misdirected, errs.CodeMisdirectedRequest,
				"This endpoint does not answer for the host of the request."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authStage is step 6.
func (c Chain) authStage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := Principal{Anonymous: true}
		if c.Authenticate != nil {
			var err error
			if p, err = c.Authenticate(r); err != nil {
				Problem(w, r, err)
				return
			}
		}
		if c.ReplicaHeader == ReplicaAuthenticated && !p.Anonymous && c.Replica != "" {
			w.Header().Set(HeaderReplica, c.Replica)
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}
