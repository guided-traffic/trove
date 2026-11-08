package proxy

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

// retryPause is the wait before the one retry of a connection failure that happened
// before a request byte was sent (R-REPO-029).
const retryPause = 250 * time.Millisecond

// statusBox holds the Cache-Status of the response a fetch ends in, for the
// upstream log record that is written when the request completes (R-OPS-011).
type statusBox struct {
	mu sync.Mutex
	v  string
}

func (b *statusBox) set(s string) {
	b.mu.Lock()
	b.v = s
	b.mu.Unlock()
}

func (b *statusBox) get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// fetcher is one fetch of an upstream item: the flight's work (R-REPO-026 step 3
// onward). It holds the facts the steps share.
type fetcher struct {
	e   *Engine
	rq  Request
	st  *reqState
	fl  *flight
	cl  *upstream.Client
	box statusBox

	// sentAt is when the upstream request was sent, on this replica's clock: the "c"
	// of a negative entry (R-REPO-028).
	sentAt time.Time
	target *url.URL
	// stored is the entry this fetch revalidates, if it is not marked gone.
	stored     entry
	haveStored bool
	// hs is the handshake the request ran, nil when it ran none (Q-OCI-127).
	hs          *handshake
	conditional bool

	// hold is the slot hold whose repository share the fetch returns when its upstream exchange ends (Q-REPO-235): its own, or
	// the background slot's when the fetch is the slot's item. Nil for a fetch that holds none or leaves the share to another.
	hold *slotHold
	// auth is the handshake of a request that has no adapter of its own (a pass-through query, a pass's request); nil takes the
	// adapter's, if it has one (authenticator.go). noPath says the request has no repository path to hand the format.
	auth   Authenticator
	noPath bool
	// wait is the wait for the fetch's own slot, reported by its first upstream request as slot_wait_ms; hasWait tells a
	// fetch that took a slot from one that took none (null).
	wait    time.Duration
	hasWait bool
	// waitReported: a request of the fetch has reported the wait already; the challenged request's retry, a revalidation
	// request and the GET after it report 0 (Q-REPO-236).
	waitReported bool
	// plain is the GET plainRequest built, kept for the request that follows a revalidation.
	plain *http.Request
	// unconditional: the next GET carries no validators of the stored copy (a revalidation said the document moved).
	unconditional bool
}

// exchanged returns the repository's share of the fetch's slot: its upstream exchange has ended.
func (f *fetcher) exchanged() { f.hold.exchanged() }

// fetch runs the fetch of an item under the flight fl: slots, circuit, the request
// to the upstream, classification, verification, storage (R-REPO-025 step 10).
func (e *Engine) fetch(ctx context.Context, rq Request, st *reqState, fl *flight) flightResult {
	st.fetched.Store(true)
	f := &fetcher{e: e, rq: rq, st: st, fl: fl}
	return f.run(ctx)
}

func (f *fetcher) run(ctx context.Context) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	if rq.Kind == Artifact && !admitsDigests(rq) {
		e.m.integrity.WithLabelValues(rem.Repo.Name.String(), "missing").Inc()
		return flightResult{err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamDigestMissing,
			"The upstream announces no SHA-256 or stronger digest for the item, which "+rem.Repo.Name.String()+" requires.")}
	}
	if rq.slot != nil {
		// The pass's request holds its background slot already (Q-REPO-179): the fetch runs under the slot's fate.
		var done func()
		ctx, done = rq.slot.link(ctx, f.fl)
		defer done()
		if rq.slot.owns(rq) {
			f.hold = rq.slot.hold // the discovery document resolved under the slot returns nothing
		}
	}
	if err := f.discover(ctx); err != nil {
		return resultOf(err)
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return flightResult{err: err}
	}
	f.cl = cl
	if rq.background() {
		// A pass reads the circuit and waits while it is open; it takes no slot for that (R-REPO-054).
		if open, retry, err := e.circuitPeek(ctx, rem); err != nil {
			return flightResult{err: err}
		} else if open {
			return flightResult{own: circuitWait(retry)}
		}
	}
	if rq.slot == nil {
		var hold *slotHold
		var err error
		if ctx, hold, err = e.acquireSlot(ctx, rq, f.fl); err != nil {
			return resultOf(err)
		}
		defer hold.release()
		f.hold, f.wait, f.hasWait = hold, hold.wait, true
	}
	res := f.inSlot(ctx)
	if rq.background() && errors.Is(context.Cause(ctx), ErrPreempted) && !res.stored && res.doc == nil && !res.negative {
		// A client fetch (or the yield of another process) took the slot: the upstream request was cancelled with ctx, the
		// partial body is gone with the spool, and no entry of any kind was written. The flight is released with failed, so
		// whoever waits for it fetches for itself at once (R-REPO-055).
		return flightResult{own: ErrPreempted}
	}
	return res
}

// inSlot is the fetch from the moment it holds its slot: the circuit, the request, the classification and the storage.
func (f *fetcher) inSlot(ctx context.Context) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	if rq.background() {
		// No probe and no claim on the half-open circuit: the request reads the circuit and waits (R-REPO-054, Q-REPO-139).
		open, retry, err := e.circuitPeek(ctx, rem)
		if err != nil {
			return flightResult{err: err}
		}
		if open {
			return flightResult{own: circuitWait(retry)}
		}
	} else {
		adm, err := e.circuitAdmit(ctx, rem)
		if err != nil {
			return flightResult{err: err}
		}
		if adm.open {
			return flightResult{err: assumedOffline(adm.retryAfter)}
		}
		defer adm.release()
	}

	if rq.Kind == Metadata {
		if en, found, err := e.getEntry(ctx, rem.id, rq.Path, rq.Variant); err != nil {
			return flightResult{err: err}
		} else if found && !en.Gone() {
			f.stored, f.haveStored = en, true
		}
	}
	if res, done := f.revalidate(ctx); done {
		return res
	}
	req, err := f.request(ctx)
	if err != nil {
		return flightResult{err: err}
	}
	resp, rcancel, idle, err := f.send(ctx, req) //nolint:bodyclose // respond closes the body
	if err != nil {
		return f.transportFailure(ctx, err, false)
	}
	defer rcancel()
	res := f.respond(ctx, resp, idle)
	return res
}

// acquireSlot takes the fetch slot of the request (R-REPO-040, R-REPO-055): a client's from the clients' queue, with the
// queue wait observed; a mirror's background slot, at most the background share of the repository, under a context that
// a client's pre-emption cancels. The returned context is the one the fetch runs under; the hold's wait is how long the fetch
// waited.
func (e *Engine) acquireSlot(ctx context.Context, rq Request, fl *flight) (context.Context, *slotHold, error) {
	rem := rq.Remote
	budget := rem.Settings.Limits.MaxConcurrentFetches
	if rq.background() {
		ceiling := rq.Ceiling
		if ceiling <= 0 {
			ceiling = BackgroundShare(budget, rem.Settings.Mirror.Concurrency)
		}
		// A follower of this replica that joins the fetch's flight keeps it from being pre-empted (R-REPO-055).
		return e.slots.acquireBackgroundJoined(ctx, rem.id, budget, ceiling, func() bool { return fl.joined.Load() > 0 })
	}
	hold, wait, err := e.slots.acquire(ctx, rem.id, budget, e.o.Config.QueueTimeout())
	e.m.queueWait.WithLabelValues(rem.Repo.Name.String()).Observe(wait.Seconds())
	return ctx, hold, err
}

// systemPopulate is the populate check of a request the engine makes for itself, with no
// caller behind it: the discovery document a probe needs.
type systemPopulate struct{}

func (systemPopulate) May(context.Context) bool              { return true }
func (systemPopulate) Deny(context.Context) error            { return nil }
func (systemPopulate) Record(context.Context, meta.Tx) error { return nil }
func (systemPopulate) Endpoint() (string, bool)              { return "", true }

// discover resolves the discovery document of a format whose upstream addressing follows
// one (WP3.0b) and binds the parsed data to the fetch's adapter. The document is an
// ordinary metadata entry of the repository: stored, fresh or revalidated, collapsed and
// counted like every other, and it is resolved before the fetch takes its slot, so a
// repository limited to one concurrent fetch cannot wait for itself. The caller's
// populate check applies: a caller that may not fill the cache gets the stored document
// or the refusal. A request for the discovery document itself needs no discovery.
func (f *fetcher) discover(ctx context.Context) error {
	rq := f.rq
	path, variant, ok := rq.Adapter.DiscoveryDocument()
	if !ok || (rq.Path == path && rq.Variant == variant) {
		return nil
	}
	pop := rq.Populate
	if pop == nil {
		pop = systemPopulate{}
	}
	doc, err := f.e.Metadata(ctx, Request{
		Remote: rq.Remote, Path: path, Variant: variant, Kind: Metadata, Mode: rq.Mode, Populate: pop,
		MaxBytes: rq.MaxBytes, Adapter: rq.Adapter, Caller: rq.Caller, Priority: rq.Priority, Ceiling: rq.Ceiling, JobID: rq.JobID, slot: rq.slot,
	})
	if err != nil {
		return err
	}
	if doc == nil || doc.Status.Negative || doc.Bytes == nil {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream of "+rq.Remote.Repo.Name.String()+" has no discovery document, so the addresses of its resources are unknown.")
	}
	b, err := doc.Bytes(ctx)
	if err != nil {
		return err
	}
	bound, err := rq.Adapter.Discover(ctx, b)
	if err != nil {
		return asUpstreamError(err)
	}
	f.rq.Adapter = bound
	return nil
}

// plainRequest builds the upstream request without the validators of a stored copy: the format's, with the URL map applied, the
// foreign-host check and the identity encoding.
func (f *fetcher) plainRequest(ctx context.Context) (*http.Request, error) {
	if f.plain != nil {
		// Built once per fetch: the format's request and the URL map's transaction are not repeated by the revalidation and the GET.
		return f.plain.Clone(ctx), nil
	}
	req, err := f.buildRequest(ctx)
	if err != nil {
		return nil, err
	}
	f.plain = req
	return req.Clone(ctx), nil
}

func (f *fetcher) buildRequest(ctx context.Context) (*http.Request, error) {
	rq := f.rq
	rem := rq.Remote
	req, err := rq.Adapter.UpstreamRequest(ctx, UpstreamBase{URL: f.cl.Origin()}, rq.Path, rq.Variant)
	if err != nil {
		return nil, err
	}
	// The URL map holds the upstream URL a document named for a path of the same package
	// (R-REPO-043): an artifact's, and a metadata document's, such as a page of a listing
	// that the index does not inline (WP3.0b). The foreign-host check below applies to it.
	mapped, err := f.e.upstreamURL(ctx, rem.id, rq.Path)
	if err != nil {
		return nil, err
	}
	if mapped != "" {
		u, err := url.Parse(mapped)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The recorded upstream URL of the item is not usable.")
		}
		req.URL, req.Host = u, ""
	}
	if err := f.cl.Allowed(req.URL); err != nil {
		return nil, err
	}
	req.Header.Del("Authorization") // the client's hop policy alone adds the credential
	req.Header.Set("Accept-Encoding", "identity")
	f.target = req.URL
	return req, nil
}

// request builds the upstream request: the format's, with the foreign-host check, the identity encoding and the validators of the
// stored copy.
func (f *fetcher) request(ctx context.Context) (*http.Request, error) {
	rq := f.rq
	req, err := f.plainRequest(ctx)
	if err != nil {
		return nil, err
	}
	// A stored document whose object is missing or damaged is fetched whole: a 304 would renew an entry that has no bytes
	// to serve (Q-REPO-202). One that a revalidation said has moved is fetched whole as well.
	if f.haveStored && rq.Kind == Metadata && !f.stored.Damaged() && !f.unconditional {
		switch {
		case f.stored.ETag != "":
			req.Header.Set("If-None-Match", f.stored.ETag)
			f.conditional = true
		case !f.stored.LastModified.IsZero():
			req.Header.Set("If-Modified-Since", f.stored.LastModified.UTC().Format(http.TimeFormat))
			f.conditional = true
		}
	}
	f.target = req.URL
	return req, nil
}

// send sends the request ([fetcher.sendOnce]) with the format's token handshake when it has one (R-REPO-033, Q-OCI-82): with the
// token the last challenge of the repository names, and, when the upstream answers 401, with the token that challenge names. The
// returned cancel ends the request and its body.
func (f *fetcher) send(ctx context.Context, req *http.Request) (resp *http.Response, cancel context.CancelFunc, idle *atomic.Bool, err error) {
	h := f.handshakeFor()
	if h == nil {
		return f.sendOnce(ctx, req)
	}
	f.hs = h
	used := f.e.preemptive(ctx, h, req)
	sctx := ctx
	if used != nil {
		sctx = outbound.WithAuthorization(ctx, used.value)
	}
	resp, cancel, idle, err = f.sendOnce(sctx, req)
	if err != nil {
		if used != nil && ctx.Err() == nil {
			f.e.dropToken(used.key, used.value)
		}
		return resp, cancel, idle, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, cancel, idle, nil
	}
	return f.e.authenticate(ctx, h, req, resp, cancel, idle, func(ctx context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error) {
		return f.sendOnce(ctx, req)
	}, used)
}

// sendOnce sends the request with the one retry of R-REPO-029 and the call facts of the
// upstream log. The returned cancel ends the request and its body.
func (f *fetcher) sendOnce(ctx context.Context, req *http.Request) (resp *http.Response, cancel context.CancelFunc, idle *atomic.Bool, err error) {
	e, rq := f.e, f.rq
	rem := rq.Remote
	idle = new(atomic.Bool)
	f.sentAt = e.now()
	for attempt := 1; ; attempt++ {
		rctx, rcancel := context.WithCancel(ctx)
		var wrote atomic.Bool
		trace := &httptrace.ClientTrace{WroteHeaderField: func(string, []string) { wrote.Store(true) }}
		rctx = httptrace.WithClientTrace(rctx, trace)
		call := outbound.Call{
			OnBehalfOf: rq.Caller.Principal, Repo: rem.Repo.Name.String(), Attempt: attempt, FlightRole: f.st.role,
			CollapsedWaiters: func() int { return int(f.fl.joined.Load()) }, CacheStatus: f.box.get,
		}
		if rq.background() {
			// The record of a pass's request names the pass's job and no caller (R-REPO-057).
			call.FlightRole, call.OnBehalfOf, call.JobID = roleMirror, "", rq.JobID
		}
		if w, ok := f.slotWait(attempt); ok {
			call.SlotWait = outbound.NewSlotWait(w)
		}
		rctx = outbound.WithCall(rctx, call)
		start := time.Now()
		resp, err = f.cl.HTTP().Do(req.Clone(rctx))
		if err == nil {
			f.e.m.request(rem.Repo.Name.String(), resp.StatusCode)
			resp.Body = newIdleBody(resp.Body, time.Duration(rem.Settings.ReadTimeoutMs)*time.Millisecond, rcancel, idle, func() {
				f.e.m.upstreamDur.WithLabelValues(rem.Repo.Name.String()).Observe(time.Since(start).Seconds())
			})
			return resp, rcancel, idle, nil
		}
		rcancel()
		f.e.m.request(rem.Repo.Name.String(), 0)
		if attempt >= 2 || wrote.Load() || !retryable(err) || ctx.Err() != nil {
			return nil, nil, idle, err
		}
		select {
		case <-time.After(retryPause):
		case <-ctx.Done():
			return nil, nil, idle, ctx.Err()
		}
	}
}

// slotWait is the slot_wait_ms of the upstream request of an attempt (Q-REPO-236): the first request sent under a slot reports
// the wait for it, every later one 0. A fetch that took no slot reports none.
func (f *fetcher) slotWait(attempt int) (time.Duration, bool) {
	switch {
	case f.rq.slot != nil:
		return f.rq.slot.takeWait(), true
	case !f.hasWait:
		return 0, false
	case attempt > 1, f.waitReported:
		return 0, true
	}
	f.waitReported = true
	return f.wait, true
}

// transportFailure turns an error of the transfer into the fetch's result: the
// answer, the circuit, the metrics and the security events.
func (f *fetcher) transportFailure(ctx context.Context, err error, idle bool) flightResult {
	e, rq := f.e, f.rq
	if he, ok := asHandshakeError(err); ok && he.rate != nil && rq.background() {
		return flightResult{own: he.rate} // the token service rate limits a mirror's request: R-REPO-054 applies
	}
	fail := e.classifyError(rq.Remote, f.target0(), err, idle)
	return f.failed(ctx, fail)
}

func (f *fetcher) target0() *url.URL {
	if f.target != nil {
		return f.target
	}
	return f.cl.Origin()
}

// failed applies a classified failure.
func (f *fetcher) failed(ctx context.Context, fail failure) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	// A background request counts toward the circuit nowhere: the circuit is the clients' (R-REPO-054, Q-REPO-139).
	if fail.count && !rq.background() {
		why := ""
		if fail.err != nil {
			why = fail.err.Code
		}
		e.circuitFailure(context.WithoutCancel(ctx), rem, why, fail.retryAfter)
	}
	if fail.sourceNetwork {
		e.m.sourceNet.WithLabelValues(rem.Repo.Name.String()).Inc()
	}
	if fail.egress {
		e.m.egressDenied.WithLabelValues(rem.Repo.Name.String()).Inc()
	}
	host := fail.host
	if host == "" {
		host = f.target0().Hostname()
	}
	switch fail.event {
	case "egress":
		e.securityEvent(ctx, rq, audit.EventProxyEgressDenied, host, map[string]string{"host": host})
	case "tls":
		e.securityEvent(ctx, rq, audit.EventProxyTLSUntrusted, host, map[string]string{"host": host})
	}
	return flightResult{err: fail.err, status: fail.status}
}

// respond handles the response by status.
func (f *fetcher) respond(ctx context.Context, resp *http.Response, idle *atomic.Bool) flightResult {
	defer func() { _ = resp.Body.Close() }()
	s := resp.StatusCode
	switch {
	case s == http.StatusNotModified && f.conditional:
		return f.notModified(ctx, resp)
	case s >= 200 && s < 300:
		if f.rq.Kind == Artifact {
			return f.artifact(ctx, resp, idle)
		}
		return f.metadata(ctx, resp, idle)
	case s == http.StatusNotFound || s == http.StatusGone:
		return f.notFound(ctx, resp)
	case !f.haveStored && f.stoodMissing(resp):
		// A miss: a negative entry. A document that is stored keeps serving stale (the 401 is a failure of the revalidation), and no gone
		// mark is set from a 401, which would stop the cached manifests and blobs of the image being served (R-REPO-029, Q-OCI-127).
		return f.notFound(ctx, resp)
	}
	if f.rq.background() {
		// The mirror's own classification of a throttling answer (R-REPO-054); a client keeps R-REPO-029's.
		if res, ok := f.rateLimited(resp); ok {
			return res
		}
	}
	return f.failed(ctx, classifyStatus(f.rq.Remote, resp))
}

// success records a 2xx or a 304 for the circuit: a client's request does, a background request never does, whatever the
// upstream answered (R-REPO-054, Q-REPO-139).
func (f *fetcher) success(ctx context.Context) {
	if !f.rq.background() {
		f.e.circuitSuccess(ctx, f.rq.Remote)
	}
}

// stoodMissing reports whether the format reads a 401 that stood after the handshake as the upstream's missing resource.
func (f *fetcher) stoodMissing(resp *http.Response) bool {
	if resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	return f.hs != nil && f.hs.stoodMissing(resp)
}

// notFound records the upstream's 404 or 410: a negative entry before the flight is
// released, and for a stored document the gone mark (R-REPO-028, R-REPO-029). Neither
// counts toward the circuit.
func (f *fetcher) notFound(ctx context.Context, resp *http.Response) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	_ = resp.Body.Close()
	f.exchanged() // no body is read: the upstream's share ends (Q-REPO-235)
	if err := e.neg.record(ctx, rem, rq.Path, rq.Variant, resp.StatusCode, f.sentAt); err != nil {
		e.log.Warn("recording a negative entry failed", "repo", rem.Repo.Name, "error", err)
	}
	if rq.Kind == Metadata && f.haveStored {
		if _, err := e.markGone(ctx, rq); err != nil {
			return flightResult{err: err}
		}
	}
	return flightResult{negative: true, status: resp.StatusCode}
}

// contentTypeBlocked applies R-REPO-042: a response whose media type is on the
// blocked list is refused unless the format expects it or the repository allows it.
func (f *fetcher) contentTypeBlocked(resp *http.Response) bool {
	rem := f.rq.Remote
	if !rem.Settings.Integrity.BlockUnexpectedContentTypes {
		return false
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	mt = strings.ToLower(mt)
	if !slices.Contains(f.e.o.Config.BlockedContentTypes, mt) {
		return false
	}
	if slices.Contains(rem.Settings.Integrity.AllowedContentTypes, mt) {
		return false
	}
	if x, ok := f.rq.Adapter.(ExpectedTypes); ok && x.ExpectsType(f.rq.Path, mt) {
		return false
	}
	return true
}

// limit is the effective size limit of the item (R-REPO-040): the repository's, else
// the system's, and a format's bound below both.
func (f *fetcher) limit() int64 {
	rem := f.rq.Remote
	var lim int64
	if f.rq.Kind == Artifact {
		lim = int64(f.e.o.Config.MaxArtifactBytes)
		if v := rem.Settings.Limits.MaxArtifactBytes; v != nil {
			lim = *v
		}
	} else {
		lim = int64(f.e.o.Config.MaxMetadataBytes)
		if v := rem.Settings.Limits.MaxMetadataBytes; v != nil {
			lim = *v
		}
	}
	if f.rq.Kind == Metadata && f.rq.MaxBytes > 0 && (lim <= 0 || f.rq.MaxBytes < lim) {
		lim = f.rq.MaxBytes
	}
	return lim
}

// admitBody applies the checks that precede the first byte: the adapter's, the
// content type and the announced length against the limit. A refusal is the fetch's
// result.
func (f *fetcher) admitBody(ctx context.Context, resp *http.Response) (flightResult, bool) {
	rem := f.rq.Remote
	if err := f.rq.Adapter.AcceptResponse(f.rq.Path, resp); err != nil {
		return flightResult{err: asUpstreamError(err), status: resp.StatusCode}, false
	}
	if f.contentTypeBlocked(resp) {
		return f.failed(ctx, failure{count: true, status: resp.StatusCode, err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamContentTypeBlocked,
			"The upstream of "+rem.Repo.Name.String()+" answered with a media type that is not expected for the item.")}), false
	}
	if lim := f.limit(); lim > 0 && resp.ContentLength > lim {
		return flightResult{err: tooLarge(rem), status: resp.StatusCode}, false
	}
	return flightResult{}, true
}

func tooLarge(rem Remote) error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge,
		"The upstream item is larger than the limit of "+rem.Repo.Name.String()+".")
}

// asUpstreamError keeps an *errs.Error and gives any other error of an adapter the
// shape of an upstream error.
func asUpstreamError(err error) error {
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream answered with something the format cannot use.", errs.WithCause(err))
}

// bodyReader returns the reader the item is spooled from: the response body bounded
// by the size limit.
func (f *fetcher) bodyReader(resp *http.Response, tee io.Writer) io.Reader {
	var r io.Reader = &limitReader{r: resp.Body, max: f.limit()}
	if tee != nil {
		r = io.TeeReader(r, tee)
	}
	return r
}
