package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

// DiscoveryDocumentMaxBytes bounds the body of a discovery request that names no bound of its own (Q-REPO-152): the largest page
// a format's discovery reads is a few MiB (an npm key range of 10,000 names, a NuGet search of 1,000 packages with their
// versions), so 32 MiB leaves room and still stops an upstream that streams without end.
const DiscoveryDocumentMaxBytes = 32 << 20

// Get is one discovery request of a mirror's pass: a GET without a body through the repository's upstream client
// (R-REPO-051, Q-REPO-151).
type Get struct {
	Remote Remote
	URL    *url.URL
	// Accept is the Accept header, "" for none.
	Accept string
	// Max bounds the body; 0 is [DiscoveryDocumentMaxBytes].
	Max int64
	// MirrorHosts are the hosts besides the upstream's and the extraHosts entries that the request may reach, and that never
	// receive a credential.
	MirrorHosts []string
	// Auth is the format's handshake with the upstream (R-OCI-021); nil leaves a 401 as the result. The handshake is not run for
	// a request to a mirror host: no credential and no token goes there.
	Auth Authenticator
	// Exchange is the format's exchange of the repository's credential at a single-sign-on realm (R-ANS-019): a request to the
	// upstream's origin is sent with the exchanged token, as a fetch is. Nil sends the request with the credential as it is.
	Exchange CredentialExchanger
	// Ceiling and JobID are those of the pass, as on a [Request].
	Ceiling int
	JobID   string
}

// GetResult is an answer of the upstream to a [Get] that is not a rate limit.
type GetResult struct {
	Status int
	Header http.Header
	Body   []byte
}

// Get sends the request of a discovery as a background request: the egress rules, the TLS verification and the redirect rules of
// the repository's client apply; the credential goes where it goes for clients (the upstream's origin and the extraHosts entries
// with sendCredentials) and never to a host of MirrorHosts; the request takes a background slot within the ceiling and a client
// fetch can pre-empt it. The host of the URL must be the upstream's, an extraHosts entry or a mirror host, else the answer is 502
// foreign_host_denied and nothing is sent.
//
// The outcomes are those of a background request (background.go): a rate-limit answer is a [*upstream.RateLimitError]; an open
// circuit is a [*BackgroundWait] and nothing is sent; a pre-emption is ErrPreempted. Any other status is a result for the format to
// judge, and none of the outcomes moves or resets the circuit. A transport failure is the classified error of R-REPO-029.
func (e *Engine) Get(ctx context.Context, g Get) (*GetResult, error) {
	rem := g.Remote
	if g.URL == nil || (g.URL.Scheme != "http" && g.URL.Scheme != "https") {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "proxy: a discovery request needs an http or https URL")
	}
	if rem.Repo.Disabled {
		return nil, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return nil, err
	}
	viaMirror, err := cl.AllowedMirror(g.URL, g.MirrorHosts)
	if err != nil {
		return nil, err
	}
	if e.offline(rem) {
		return nil, offlineMiss(rem)
	}
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return nil, err
	}
	if open {
		return nil, circuitWait(retry)
	}
	budget := rem.Settings.Limits.MaxConcurrentFetches
	ceiling := g.Ceiling
	if ceiling <= 0 {
		ceiling = BackgroundShare(budget, rem.Settings.Mirror.Concurrency)
	}
	ctx, hold, err := e.slots.acquireBackgroundJoined(ctx, rem.id, budget, ceiling, nil)
	if err != nil {
		return nil, err
	}
	defer hold.release()
	res, err := e.get(ctx, g, cl, viaMirror, hold.wait)
	if err != nil && errors.Is(context.Cause(ctx), ErrPreempted) {
		return nil, ErrPreempted
	}
	return res, err
}

func (e *Engine) get(ctx context.Context, g Get, cl *upstream.Client, viaMirror bool, slotWait time.Duration) (*GetResult, error) {
	rem := g.Remote
	// Only a mirror host carries the mark: the credential policy of the client decides for every other host.
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	if viaMirror {
		rctx = outbound.WithoutCredentials(rctx)
	}
	rctx = outbound.WithCall(rctx, outbound.Call{Repo: rem.Repo.Name.String(), FlightRole: roleMirror, JobID: g.JobID, SlotWait: outbound.NewSlotWait(slotWait)})
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, g.URL.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	if g.Accept != "" {
		req.Header.Set("Accept", g.Accept)
	}
	req.Header.Set("Accept-Encoding", "identity")
	f := &fetcher{
		e: e, rq: Request{Remote: rem, Path: g.URL.Path, Priority: PriorityBackground, JobID: g.JobID}, st: &reqState{role: roleMirror},
		fl: &flight{done: make(chan struct{})}, cl: cl, target: g.URL,
	}
	start := time.Now()
	// The handshake of a format (R-REPO-033, Q-OCI-82) is run for a request to the upstream's own hosts: a mirror host never
	// receives a credential or a token. The request after the first reports no slot wait (Q-REPO-236).
	var h *handshake
	if g.Auth != nil && !viaMirror {
		h = &handshake{cl: cl, rem: rem, auth: g.Auth, jobID: g.JobID, background: true}
	}
	send := func(c context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error) {
		r, err := cl.HTTP().Do(req.Clone(c)) //nolint:bodyclose // the caller closes it through the idle body below
		if err != nil {
			e.m.request(rem.Repo.Name.String(), 0)
			return nil, nil, nil, err
		}
		e.m.request(rem.Repo.Name.String(), r.StatusCode)
		return r, func() {}, nil, nil
	}
	// A repository whose credential is exchanged at a realm sends the exchanged token, as a fetch does (Q-ANS-51); the exchange covers
	// the request to the upstream's origin and never a mirror host.
	var resp *http.Response
	exchanged := false
	if g.Exchange != nil && !viaMirror {
		c := exchangeCall{cl: cl, rem: rem, x: g.Exchange, jobID: g.JobID, background: true}
		var handled bool
		resp, _, _, handled, err = e.sendExchangedVia(rctx, c, req, send) //nolint:bodyclose // closed through the idle body below
		if handled {
			if err != nil {
				return nil, f.failureOf(ctx, err)
			}
			exchanged = true
		}
	}
	if !exchanged {
		var used *attempt
		first := rctx
		if h != nil {
			if used = e.preemptive(ctx, h, req); used != nil {
				first = outbound.WithAuthorization(rctx, used.value)
			}
		}
		resp, _, _, err = send(first) //nolint:bodyclose // closed through the idle body below
		if err != nil {
			if used != nil && ctx.Err() == nil {
				e.dropToken(used.key, used.value)
			}
			return nil, f.failureOf(ctx, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && h != nil {
			later := func(c context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error) {
				c = outbound.WithCall(c, outbound.Call{Repo: rem.Repo.Name.String(), FlightRole: roleMirror, JobID: g.JobID, SlotWait: outbound.NewSlotWait(0)})
				return send(c)
			}
			resp, _, _, err = e.authenticate(rctx, h, req, resp, func() {}, nil, later, used) //nolint:bodyclose // closed through the idle body below
			if err != nil {
				return nil, f.failureOf(ctx, err)
			}
		}
	}
	idle := new(atomic.Bool)
	body := newIdleBody(resp.Body, time.Duration(rem.Settings.ReadTimeoutMs)*time.Millisecond, rcancel, idle, func() {
		e.m.upstreamDur.WithLabelValues(rem.Repo.Name.String()).Observe(time.Since(start).Seconds())
	})
	defer func() { _ = body.Close() }()
	limit := g.Max
	if limit <= 0 {
		limit = DiscoveryDocumentMaxBytes
	}
	data, err := io.ReadAll(&limitReader{r: body, max: limit})
	switch {
	case errors.Is(err, errTooLargeBody):
		return nil, tooLarge(rem)
	case err != nil:
		return nil, f.transportFailure(ctx, err, idle.Load()).err
	}
	if rl, ok := upstream.RateLimitOf(resp.StatusCode, resp.Header, data[:min(len(data), upstream.SlowDownSniffBytes)], e.now()); ok {
		return nil, &upstream.RateLimitError{RateLimit: rl, Repo: rem.Repo.Name.String()}
	}
	return &GetResult{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// failureOf is the error a background request of a pass returns for err: the mirror's own outcome (a rate limit of the token
// service) or the classified failure.
func (f *fetcher) failureOf(ctx context.Context, err error) error {
	r := f.transportFailure(ctx, err, false)
	if r.own != nil {
		return r.own
	}
	return r.err
}
