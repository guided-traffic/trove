package proxy

import (
	"context"
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
	stored      entry
	haveStored  bool
	conditional bool
}

// fetch runs the fetch of an item under the flight fl: slots, circuit, the request
// to the upstream, classification, verification, storage (R-REPO-025 step 10).
func (e *Engine) fetch(ctx context.Context, rq Request, st *reqState, fl *flight) flightResult {
	f := &fetcher{e: e, rq: rq, st: st, fl: fl}
	return f.run(ctx)
}

func (f *fetcher) run(ctx context.Context) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	if rq.Kind == Artifact && !admitsDigests(rq) {
		e.m.integrity.WithLabelValues(rem.Repo.Key, "missing").Inc()
		return flightResult{err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamDigestMissing,
			"The upstream announces no SHA-256 or stronger digest for the item, which "+rem.Repo.Key+" requires.")}
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return flightResult{err: err}
	}
	f.cl = cl
	release, wait, err := e.slots.acquire(ctx, rem.id, rem.Settings.Limits.MaxConcurrentFetches, e.o.Config.QueueTimeout())
	e.m.queueWait.WithLabelValues(rem.Repo.Key).Observe(wait.Seconds())
	if err != nil {
		return flightResult{err: err}
	}
	defer release()
	adm, err := e.circuitAdmit(ctx, rem)
	if err != nil {
		return flightResult{err: err}
	}
	if adm.open {
		return flightResult{err: assumedOffline(adm.retryAfter)}
	}
	defer adm.release()

	if rq.Kind == Metadata {
		if en, found, err := e.getEntry(ctx, rem.id, rq.Path, rq.Variant); err != nil {
			return flightResult{err: err}
		} else if found && !en.Gone() {
			f.stored, f.haveStored = en, true
		}
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

// request builds the upstream request: the format's, with the foreign-host check,
// the identity encoding and the validators of the stored copy.
func (f *fetcher) request(ctx context.Context) (*http.Request, error) {
	rq := f.rq
	rem := rq.Remote
	req, err := rq.Adapter.UpstreamRequest(ctx, UpstreamBase{URL: f.cl.Origin()}, rq.Path, rq.Variant)
	if err != nil {
		return nil, err
	}
	if rq.Kind == Artifact {
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
	}
	if err := f.cl.Allowed(req.URL); err != nil {
		return nil, err
	}
	req.Header.Del("Authorization") // the client's hop policy alone adds the credential
	req.Header.Set("Accept-Encoding", "identity")
	if f.haveStored && rq.Kind == Metadata {
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

// send sends the request with the one retry of R-REPO-029 and the call facts of the
// upstream log. The returned cancel ends the request and its body.
func (f *fetcher) send(ctx context.Context, req *http.Request) (resp *http.Response, cancel context.CancelFunc, idle *atomic.Bool, err error) {
	e, rq := f.e, f.rq
	rem := rq.Remote
	idle = new(atomic.Bool)
	f.sentAt = e.now()
	for attempt := 1; ; attempt++ {
		rctx, rcancel := context.WithCancel(ctx)
		var wrote atomic.Bool
		trace := &httptrace.ClientTrace{WroteHeaderField: func(string, []string) { wrote.Store(true) }}
		rctx = httptrace.WithClientTrace(rctx, trace)
		rctx = outbound.WithCall(rctx, outbound.Call{
			OnBehalfOf: rq.Caller.Principal, Repo: rem.Repo.Key, Attempt: attempt, FlightRole: f.st.role,
			CollapsedWaiters: func() int { return int(f.fl.joined.Load()) }, CacheStatus: f.box.get,
		})
		start := time.Now()
		resp, err = f.cl.HTTP().Do(req.Clone(rctx))
		if err == nil {
			f.e.m.request(rem.Repo.Key, resp.StatusCode)
			resp.Body = newIdleBody(resp.Body, time.Duration(rem.Settings.ReadTimeoutMs)*time.Millisecond, rcancel, idle, func() {
				f.e.m.upstreamDur.WithLabelValues(rem.Repo.Key).Observe(time.Since(start).Seconds())
			})
			return resp, rcancel, idle, nil
		}
		rcancel()
		f.e.m.request(rem.Repo.Key, 0)
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

// transportFailure turns an error of the transfer into the fetch's result: the
// answer, the circuit, the metrics and the security events.
func (f *fetcher) transportFailure(ctx context.Context, err error, idle bool) flightResult {
	e, rq := f.e, f.rq
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
	if fail.count {
		why := ""
		if fail.err != nil {
			why = fail.err.Code
		}
		e.circuitFailure(context.WithoutCancel(ctx), rem, why, fail.retryAfter)
	}
	if fail.sourceNetwork {
		e.m.sourceNet.WithLabelValues(rem.Repo.Key).Inc()
	}
	if fail.egress {
		e.m.egressDenied.WithLabelValues(rem.Repo.Key).Inc()
	}
	switch fail.event {
	case "egress":
		e.securityEvent(ctx, rq, audit.EventProxyEgressDenied, f.target0().Hostname(), map[string]string{"host": f.target0().Hostname()})
	case "tls":
		e.securityEvent(ctx, rq, audit.EventProxyTLSUntrusted, f.target0().Hostname(), map[string]string{"host": f.target0().Hostname()})
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
	}
	return f.failed(ctx, classifyStatus(f.rq.Remote, resp))
}

// notFound records the upstream's 404 or 410: a negative entry before the flight is
// released, and for a stored document the gone mark (R-REPO-028, R-REPO-029). Neither
// counts toward the circuit.
func (f *fetcher) notFound(ctx context.Context, resp *http.Response) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	if err := e.neg.record(ctx, rem, rq.Path, rq.Variant, resp.StatusCode, f.sentAt); err != nil {
		e.log.Warn("recording a negative entry failed", "repo", rem.Repo.Key, "error", err)
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
	if f.rq.MaxBytes > 0 && (lim <= 0 || f.rq.MaxBytes < lim) {
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
			"The upstream of "+rem.Repo.Key+" answered with a media type that is not expected for the item.")}), false
	}
	if lim := f.limit(); lim > 0 && resp.ContentLength > lim {
		return flightResult{err: tooLarge(rem), status: resp.StatusCode}, false
	}
	return flightResult{}, true
}

func tooLarge(rem Remote) error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge,
		"The upstream item is larger than the limit of "+rem.Repo.Key+".")
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
