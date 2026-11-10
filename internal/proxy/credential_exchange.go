package proxy

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// CredentialExchanger is what the engine needs of a format whose upstream is called with a short-lived token that a single-sign-on realm
// issues for the repository's stored credential, a refresh token (R-ANS-019, Q-ANS-51). It differs from [Authenticator] in what it is
// given: the credential in effect, for the one request to the realm that needs it in its body, and nothing is waited for from the
// upstream: the exchange happens before the first request, so the credential itself never goes to the upstream.
//
// The engine runs it for a repository whose authMode is bearer and whose adapter answers ok: it sends the realm request through the
// repository's client (only to a host that may receive the credential: the upstream's origin or an extraHosts entry with
// sendCredentials), keeps the token in the replica's cache under the key and the credential's digest, sends the upstream request with
// the token in place of the credential, and on a 401 of the upstream exchanges again and sends the request once more.
type CredentialExchanger interface {
	// ExchangeRequest builds the request to the realm for credential. ok false: the repository does no exchange (its credential is the
	// token as it is). key names the token in the replica's cache; the engine adds the repository, its configuration version, the realm and the
	// credential's digest.
	ExchangeRequest(ctx context.Context, b UpstreamBase, credential string) (req *http.Request, key string, ok bool, err error)
	// ReadExchange reads the realm's 2xx answer, at most [MaxTokenAnswerBytes] of it: the Authorization value and the token's lifetime,
	// shortened by the format to the time it is to be used (0: [DefaultTokenLifetime]).
	ReadExchange(ctx context.Context, status int, body []byte) (authorization string, ttl time.Duration, err error)
}

// exchangeAuth lets the exchange reuse the request and the bounded read of the handshake: it has no challenge to answer.
type exchangeAuth struct{ x CredentialExchanger }

func (exchangeAuth) TokenRequest(context.Context, UpstreamBase, *http.Request, string, int, http.Header) (*http.Request, string, bool, error) {
	return nil, "", false, nil
}

func (a exchangeAuth) ReadToken(ctx context.Context, status int, body []byte) (string, time.Duration, error) {
	return a.x.ReadExchange(ctx, status, body)
}

// exchangeCall is what one request needs to be sent with an exchanged token: the client, the repository, the format's exchange and the
// facts of the caller the realm request is logged for.
type exchangeCall struct {
	cl         *upstream.Client
	rem        Remote
	x          CredentialExchanger
	caller     CallerFacts
	jobID      string
	background bool
}

// begin finds the realm request for req: ok false when the repository does no exchange for it (another authMode, a request to another
// host than the upstream's origin, a format without an exchange or a repository without an authUrl).
func (c exchangeCall) begin(ctx context.Context, req *http.Request) (h *handshake, treq *http.Request, key string, ok bool, err error) {
	if c.x == nil || c.rem.Settings.AuthMode != repository.AuthBearer || !sameOrigin(req.URL, c.cl.Origin()) {
		return nil, nil, "", false, nil
	}
	cred, has := c.cl.BearerSecret()
	if !has {
		return nil, nil, "", false, nil
	}
	treq, key, ok, err = c.x.ExchangeRequest(ctx, UpstreamBase{URL: c.cl.Origin()}, cred)
	if err != nil || !ok {
		return nil, nil, "", false, err
	}
	// The refresh token is in the body of the request to the realm: the request carries no Authorization header of the credential
	// besides (requestToken, noCredential), so that the realm receives the credential once.
	h = &handshake{cl: c.cl, rem: c.rem, auth: exchangeAuth{c.x}, caller: c.caller, jobID: c.jobID, background: c.background, noCredential: true}
	return h, treq, key, true, nil
}

// sendExchanged is [fetcher.send] for a repository whose credential is exchanged: handled false means the repository does no exchange
// for this request and the caller sends it as it is.
func (f *fetcher) sendExchanged(ctx context.Context, req *http.Request) (resp *http.Response, cancel context.CancelFunc, idle *atomic.Bool, handled bool, err error) {
	x, _ := f.rq.Adapter.(CredentialExchanger)
	c := exchangeCall{cl: f.cl, rem: f.rq.Remote, x: x, caller: f.rq.Caller, jobID: f.rq.JobID, background: f.rq.background()}
	return f.e.sendExchangedVia(ctx, c, req, func(ctx context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error) {
		return f.sendOnce(ctx, req)
	})
}

// sendExchangedVia exchanges the credential and sends req with the token by once, which sends the request under the context it is
// given (it carries the token). It is the one implementation of the exchange for the fetch of a document or an artifact and for the
// request of a mirror's discovery (Get).
func (e *Engine) sendExchangedVia(ctx context.Context, c exchangeCall, req *http.Request, once func(context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error)) (resp *http.Response, cancel context.CancelFunc, idle *atomic.Bool, handled bool, err error) {
	h, treq, key, ok, err := c.begin(ctx, req)
	if err != nil {
		return nil, nil, nil, true, &handshakeError{err: upstreamFailure(err)}
	}
	if !ok {
		return nil, nil, nil, false, nil
	}
	rem := c.rem
	// The host rule of R-REPO-033: the credential is in the body of the realm request, so the realm must be a host that may have it; a
	// realm anywhere else is refused before a byte is sent.
	if treq == nil || treq.URL == nil || (treq.URL.Scheme != "http" && treq.URL.Scheme != "https") || treq.URL.User != nil {
		return nil, nil, nil, true, &handshakeError{err: tokenError(rem, "names no usable address")}
	}
	if err := c.cl.Allowed(treq.URL); err != nil {
		return nil, nil, nil, true, &handshakeError{err: upstreamFailure(err)}
	}
	if !c.cl.CredentialHost(treq.URL) {
		return nil, nil, nil, true, &handshakeError{err: tokenError(rem, "is on a host that may not receive the repository's credential: it must be the upstream or an extraHosts entry with sendCredentials")}
	}
	tk := tokenKeyOf(*h, key, treq.URL)
	name := rem.Repo.Name.String()
	rejected := ""
	for attempt := range 2 {
		ent, _, terr := e.tokenOnce(ctx, name, tk, rejected, func(ctx context.Context) (tokenEntry, error) {
			return e.requestToken(ctx, *h, treq)
		})
		if terr != nil {
			return nil, nil, nil, true, exchangeFailure(rem, terr)
		}
		resp, cancel, idle, err = once(outbound.WithAuthorization(ctx, ent.authorization))
		if err != nil {
			if ctx.Err() == nil {
				e.dropToken(tk, ent.authorization)
			}
			return resp, cancel, idle, true, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt == 1 {
			return resp, cancel, idle, true, nil
		}
		// The upstream refused the token (it expired early or was revoked): the next round obtains another and sends once more.
		rejected = ent.authorization
		_ = resp.Body.Close()
		cancel()
	}
	return resp, cancel, idle, true, nil
}

// exchangeFailure answers a failed exchange: a realm that refused the credential (a 4xx) is upstream_auth_failed, the answer a
// credential the upstream refuses has everywhere (R-REPO-033); any other failure keeps its own classification.
func exchangeFailure(rem Remote, err error) error {
	he, ok := asHandshakeError(err)
	if !ok || he.err == nil || he.err.Code != errs.CodeUpstreamUnexpectedStatus {
		return err
	}
	refused := errs.E(errs.UpstreamFailed, errs.CodeUpstreamAuthFailed, "The single sign-on realm of "+rem.Repo.Name.String()+" refused the repository's credential (upstream_authentication_failed).")
	out := &handshakeError{err: refused, rate: he.rate}
	if he.fail != nil {
		// The classification is what the engine answers with and what the security event names: it follows the new meaning.
		f := *he.fail
		f.err = refused
		out.fail = &f
	}
	return out
}
