package engine

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// read is the read flow of R-BAS-018 step 6 (R-BAS-020 to R-BAS-025): the cache entry for the cache key and
// the caller's answers decide between a hit, a check and a miss.
func (e *engine) read(q *rq) {
	if q.credAns != nil && q.credAns.Subject.Kind == iface.SubjectInvalid {
		// A kept invalid answer: the caller costs the main installation one request per period (R-BAS-020).
		e.refuse(q, errInvalidToken(q.r), metrics.RefusedInvalid)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		if !e.attempt(q) {
			return
		}
		// A fetch this request waited for has ended: decide again with what it left. The second round
		// does not wait again, so a result that is not shared (a private document, a refusal) costs each
		// waiter its own request, never a queue (R-BAS-025).
		q.collapsed = true
	}
}

// attempt decides once; it reports true when a collapsed fetch ended and the decision is to be made again.
func (e *engine) attempt(q *rq) (again bool) {
	ctx := q.ctx()
	e.computeKey(q) // a fetch the request waited for may have taught the Vary of the target
	e.learnVaryFromCache(ctx, q)
	shape := e.answers.shape(ctx, q.cred.key, q.repo)
	if shape != nil && shape.Read == iface.ReadNone {
		e.refuseNone(q, shape)
		return false
	}
	url := e.answers.url(ctx, q.cred.key, q.cacheKey)
	if url != nil && url.Refusal != nil {
		q.finish(metrics.ClassPass, metrics.ResultHit)
		e.writeRefusal(q, url.Refusal, metrics.SourceCache)
		return false
	}
	if ent, ok := e.loadEntry(ctx, q.cacheKey); ok && e.fromEntry(q, ent, shape, url) {
		return false
	}
	shape = e.answers.shape(ctx, q.cred.key, q.repo)
	if e.fromNegative(q, shape) {
		return false
	}
	return e.fromPrivateOrMiss(q)
}

// refuseNone answers a read-shape answer none with the stored status, without a request (R-BAS-022).
func (e *engine) refuseNone(q *rq, shape *answerVal) {
	e.refuse(q, errShapeNone(q.r, shape.Status), "")
}

// isFull reports whether the shape answer lets the caller read every cached item of the repository.
func isFull(shape *answerVal) bool { return shape != nil && shape.Read == iface.ReadFull }

// fromEntry decides for a request whose cache key has an entry (R-BAS-022, R-BAS-023): serve it, ask the
// check, or send the conditional request. It reports false when the entry turned out to be unusable.
func (e *engine) fromEntry(q *rq, ent cache.Entry, shape, url *answerVal) bool {
	unreachable := !e.d.Link.Reachable()
	allowed := isFull(shape) || (url != nil && url.Allowed)
	// The grace period of R-BAS-027 extends the validity of an answer, never past a withdrawal: a content entry
	// of the feed received after validatedAt - 15 s makes the entry unservable while unreachable as well
	// (Q-BAS-122).
	if allowed && !e.withdrawn(ent) && (unreachable || e.fresh(ent)) {
		if !e.serveEntry(q, ent, metrics.ResultHit) {
			return false
		}
		if isFull(shape) {
			e.refreshShape(q, shape)
		}
		return true
	}
	if unreachable {
		if priv := e.privGet(q.cred.key, q.cacheKey); priv != nil && e.privUsable(priv) {
			return e.servePrivate(q, priv, metrics.ResultHit)
		}
		e.unreachable(q)
		return true
	}
	if shape == nil && (url == nil || !url.Allowed) {
		var done bool
		if shape, done = e.checkShape(q); done {
			return true
		}
		if shape.Read == iface.ReadNone {
			e.refuseNone(q, shape)
			return true
		}
		if isFull(shape) && e.fresh(ent) {
			return e.serveEntry(q, ent, metrics.ResultChecked)
		}
	}
	return e.conditional(q, &ent, e.privGet(q.cred.key, q.cacheKey))
}

// fromNegative replays a negative entry to a caller whose full read of the repository is confirmed
// (R-BAS-024, Q-BAS-42); a caller without a shape answer gets one first.
func (e *engine) fromNegative(q *rq, shape *answerVal) bool {
	n := e.negGet(q.ctx(), q.cacheKey)
	if n == nil || n.Repository != q.repo {
		return false
	}
	if shape == nil {
		var done bool
		if shape, done = e.checkShape(q); done {
			return true
		}
		if shape.Read == iface.ReadNone {
			e.refuseNone(q, shape)
			return true
		}
	}
	if !isFull(shape) {
		return false
	}
	q.finish(metrics.ClassAbsent, metrics.ResultHit)
	ref := n.Refusal
	e.writeRefusal(q, &ref, metrics.SourceCache)
	return true
}

// fromPrivateOrMiss handles a request without a usable entry: the caller's private copy is validated with a
// conditional request, anything else is a miss.
func (e *engine) fromPrivateOrMiss(q *rq) (again bool) {
	if priv := e.privGet(q.cred.key, q.cacheKey); priv != nil {
		if !e.d.Link.Reachable() {
			if e.privUsable(priv) {
				e.servePrivate(q, priv, metrics.ResultHit)
			} else {
				e.unreachable(q)
			}
			return false
		}
		e.conditional(q, nil, priv)
		return false
	}
	return e.miss(q)
}

// serveEntry serves a cache entry to a caller the main installation allowed (a hit, or after a check).
func (e *engine) serveEntry(q *rq, ent cache.Entry, result string) bool {
	src := e.entrySource(q.ctx(), q.cacheKey, ent)
	class := metrics.ClassShared
	if ent.Kind == cache.EntryArtifact {
		class = metrics.ClassArtifact
		src.download = func(q *rq) { e.downloadEvent(q, ent.Repository, ent.SHA256) }
	}
	if !e.serveSource(q, src) {
		return false
	}
	q.finish(class, result)
	return true
}

// servePrivate serves the caller's private copy of a document.
func (e *engine) servePrivate(q *rq, d *privDoc, result string) bool {
	src := bytesSource(d.Header, d.Body)
	if !e.serveSource(q, src) {
		return false
	}
	q.finish(metrics.ClassPrivate, result)
	return true
}

// checkResult is what the check leader hands to its waiters.
type checkResult struct {
	answer iface.CheckAnswer
	sentAt time.Time
}

// checkShape asks the check of R-BAS-011 for the caller's credential and the addressed repository, one request
// per credential and repository, collapsed (R-BAS-022). done is true when it answered the request itself.
func (e *engine) checkShape(q *rq) (shape *answerVal, done bool) {
	if !e.d.Link.Reachable() {
		e.unreachable(q)
		return nil, true
	}
	if v := e.d.Guard.Miss(q.ctx(), q.addr, guard.Credentialed(!q.cred.anonymous)); !v.Allowed {
		e.refuseVerdict(q, v)
		return nil, true
	}
	res, err := e.checks.do(q.ctx(), "check|"+q.cred.key+"|"+q.repo, func(ctx context.Context) (any, error) {
		req := iface.CheckRequest{ClientAddress: q.clientText, Repositories: []string{q.repo}}
		if !q.cred.anonymous {
			t := q.cred.token
			req.Credential = &t
		}
		sent := e.now()
		ans, err := e.d.Main.Check(ctx, e.budgetClass(q), req)
		return checkResult{answer: ans, sentAt: sent}, err
	})
	if err != nil {
		e.mainError(q, err)
		return nil, true
	}
	r := res.(checkResult)
	return e.applyCheck(q, r)
}

func (e *engine) budgetClass(q *rq) mainclient.Class {
	if q.cred.anonymous {
		return mainclient.Anonymous
	}
	return mainclient.Authenticated
}

// applyCheck keeps the credential and shape answers of a check and turns the subjects the Bastion does not
// serve into their answers (R-BAS-011, R-BAS-029).
func (e *engine) applyCheck(q *rq, r checkResult) (*answerVal, bool) {
	sub := r.answer.Subject
	shape := e.recordCheck(q.ctx(), q.cred, q.repo, q.addr, r)
	switch sub.Kind {
	case iface.SubjectInvalid:
		e.refuse(q, errInvalidToken(q.r), metrics.RefusedInvalid)
		return nil, true
	case iface.SubjectAnonymousRefused:
		e.refuse(q, errCredentialsRequired(q.r), metrics.RefusedInvalid)
		return nil, true
	case iface.SubjectThrottled:
		after := time.Second
		if sub.RetryAfterSeconds != nil {
			after = time.Duration(*sub.RetryAfterSeconds) * time.Second
		}
		e.refuseVerdict(q, guard.Verdict{Code: iface.CodeRateLimited, RetryAfter: after, Reason: metrics.RefusedRateLimited})
		return nil, true
	}
	if shape == nil {
		// A subject kind this Bastion does not know: the interface versions would differ.
		e.fail(q, mainclient.ErrUnreachable)
		return nil, true
	}
	q.subject = sub
	return shape, false
}

// keepInvalid keeps the main installation's statement that a credential is invalid, like an allowance, and
// counts the failed authentication (R-BAS-020, R-BAS-036).
func (e *engine) keepInvalid(q *rq, o time.Time) {
	sub := iface.Subject{Kind: iface.SubjectInvalid}
	if !q.cred.anonymous {
		e.answers.put(q.ctx(), answers.Credential, q.cred.key, &answerVal{Answer: answerBase(answers.Credential, o, sub, ""), Subject: sub})
		e.d.Guard.FailedAuth(q.ctx(), q.addr)
	}
}

// conditional sends the conditional request of R-BAS-022 and R-BAS-023: the caller's own credential and the
// validator of the cached bytes, or of the caller's private copy.
func (e *engine) conditional(q *rq, ent *cache.Entry, priv *privDoc) bool {
	validator := ""
	switch {
	case priv != nil:
		validator = priv.ETag
	case ent != nil:
		validator = entryETag(*ent)
	}
	e.forward(q, mainclient.KindConditional, validator, &baseline{ent: ent, priv: priv})
	return true
}

// miss forwards the request; concurrent misses for one cache key share one fetch per replica and, with Valkey
// and the bucket backend, across replicas (R-BAS-025). It reports true when the request waited for a fetch and
// is to be decided again.
func (e *engine) miss(q *rq) (again bool) {
	if q.collapsed || e.isVolatileFor(q) {
		e.forward(q, mainclient.KindForward, "", nil)
		return false
	}
	key := q.cacheKey
	c, leader := e.fetches.join(key)
	if !leader {
		return e.waitLeader(q, c)
	}
	held, release := e.remoteLock(q)
	// The flight ends when the request returns, or, for an artifact fill that outlives the request, when the
	// fill is committed or lost: the waiters wait for the main installation's speed, never for a client's.
	q.flight = newHandle(func() {
		if release != nil {
			release()
		}
		e.fetches.finish(key, c, nil, nil)
	})
	defer q.flight.done()
	if !held {
		// Another replica fetches the key: wait for it, then decide again.
		return e.waitRemote(q)
	}
	e.forward(q, mainclient.KindForward, "", nil)
	return false
}

// waitLeader waits for the fetch another request of this replica leads for the same cache key. A wait that
// outlasts collapseWait is not extended and not turned into a request of its own, which would multiply the load
// on a main installation that is slow: it answers 503 bastion_busy with Retry-After, and the retry finds the
// entry or joins the fetch again (R-BAS-025, Q-BAS-123).
func (e *engine) waitLeader(q *rq, c *call) (again bool) {
	t := time.NewTimer(e.collapseWait)
	defer t.Stop()
	select {
	case <-c.done:
		return true
	case <-q.ctx().Done():
		q.finish(metrics.ClassPass, metrics.ResultError)
		return false
	case <-t.C:
		e.refuse(q, errBusy(), metrics.RefusedBusy)
		return false
	}
}

// isVolatileFor reports whether recent fetches of the request's cache key left nothing shareable (a search, a
// document too large to keep) or nothing shareable for this caller (a private document, a refusal): such a fetch
// is not collapsed, so no waiter pays a second round trip for nothing. A mark that depends on the caller is the
// caller's own and never switches collapsing off for anyone else (Q-BAS-125).
func (e *engine) isVolatileFor(q *rq) bool {
	return e.isVolatile(q.cacheKey) || e.isVolatile(volatileKeyOf(q))
}

// isVolatile reports whether the mark of a key is set and has not run out.
func (e *engine) isVolatile(key string) bool {
	until, ok := e.volatile.get(key)
	return ok && e.now().Before(until)
}

// volatileKeyOf is the mark of the caller for the request's cache key; a cache key starts with "/", a
// credential key never does, so the two kinds of mark cannot meet.
func volatileKeyOf(q *rq) string { return q.cred.key + "|" + q.cacheKey }

// markVolatile marks the cache key for every caller: what the main installation answered does not depend on
// who asked.
func (e *engine) markVolatile(key string) { e.volatile.put(key, e.now().Add(volatileTTL), 0) }

// markVolatileFor marks the cache key for this caller alone: the answer depended on the caller's rights.
func (e *engine) markVolatileFor(q *rq) {
	e.volatile.put(volatileKeyOf(q), e.now().Add(volatileTTL), 0)
}

// remoteLock takes the fetch lock across replicas when Valkey and the bucket backend make a fetch of one
// replica useful to the others (R-BAS-025). held is true when the caller fetches; a lock that cannot be taken
// for any reason other than another holder only makes this replica fetch itself.
func (e *engine) remoteLock(q *rq) (held bool, release func()) {
	if !e.remoteCollapsing() {
		return true, nil
	}
	ok, rel, err := e.d.Shared.Flights.TryAcquire(q.ctx(), hashKey(q.cacheKey))
	if err != nil {
		// Valkey could not be asked: held is true and the lock is an optimization, so this replica fetches
		// itself (R-BAS-034).
		e.log.Debug("the fetch lock could not be asked; fetching without it", "error", err)
		return true, rel
	}
	return ok, rel
}

func (e *engine) remoteCollapsing() bool {
	return e.cfg.Cache.Backend == "bucket" && e.d.Shared != nil && e.d.Shared.Flights != nil && e.d.Shared.Shared != nil && e.d.Shared.Shared()
}

// remoteWaitMax is how long a replica waits for another replica's fetch: the lifetime of the lock.
const remoteWaitMax = 30 * time.Second

// waitRemote waits until the fetch lock of the key is free, then reports true so the request is decided
// again; after remoteWaitMax it fetches itself.
func (e *engine) waitRemote(q *rq) (again bool) {
	deadline := time.NewTimer(remoteWaitMax)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-q.ctx().Done():
			q.finish(metrics.ClassPass, metrics.ResultError)
			return false
		case <-deadline.C:
			e.forward(q, mainclient.KindForward, "", nil)
			return false
		case <-tick.C:
			ok, rel, err := e.d.Shared.Flights.TryAcquire(q.ctx(), hashKey(q.cacheKey))
			if err != nil || ok {
				// The lock is free, or Valkey cannot say (then it is an optimization that is skipped).
				if rel != nil {
					rel()
				}
				return true
			}
		}
	}
}
