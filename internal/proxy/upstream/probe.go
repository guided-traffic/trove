package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
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
	CodeForeignHostDenied = errs.CodeForeignHostDenied
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
// path, or the URL itself. For the format that declares the registry root the probe
// resource is at the root of the host: the path of the URL is a namespace prefix of
// image names there (R-OCI-021), not a base of the API, and the version check is
// /v2/ (Q-REPO-281).
func probeURL(base *url.URL, probePath string, registryRoot bool) *url.URL {
	u := *base
	if probePath == "" {
		return &u
	}
	if registryRoot {
		u.Path, u.RawQuery = "/"+strings.TrimPrefix(probePath, "/"), ""
	} else {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(probePath, "/")
	}
	u.RawPath = ""
	return &u
}

// probe sends the request and reports what came back.
func (c *Clients) probe(ctx context.Context, cl *Client, format string) repository.UpstreamTestResult {
	var probePath string
	var challengeReachable, registryRoot bool
	if e, ok := c.cat.Lookup(format); ok {
		probePath, challengeReachable, registryRoot = e.ProbePath, e.ProbeChallengeReachable, e.RegistryRoot
	}
	target := probeURL(cl.origin, probePath, registryRoot)
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
	if status == http.StatusUnauthorized && challengeReachable && cl.settings.AuthMode != repository.AuthBearer {
		if realm, service, ok := bearerParams(resp.Header); ok {
			// A registry's 401 with a Bearer challenge asks for the token handshake: the credential is judged by the token service
			// the challenge names, so the verdict is its answer (R-REPO-064). A repository with authMode bearer runs no handshake
			// and sends its stored token as it is: the same answer says the registry refused that token.
			c.tokenCheck(ctx, cl, &res, realm, service)
		}
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		leaf := resp.TLS.PeerCertificates[0]
		res.TLS = &repository.UpstreamTLS{PeerSubject: leaf.Subject.String(), NotAfter: leaf.NotAfter.UTC()}
	}
	return res
}

// maxChallengeBytes bounds a WWW-Authenticate value the test reads; a longer one reads as no challenge.
const maxChallengeBytes = 8 << 10

// tokenCheck is the login of R-REPO-064 for a Bearer challenge: it names the token service in the result, refuses a token service
// the repository may not reach with foreign_host_denied and asks nothing there, and asks an allowed one once without a scope, as a
// client's login does, when the repository has a Basic credential and the token service's host receives it. Without that request
// the verdict on the credential stays nil.
func (c *Clients) tokenCheck(ctx context.Context, cl *Client, res *repository.UpstreamTestResult, realm, service string) {
	res.Authenticated = nil
	u, err := url.Parse(realm)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		res.Error = &repository.UpstreamTestError{Code: CodeRequestFailed, Message: "The registry names a token service that is no URL."}
		return
	}
	host := strings.ToLower(u.Hostname())
	ts := &repository.UpstreamTokenService{Host: host}
	res.TokenService = ts
	if err := cl.Allowed(u); err != nil {
		code := CodeForeignHostDenied
		if e, ok := errs.As(err); ok && e.Code == CodeInsecureRedirect {
			code = CodeInsecureRedirect
		}
		msg := "The registry's token service is on " + host + ", which is neither the upstream nor one of the repository's further hosts; trove did not ask it."
		if code == CodeInsecureRedirect {
			msg = "The registry's token service on " + host + " is reached over plain http, which an https upstream never is."
		}
		res.Error = &repository.UpstreamTestError{Code: code, Message: msg}
		return
	}
	ts.Allowed, ts.ReceivesCredential = true, cl.CredentialHost(u)
	if cl.settings.AuthMode != repository.AuthBasic || !ts.ReceivesCredential || cl.cred.headers().Get("Authorization") == "" {
		return
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	u.RawQuery, u.Fragment = q.Encode(), ""
	ctx = outbound.WithCredentialsOnTheFirstHopOnly(outbound.WithCall(ctx, outbound.Call{Repo: cl.key, FlightRole: probeFlightRole}))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		res.Error = &repository.UpstreamTestError{Code: CodeRequestFailed, Message: "The request to the token service cannot be built."}
		return
	}
	resp, err := cl.http.Do(req) //nolint:gosec // G704: the host was checked with Client.Allowed above and every address is checked against the egress policy (R-REPO-038)
	if err != nil {
		res.Error = probeError(err, u)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody+1))
	verdict, why := tokenVerdict(resp.StatusCode, body, err)
	res.Authenticated = verdict
	if why != "" {
		res.Error = &repository.UpstreamTestError{Code: CodeRequestFailed, Message: "The token service on " + host + " " + why}
	}
}

// tokenVerdict reads the answer of a token service to a login: 200 with a token accepts the credential, 401 and 403 refuse it,
// anything else is no verdict and the reason. The token is dropped: the test keeps nothing.
func tokenVerdict(status int, body []byte, readErr error) (*bool, string) {
	yes, no := true, false
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &no, ""
	case status != http.StatusOK:
		return nil, "answered " + http.StatusText(status) + " (" + strconv.Itoa(status) + ")."
	case readErr != nil:
		return nil, "did not finish its answer."
	case len(body) > maxProbeBody:
		return nil, "answered with more than 64 KiB."
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &tok) != nil || (tok.Token == "" && tok.AccessToken == "") {
		return nil, "answered 200 without a token."
	}
	return &yes, ""
}

// bearerParams finds the Bearer challenge of an answer and returns its realm and service (RFC 9110 11.6.1: a scheme, then
// comma-separated name=value parameters whose value is a token or a quoted string).
func bearerParams(h http.Header) (realm, service string, ok bool) {
	for _, v := range h.Values("Www-Authenticate") {
		if len(v) > maxChallengeBytes {
			continue
		}
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if !strings.EqualFold(scheme, "bearer") {
			continue
		}
		params := challengeParams(rest)
		if params["realm"] != "" {
			return params["realm"], params["service"], true
		}
	}
	return "", "", false
}

// challengeParams reads the name=value parameters of a challenge; the first value of a name counts.
func challengeParams(s string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			break
		}
		name := strings.ToLower(strings.TrimSpace(s[i : i+eq]))
		i += eq + 1
		var val strings.Builder
		if i < len(s) && s[i] == '"' {
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
			}
			i++
		} else {
			for ; i < len(s) && s[i] != ','; i++ {
				val.WriteByte(s[i])
			}
		}
		if _, set := out[name]; !set {
			out[name] = strings.TrimSpace(val.String())
		}
	}
	return out
}
