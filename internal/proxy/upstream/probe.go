package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
)

// The codes of UpstreamTestError and of Classify, stable like the other codes of
// the API (R-ARCH-025).
const (
	CodeDNSError          = "dns_error"
	CodeConnectionRefused = "connection_refused"
	CodeTimeout           = "timeout"
	CodeTLSUntrusted      = "tls_untrusted"
	CodeOffline           = "offline"
	CodeTooManyRedirects  = "too_many_redirects"
	CodeInsecureRedirect  = "insecure_redirect"
	CodeRequestFailed     = "request_failed"
	CodeEgressDenied      = errs.CodeEgressDenied
	CodeSourceNetwork     = errs.CodeSourceNetworkUnavailable
	CodeSecretUnavailable = errs.CodeUpstreamSecretUnavailable
)

const (
	maxProbeBody     = 64 << 10
	probeSlack       = 5 * time.Second
	probeFlightRole  = "probe"
	errMessageMaxLen = 300
)

// Classify names the reason a request to an upstream failed, as the stable code
// the upstream test reports and the engine maps to its answers. It does not
// decide what counts toward the circuit (R-REPO-029). A nil error has no code.
func Classify(err error) string {
	var (
		dns    *net.DNSError
		unk    x509.UnknownAuthorityError
		host   x509.HostnameError
		inval  x509.CertificateInvalidError
		verify *tls.CertificateVerificationError
		ne     net.Error
	)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, outbound.ErrEgressDenied):
		return CodeEgressDenied
	case errors.Is(err, outbound.ErrSourceNetworkUnavailable):
		return CodeSourceNetwork
	case errors.Is(err, outbound.ErrTooManyRedirects):
		return CodeTooManyRedirects
	case errors.Is(err, outbound.ErrInsecureRedirect):
		return CodeInsecureRedirect
	case hasCode(err, errs.CodeUpstreamSecretUnavailable):
		return CodeSecretUnavailable
	case errors.As(err, &dns):
		return CodeDNSError
	case errors.As(err, &verify), errors.As(err, &unk), errors.As(err, &host), errors.As(err, &inval):
		return CodeTLSUntrusted
	case errors.Is(err, syscall.ECONNREFUSED):
		return CodeConnectionRefused
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return CodeTimeout
	}
	return CodeRequestFailed
}

func hasCode(err error, code string) bool {
	e, ok := errs.As(err)
	return ok && e.Code == code
}

// probeError turns a failed request into the error of the result. The text is
// built here and never repeats the URL of the request, whose query may carry a
// token.
func probeError(err error, target *url.URL) *repository.UpstreamTestError {
	code := Classify(err)
	host := target.Hostname()
	var msg string
	switch code {
	case CodeEgressDenied:
		var d *outbound.EgressDeniedError
		if errors.As(err, &d) {
			msg = "The address " + d.Addr.String() + " of " + host + " is refused by the egress address policy (" + d.Rule + ")."
		} else {
			msg = "The address of " + host + " is refused by the egress address policy."
		}
	case CodeSourceNetwork:
		msg = "This replica has no interface address inside the repository's sourceNetwork."
	case CodeDNSError:
		msg = "The name " + host + " does not resolve."
	case CodeConnectionRefused:
		msg = "The connection to " + host + " was refused."
	case CodeTimeout:
		msg = "The upstream did not answer in time."
	case CodeTLSUntrusted:
		msg = "The certificate of " + host + " is not trusted: it is not signed by a CA of proxy.outbound.tls or does not match the name."
	case CodeTooManyRedirects:
		msg = "The upstream redirected more often than proxy.max_redirects allows."
	case CodeInsecureRedirect:
		msg = "The upstream redirected from https to http, which trove refuses."
	case CodeSecretUnavailable:
		msg = "The credential of the repository cannot be unsealed or resolved on this replica."
	default:
		msg = "The request failed: " + innermost(err)
	}
	return &repository.UpstreamTestError{Code: code, Message: msg}
}

// innermost is the text of the root cause of a wrapped error, which for a
// transport failure is the system's wording ("connection reset by peer").
func innermost(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			break
		}
		err = next
	}
	s := err.Error()
	if len(s) > errMessageMaxLen {
		s = s[:errMessageMaxLen]
	}
	return s
}

// TestSaved probes a saved remote repository: the same client, egress rules,
// credentials and source network a real fetch uses (R-REPO-024). It never reads or
// writes the circuit state.
func (c *Clients) TestSaved(ctx context.Context, r repository.Repository, s repository.RemoteSettings) repository.UpstreamTestResult {
	if res, off := c.offline(s); off {
		return res
	}
	cl, err := c.For(ctx, r, s)
	if err != nil {
		return failed(probeError(err, mustParse(s.URL)))
	}
	return c.probe(ctx, cl, r.Format)
}

// TestUnsaved probes a section that is not saved. The inline secret is used for
// this probe and dropped with the client.
func (c *Clients) TestUnsaved(ctx context.Context, p repository.UnsavedProbe) repository.UpstreamTestResult {
	if res, off := c.offline(p.Settings); off {
		return res
	}
	cl, err := c.Ephemeral("upstream-test", p.Settings, p.Password)
	if err != nil {
		return failed(probeError(err, mustParse(p.Settings.URL)))
	}
	defer cl.Close()
	return c.probe(ctx, cl, p.Format)
}

// offline answers for a repository whose upstream traffic is stopped: the mode
// offline of the repository or the global switch of the configuration (R-REPO-030,
// R-REPO-031). The probe is upstream traffic, so none is sent.
func (c *Clients) offline(s repository.RemoteSettings) (repository.UpstreamTestResult, bool) {
	switch {
	case c.o.Proxy.GlobalOffline:
		return failed(&repository.UpstreamTestError{Code: CodeOffline, Message: "proxy.global_offline is on: no remote repository contacts its upstream."}), true
	case s.Availability.Mode == repository.AvailabilityOffline:
		return failed(&repository.UpstreamTestError{Code: CodeOffline, Message: "The repository is offline: set availability.mode to online to test the upstream."}), true
	}
	return repository.UpstreamTestResult{}, false
}

func failed(e *repository.UpstreamTestError) repository.UpstreamTestResult {
	return repository.UpstreamTestResult{Error: e}
}

func mustParse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return &url.URL{}
	}
	return u
}

// probeURL is the probe resource of a format below the upstream URL: its probe
// path, or the URL itself.
func probeURL(base *url.URL, probePath string) *url.URL {
	u := *base
	if probePath == "" {
		return &u
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(probePath, "/")
	u.RawPath = ""
	return &u
}

// probe sends the request and reports what came back.
func (c *Clients) probe(ctx context.Context, cl *Client, format string) repository.UpstreamTestResult {
	var probePath string
	if e, ok := c.cat.Lookup(format); ok {
		probePath = e.ProbePath
	}
	target := probeURL(cl.origin, probePath)
	s := cl.settings
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.ConnectTimeoutMs+s.ReadTimeoutMs)*time.Millisecond+probeSlack)
	defer cancel()

	var local string
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if a, ok := i.Conn.LocalAddr().(*net.TCPAddr); ok && s.SourceNetwork != nil {
			local = a.IP.String()
		}
	}})
	ctx = outbound.WithCall(ctx, outbound.Call{Repo: cl.key, FlightRole: probeFlightRole})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return failed(&repository.UpstreamTestError{Code: CodeRequestFailed, Message: "The probe request cannot be built from the upstream URL."})
	}
	start := time.Now()
	resp, err := cl.http.Do(req)
	res := repository.UpstreamTestResult{DurationMs: time.Since(start).Milliseconds()}
	if local != "" {
		res.SourceAddress = &local
	}
	if err != nil {
		res.Error = probeError(err, target)
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBody))
	res.DurationMs = time.Since(start).Milliseconds()
	status := resp.StatusCode
	authed := status != http.StatusUnauthorized && status != http.StatusForbidden
	res.Reachable, res.HTTPStatus, res.Authenticated = true, &status, &authed
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		leaf := resp.TLS.PeerCertificates[0]
		res.TLS = &repository.UpstreamTLS{PeerSubject: leaf.Subject.String(), NotAfter: leaf.NotAfter.UTC()}
	}
	return res
}
