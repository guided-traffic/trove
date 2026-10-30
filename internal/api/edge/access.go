package edge

import (
	"context"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/meta"
)

// Access is what a request holds to ask the decision point: the subject, whose
// endpoint selects the exposure ceiling of every question (R-EP-013), the index
// the request decides on, and the decider. The shared step of an entry point
// takes the index once with [Begin]; every later decision of the request, in the
// handler or in a domain call it makes, asks the same Access and so is answered
// on the same index (R-AUTHZ-015 `index.ForRequest`, Q-AUTHZ-159).
//
// The holder travels in the request context ([WithAccess], [AccessFrom]). That
// is a consistency property and not a security one: a handler that loses it
// begins a second index, which costs a possible disagreement between two
// decisions of one request and never a ceiling, because the ceiling travels in
// the Subject. A format receives the holder's parts in its request and needs no
// context.
//
// An Access is safe for concurrent use: the fan-out of a request (the members of
// a virtual repository) decides on one holder. The index moves only forward, in
// [Access.Know].
type Access struct {
	// Subject is the caller of the request with the endpoint it arrived on; it
	// does not change.
	Subject authz.Subject
	// D is the decision point.
	D authz.Decider

	idx atomic.Pointer[authz.Index]
}

// Begin takes the index a request decides on and returns the holder
// ([authz.Decider.Begin]): it waits for a confirmed index or for the epoch of
// Trove-Authz-Min-Epoch, and first makes keys known. The error is the refusal,
// 503 authz_stale or 412 authz_epoch_unknown.
func Begin(ctx context.Context, d authz.Decider, sub authz.Subject, keys ...string) (*Access, error) {
	idx, err := d.Begin(ctx, sub, keys...)
	if err != nil {
		return nil, err
	}
	a := &Access{Subject: sub, D: d}
	a.idx.Store(idx)
	return a, nil
}

type accessKey struct{}

// WithAccess returns ctx carrying the holder of the request.
func WithAccess(ctx context.Context, a *Access) context.Context {
	return context.WithValue(ctx, accessKey{}, a)
}

// AccessFrom returns the holder the shared step stored, false for a context that
// carries none (a job, a command, a handler that runs without the step).
func AccessFrom(ctx context.Context) (*Access, bool) {
	a, ok := ctx.Value(accessKey{}).(*Access)
	return a, ok && a != nil
}

// Resolve returns the holder of the request: the one in ctx, brought up to keys
// the request has learned since, or a new one begun on sub when ctx carries none.
// A request that reaches a handler without the shared step still decides, on an
// index of its own.
func Resolve(ctx context.Context, d authz.Decider, sub authz.Subject, keys ...string) (*Access, error) {
	a, ok := AccessFrom(ctx)
	if !ok {
		return Begin(ctx, d, sub, keys...)
	}
	for _, k := range keys {
		if err := a.Know(ctx, k); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Index returns the index the request decides on.
func (a *Access) Index() *authz.Index { return a.idx.Load() }

// Epoch returns the epoch of that index, the value of Trove-Authz-Epoch
// (R-AUTHZ-032).
func (a *Access) Epoch() int64 { return a.Index().Epoch() }

// Know advances the request to a newer index when it learns a repository key the
// index does not hold ([authz.Decider.Know]); every later decision of the request
// uses it.
func (a *Access) Know(ctx context.Context, key string) error {
	cur := a.Index()
	next, err := a.D.Know(ctx, cur, a.Subject, key)
	if err != nil {
		return err
	}
	// The index only moves forward: a concurrent Know that advanced further wins.
	for next != cur && next.Epoch() >= cur.Epoch() {
		if a.idx.CompareAndSwap(cur, next) {
			break
		}
		cur = a.Index()
	}
	return nil
}

// Decide decides every action of the coordinate ([authz.Decider.DecideOn]).
func (a *Access) Decide(ctx context.Context, c authz.Coordinate) authz.Decision {
	return a.D.DecideOn(ctx, a.Index(), a.Subject, c)
}

// Readable filters repository keys to those the subject may read.
func (a *Access) Readable(ctx context.Context, keys []string) ([]string, error) {
	return a.D.ReadableOn(ctx, a.Index(), a.Subject, keys)
}

// ReadableAnonymously filters repository keys to those an anonymous caller on the
// same endpoint may read, on the request's index: whether a repository needs a
// credential (R-API-030). The endpoint ceiling and the runtime switch of
// anonymous access apply as for any anonymous request.
func (a *Access) ReadableAnonymously(ctx context.Context, keys []string) ([]string, error) {
	return a.D.ReadableOn(ctx, a.Index(), authz.Subject{Anonymous: true, Endpoint: a.Subject.Endpoint}, keys)
}

// Exposed filters repository keys to those the subject's endpoint exposes.
func (a *Access) Exposed(keys []string) []string {
	return a.D.ExposedOn(a.Index(), a.Subject, keys)
}

// PathsAllowed decides one action for many paths of one repository.
func (a *Access) PathsAllowed(ctx context.Context, base authz.Coordinate, act authz.Action, paths []string) ([]bool, error) {
	return a.D.PathsAllowedOn(ctx, a.Index(), a.Subject, base, act, paths)
}

// Require checks a system capability for a management operation.
func (a *Access) Require(ctx context.Context, permission string) error {
	return a.D.RequireOn(ctx, a.Index(), a.Subject, permission)
}

// Holds reports whether the subject holds a permission, without an audit event.
func (a *Access) Holds(permission string) bool {
	return a.D.HoldsOn(a.Index(), a.Subject, permission)
}

// Check evaluates the subject's own rights for the given coordinates.
func (a *Access) Check(items []authz.CheckItem) ([]bool, error) {
	return a.D.CheckOn(a.Index(), a.Subject, items)
}

// RecordAllowed records an allowed write in the transaction of the change.
func (a *Access) RecordAllowed(ctx context.Context, tx meta.Tx, c authz.Coordinate, d authz.Decision) error {
	return a.D.RecordAllowed(ctx, tx, a.Subject, c, d)
}
