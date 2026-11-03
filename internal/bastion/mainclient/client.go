package mainclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// Purpose is the purpose label of the Bastion's outbound client in the shared outbound metrics.
const Purpose = "bastion_main"

const (
	// maxInterfaceBody bounds the body of an interface answer; the largest is a full feed page.
	maxInterfaceBody = 16 << 20
	// problemPeek is how much of a 400 or 401 problem body is read to find its code.
	problemPeek = 8 << 10
	// bodyAllowance is added to the connect and response-header timeouts for the time an interface answer
	// may take to arrive after its headers.
	bodyAllowance = 10 * time.Second
)

// HTTPClient is the [Client] over net/http (R-BAS-006, R-BAS-008, R-BAS-017, R-BAS-021, R-BAS-025, R-BAS-044).
type HTTPClient struct {
	o         Options
	base      *url.URL
	origin    origin
	http      *http.Client
	now       func() time.Time
	opTimeout time.Duration
}

var _ Client = (*HTTPClient)(nil)

// New builds the client. A bastion.main.url that is not https, a base URL that is not an origin, a CA file that
// does not load and a missing token source are errors; the connection itself is not tried.
func New(o Options) (*HTTPClient, error) {
	if o.Metrics == nil || o.Tokens == nil {
		return nil, errors.New("mainclient: Metrics and Tokens are required")
	}
	base, err := url.Parse(o.URL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("mainclient: bastion.main.url must be an https URL (R-BAS-044)")
	}
	org, err := newOrigin(o.BaseURL)
	if err != nil {
		return nil, err
	}
	c := &HTTPClient{o: o, base: base, origin: org, now: o.Now, opTimeout: o.ConnectTimeout + o.ResponseHeaderTimeout + bodyAllowance}
	if c.now == nil {
		c.now = time.Now
	}
	if c.http, err = c.factoryClient(); err != nil {
		return nil, err
	}
	if o.WrapTransport != nil {
		c.http.Transport = o.WrapTransport(c.http.Transport)
	}
	// A redirect is the main installation's answer, not something to follow: the Bastion token travels in a
	// header that a redirect would carry to another host.
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// factoryClient builds the client of the shared outbound factory: TLS 1.2 or later, the system roots plus
// bastion.main.ca_file read again without a restart (R-BAS-044), the trace context of the Bastion's span. The
// egress address policy is empty: the destination is operator configuration, not input (H-446).
func (c *HTTPClient) factoryClient() (*http.Client, error) {
	cfg := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
	if c.o.CAFile != "" {
		cfg.TLS.CAFiles = []string{c.o.CAFile}
	}
	opts := []outbound.Option{outbound.WithVersion(c.o.Release)}
	if c.o.Tracer != nil {
		opts = append(opts, outbound.WithTracer(c.o.Tracer))
	}
	if c.o.Obs != nil {
		opts = append(opts, outbound.WithMetrics(c.o.Obs))
	}
	if c.o.Log != nil {
		opts = append(opts, outbound.WithLogger(c.o.Log))
	}
	if c.o.TrustCheckEvery > 0 {
		opts = append(opts, outbound.WithTrustCheckEvery(c.o.TrustCheckEvery))
	}
	if c.o.TLSMetrics != nil {
		opts = append(opts, outbound.WithTLSMetrics(c.o.TLSMetrics))
	}
	f := outbound.New(cfg, opts...)
	if err := f.Err(); err != nil {
		return nil, fmt.Errorf("mainclient: bastion.main.ca_file: %w", err)
	}
	return f.Client(Purpose,
		outbound.WithConnectTimeout(c.o.ConnectTimeout),
		outbound.WithResponseHeaderTimeout(c.o.ResponseHeaderTimeout),
		outbound.WithDirect(),
	), nil
}

// Close closes the idle connections.
func (c *HTTPClient) Close() { c.http.CloseIdleConnections() }

// origin is the base URL of the Bastion split into the values of the forwarded headers (R-BAS-017).
type origin struct {
	proto string
	// host is the host without the port, bracketed for IPv6; hostPort is host:port as the base URL spells it
	// (the port only when it is not the default of the scheme).
	host, hostPort string
	port           string
}

func newOrigin(raw string) (origin, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return origin{}, errors.New("mainclient: server.base_url must be an origin (scheme, host, optional port)")
	}
	o := origin{proto: u.Scheme, host: strings.ToLower(u.Hostname()), port: u.Port()}
	if strings.Contains(o.host, ":") {
		o.host = "[" + o.host + "]"
	}
	o.hostPort = o.host
	if o.port != "" {
		o.hostPort += ":" + o.port
	} else if u.Scheme == "https" {
		o.port = "443"
	} else {
		o.port = "80"
	}
	return o, nil
}

// apply writes the forwarded headers of R-BAS-017: exactly one entry in each family, replacing whatever was
// there. addr is the client address; an address that does not parse leaves the for entries out.
func (o origin) apply(h http.Header, addr string) {
	for _, k := range []string{iface.HeaderForwardedFor, iface.HeaderForwardedProto, iface.HeaderForwardedHost, iface.HeaderForwardedPort, iface.HeaderForwarded} {
		h.Del(k)
	}
	h.Set(iface.HeaderForwardedProto, o.proto)
	h.Set(iface.HeaderForwardedHost, o.host)
	h.Set(iface.HeaderForwardedPort, o.port)
	var el []string
	if a, ok := parseClient(addr); ok {
		h.Set(iface.HeaderForwardedFor, a.String())
		forVal := a.String()
		if a.Is6() {
			forVal = `"[` + a.String() + `]"`
		}
		el = append(el, "for="+forVal)
	}
	host := o.hostPort
	if strings.ContainsAny(host, ":[") {
		host = `"` + host + `"`
	}
	el = append(el, "proto="+o.proto, "host="+host)
	h.Set(iface.HeaderForwarded, strings.Join(el, ";"))
}

// parseClient reads the client address the engine determined, with or without a port or brackets.
func parseClient(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

// Handshake implements [Client].
func (c *HTTPClient) Handshake(ctx context.Context) (iface.Handshake, error) {
	var hs iface.Handshake
	if err := c.interfaceCall(ctx, ifaceCall{kind: KindHandshake, method: http.MethodGet, path: iface.PathHandshake, out: &hs}); err != nil {
		return iface.Handshake{}, err
	}
	if hs.InterfaceVersion != iface.InterfaceVersion {
		return iface.Handshake{}, &MismatchError{Main: hs.InterfaceVersion}
	}
	return hs, nil
}

// Check implements [Client].
func (c *HTTPClient) Check(ctx context.Context, class Class, req iface.CheckRequest) (iface.CheckAnswer, error) {
	var ans iface.CheckAnswer
	err := c.interfaceCall(ctx, ifaceCall{kind: KindCheck, slot: true, class: class, method: http.MethodPost, path: iface.PathCheck, in: req, out: &ans})
	if err != nil {
		return iface.CheckAnswer{}, err
	}
	return ans, nil
}

// Feed implements [Client].
func (c *HTTPClient) Feed(ctx context.Context, after iface.Cursor, limit int) (iface.FeedAnswer, error) {
	q := url.Values{iface.QueryAfter: {after.String()}}
	if limit > 0 {
		q.Set(iface.QueryLimit, strconv.Itoa(limit))
	}
	var ans iface.FeedAnswer
	if err := c.interfaceCall(ctx, ifaceCall{kind: KindFeed, method: http.MethodGet, path: iface.PathFeed, query: q, out: &ans}); err != nil {
		return iface.FeedAnswer{}, err
	}
	return ans, nil
}

// Downloads implements [Client]. A batch takes a slot of the budget as an authenticated request (R-BAS-025).
func (c *HTTPClient) Downloads(ctx context.Context, req iface.DownloadsRequest) error {
	return c.interfaceCall(ctx, ifaceCall{kind: KindDownloads, slot: true, class: Authenticated, method: http.MethodPost, path: iface.PathDownloads, in: req})
}

// ifaceCall describes one request of the interface.
type ifaceCall struct {
	kind   Kind
	slot   bool
	class  Class
	method string
	path   string
	query  url.Values
	in     any
	// out is the body to decode on a 200; nil means any 2xx is success and the body is ignored.
	out any
}

func (c *HTTPClient) interfaceCall(ctx context.Context, call ifaceCall) error {
	ctx, cancel := context.WithTimeout(ctx, c.opTimeout)
	defer cancel()
	if call.slot {
		release, err := c.acquire(ctx, call.class, call.kind)
		if err != nil {
			return err
		}
		defer release()
	}
	var body io.Reader
	if call.in != nil {
		b, err := iface.Marshal(call.in)
		if err != nil {
			return fmt.Errorf("mainclient: encoding the %s request: %w", call.kind, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, call.method, c.base.String(), body)
	if err != nil {
		return fmt.Errorf("mainclient: building the %s request: %w", call.kind, err)
	}
	req.URL.Path, req.URL.RawQuery = call.path, call.query.Encode()
	c.commonHeaders(req.Header)
	tok := c.o.Tokens.Token()
	if tok.Value == "" {
		return errors.New("mainclient: no Bastion token in use")
	}
	req.Header.Set("Authorization", iface.BearerScheme+" "+tok.Value)
	req.Header.Set("Accept", iface.MediaType)
	if body != nil {
		req.Header.Set("Content-Type", iface.MediaType)
	}
	c.origin.apply(req.Header, "")
	resp, err := c.send(ctx, call.kind, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.decode(resp, call)
}

// commonHeaders sets the headers of every request of the Bastion (R-BAS-008).
func (c *HTTPClient) commonHeaders(h http.Header) {
	h.Set("User-Agent", iface.UserAgentPrefix+c.o.Release)
	h.Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
	h.Set(iface.HeaderReplica, c.o.Replica)
}

// acquire takes a slot of the budget and counts a request that found none.
func (c *HTTPClient) acquire(ctx context.Context, class Class, kind Kind) (func(), error) {
	if c.o.Budget == nil {
		return func() {}, nil
	}
	release, err := c.o.Budget.Acquire(ctx, class)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			c.o.Metrics.MainRequests.WithLabelValues(string(kind), metrics.OutcomeBusy).Inc()
		}
		return nil, err
	}
	return release, nil
}

// send performs the request and counts it. A transport failure is an [UnreachableError]; the cancellation of
// the caller's own context is returned as it is, because it says nothing about the main installation.
func (c *HTTPClient) send(ctx context.Context, kind Kind, req *http.Request) (*http.Response, error) {
	start := c.now()
	resp, err := c.http.Do(req)
	c.o.Metrics.MainDuration.WithLabelValues(string(kind)).Observe(c.now().Sub(start).Seconds())
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL carries the caller's query
		}
		timeout := isTimeout(err)
		outcome := metrics.OutcomeError
		if timeout {
			outcome = metrics.OutcomeTimeout
		}
		c.o.Metrics.MainRequests.WithLabelValues(string(kind), outcome).Inc()
		if cerr := ctx.Err(); cerr != nil && !errors.Is(cerr, context.DeadlineExceeded) {
			return nil, cerr
		}
		return nil, &UnreachableError{Err: err, Timeout: timeout}
	}
	c.o.Metrics.MainRequests.WithLabelValues(string(kind), statusClass(resp.StatusCode)).Inc()
	return resp, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return metrics.Outcome5xx
	case status >= 400:
		return metrics.Outcome4xx
	case status >= 300:
		return metrics.Outcome3xx
	}
	return metrics.Outcome2xx
}

// checkVersion compares the interface version an answer states with the Bastion's (R-BAS-009, R-BAS-028).
func checkVersion(h http.Header) error {
	v := h.Get(iface.HeaderInterface)
	if v == "" || v == strconv.Itoa(iface.InterfaceVersion) {
		return nil
	}
	n, _ := strconv.Atoi(v)
	return &MismatchError{Main: n}
}

// problem is the part of a problem body the client reads.
type problem struct {
	Code             string `json:"code"`
	InterfaceVersion int    `json:"interfaceVersion"`
}

// readProblem reads at most problemPeek bytes of a body and returns what it parsed and what it read.
func readProblem(body io.Reader) (problem, []byte) {
	buf, _ := io.ReadAll(io.LimitReader(body, problemPeek))
	var p problem
	_ = json.Unmarshal(buf, &p)
	return p, buf
}

// classify turns an answer that refuses the Bastion into its typed error; nil means it does not.
func (c *HTTPClient) classify(status int, p problem) error {
	switch {
	case status == http.StatusBadRequest && p.Code == iface.CodeInterfaceMismatch:
		return &MismatchError{Main: p.InterfaceVersion}
	case status == http.StatusUnauthorized && p.Code == iface.CodeTokenInvalid:
		c.o.Metrics.MainTokenRefused.Set(1)
		return ErrTokenRefused
	}
	return nil
}

// decode reads the answer of an interface operation.
func (c *HTTPClient) decode(resp *http.Response, call ifaceCall) error {
	if err := checkVersion(resp.Header); err != nil {
		return err
	}
	if resp.StatusCode/100 == 2 {
		c.o.Metrics.MainTokenRefused.Set(0)
		if call.out == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxInterfaceBody))
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			return &AnswerError{Status: resp.StatusCode}
		}
		lr := &io.LimitedReader{R: resp.Body, N: maxInterfaceBody + 1}
		if err := iface.DecodeStrict(lr, call.out); err != nil {
			return &AnswerError{Status: resp.StatusCode, Err: err}
		}
		return nil
	}
	p, _ := readProblem(resp.Body)
	if err := c.classify(resp.StatusCode, p); err != nil {
		return err
	}
	if resp.StatusCode >= 500 {
		return &UnreachableError{Status: resp.StatusCode}
	}
	return &AnswerError{Status: resp.StatusCode, Code: p.Code}
}

// Do implements [Client].
func (c *HTTPClient) Do(ctx context.Context, f Forward) (*Response, error) {
	if f.Request == nil || f.Request.URL == nil {
		return nil, errors.New("mainclient: a forward needs a request")
	}
	kind := f.Kind
	if kind == "" {
		kind = KindForward
	}
	if f.Probe {
		kind = KindProbe
	}
	req, err := c.forwardRequest(ctx, f)
	if err != nil {
		return nil, err
	}
	release, err := c.acquire(ctx, f.Class, kind)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, kind, req)
	if err != nil {
		release()
		return nil, err
	}
	out, err := c.accept(resp, release)
	if err != nil {
		_ = resp.Body.Close()
		release()
		return nil, err
	}
	return out, nil
}

// accept turns the main installation's answer to a forward into a [Response], or into the error it stands for.
// On an error the caller closes the body and releases the slot.
func (c *HTTPClient) accept(resp *http.Response, release func()) (*Response, error) {
	if err := checkVersion(resp.Header); err != nil {
		return nil, err
	}
	m, marked, merr := iface.ParseMarking(resp.Header)
	if merr != nil {
		return nil, &AnswerError{Status: resp.StatusCode, Err: merr}
	}
	body := resp.Body
	if (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), iface.ProblemMediaType) {
		p, buf := readProblem(body)
		if err := c.classify(resp.StatusCode, p); err != nil {
			return nil, err
		}
		body = &prefixed{Reader: io.MultiReader(bytes.NewReader(buf), resp.Body), Closer: resp.Body}
	}
	if !marked && resp.StatusCode >= 500 {
		return nil, &UnreachableError{Status: resp.StatusCode}
	}
	if marked {
		c.o.Metrics.MainTokenRefused.Set(0)
	}
	iface.StripMarking(resp.Header)
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: &slotBody{ReadCloser: body, release: release}, Marking: m, Marked: marked}, nil
}

type prefixed struct {
	io.Reader
	io.Closer
}

// slotBody releases the budget slot when the body ends or is closed (R-BAS-025).
type slotBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *slotBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}

func (b *slotBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

// stripped are the request headers of a caller that never go to the main installation: the Bastion sets its
// own forwarded-for, interface and conditional headers, the caller's cookies are not its business, a range or a
// condition would turn the Bastion's fetch into a partial or empty one (R-BAS-021), and the caller's trace
// context is not continued (R-BAS-042).
func stripped(name string) bool {
	switch name {
	case "Cookie", "Cookie2", "Forwarded", "Range", "Host", "Content-Length", "Expect",
		"Traceparent", "Tracestate", "Baggage", "Authorization-Info",
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "User-Agent":
		return true
	}
	return strings.HasPrefix(name, "X-Forwarded-") || strings.HasPrefix(name, "If-") || strings.HasPrefix(name, "Trove-")
}

func (c *HTTPClient) forwardRequest(ctx context.Context, f Forward) (*http.Request, error) {
	src := f.Request
	var body io.Reader
	if !f.Probe && f.Body != nil {
		body = f.Body
	}
	req, err := http.NewRequestWithContext(ctx, src.Method, c.base.String(), body)
	if err != nil {
		return nil, fmt.Errorf("mainclient: building the forward: %w", err)
	}
	req.URL.Path, req.URL.RawPath, req.URL.RawQuery = src.URL.Path, src.URL.RawPath, src.URL.RawQuery
	if body != nil && src.ContentLength > 0 {
		req.ContentLength = src.ContentLength
	}
	connection := map[string]bool{}
	for _, v := range src.Header.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			connection[http.CanonicalHeaderKey(strings.TrimSpace(t))] = true
		}
	}
	for k, vs := range src.Header {
		if stripped(k) || connection[k] {
			continue
		}
		req.Header[k] = append([]string(nil), vs...)
	}
	c.commonHeaders(req.Header)
	tok := c.o.Tokens.Token()
	if tok.Value == "" {
		return nil, errors.New("mainclient: no Bastion token in use")
	}
	req.Header.Set(iface.HeaderAuthorization, iface.BearerScheme+" "+tok.Value)
	if f.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", f.IfNoneMatch)
	}
	if f.Probe {
		req.Header.Del("Authorization")
		req.Header.Set(iface.HeaderProbe, iface.ProbeValue)
		req.Body, req.GetBody, req.ContentLength = http.NoBody, nil, 0
	}
	c.origin.apply(req.Header, f.ClientAddress)
	return req, nil
}
