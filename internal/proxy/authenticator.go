package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// CacheUpstreamTokens is the name of the registered cache of the tokens that upstream handshakes produced (R-REPO-033,
// R-ARCH-032): per replica, in memory, never written to PostgreSQL or Valkey. The key carries the repository's configuration
// version, the token service and a digest of the credential in effect, so a token is only ever sent under the configuration and
// the credential it was obtained with; the change handler drops the tokens of the older versions.
const CacheUpstreamTokens = "proxy_upstream_tokens"

// CacheUpstreamChallenges is the name of the registered cache of the last challenge an upstream sent a repository (Q-OCI-82): the
// engine hands it to the format before a request, to learn the token the request needs. Per replica, in memory, keyed by the
// repository and its configuration version.
const CacheUpstreamChallenges = "proxy_upstream_challenges"

const (
	// MaxTokenAnswerBytes bounds the answer of an upstream's token service (R-REPO-033).
	MaxTokenAnswerBytes = 64 << 10
	// DefaultTokenLifetime is the lifetime of a token whose service names none.
	DefaultTokenLifetime = 60 * time.Second
	// tokenSkew is how long before its lifetime ends a token stops being used, so that a request does not meet its expiry on
	// the way.
	tokenSkew = 10 * time.Second
	// maxTokenLifetime bounds a lifetime the service asked for: the SafetyTTL of the cache.
	maxTokenLifetime = time.Hour
	// maxAuthorizationBytes bounds the Authorization value an adapter may return.
	maxAuthorizationBytes = 8 << 10
	// maxTokenKeyBytes bounds the key a format names for a token: it comes from the upstream's request and challenge.
	maxTokenKeyBytes = 1 << 10
	// maxChallengeBytes bounds the challenge that is remembered.
	maxChallengeBytes = 8 << 10
	// tokenTimeout bounds one request to a token service, whatever the callers that wait for it do.
	tokenTimeout = 30 * time.Second
	maxTokenKeys = 4096
	// challengeLifetime is how long a remembered challenge is used: it is a hint, and a registry that moves its token service is
	// learned again from the first 401 after that.
	challengeLifetime = 5 * time.Minute
	// rateSniffBytes is how much of a non-2xx answer of a token service is read for a mirror's rate-limit classification.
	rateSniffBytes = upstream.SlowDownSniffBytes
)

// Authenticator is what the engine needs of a format that authenticates to its upstream with a token handshake, a 401 challenge
// answered by a token service (R-REPO-033). internal/formathost adapts a formats.Authenticator to it.
//
// The format reads the protocol and the engine does the I/O: on a 401 for a request, and with the last challenge it remembered
// before a request, the engine asks TokenRequest, sends the request it returns through the repository's client, hands the bounded
// answer to ReadToken and sends the original request with the Authorization value ReadToken returns (Q-OCI-82). A format whose
// adapter has no handshake answers ok false and the 401 stands.
type Authenticator interface {
	// TokenRequest reads a challenge for orig, the request that was answered 401 or is about to be sent, and path ("" for a
	// request that has none: a pass's discovery request, a pass-through query). The format takes the scope, and so the key, from
	// orig and path, and only the realm and the service from the challenge. key names the token in the replica's cache; the engine
	// adds the repository, its configuration version, the token service and the credential. ok false: no handshake for this
	// request.
	TokenRequest(ctx context.Context, b UpstreamBase, orig *http.Request, path string, status int, challenge http.Header) (req *http.Request, key string, ok bool, err error)
	// ReadToken reads the token service's 2xx answer, at most [MaxTokenAnswerBytes] of it: the Authorization value, a valid header
	// field value, and the token's lifetime (0: [DefaultTokenLifetime]). The engine answers every other status itself.
	ReadToken(ctx context.Context, status int, body []byte) (authorization string, ttl time.Duration, err error)
}

// handshakeError is an error of the handshake that is the fetch's answer as it is: it is not an unreachable upstream, and it
// does not count toward the circuit, which describes the registry and not its token service.
type handshakeError struct {
	err *errs.Error
	// fail is the classified failure of a request to the token service, with the host the event is to name; nil for an error
	// that has no security event.
	fail *failure
	// rate is the token service's rate-limit answer to a background request (R-REPO-054): the mirror's back-off applies to it.
	rate *upstream.RateLimitError
}

func (h *handshakeError) Error() string { return h.err.Error() }
func (h *handshakeError) Unwrap() error { return h.err }

// failure is the classification the engine applies: the security event and the metric of a refusal at the token service's host,
// never the circuit.
func (h *handshakeError) failure() failure {
	if h.fail == nil {
		return failure{err: h.err}
	}
	f := *h.fail
	f.count, f.retryAfter = false, 0
	return f
}

// tokenKey names a token: the repository and its configuration version, and a digest of what makes a token valid for a request
// (the format's key, the token service and the credential it was obtained with).
type tokenKey struct {
	repo, version int64
	digest        [sha256.Size]byte
}

type tokenEntry struct {
	authorization string
	expires       time.Time
}

type challengeKey struct{ repo, version int64 }

// tokenCall is one request to a token service that the identical requests of this replica wait for.
type tokenCall struct {
	done chan struct{}
	ent  tokenEntry
	err  error
}

// registerTokens creates the caches of upstream tokens and challenges.
func (e *Engine) registerTokens() {
	e.tokens = cache.Register(e.o.Caches, CacheUpstreamTokens, cache.Options[tokenKey, tokenEntry]{
		MaxEntries: maxTokenKeys, SafetyTTL: maxTokenLifetime, Areas: []string{cluster.AreaRepoConfig},
		OnChange: func(c *cache.Cache[tokenKey, tokenEntry], _ string, _ int, payload json.RawMessage) {
			id, older, ok := repository.ChangeEviction(payload)
			if !ok {
				c.Flush(cache.ReasonChange)
				return
			}
			c.InvalidateFunc(func(k tokenKey) bool { return k.repo == id && older(k.version) })
		},
	})
	e.challenges = cache.Register(e.o.Caches, CacheUpstreamChallenges, cache.Options[challengeKey, http.Header]{
		MaxEntries: maxTokenKeys, SafetyTTL: challengeLifetime, Areas: []string{cluster.AreaRepoConfig},
		OnChange: func(c *cache.Cache[challengeKey, http.Header], _ string, _ int, payload json.RawMessage) {
			id, older, ok := repository.ChangeEviction(payload)
			if !ok {
				c.Flush(cache.ReasonChange)
				return
			}
			c.InvalidateFunc(func(k challengeKey) bool { return k.repo == id && older(k.version) })
		},
	})
}

// handshake is what one request needs to run the handshake for an upstream's challenge.
type handshake struct {
	cl   *upstream.Client
	rem  Remote
	auth Authenticator
	// path is the repository path the request is made for; "" for a request that has none.
	path   string
	caller CallerFacts
	jobID  string
	// background: the request is a mirror's, so a rate-limit answer of the token service is the mirror's rate limit.
	background bool
}

// attempt is a token and where it came from.
type attempt struct {
	key    tokenKey
	value  string
	cached bool
}

// preemptive finds the token a request needs before it is sent, with the last challenge of the repository: a token the replica
// holds, or one it fetches first. nil: the request goes out as it is. It is an optimisation and never the answer (Q-OCI-82): any
// failure (the format, the token service, a token that cannot be had) forgets the remembered challenge and sends the request
// without a token, so that its own 401 runs the handshake with the current challenge. Only a request to the upstream's origin can
// carry a token, so no other asks.
func (e *Engine) preemptive(ctx context.Context, h *handshake, req *http.Request) *attempt {
	if !sameOrigin(req.URL, h.cl.Origin()) {
		return nil
	}
	challenge, ok := e.recallChallenge(h)
	if !ok {
		return nil
	}
	a, ok, err := e.token(ctx, *h, req, challenge, "", true, nil)
	if err != nil {
		e.log.Debug("the pre-emptive token of a request failed; it is sent without one", "repo", h.rem.Repo.Name, "error", err)
		e.forgetChallenge(*h)
		return nil
	}
	if !ok {
		e.forgetChallenge(*h)
		return nil
	}
	return &a
}

// sameOrigin reports whether two URLs have the same scheme, host and effective port.
func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// token finds the token for orig: from the replica's cache or from the token service. ok false: the 401 stands (the format knows
// no handshake for the request, or the repository sends a static bearer token). rejected is the value of a token the upstream
// refused just now: it is dropped from the cache, and no other request's token is. soft is the call before a request: an error of
// the format's TokenRequest then only means the request goes out as it is. stand is the key of a token that was fetched for this
// request and refused: a challenge that names the same key leaves the 401 as the answer, without a second token request.
func (e *Engine) token(ctx context.Context, h handshake, orig *http.Request, challenge http.Header, rejected string, soft bool, stand *tokenKey) (a attempt, ok bool, err error) {
	if h.rem.Settings.AuthMode == repository.AuthBearer {
		return attempt{}, false, nil // the stored secret is the token (R-REPO-033): no handshake
	}
	treq, key, ok, err := h.auth.TokenRequest(ctx, UpstreamBase{URL: h.cl.Origin()}, orig, h.path, http.StatusUnauthorized, challenge)
	switch {
	case err != nil && soft:
		return attempt{}, false, nil
	case err != nil:
		return attempt{}, false, &handshakeError{err: upstreamFailure(err)}
	case !ok:
		return attempt{}, false, nil
	case len(key) > maxTokenKeyBytes:
		return attempt{}, false, &handshakeError{err: tokenError(h.rem, "was given a key that is too long")}
	}
	// The host rule of R-REPO-033: a token service on a host that is neither the upstream nor an extraHosts entry is refused
	// before a byte is sent; the credential goes to it only where the client's hop policy allows (the origin and the
	// sendCredentials hosts). The address names no user: it comes from the upstream.
	if treq == nil || treq.URL == nil || (treq.URL.Scheme != "http" && treq.URL.Scheme != "https") || treq.URL.User != nil {
		return attempt{}, false, &handshakeError{err: tokenError(h.rem, "names no usable address")}
	}
	if err := h.cl.Allowed(treq.URL); err != nil {
		return attempt{}, false, &handshakeError{err: upstreamFailure(err)}
	}
	tk := tokenKeyOf(h, key, treq.URL)
	if stand != nil && *stand == tk {
		return attempt{}, false, nil
	}
	ent, cached, err := e.tokenOnce(ctx, h.rem.Repo.Name.String(), tk, rejected, func(ctx context.Context) (tokenEntry, error) {
		return e.requestToken(ctx, h, treq)
	})
	if err != nil {
		return attempt{}, false, err
	}
	return attempt{key: tk, value: ent.authorization, cached: cached}, true, nil
}

// tokenKeyOf digests what a token is valid for: the format's key, the token service (scheme, host, effective port, path and the
// service it names), and the credential in effect (a rotated secret moves no configuration version).
func tokenKeyOf(h handshake, key string, u *url.URL) tokenKey {
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	fp := h.cl.CredentialFingerprint()
	d := sha256.New()
	for _, part := range []string{key, strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), port, u.EscapedPath(), u.Query().Get("service"), string(fp[:])} {
		_, _ = fmt.Fprintf(d, "%d:%s", len(part), part)
	}
	k := tokenKey{repo: h.rem.id, version: h.rem.Repo.Version}
	copy(k.digest[:], d.Sum(nil))
	return k
}

// rememberChallenge keeps the challenge of an upstream's 401 for the repository's configuration version.
func (e *Engine) rememberChallenge(h handshake, challenge http.Header) {
	if e.challenges == nil {
		return
	}
	vals := challenge.Values("Www-Authenticate")
	size := 0
	for _, v := range vals {
		size += len(v)
	}
	if len(vals) == 0 || size > maxChallengeBytes {
		return
	}
	e.challenges.Set(challengeKey{repo: h.rem.id, version: h.rem.Repo.Version}, http.Header{"Www-Authenticate": append([]string(nil), vals...)})
}

// forgetChallenge drops the remembered challenge of the repository's configuration version.
func (e *Engine) forgetChallenge(h handshake) {
	if e.challenges != nil {
		e.challenges.Remove(challengeKey{repo: h.rem.id, version: h.rem.Repo.Version})
	}
}

// recallChallenge returns a copy of the remembered challenge.
func (e *Engine) recallChallenge(h *handshake) (http.Header, bool) {
	if e.challenges == nil || h.rem.Settings.AuthMode == repository.AuthBearer {
		return nil, false
	}
	c, ok := e.challenges.Get(challengeKey{repo: h.rem.id, version: h.rem.Repo.Version})
	if !ok {
		return nil, false
	}
	return c.Clone(), true
}

// dropToken removes the token from the cache if it is still the value that was rejected: a token another request stored since
// stays.
func (e *Engine) dropToken(key tokenKey, value string) {
	if e.tokens == nil {
		return
	}
	e.amu.Lock()
	defer e.amu.Unlock()
	if ent, ok := e.tokens.Get(key); ok && ent.authorization == value {
		e.tokens.Remove(key)
	}
}

// upstreamFailure is err as the *errs.Error of an upstream failure ([asUpstreamError]).
func upstreamFailure(err error) *errs.Error {
	if ee, ok := errs.As(asUpstreamError(err)); ok {
		return ee
	}
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream answered with something the format cannot use.")
}

func tokenError(rem Remote, what string) *errs.Error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The token service of "+rem.Repo.Name.String()+" "+what+".")
}

// tokenOnce returns the token under key: a current one from the cache that is not the rejected value, else the result of one
// request to the token service that the identical callers of this replica wait for. The request belongs to every caller: it runs in
// a goroutine of its own and ends with the engine, not with the first caller's context, and every caller waits for it or for its
// own context. cached says the token came from the cache.
func (e *Engine) tokenOnce(ctx context.Context, repo string, key tokenKey, rejected string, fn func(context.Context) (tokenEntry, error)) (ent tokenEntry, cached bool, err error) {
	e.amu.Lock()
	if e.tokens != nil {
		if got, ok := e.tokens.Get(key); ok {
			switch {
			case got.authorization == rejected:
				e.tokens.Remove(key)
			case e.now().Before(got.expires):
				e.amu.Unlock()
				return got, true, nil
			default:
				e.tokens.Remove(key)
			}
		}
	}
	c, joined := e.acalls[key]
	if !joined {
		c = &tokenCall{done: make(chan struct{})}
		e.acalls[key] = c
	}
	e.amu.Unlock()
	if !joined {
		e.leadToken(ctx, repo, key, c, fn)
	}
	select {
	case <-c.done:
		return c.ent, false, c.err
	case <-ctx.Done():
		return tokenEntry{}, false, &handshakeError{err: errs.E(errs.Unavailable, errs.CodeUnavailable,
			"The request ended before the token service of "+repo+" answered.", errs.WithCause(ctx.Err()))}
	}
}

// leadToken starts the request to the token service for the call c. Whatever happens in it, the call is removed and released.
func (e *Engine) leadToken(ctx context.Context, repo string, key tokenKey, c *tokenCall, fn func(context.Context) (tokenEntry, error)) {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenTimeout)
	stop := context.AfterFunc(e.base, cancel)
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		defer cancel()
		defer stop()
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("the token handshake of a format panicked", "repo", repo, "panic", r)
				c.ent, c.err = tokenEntry{}, &handshakeError{err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamError,
					"The token handshake of "+repo+" failed.")}
			}
			e.amu.Lock()
			if c.err == nil && e.tokens != nil && e.now().Before(c.ent.expires) {
				// Stored without the fill rule of R-ARCH-018: the key holds the configuration version this request read, so
				// the token is reached only under the configuration it was obtained with.
				e.tokens.Set(key, c.ent)
			}
			delete(e.acalls, key)
			e.amu.Unlock()
			close(c.done)
		}()
		c.ent, c.err = fn(bctx)
	}()
}

// tokenFailure is the failure of a request to the token service at treq's host: the answer, the security event and the metric,
// and never the circuit.
func tokenFailure(treq *http.Request, f failure) *handshakeError {
	f.host = treq.URL.Hostname()
	return &handshakeError{err: f.err, fail: &f}
}

// requestToken sends the token service's request through the repository's client and reads its answer, bounded.
func (e *Engine) requestToken(ctx context.Context, h handshake, treq *http.Request) (tokenEntry, error) {
	rem := h.rem
	name := rem.Repo.Name.String()
	// No flight: flight_role stays empty in the upstream log record, as for an exchange (Q-REPO-92). The credential goes to the
	// service's host and to no redirect target: the redirect is the upstream's to choose.
	ctx = outbound.WithCredentialsOnTheFirstHopOnly(outbound.WithCall(ctx, outbound.Call{OnBehalfOf: h.caller.Principal, Repo: name, Attempt: 1, JobID: h.jobID}))
	out := treq.Clone(ctx)
	out.Header.Del("Authorization") // the client's hop policy alone adds the credential, to the hosts that may have it
	out.Header.Set("Accept-Encoding", "identity")
	resp, err := h.cl.HTTP().Do(out) //nolint:gosec // G704: the host was checked with Client.Allowed above and every address is checked against the egress policy (R-REPO-038)
	if err != nil {
		e.m.request(name, 0)
		return tokenEntry{}, tokenFailure(treq, e.classifyError(rem, treq.URL, err, false))
	}
	defer func() { _ = resp.Body.Close() }()
	e.m.request(name, resp.StatusCode)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		he := tokenFailure(treq, classifyStatus(rem, resp))
		if h.background {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, rateSniffBytes))
			if rl, ok := upstream.RateLimitOf(resp.StatusCode, resp.Header, body, e.now()); ok {
				he.rate = &upstream.RateLimitError{RateLimit: rl, Repo: name}
			}
		}
		return tokenEntry{}, he
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxTokenAnswerBytes+1))
	switch {
	case err != nil:
		return tokenEntry{}, tokenFailure(treq, e.classifyError(rem, treq.URL, err, false))
	case len(body) > MaxTokenAnswerBytes:
		return tokenEntry{}, &handshakeError{err: tokenError(rem, "answered with more than 64 KiB")}
	}
	value, ttl, err := h.auth.ReadToken(ctx, resp.StatusCode, body)
	if err != nil {
		return tokenEntry{}, &handshakeError{err: upstreamFailure(err)}
	}
	if strings.TrimSpace(value) == "" || len(value) > maxAuthorizationBytes || !httpguts.ValidHeaderFieldValue(value) {
		return tokenEntry{}, &handshakeError{err: tokenError(rem, "answered with a token that cannot be sent")}
	}
	if ttl <= 0 {
		ttl = DefaultTokenLifetime
	}
	ttl = min(ttl, maxTokenLifetime)
	return tokenEntry{authorization: value, expires: e.now().Add(ttl - tokenSkew)}, nil
}

// asHandshakeError returns the handshake error in err's chain.
func asHandshakeError(err error) (*handshakeError, bool) {
	var h *handshakeError
	if errors.As(err, &h) {
		return h, true
	}
	return nil, false
}

// authenticator is the handshake of the fetch: the explicit one of a query or a pass's request, else the adapter's.
func (f *fetcher) authenticator() Authenticator {
	if f.auth != nil {
		return f.auth
	}
	a, _ := f.rq.Adapter.(Authenticator)
	return a
}

// handshakeFor is the handshake the fetch runs; nil when the fetch has no authenticator.
func (f *fetcher) handshakeFor() *handshake {
	auth := f.authenticator()
	if auth == nil {
		return nil
	}
	h := &handshake{cl: f.cl, rem: f.rq.Remote, auth: auth, caller: f.rq.Caller, jobID: f.rq.JobID, background: f.rq.background()}
	if !f.noPath {
		h.path = f.rq.Path
	}
	return h
}

// retryFunc sends the request again under ctx, which carries the token ([outbound.WithAuthorization]); it returns what the
// first send returned.
type retryFunc func(ctx context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error)

// authenticate answers an upstream 401 (resp, for req) with the handshake of R-REPO-033 and returns the response to the retry,
// or resp itself when the 401 stands. used is the token req was sent with, nil for none. The 401's own challenge is the one to
// follow: a token the replica held that the answer shows rejected is dropped and a fresh one is obtained; a token that was
// fetched for the request from a remembered challenge and refused is followed by one handshake of the 401's challenge when that
// names another key (the registry moved its token service), and otherwise the 401 stands. A fresh token that is refused leaves
// the 401 as the answer. A challenge is remembered when it came from the upstream's origin and its token was accepted; a
// remembered challenge that led to a refusal is forgotten. resp, rcancel and idle are what the first send returned for req.
func (e *Engine) authenticate(ctx context.Context, h *handshake, req *http.Request, resp *http.Response, rcancel context.CancelFunc, idle *atomic.Bool, retry retryFunc, used *attempt) (*http.Response, context.CancelFunc, *atomic.Bool, error) {
	rejected := ""
	var stand *tokenKey
	if used != nil {
		rejected = used.value
		if !used.cached {
			stand = &used.key
		}
	}
	for range 2 {
		challenge := resp.Header.Clone()
		// The challenge of this answer is remembered only if this answer came from the origin, not from a redirect target.
		fromOrigin := resp.Request != nil && sameOrigin(resp.Request.URL, h.cl.Origin())
		a, ok, err := e.token(ctx, *h, req, challenge, rejected, false, stand)
		stand = nil
		if err != nil {
			if used != nil {
				e.forgetChallenge(*h)
			}
			_ = resp.Body.Close()
			rcancel()
			return nil, nil, idle, err
		}
		if !ok {
			if used != nil {
				e.forgetChallenge(*h)
				e.dropToken(used.key, used.value)
			}
			return resp, rcancel, idle, nil
		}
		_ = resp.Body.Close()
		rcancel()
		resp, rcancel, idle, err = retry(outbound.WithAuthorization(ctx, a.value))
		if err != nil {
			if ctx.Err() == nil {
				e.dropToken(a.key, a.value) // a token the transport refused is not kept for its lifetime
			}
			return nil, nil, idle, err
		}
		if resp.StatusCode != http.StatusUnauthorized {
			if fromOrigin {
				e.rememberChallenge(*h, challenge)
			}
			return resp, rcancel, idle, nil
		}
		if !a.cached {
			e.forgetChallenge(*h)
			return resp, rcancel, idle, nil
		}
		rejected = a.value
	}
	return resp, rcancel, idle, nil
}
