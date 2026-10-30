package proxy

import (
	"context"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// Probe answers whether a remote repository serves a package, for the shadowing check
// of a first publish (R-REPO-048 steps 3 to 5). The caller has read the stored
// entries before ([Engine.EntryState], steps 1 and 2), decides without regard to the
// caller and without the cache-fill check, and no database transaction is open.
//
//   - A repository that is offline serves nothing: NotServed, no request.
//   - An open circuit is Undetermined, with the time it stays open, and no request.
//   - Otherwise one GET of the package document through the repository's client, with
//     its egress rules, TLS verification, credentials, redirect rules, limits and
//     fetch slots. 2xx is Served, 404 and 410 are NotServed, and every other outcome
//     is Undetermined. The outcome counts toward the circuit like any upstream
//     request. The probe stores nothing, neither an entry nor a negative entry, and
//     reads no negative entry: a value from Valkey may make trove refuse, never admit
//     a write (R-ARCH-030).
func (e *Engine) Probe(ctx context.Context, rem Remote, path string, a Adapter) (ProbeResult, error) {
	if e.offline(rem) {
		return ProbeResult{Outcome: NotServed}, nil
	}
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return ProbeResult{}, err
	}
	if open {
		return ProbeResult{Outcome: Undetermined, RetryAfter: retry}, nil
	}
	f := &fetcher{
		e: e, rq: Request{Remote: rem, Path: path, Kind: Metadata, Adapter: a, Caller: CallerFacts{Principal: "system"}},
		st: &reqState{role: "probe"}, fl: &flight{done: make(chan struct{})},
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return ProbeResult{Outcome: Undetermined}, nil //nolint:nilerr // an unusable credential is an upstream failure for the check
	}
	f.cl = cl
	release, wait, err := e.slots.acquire(ctx, rem.id, rem.Settings.Limits.MaxConcurrentFetches, e.o.Config.QueueTimeout())
	e.m.queueWait.WithLabelValues(rem.Repo.Key).Observe(wait.Seconds())
	if err != nil {
		if ee, ok := errs.As(err); ok && ee.Code == errs.CodeFetchQueueTimeout {
			return ProbeResult{Outcome: Undetermined, RetryAfter: 5 * time.Second}, nil
		}
		return ProbeResult{}, err
	}
	defer release()
	adm, err := e.circuitAdmit(ctx, rem)
	if err != nil {
		return ProbeResult{}, err
	}
	if adm.open {
		return ProbeResult{Outcome: Undetermined, RetryAfter: adm.retryAfter}, nil
	}
	defer adm.release()
	req, err := f.request(ctx)
	if err != nil {
		return ProbeResult{Outcome: Undetermined}, nil //nolint:nilerr // a request the engine refuses to send decides nothing
	}
	pctx, cancel := context.WithTimeout(ctx, time.Duration(rem.Settings.ConnectTimeoutMs+rem.Settings.ReadTimeoutMs)*time.Millisecond+5*time.Second)
	defer cancel()
	resp, rcancel, _, err := f.send(pctx, req)
	if err != nil {
		res := f.transportFailure(pctx, err, false)
		return ProbeResult{Outcome: Undetermined, RetryAfter: retryOf(res)}, nil
	}
	defer rcancel()
	defer func() { _ = resp.Body.Close() }()
	switch s := resp.StatusCode; {
	case s >= 200 && s < 300:
		e.circuitSuccess(ctx, rem)
		return ProbeResult{Outcome: Served}, nil
	case s == http.StatusNotFound || s == http.StatusGone:
		return ProbeResult{Outcome: NotServed}, nil
	}
	res := f.failed(ctx, classifyStatus(rem, resp))
	return ProbeResult{Outcome: Undetermined, RetryAfter: retryOf(res)}, nil
}

// retryOf is the Retry-After an error carries, 0 for none.
func retryOf(r flightResult) time.Duration {
	if ee, ok := errs.As(r.err); ok {
		return ee.RetryAfter
	}
	return 0
}
