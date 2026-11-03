package engine

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability"
)

// keyPattern is the repository key rule of INV-5 (internal/repository.KeyPattern, which the Bastion may not
// import); TestR_BAS_018_TheKeyRuleIsTheOneOfINV5 holds the two equal.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// rq is one request on its way through the flow.
type rq struct {
	w    http.ResponseWriter
	out  *countingWriter
	r    *http.Request
	e    *engine
	span trace.Span

	start      time.Time
	peer       netip.Addr
	client     netip.Addr
	clientText string
	addr       string // the guard's key of the client: an IPv6 address as its /64

	hasAuth bool
	cred    credential
	credAns *answerVal
	subject iface.Subject

	format, repo string
	target       string
	baseKey      string
	cacheKey     string

	class, result, reason string
	counted               bool
	collapsed             bool
	mainRequestID         string
	limiter               string

	// flight is the flight this request leads, nil if it leads none; hold is the context of the request to the
	// main installation, which a fill takes over from the request (Q-BAS-123).
	flight *handle
	hold   *mainHold
}

// countingWriter counts the body bytes served by their source (trove_bastion_bytes_served_total).
type countingWriter struct {
	http.ResponseWriter
	q      *rq
	source string
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	if n > 0 {
		c.q.e.d.Metrics.BytesServed.WithLabelValues(c.source).Add(float64(n))
	}
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Flush implements http.Flusher.
func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (q *rq) ctx() context.Context { return q.r.Context() }

// finish records the class and result of the request once; the first call wins.
func (q *rq) finish(class, result string) {
	if q.counted {
		return
	}
	q.counted, q.class, q.result = true, class, result
	q.e.d.Metrics.Requests.WithLabelValues(class, result).Inc()
}

// ServeHTTP implements [http.Handler]: the steps of R-BAS-018 in their order, the first that answers ends it.
func (e *engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := e.newRq(w, r)
	defer q.end()
	if !e.stepHost(q) || !e.stepBlocked(q) || !e.stepPath(q) || !e.stepLimits(q) {
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if e.stepCredentials(q) {
			e.read(q)
		}
	case http.MethodOptions:
		e.refuse(q, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "A Bastion serves GET and HEAD.", errs.WithAllow(http.MethodGet, http.MethodHead)), metrics.RefusedNotServed)
	default:
		e.write(q)
	}
}

func (e *engine) newRq(w http.ResponseWriter, r *http.Request) *rq {
	q := &rq{w: w, r: r, e: e, start: e.now(), span: trace.SpanFromContext(r.Context())}
	q.out = &countingWriter{ResponseWriter: w, q: q, source: metrics.SourceMain}
	q.peer, q.client = e.addresses(r)
	if q.client.IsValid() {
		q.clientText = q.client.Unmap().String()
		q.addr = guard.AddressKey(q.client)
	} else {
		q.addr = "invalid"
	}
	q.hasAuth = strings.TrimSpace(r.Header.Get("Authorization")) != ""
	q.target = r.RequestURI
	if !strings.HasPrefix(q.target, "/") {
		q.target = r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			q.target += "?" + r.URL.RawQuery
		}
	}
	return q
}

// addresses returns the TCP peer and the client address as the chain resolved them (R-ARCH-006 with the
// Bastion's own trusted proxies); without a chain the peer is the client.
func (e *engine) addresses(r *http.Request) (peer, client netip.Addr) {
	info := httpserver.InfoFrom(r.Context())
	peer, client = info.Peer, info.Client
	if !peer.IsValid() {
		if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
			peer = ap.Addr()
		} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			peer, _ = netip.ParseAddr(host)
		}
	}
	if !client.IsValid() {
		client = peer
	}
	return peer.Unmap(), client
}

// end finishes the request: the counter, the request log fields and the span of R-BAS-042.
func (q *rq) end() {
	if !q.counted {
		q.finish(metrics.ClassPass, metrics.ResultError)
	}
	if ri := observability.RequestInfoFrom(q.ctx()); ri != nil {
		ri.SetRemoteAddr(q.clientText)
		if q.repo != "" {
			ri.SetRepo(q.repo, q.format)
		}
		ri.SetCache(q.result)
		if q.subject.TokenID != "" {
			ri.SetToken(q.subject.TokenID, q.cred.method)
		}
		if q.reason != "" {
			ri.SetDenyReason(q.reason)
		}
		if q.limiter != "" {
			ri.SetLimit(q.limiter, "bastion")
		}
		ri.SetField("bastion_class", q.class)
		ri.SetField("bastion_result", q.result)
		if q.cred.hint != "" {
			ri.SetField("token_hint", q.cred.hint)
		}
		if q.mainRequestID != "" {
			ri.SetField("main_request_id", q.mainRequestID)
		}
	}
	q.span.SetAttributes(attribute.String("trove.bastion.class", q.class), attribute.String("trove.bastion.result", q.result))
	if q.reason != "" {
		q.span.SetAttributes(attribute.String("trove.bastion.refusal", q.reason))
	}
}

// stepHost is step 1: the effective host must be the host of server.base_url (421 misdirected_request).
func (e *engine) stepHost(q *rq) bool {
	if e.effectiveHost(q) == e.baseHost {
		return true
	}
	e.refuse(q, errs.E(errs.Misdirected, errs.CodeMisdirectedRequest, "This Bastion does not answer for the host of the request."), metrics.RefusedNotServed)
	return false
}

// effectiveHost is the request's host, or the host a trusted proxy forwarded (R-ARCH-006, R-EP-003). An IP
// literal and a missing host are never the Bastion's host.
func (e *engine) effectiveHost(q *rq) string {
	h := q.r.Host
	if e.trustedPeer(q.peer) {
		if fh := e.forwardedHost(q.r.Header); fh != "" {
			h = fh
		}
	}
	h = strings.ToLower(strings.TrimSuffix(hostOnly(h), "."))
	if h == "" {
		return ""
	}
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return ""
	}
	return h
}

func (e *engine) trustedPeer(a netip.Addr) bool {
	for _, p := range e.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// forwardedHost reads the host the right-most hop reports, from the family server.forwarded_header selects.
func (e *engine) forwardedHost(h http.Header) string {
	if !e.forwarded {
		vals := h.Values("X-Forwarded-Host")
		if len(vals) == 0 {
			return ""
		}
		parts := strings.Split(vals[len(vals)-1], ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	vals := h.Values("Forwarded")
	if len(vals) == 0 {
		return ""
	}
	elems := strings.Split(vals[len(vals)-1], ",")
	for _, pair := range strings.Split(elems[len(elems)-1], ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(k, "host") {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// stepBlocked is step 2: a blocked address answers 429 address_blocked.
func (e *engine) stepBlocked(q *rq) bool {
	return e.admit(q, e.d.Guard.Blocked(q.ctx(), q.addr))
}

// stepPath is step 3: only /api/<format>/<key>/… and /files/<key>/… are served (R-BAS-003).
func (e *engine) stepPath(q *rq) bool {
	format, repo, ok := classifyPath(q.r.URL.EscapedPath())
	if !ok {
		e.refuse(q, errs.E(errs.NotFound, errs.CodeBastionNotServed, "This path is not served by a Bastion."), metrics.RefusedNotServed)
		return false
	}
	q.format, q.repo = format, repo
	return true
}

// classifyPath reads the family, the format and the repository key from the path as received. The key rule is
// INV-5's; the Bastion reads no other part of a path (R-BAS-018). The main installation's router decodes every
// segment before it compares it (net/http.ServeMux), so a segment that names the family, the format or the key
// in an encoded spelling (/api/%761/users is /api/v1/users there) is not one the Bastion can classify by what it
// received: such a path is not served, and so is a path with a dot segment, which the router answers with a
// redirect to the cleaned path (R-BAS-003, Q-BAS-121).
func classifyPath(p string) (format, repo string, ok bool) {
	if !strings.HasPrefix(p, "/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	// The segments the classification reads: the family and the key of /files/<key>, and the family, the format
	// and the key of /api/<format>/<key>.
	named := 3
	if parts[0] == "files" {
		named = 2
	}
	for i, seg := range parts {
		if seg == "." || seg == ".." || (i < named && strings.Contains(seg, "%")) {
			return "", "", false
		}
	}
	switch parts[0] {
	case "files":
		if len(parts) >= 2 && keyPattern.MatchString(parts[1]) {
			return "files", parts[1], true
		}
	case "api":
		if len(parts) >= 3 && parts[1] != "" && parts[1] != "v1" && keyPattern.MatchString(parts[2]) {
			return parts[1], parts[2], true
		}
	}
	return "", "", false
}

// stepLimits is step 4, first half: the per-address request limit of the class (R-BAS-035).
func (e *engine) stepLimits(q *rq) bool {
	return e.admit(q, e.d.Guard.Request(q.ctx(), q.addr, guard.Credentialed(q.hasAuth)))
}

// stepCredentials is step 4, second half (R-BAS-019, R-BAS-036): the credential of a read, refused before any
// request when it is malformed, a Bastion token or of an unknown type; anonymous only while the main
// installation admits anonymous requests; the failed-authentication timeout for a credential the Bastion has
// no usable answer for.
func (e *engine) stepCredentials(q *rq) bool {
	c, ok := parseCredential(q.r.Header)
	if !ok {
		q.cred = c
		e.d.Guard.FailedAuth(q.ctx(), q.addr)
		e.refuse(q, errInvalidToken(q.r), metrics.RefusedInvalid)
		return false
	}
	q.cred = c
	if c.anonymous {
		st := e.d.Link.State()
		switch {
		case !st.Handshaken:
			// The main installation has not said yet whether it admits anonymous requests (Q-BAS-99).
			e.unreachable(q)
			return false
		case !st.AnonymousAccess:
			e.refuse(q, errCredentialsRequired(q.r), metrics.RefusedInvalid)
			return false
		}
		return true
	}
	if q.credAns = e.answers.credential(q.ctx(), c.key); q.credAns != nil {
		q.subject = q.credAns.Subject
		return true
	}
	return e.admit(q, e.d.Guard.AuthTimeout(q.ctx(), q.addr))
}

// admit renders the guard's verdict: nil for an admitted request.
func (e *engine) admit(q *rq, v guard.Verdict) bool {
	if v.Allowed {
		return true
	}
	e.refuseVerdict(q, v)
	return false
}

// refuseVerdict renders a refusal of the guard as the problem details of R-BAS-029 with Retry-After and the
// RateLimit-Policy and RateLimit fields of R-API-016.
func (e *engine) refuseVerdict(q *rq, v guard.Verdict) {
	if v.Quiet {
		markQuiet(q.ctx())
	}
	// Every refusal of the guard is a 429 (guard.Verdict.Status says so); the problem class carries it.
	code := v.Code
	if code == "" {
		code = iface.CodeRateLimited
	}
	name, quota, window := e.policyOf(v.Reason, q.cred.anonymous || !q.hasAuth)
	retry := int64((v.RetryAfter + time.Second - 1) / time.Second)
	retry = max(retry, 1)
	q.limiter = name
	err := errs.E(errs.RateLimited, code, "Too many requests; retry after the period in the Retry-After header.",
		errs.WithRetryAfter(v.RetryAfter),
		errs.WithHeader("RateLimit-Policy", `"`+name+`";q=`+strconv.Itoa(quota)+";w="+strconv.Itoa(window)),
		errs.WithHeader("RateLimit", `"`+name+`";r=0;t=`+strconv.FormatInt(retry, 10)))
	e.refuse(q, err, v.Reason)
}

// policyOf names the limit that refused and its quota and window in seconds, for the RateLimit-Policy field.
func (e *engine) policyOf(reason string, anonymous bool) (name string, quota, window int) {
	c := e.cfg
	switch reason {
	case metrics.RefusedMissLimited:
		quota = c.Limits.Misses.AuthenticatedPerMinute
		if anonymous {
			quota = c.Limits.Misses.AnonymousPerMinute
		}
		return "bastion:misses", quota, 60
	case metrics.RefusedAuthTimeout:
		return "bastion:failed_auth", c.FailedAuth.MaxFailures, int(c.FailedAuth.Window / time.Second)
	case metrics.RefusedBlocked:
		return "bastion:blocking", c.Blocking.Threshold, int(c.Blocking.Window / time.Second)
	}
	burst, rps := c.Limits.Authenticated.Burst, c.Limits.Authenticated.RequestsPerSecond
	if anonymous {
		burst, rps = c.Limits.Anonymous.Burst, c.Limits.Anonymous.RequestsPerSecond
	}
	return "bastion:requests", burst, max(1, (burst+rps-1)/rps)
}
