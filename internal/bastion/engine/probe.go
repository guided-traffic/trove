package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/errs"
)

// probeRec is a probe answer the Bastion keeps per method and target for metadata_revalidate_after
// (R-BAS-014): the refusal of a write, replayed without a request.
type probeRec struct {
	ref   refusal
	until time.Time
}

// probeResult is what the probe leader hands to its waiters: the answer, or the route that takes a body.
type probeResult struct {
	ref      *refusal
	withBody bool
	err      error
}

// write is the write flow of R-BAS-014 (Q-BAS-38): a method other than GET and HEAD is first sent to the main
// installation as a bodiless probe, without the caller's credential. The Bastion reads none of the caller's
// body while it waits.
func (e *engine) write(q *rq) {
	if max := int64(e.cfg.Main.MaxForwardedBody); q.r.ContentLength > max {
		e.refuse(q, errs.E(errs.TooLarge, errs.CodeTooLarge, "The body is larger than this Bastion forwards."), metrics.RefusedReadOnly)
		return
	}
	key := q.r.Method + " " + q.target
	if rec, ok := e.probes.get(key); ok && e.now().Before(rec.until) {
		ref := rec.ref
		q.reason = metrics.RefusedReadOnly
		e.d.Metrics.Refused.WithLabelValues(metrics.RefusedReadOnly).Inc()
		q.finish(metrics.ClassPass, metrics.ResultHit)
		e.writeRefusal(q, &ref, metrics.SourceCache)
		return
	}
	if !e.d.Link.Reachable() {
		e.unreachable(q)
		return
	}
	if v := e.d.Guard.Miss(q.ctx(), q.addr, guard.Credentialed(q.hasAuth)); !v.Allowed {
		e.refuseVerdict(q, v)
		return
	}
	res, err := e.probeFlights.do(q.ctx(), key, func(ctx context.Context) (any, error) { return e.probe(ctx, q, key), nil })
	if err != nil {
		e.mainError(q, err)
		return
	}
	pr := res.(probeResult)
	switch {
	case pr.err != nil:
		e.mainError(q, pr.err)
	case pr.ref != nil:
		q.reason = metrics.RefusedReadOnly
		if pr.ref.Status == http.StatusForbidden {
			e.d.Metrics.Refused.WithLabelValues(metrics.RefusedReadOnly).Inc()
		}
		q.finish(metrics.ClassPass, metrics.ResultMiss)
		e.writeRefusal(q, pr.ref, metrics.SourceMain)
	case pr.withBody:
		e.forwardWithBody(q)
	}
}

// probe sends the bodiless probe and keeps a 403 or 404 answer (R-BAS-014).
func (e *engine) probe(ctx context.Context, q *rq, key string) probeResult {
	fr := e.forwardRequest(q)
	fr.Header.Del("Authorization")
	resp, err := e.d.Main.Do(ctx, mainclient.Forward{
		Request: fr, Class: e.probeClass(q), Kind: mainclient.KindProbe, ClientAddress: q.clientText, Probe: true,
	})
	if err != nil {
		return probeResult{err: err}
	}
	defer resp.Body.Close()
	q.mainRequestID = resp.Header.Get("X-Request-Id")
	if !resp.Marked && resp.Status >= 500 {
		return probeResult{err: mainclient.ErrUnreachable}
	}
	if resp.Status == http.StatusNoContent && resp.Route == iface.RouteReadWithBody {
		return probeResult{withBody: true}
	}
	body, _, whole := readBounded(resp.Body, maxRefusalBody)
	h := replayHeader(resp.Header)
	h.Del("Content-Length")
	ref := &refusal{Status: resp.Status, Header: h, Body: body}
	if !whole {
		ref.Body = nil
		return probeResult{ref: ref}
	}
	if resp.Status == http.StatusForbidden || resp.Status == http.StatusNotFound {
		e.probes.put(key, &probeRec{ref: *ref, until: e.now().Add(e.cfg.Cache.MetadataRevalidateAfter)}, 0)
	}
	return probeResult{ref: ref}
}

// probeClass is the class of the budget slot of a probe: the probe itself carries no credential, but it is made
// on behalf of the caller, so a caller that presented one takes an authenticated slot and only a caller without
// one takes an anonymous slot (R-BAS-025). With bastion.main.anonymous_share 0 a credentialed caller's write is
// still refused by the main installation's probe answer and not by the budget.
func (e *engine) probeClass(q *rq) mainclient.Class {
	if q.hasAuth {
		return mainclient.Authenticated
	}
	return mainclient.Anonymous
}

// forwardWithBody forwards the real request of a read route that takes a body, after a 204 probe (R-BAS-014):
// the caller's credential and the body, at most max_forwarded_body. The answer is pass: nothing is kept.
func (e *engine) forwardWithBody(q *rq) {
	if !e.stepCredentials(q) {
		return
	}
	limit := int64(e.cfg.Main.MaxForwardedBody)
	fr := e.forwardRequest(q)
	fr.Method = q.r.Method
	body, length, ok := e.requestBody(q, limit)
	if !ok {
		e.refuse(q, errs.E(errs.TooLarge, errs.CodeTooLarge, "The body is larger than this Bastion forwards."), metrics.RefusedReadOnly)
		return
	}
	fr.ContentLength = length
	if v := e.d.Guard.Miss(q.ctx(), q.addr, guard.Credentialed(!q.cred.anonymous)); !v.Allowed {
		e.refuseVerdict(q, v)
		return
	}
	resp, err := e.d.Main.Do(q.ctx(), mainclient.Forward{
		Request: fr, Class: e.budgetClass(q), Kind: mainclient.KindForward, ClientAddress: q.clientText, Body: body,
	})
	if err != nil {
		e.mainError(q, err)
		return
	}
	defer resp.Body.Close()
	q.mainRequestID = resp.Header.Get("X-Request-Id")
	if !resp.Marked && resp.Status >= 500 {
		e.fail(q, mainclient.ErrUnreachable)
		return
	}
	e.passThrough(q, resp, nil, metrics.ClassPass, metrics.ResultMiss)
}

// requestBody returns the caller's body for the real request: streamed when its length is known, held in
// memory up to the limit when it is not (H-448).
func (e *engine) requestBody(q *rq, limit int64) (r io.Reader, length int64, ok bool) {
	if q.r.ContentLength >= 0 {
		return io.LimitReader(q.r.Body, q.r.ContentLength), q.r.ContentLength, true
	}
	b, err := io.ReadAll(io.LimitReader(q.r.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, 0, false
	}
	return bytes.NewReader(b), int64(len(b)), true
}
