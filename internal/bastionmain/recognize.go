package bastionmain

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
)

// RecognizeOptions configure [Recognize].
type RecognizeOptions struct {
	// Tokens validates a Bastion token (work package 1). Nil makes every Bastion token invalid: a main
	// installation that cannot validate one recognizes no Bastion.
	Tokens TokenAuthenticator
	// Metrics counts the interface requests; nil counts nothing.
	Metrics *Metrics
	// Throttle returns the failed-credential throttle of the request's client address (R-AUTH-038), nil for
	// none. The authenticator asks it when the token is not in the replica's cache and refuses the lookup
	// while the address is in throttled mode (429), and every token the authenticator refuses counts as a
	// failure of the address: the stage runs before the authentication stage, so without it a flood of
	// well-formed tokens would reach the token store unthrottled.
	Throttle func(r *http.Request) Throttle
	// Source returns the client address and the user agent of the request as the chain resolved them before
	// a Bastion's forwarded headers count (server.trusted_proxies only): the address the token came from,
	// which token.rejected names and the token's last use records (R-BAS-017). Nil names none.
	Source func(r *http.Request) (clientAddr, userAgent string)
	// Forwarded re-derives, for a recognized request, what the chain resolved from the forwarded headers -
	// the client address, the effective host and the response base URL - with the Bastions' egress addresses
	// (bastions.trusted_proxies) trusted besides server.trusted_proxies, and returns the context the request
	// is served with (R-BAS-017, Q-BAS-116). It runs after the token check, so the headers of a request
	// without a valid Bastion token never count through it. Nil keeps what the chain resolved.
	Forwarded func(r *http.Request) context.Context
	// Problem renders an error as the problem details of the management API, whatever the path: the
	// answers of this stage concern the Bastion, never the caller, so a package route does not render
	// them in its format's shape (R-BAS-008). The composition root passes errs.Write with the request's ID
	// and logger.
	Problem func(w http.ResponseWriter, r *http.Request, err error)
}

// Throttle is the failed-credential throttle of one client address (R-AUTH-038): token.Throttle, which the
// authentication stage uses, has the same methods.
type Throttle interface {
	// Exceeded reports whether the address is in throttled mode.
	Exceeded(ctx context.Context) bool
	// Count records one rejected credential from the address.
	Count(ctx context.Context)
}

// ThrottleScope prefixes the bucket of the throttle of rejected Bastion tokens: they count against a budget of
// their own, so the rejected credentials of the authentication stage never refuse a Bastion token and the other
// way round.
const ThrottleScope = "bastion:"

// UnknownBastion is the bastion label of an interface request whose token named no registration.
const UnknownBastion = "unknown"

// maxReplicaName bounds the Trove-Bastion-Replica value taken into a Recognition: a pod name is at most 253
// characters.
const maxReplicaName = 253

// Recognize is the Recognize stage of the Bastion interface (R-BAS-008, R-BAS-009, Q-BAS-52): it runs before the
// authentication and decides, for every request of the default endpoint, whether it is a request of a Bastion.
//
//   - A request that carries Trove-Bastion-Authorization is a forward, whatever its path; the token is the
//     Bearer credential of that header. A request without it is an interface call when its path, decoded
//     segment by segment as the router decodes it, lies below [iface.PathPrefix]; the token is then the
//     Bearer credential of Authorization (Q-BAS-115). Only a value that has the prefix of a Bastion token is
//     looked at: any other credential leaves the request ordinary, with no Recognition and no marking, and
//     every Trove-Bastion-* header is ignored.
//   - A Bastion token the authenticator refuses answers 401 bastion_token_invalid; one it does not look up
//     because the sender's address is throttled answers 429 rate_limited. A failure of the store is a 503, not
//     a verdict on the token.
//   - A request with a valid token whose Trove-Bastion-Interface is missing or not [iface.InterfaceVersion]
//     answers 400 bastion_interface_mismatch with the member interfaceVersion, and is served in no other way.
//   - A forward whose path is not one of the package clients' families (/api/<format>/ and /files/, decoded as
//     the router decodes it) answers 404 not_found: a Bastion forwards nothing else, and no other route runs
//     for it, with the caller's credential or without one (R-BAS-014, Q-BAS-115).
//   - Otherwise the request carries a [Recognition]; every answer to it has Trove-Bastion-Interface. On an
//     interface route the validated token is taken out of Authorization, so no later stage sees it; on a
//     forwarded request Trove-Bastion-Authorization is taken out of the request the stages behind receive
//     (nothing downstream needs it, and a remote repository must never be able to forward it). The forwarded
//     headers are re-derived with the Bastions' egress addresses trusted ([RecognizeOptions.Forwarded]).
//
// The 401, 404, 429 and 400 answers are written by o.Problem as the management API's problem details, also on
// a package route. Interface requests are counted by registration, operation and outcome.
func Recognize(o RecognizeOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			segs := routingSegments(r.URL.EscapedPath())
			forward := len(r.Header.Values(iface.HeaderAuthorization)) > 0
			route := !forward && interfacePath(segs)
			raw := bastionCredential(r, route)
			if !strings.HasPrefix(raw, iface.TokenPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			op := operationOf(r, route)
			req := AuthRequest{}
			if o.Throttle != nil {
				req.Throttle = o.Throttle(r)
			}
			if o.Source != nil {
				req.ClientAddr, req.UserAgent = o.Source(r)
			}
			tok, err := authenticate(r.Context(), o.Tokens, raw, req)
			if err != nil {
				o.refuse(w, r, op, req.Throttle, err)
				return
			}
			if v := r.Header.Values(iface.HeaderInterface); len(v) != 1 || strings.TrimSpace(v[0]) != strconv.Itoa(iface.InterfaceVersion) {
				o.count(tok.Name, op, OutcomeMismatch)
				w.Header().Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
				o.problem(w, r, errs.E(errs.Invalid, errs.CodeBastionInterfaceMismatch,
					"The Bastion speaks another version of the Bastion interface than this installation ("+strconv.Itoa(iface.InterfaceVersion)+").",
					errs.WithBadRequest(), errs.WithExtension(iface.ProblemMemberInterfaceVersion, iface.InterfaceVersion)))
				return
			}
			w.Header().Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
			if !route && !forwardablePath(segs) {
				// The route-family backstop: whatever the Bastion sent, a forward reaches no route of the management
				// API, the login tree or the UI. The answer is the one every path without a route gets.
				o.problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
				return
			}
			rec := Recognition{
				Bastion: tok.Name, BastionID: tok.BastionID, TokenID: tok.TokenID, TokenExpiresAt: tok.ExpiresAt,
				Interface: iface.InterfaceVersion, Replica: clip(r.Header.Get(iface.HeaderReplica), maxReplicaName), Route: route,
				Probe: !route && r.Header.Get(iface.HeaderProbe) == iface.ProbeValue && r.Method != http.MethodGet && r.Method != http.MethodHead,
			}
			// The stages behind this one get a request of their own: the credential that proved the
			// Bastion is not in it, and the forwarded headers count with the Bastion's egress trusted.
			ctx := WithRecognition(r.Context(), rec)
			if o.Forwarded != nil {
				ctx = o.Forwarded(r.WithContext(ctx))
			}
			r2 := r.Clone(ctx)
			if route {
				r2.Header.Del("Authorization")
			} else {
				r2.Header.Del(iface.HeaderAuthorization)
			}
			if !route || op == "" {
				next.ServeHTTP(w, r2)
				return
			}
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r2)
			o.count(tok.Name, op, outcomeOf(sw.status))
		})
	}
}

// refuse answers a Bastion token the authenticator did not accept: 429 for a throttled address, 401 for a
// token it refused (counted as a failure of the address), 503 for a store that did not answer.
func (o RecognizeOptions) refuse(w http.ResponseWriter, r *http.Request, op string, th Throttle, err error) {
	switch {
	case errors.Is(err, ErrThrottled):
		o.count(UnknownBastion, op, OutcomeRefused)
		o.problem(w, r, errs.E(errs.RateLimited, errs.CodeRateLimited, "Too many rejected credentials from this address.", errs.WithRetryAfter(throttleWindow)))
	case errors.Is(err, ErrTokenInvalid):
		if th != nil {
			th.Count(r.Context())
		}
		o.count(UnknownBastion, op, OutcomeInvalid)
		o.problem(w, r, errs.E(errs.Unauthenticated, errs.CodeBastionTokenInvalid,
			"The Bastion token is invalid, revoked, expired or its registration is disabled.",
			errs.WithChallenge(`Bearer realm="trove", error="invalid_token"`)))
	default:
		o.count(UnknownBastion, op, OutcomeError)
		o.problem(w, r, storeError(err))
	}
}

// routingSegments splits an escaped request path into its segments and decodes each one on its own, which is
// how the router (net/http.ServeMux) matches a request against its patterns: "/api/%76%31/tokens" is api, v1,
// tokens - a route of the management API - and "/api%2Fv1/tokens" is api/v1, tokens, which no pattern holds. A
// segment that does not decode stays as it is, as the router keeps it. The decoded r.URL.Path is not that path.
func routingSegments(escaped string) []string {
	parts := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	for i, p := range parts {
		if d, err := url.PathUnescape(p); err == nil {
			parts[i] = d
		}
	}
	return parts
}

// interfacePath reports whether the routing segments lie below iface.PathPrefix (/api/v1/bastion/).
func interfacePath(segs []string) bool {
	return len(segs) >= 4 && segs[0] == "api" && segs[1] == "v1" && segs[2] == "bastion"
}

// forwardablePath reports whether the routing segments lie in a family a Bastion forwards: /files/… or
// /api/<format>/…, where <format> is any segment but v1 (R-BAS-003, R-BAS-014). Every route of these families
// tells the marking stage its kind before it decides or reads a body (the format dispatcher, the raw file
// handler); no route of any other family does.
func forwardablePath(segs []string) bool {
	switch {
	case len(segs) >= 2 && segs[0] == "files":
		return true
	case len(segs) >= 2 && segs[0] == "api":
		return segs[1] != "" && segs[1] != "v1"
	}
	return false
}

func (o RecognizeOptions) problem(w http.ResponseWriter, r *http.Request, err error) {
	if o.Problem != nil {
		o.Problem(w, r, err)
		return
	}
	errs.Write(w, r, err, "", nil)
}

func (o RecognizeOptions) count(bastion, op, outcome string) {
	if o.Metrics != nil && op != "" {
		o.Metrics.Interface.WithLabelValues(bastion, op, outcome).Inc()
	}
}

// authenticate asks the authenticator, treating a missing one as a refusal of every token.
func authenticate(ctx context.Context, a TokenAuthenticator, raw string, req AuthRequest) (BastionToken, error) {
	if a == nil {
		return BastionToken{}, ErrTokenInvalid
	}
	return a.Authenticate(ctx, raw, req)
}

// storeError is the answer to a failure of the token store: an error already in the registry passes, any other
// is a 503 like the authentication stage's (R-AUTH-023), never a 401 that would make the Bastion discard a
// working token.
func storeError(err error) error {
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The Bastion token cannot be checked right now.", errs.WithCause(err))
}

// bastionCredential returns the raw credential of the request that may be a Bastion token: the Bearer value
// of Authorization on an interface call, of Trove-Bastion-Authorization on any other request. Only the first
// header line counts, as for Authorization everywhere else; any other scheme is no credential of a Bastion.
func bastionCredential(r *http.Request, route bool) string {
	name := iface.HeaderAuthorization
	if route {
		name = "Authorization"
	}
	v := r.Header.Values(name)
	if len(v) == 0 {
		return ""
	}
	scheme, rest, ok := strings.Cut(strings.TrimSpace(v[0]), " ")
	if !ok || !strings.EqualFold(scheme, iface.BearerScheme) {
		return ""
	}
	return strings.TrimSpace(rest)
}

// operationOf names the interface operation of a route request, "" for a path below the prefix that no
// operation serves and for a forwarded request.
func operationOf(r *http.Request, route bool) string {
	if !route {
		return ""
	}
	switch r.URL.Path {
	case iface.PathHandshake:
		return iface.OperationHandshake
	case iface.PathCheck:
		return iface.OperationCheck
	case iface.PathFeed:
		return iface.OperationFeed
	case iface.PathDownloads:
		return iface.OperationDownloads
	}
	return ""
}

// outcomeOf is the outcome label of an answered interface request.
func outcomeOf(status int) string {
	switch {
	case status == http.StatusNotImplemented:
		return OutcomeNotServed
	case status >= 500:
		return OutcomeError
	case status >= 400:
		return OutcomeRefused
	}
	return OutcomeOK
}

// clip cuts s to at most n bytes at a character boundary, and drops control characters: the value is
// attacker-influenced text that reaches the database and the audit trail.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) { // the cut fell inside a multi-byte character
		s = s[:len(s)-1]
	}
	return s
}

// statusWriter records the status of the answer for the outcome label.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
