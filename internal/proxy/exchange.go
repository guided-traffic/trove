package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

// ErrOffline is the answer of [Engine.Exchange] for a repository that may not contact
// its upstream: offline mode, or proxy.global_offline (R-REPO-030, R-REPO-031).
var ErrOffline = errors.New("proxy: the repository may not contact its upstream")

// Exchange sends one request of a format through the client of a remote repository,
// outside the cache: the bulk advisory request of an npm audit (R-NPM-038). The client
// applies the egress rules, the trust store, the redirect rules and the credential
// policy, so the repository's credential reaches only the hosts R-REPO-033 names, and a
// request to another host carries none.
//
// It takes no fetch slot and neither reads nor moves the circuit: the service may be
// another host than the upstream whose health the circuit tracks, and the caller bounds
// the exchange with ctx. A transport failure is classified as a fetch's is, an egress
// or TLS refusal is counted and recorded as a security event, and the response comes
// back whatever its status. The caller closes its body.
func (e *Engine) Exchange(ctx context.Context, rem Remote, req *http.Request, caller CallerFacts) (*http.Response, error) {
	switch {
	case rem.Repo.Disabled:
		return nil, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	case e.offline(rem):
		return nil, ErrOffline
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return nil, err
	}
	// The exchange client has no overall timeout and no stall bound: a caller whose context has no deadline gets the longest
	// bound a format sets, so a provider that stops answering cannot hold the request for good (Q-NPM-78).
	ctx, cancel := boundExchange(ctx, upstream.ExchangeHeaderTimeout)
	// No flight: flight_role stays empty in the upstream log record (Q-REPO-92).
	rctx := outbound.WithCall(ctx, outbound.Call{OnBehalfOf: caller.Principal, Repo: rem.Repo.Name.String(), Attempt: 1})
	out := req.Clone(rctx)
	out.Header.Del("Authorization") // the client's hop policy alone adds the credential
	start := time.Now()
	resp, err := cl.ExchangeHTTP().Do(out) //nolint:gosec // G704: the URL is the remote's upstream or its providerURL, which only repository configuration sets; the client checks every address against the egress policy (R-REPO-038)
	if err != nil {
		cancel()
		e.m.request(rem.Repo.Name.String(), 0)
		return nil, e.exchangeFailure(ctx, rem, out, caller, err)
	}
	e.m.request(rem.Repo.Name.String(), resp.StatusCode)
	e.m.upstreamDur.WithLabelValues(rem.Repo.Name.String()).Observe(time.Since(start).Seconds())
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel} // the bound runs until the caller has read the body
	return resp, nil
}

// boundExchange gives a context without a deadline the bound and leaves one that has a deadline as it is.
func boundExchange(ctx context.Context, bound time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, bound)
}

// cancelOnClose ends the context of an exchange when its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// exchangeFailure classifies a failed exchange. The circuit is not told.
func (e *Engine) exchangeFailure(ctx context.Context, rem Remote, req *http.Request, caller CallerFacts, err error) error {
	fail := e.classifyError(rem, req.URL, err, false)
	if fail.sourceNetwork {
		e.m.sourceNet.WithLabelValues(rem.Repo.Name.String()).Inc()
	}
	if fail.egress {
		e.m.egressDenied.WithLabelValues(rem.Repo.Name.String()).Inc()
	}
	rq := Request{Remote: rem, Path: req.URL.Path, Caller: caller}
	host := req.URL.Hostname()
	switch fail.event {
	case "egress":
		e.securityEvent(ctx, rq, audit.EventProxyEgressDenied, host, map[string]string{"host": host})
	case "tls":
		e.securityEvent(ctx, rq, audit.EventProxyTLSUntrusted, host, map[string]string{"host": host})
	}
	if fail.err == nil {
		return err
	}
	return fail.err
}
