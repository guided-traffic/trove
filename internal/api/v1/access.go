package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
)

// Beginner is the optional third method of an [Access]: the shared step of an
// operation that decides asks it for the holder of the request's index before it
// checks the declaration, and hands the holder to the handler in the request's
// context, so that every question of the request is answered on one index
// (R-AUTHZ-015, Q-AUTHZ-159, Q-API-158).
type Beginner interface {
	// Begin takes the index. permission is the capability the step is about to
	// check, empty for an action declaration; keys are the repositories the
	// request names. A nil holder with no error means the access needs no index
	// for this permission (the bootstrap bridge opens it without a decision).
	Begin(ctx context.Context, permission string, keys ...string) (*edge.Access, error)
}

// NewAccess adapts the decision point to [Access]: the permission of an
// operation's x-trove-permission is checked for the caller of the request, on the
// index the request holds.
func NewAccess(d authz.Decider) Access { return authzAccess{d: d} }

type authzAccess struct{ d authz.Decider }

// holder is the request's holder: the one the shared step stored, brought up to
// keys, or a new one for a request that reached the handler without the step.
func (x authzAccess) holder(ctx context.Context, keys ...string) (*edge.Access, error) {
	return edge.Resolve(ctx, x.d, SubjectFrom(ctx), keys...)
}

// Begin implements [Beginner].
func (x authzAccess) Begin(ctx context.Context, _ string, keys ...string) (*edge.Access, error) {
	return edge.Begin(ctx, x.d, SubjectFrom(ctx), keys...)
}

func (x authzAccess) Require(ctx context.Context, permission string) error {
	a, err := x.holder(ctx)
	if err != nil {
		return err
	}
	return a.Require(ctx, permission)
}

// Holds implements [Prober]: the question of whether the caller holds a
// permission, asked without an audit event.
func (x authzAccess) Holds(ctx context.Context, permission string) (bool, error) {
	a, err := x.holder(ctx)
	if err != nil {
		return false, err
	}
	return a.Holds(permission), nil
}

// RequireAction decides a repository action on the coordinate an operation's
// declaration names, for the caller of the request (the action form of
// x-trove-permission). The endpoint the request arrived on is part of the
// subject, because the exposure of an endpoint is a ceiling (R-EP-013).
func (x authzAccess) RequireAction(ctx context.Context, c authz.Coordinate) error {
	a, err := x.holder(ctx, c.Repository)
	if err != nil {
		return err
	}
	return a.Decide(ctx, c).Err()
}

var (
	_ ActionAccess = authzAccess{}
	_ Prober       = authzAccess{}
	_ Beginner     = authzAccess{}
)

// requestHolder is the holder a handler decides through when its declaration
// did not begin one: the stored holder brought up to keys, or a new one. It
// returns the context to pass on, which carries the holder, so that the
// permission questions the handler asks through [Access] are answered on the
// same index. Without a decision point wired the holder is nil and the context
// unchanged; the caller answers 503 where it needs a decision.
func requestHolder(ctx context.Context, d authz.Decider, keys ...string) (context.Context, *edge.Access, error) {
	if d == nil {
		return ctx, nil, nil
	}
	acc, err := edge.Resolve(ctx, d, SubjectFrom(ctx), keys...)
	if err != nil {
		return ctx, nil, err
	}
	return edge.WithAccess(ctx, acc), acc, nil
}

// errNoDecisionPoint is the 503 of an operation that needs a decision and has
// none wired.
func errNoDecisionPoint() error { return unavailable("The authorization state") }
