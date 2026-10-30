package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

// failure is the classification of an upstream outcome (R-REPO-029): what the client
// is told, whether the circuit counts it, and which security event it is.
type failure struct {
	err *errs.Error
	// count: toward the circuit.
	count bool
	// retryAfter: a 429 that opens the circuit for this long at once.
	retryAfter time.Duration
	// status is the upstream's HTTP status, 0 for a transport error.
	status int
	// event is the security event the failure writes, "" for none.
	event string
	// sourceNetwork: counts trove_remote_source_network_unavailable_total.
	sourceNetwork bool
	// egress: counts trove_remote_egress_denied_total.
	egress bool
}

// classifyStatus maps the status of an upstream response that is neither a success
// nor a 404 or 410 (R-REPO-029 table).
func classifyStatus(rem Remote, resp *http.Response) failure {
	f := failure{status: resp.StatusCode}
	switch s := resp.StatusCode; s {
	case http.StatusUnauthorized, http.StatusForbidden:
		f.err = errs.E(errs.UpstreamFailed, errs.CodeUpstreamAuthFailed,
			"The upstream of "+rem.Repo.Key+" refused the repository's credentials.")
	case http.StatusTooManyRequests:
		f.count = true
		f.retryAfter = retryAfterOf(resp.Header, time.Duration(rem.Settings.Availability.CircuitOpenSeconds)*time.Second)
		f.err = errs.E(errs.Unavailable, errs.CodeUnavailable,
			"The upstream of "+rem.Repo.Key+" is rate limiting trove.", errs.WithRetryAfter(f.retryAfter))
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		f.count = true
		f.err = errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream of "+rem.Repo.Key+" answered "+strconv.Itoa(s)+".")
	default:
		f.err = errs.E(errs.UpstreamFailed, errs.CodeUpstreamUnexpectedStatus,
			"The upstream of "+rem.Repo.Key+" answered "+strconv.Itoa(s)+", which trove does not act on.")
	}
	return f
}

// retryAfterOf reads Retry-After (seconds or an HTTP date), capped at max; a missing
// or unreadable value is max, so the circuit does not reopen faster than the
// operator configured.
func retryAfterOf(h http.Header, max time.Duration) time.Duration {
	v := h.Get("Retry-After")
	d := max
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	if max > 0 && d > max {
		d = max
	}
	return max0(d)
}

func max0(d time.Duration) time.Duration {
	if d < time.Second {
		return time.Second
	}
	return d
}

// classifyError maps a failure to send the request or to read its body.
func (e *Engine) classifyError(rem Remote, target *url.URL, err error, idle bool) failure {
	host := target.Hostname()
	switch {
	case idle:
		return failure{count: true, err: errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout,
			"The upstream of "+rem.Repo.Key+" stopped sending.")}
	case errors.Is(err, context.Canceled), errors.Is(err, errShuttingDown):
		return failure{err: errs.E(errs.Unavailable, errs.CodeShuttingDown, "The replica is shutting down; retry.", errs.WithRetryAfter(time.Second))}
	}
	if ee, ok := errs.As(err); ok {
		if ee.Code == errs.CodeUpstreamSecretUnavailable {
			return failure{err: ee}
		}
	}
	switch upstream.Classify(err) {
	case upstream.CodeEgressDenied:
		return failure{egress: true, event: "egress", err: errs.E(errs.UpstreamFailed, errs.CodeEgressDenied,
			"The address of "+host+" is refused by the egress address policy.")}
	case upstream.CodeSourceNetwork:
		return failure{sourceNetwork: true, err: errs.E(errs.UpstreamFailed, errs.CodeSourceNetworkUnavailable,
			"This replica has no interface address inside the sourceNetwork of "+rem.Repo.Key+".")}
	case upstream.CodeTLSUntrusted:
		return failure{event: "tls", err: errs.E(errs.UpstreamFailed, errs.CodeTLSUntrusted,
			"The certificate of "+host+" is not trusted.")}
	case upstream.CodeTooManyRedirects:
		return failure{err: errs.E(errs.UpstreamFailed, errs.CodeTooManyRedirects, "The upstream redirected more often than proxy.max_redirects allows.")}
	case upstream.CodeInsecureRedirect:
		return failure{err: errs.E(errs.UpstreamFailed, errs.CodeInsecureRedirect, "The upstream redirected from https to http, which trove refuses.")}
	case upstream.CodeTimeout:
		return failure{count: true, err: errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout, "The upstream of "+rem.Repo.Key+" did not answer in time.")}
	}
	return failure{count: true, err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamUnreachable,
		"The upstream of "+rem.Repo.Key+" cannot be reached.", errs.WithCause(err))}
}

// retryable reports whether a failure before any request byte was sent is one of
// the connection failures that get the one immediate retry (R-REPO-029).
func retryable(err error) bool {
	switch upstream.Classify(err) {
	case upstream.CodeDNSError, upstream.CodeConnectionRefused, upstream.CodeTimeout, upstream.CodeRequestFailed:
		return !errors.Is(err, context.Canceled) && !errors.Is(err, outbound.ErrSourceNetworkUnavailable)
	}
	return false
}

var errShuttingDown = errors.New("proxy: shutting down")
