package authz

import "context"

// PathsAllowed decides one action for many paths of one repository on the
// installed index and reports, per path, whether the subject holds it. It is the
// listing form of
// Decide (R-AUTHZ-016: a listing contains only the items the subject may read; a
// folder delete needs `delete` on every item of the subtree, R-STOR-018): the
// same evaluation as Decide, token scope and endpoint ceiling included, but it
// writes no audit event, because a listing that omits entries is not a refusal
// and a subtree check refuses once, with the Decide of the path the caller named.
//
// base carries the repository and the kind of request; its Path, Package and
// Actions are ignored. A refusal to decide (503 authz_stale, 412
// authz_epoch_unknown) is returned as an error. An anonymous subject while
// anonymous access is off is refused as Decide refuses it, with 401.
func (e *Engine) PathsAllowed(ctx context.Context, sub Subject, base Coordinate, a Action, paths []string) ([]bool, error) {
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	return e.PathsAllowedOn(ctx, idx, sub, base, a, paths)
}

// PathsAllowedOn implements [Decider]: [Engine.PathsAllowed] on the index the
// request decides on.
func (e *Engine) PathsAllowedOn(ctx context.Context, idx *Index, sub Subject, base Coordinate, a Action, paths []string) ([]bool, error) {
	out := make([]bool, len(paths))
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	if sub.Anonymous && sub.anon != anonOn {
		return out, Unauthenticated()
	}
	base.Actions, base.Package = []Action{a}, ""
	if base.Kind == "" {
		base.Kind = KindMetadata
		if a != Read {
			base.Kind = KindWrite
		}
	}
	for i, p := range paths {
		c := base
		c.Path = p
		out[i] = e.evaluate(idx, sub, c).Outcome == Allow
	}
	return out, nil
}
