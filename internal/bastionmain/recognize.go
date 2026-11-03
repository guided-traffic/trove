package bastionmain

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	// none. A request that carries a Bastion token is refused 429 while its address is in throttled mode,
	// before the token is looked up, and every token the authenticator refuses counts as a failure of the
	// address: the stage runs before the authentication stage, so without it a flood of well-formed tokens
	// would reach the token store unthrottled.
	Throttle func(r *http.Request) Throttle
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
//   - The token is the Bearer credential of Trove-Bastion-Authorization on a forwarded request and of
//     Authorization on a route below [iface.PathPrefix]. Only a value that has the prefix of a Bastion token
//     is looked at: any other credential leaves the request ordinary, with no Recognition and no marking, and
//     every Trove-Bastion-* header is ignored.
//   - A Bastion token the authenticator refuses answers 401 bastion_token_invalid. A failure of the store is
//     a 503, not a verdict on the token.
//   - A request with a valid token whose Trove-Bastion-Interface is missing or not [iface.InterfaceVersion]
//     answers 400 bastion_interface_mismatch with the member interfaceVersion, and is served in no other way.
//   - Otherwise the request carries a [Recognition]; every answer to it has Trove-Bastion-Interface. On an
//     interface route the validated token is taken out of Authorization, so no later stage sees it; on a
//     forwarded request Trove-Bastion-Authorization is taken out of the request the stages behind receive
//     (nothing downstream needs it, and a remote repository must never be able to forward it).
//
// The 401 and 400 answers are written by o.Problem as the management API's problem details, also on a package
// route. Interface requests are counted by registration, operation and outcome.
func Recognize(o RecognizeOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := strings.HasPrefix(r.URL.Path, iface.PathPrefix)
			raw := bastionCredential(r, route)
			if !strings.HasPrefix(raw, iface.TokenPrefix) {
				next.ServeHTTP(w, r)
				return
			}
			op := operationOf(r, route)
			var th Throttle
			if o.Throttle != nil {
				th = o.Throttle(r)
			}
			if th != nil && th.Exceeded(r.Context()) {
				o.count(UnknownBastion, op, OutcomeRefused)
				o.problem(w, r, errs.E(errs.RateLimited, errs.CodeRateLimited, "Too many rejected credentials from this address.", errs.WithRetryAfter(time.Minute)))
				return
			}
			tok, err := authenticate(r.Context(), o.Tokens, raw)
			if err != nil {
				if errors.Is(err, ErrTokenInvalid) {
					if th != nil {
						th.Count(r.Context())
					}
					o.count(UnknownBastion, op, OutcomeInvalid)
					o.problem(w, r, errs.E(errs.Unauthenticated, errs.CodeBastionTokenInvalid,
						"The Bastion token is invalid, revoked, expired or its registration is disabled.",
						errs.WithChallenge(`Bearer realm="trove", error="invalid_token"`)))
					return
				}
				o.count(UnknownBastion, op, OutcomeError)
				o.problem(w, r, storeError(err))
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
			rec := Recognition{
				Bastion: tok.Name, BastionID: tok.BastionID, TokenID: tok.TokenID, TokenExpiresAt: tok.ExpiresAt,
				Interface: iface.InterfaceVersion, Replica: clip(r.Header.Get(iface.HeaderReplica), maxReplicaName), Route: route,
				Probe: !route && r.Header.Get(iface.HeaderProbe) == iface.ProbeValue && r.Method != http.MethodGet && r.Method != http.MethodHead,
			}
			w.Header().Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
			// The stages behind this one get a request of their own: the credential that proved the
			// Bastion is not in it.
			r2 := r.Clone(WithRecognition(r.Context(), rec))
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
func authenticate(ctx context.Context, a TokenAuthenticator, raw string) (BastionToken, error) {
	if a == nil {
		return BastionToken{}, ErrTokenInvalid
	}
	return a.Authenticate(ctx, raw)
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
// of Authorization on an interface route, of Trove-Bastion-Authorization on any other request. Only the first
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
