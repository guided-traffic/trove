package authz

import (
	"context"
	"encoding/json"
	"fmt"
)

// Classifier gives the package and the version a path of a repository belongs to:
// what the repository's format makes of the path (R-REPO-003, R-EP-014). The
// decision point cannot ask a format itself; the caller that knows the format
// hands it one. Nil leaves every path without a package and a version, which a
// format with package names refuses wherever a pattern narrows names (Q-AUTHZ-160).
type Classifier func(path string) (pkg, version string)

// PathsAllowed decides one action for many paths of one repository on the
// installed index and reports, per path, whether the subject holds it. It is the
// listing form of
// Decide (R-AUTHZ-016: a listing contains only the items the subject may read; a
// folder delete needs `delete` on every item of the subtree, R-STOR-018): the
// same evaluation as Decide, token scope and endpoint ceiling included, but it
// writes no audit event, because a listing that omits entries is not a refusal
// and a subtree check refuses once, with the Decide of the path the caller named.
//
// base carries the repository and the kind of request; its Path, Package, Version
// and Actions are ignored. classify completes the coordinate of each path with the
// package and the version its format gives it, so that a listing and a subtree
// are decided on the coordinate Decide would see for each of them (R-EP-014). A
// refusal to decide (503 authz_stale, 412 authz_epoch_unknown) is returned as an
// error. An anonymous subject while anonymous access is off is refused as Decide
// refuses it, with 401.
func (e *Engine) PathsAllowed(ctx context.Context, sub Subject, base Coordinate, a Action, paths []string, classify Classifier) ([]bool, error) {
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	return e.PathsAllowedOn(ctx, idx, sub, base, a, paths, classify)
}

// PathsAllowedOn implements [Decider]: [Engine.PathsAllowed] on the index the
// request decides on.
func (e *Engine) PathsAllowedOn(ctx context.Context, idx *Index, sub Subject, base Coordinate, a Action, paths []string, classify Classifier) ([]bool, error) {
	out := make([]bool, len(paths))
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	if sub.Anonymous && sub.anon != anonOn {
		return out, Unauthenticated()
	}
	base.Actions, base.Package, base.Version = []Action{a}, "", ""
	if base.Kind == "" {
		base.Kind = KindMetadata
		if a != Read {
			base.Kind = KindWrite
		}
	}
	for i, p := range paths {
		c := base
		c.Path = p
		if classify != nil {
			c.Package, c.Version = classify(p)
		}
		out[i] = e.evaluate(idx, sub, c).Outcome == Allow
	}
	return out, nil
}

// EncodeSubject returns the subject in a form that [Engine.PathsAllowedFor] decides for later, outside the request: the job that finishes
// a large delete re-decides what the removal cascades to for the caller that asked. It holds the principal, its groups, the credential's
// scope ceiling and the frozen bundle of a CI token, and no secret.
func EncodeSubject(s Subject) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("authz: encode subject: %w", err)
	}
	return b, nil
}

// PathsAllowedFor is [Engine.PathsAllowed] for a subject encoded by [EncodeSubject]: the decision is made on the index installed now, not
// on the one the request began with, so a grant withdrawn since the request is withdrawn for the job too.
func (e *Engine) PathsAllowedFor(ctx context.Context, encoded []byte, base Coordinate, a Action, paths []string, classify Classifier) ([]bool, error) {
	var sub Subject
	if err := json.Unmarshal(encoded, &sub); err != nil {
		return nil, fmt.Errorf("authz: decode subject: %w", err)
	}
	return e.PathsAllowed(ctx, sub, base, a, paths, classify)
}
