package authz

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
)

// The populate check of R-AUTHZ-008 and MemberUsable of R-AUTHZ-015. Fetching a
// path a remote repository has not cached, and refreshing an expired metadata
// document, bring new content into the cache: they need read plus the action the
// repository's cacheFillAction names. Three rules refuse whatever the grants
// say: a request that came with the session cookie, a request on an endpoint
// with cache_fill: false (R-EP-019, carried in the subject, Q-AUTHZ-153), and a
// remote repository whose cacheFillAction names no action. Whether a request
// needs its upstream (nothing cached, or an expired document) is the remote
// engine's to know; this file only answers whether it may.

// populateAction is the action a remote repository's cacheFillAction names: read
// by default (Q-AUTHZ-4), deploy for a frozen mirror. ok is false for a value
// that names neither, which refuses every populate.
func populateAction(r *repoInfo) (Action, bool) {
	switch r.cacheFill {
	case "", string(Read):
		return Read, true
	case string(Deploy):
		return Deploy, true
	}
	return "", false
}

// sessionCredential reports whether the request came with the session cookie
// (R-AUTH-001 item 3). The cookie accompanies top-level navigations and image
// loads from other sites, so it never populates (R-AUTHZ-008).
func sessionCredential(sub Subject) bool {
	return sub.SessionID != "" || sub.AuthMethod == "session"
}

// endpointFills reports whether the subject's endpoint fills caches (R-EP-019).
// A subject without an endpoint (a job, a command) has no such ceiling.
func endpointFills(sub Subject) bool { return sub.Endpoint == "" || sub.CacheFill }

// populate decides the populate check on a coordinate of a remote repository.
// readRefused says that the refusal is the read decision itself, which the caller
// answers and audits as a read.
func (e *Engine) populate(idx *Index, sub Subject, c Coordinate) (d Decision, readRefused bool) {
	rc := c
	rc.Actions = []Action{Read}
	d = e.evaluate(idx, sub, rc)
	if d.Outcome != Allow {
		return d, true
	}
	deny := Decision{Outcome: DenyVisible, Reason: ReasonPopulate, Target: targetOf(c), Epoch: d.Epoch}
	r := idx.repo(c.Repository)
	if r == nil || r.Class != repository.Remote {
		return deny, false // only a remote repository has an upstream to fill from
	}
	if sessionCredential(sub) || !endpointFills(sub) {
		return deny, false
	}
	a, ok := populateAction(r)
	if !ok {
		return deny, false
	}
	deny.Missing = a
	if a == Read {
		return d, false
	}
	pc := c
	pc.Actions = []Action{a}
	pd := e.evaluate(idx, sub, pc)
	if pd.Outcome != Allow {
		return deny, false
	}
	reason := d.Reason
	if pd.Reason == ReasonAllowRole {
		reason = ReasonAllowRole
	}
	return Decision{
		Outcome: Allow, Reason: reason, Epoch: d.Epoch, Target: targetOf(c),
		Sources: dedupStrings(append(append([]string(nil), d.Sources...), pd.Sources...)),
	}, false
}

// MayPopulateOn implements [Decider]: the populate check as a question, audited
// and counted nowhere. The remote engine asks it where a refusal is answered from
// the cache (an expired document served stale), and the virtual resolver through
// [Engine.MemberUsableOn].
func (e *Engine) MayPopulateOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision {
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		return *refused
	}
	d, _ := e.populate(idx, sub, c)
	return d
}

// PopulateOn implements [Decider]: the populate check of a request that has
// nothing to serve without a fetch. A refusal of the populate rules is 403
// populate_not_permitted and is audited like a refused write, whatever
// authz.audit.reads says, because it refuses a change of the cache (R-AUTHZ-029);
// a refused read stays a read and is audited as one.
func (e *Engine) PopulateOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision {
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		e.count(*refused, sub, c)
		e.audit(ctx, sub, c, *refused)
		return *refused
	}
	start := time.Now()
	d, readRefused := e.populate(idx, sub, c)
	e.m.eval.Observe(time.Since(start).Seconds())
	ac := c
	ac.Actions = []Action{Read}
	if !readRefused {
		ac.Kind = KindWrite
		if d.Missing != "" && d.Missing != Read {
			ac.Actions = append(ac.Actions, d.Missing)
		}
	}
	e.count(d, sub, ac)
	e.audit(ctx, sub, ac, d)
	return d
}

// MemberUsableOn implements [Decider]: MemberUsable of R-AUTHZ-015 for one member
// of the virtual repository c addresses. The member is decided with the request's
// coordinate and its grants, while the exposure ceiling stays on the repository
// the client addressed (Q-EP-11); fill is the populate check of a remote member
// and false for any other. A member it may not use is skipped silently: nothing
// is audited or counted (R-AUTHZ-009 rule 2).
func (e *Engine) MemberUsableOn(ctx context.Context, idx *Index, sub Subject, member string, c Coordinate) (read, fill bool) {
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		return false, false
	}
	mc := c
	if mc.Addressed == "" {
		mc.Addressed = c.Repository
	}
	mc.Repository = member
	rc := mc
	rc.Actions = []Action{Read}
	if e.evaluate(idx, sub, rc).Outcome != Allow {
		return false, false
	}
	if m := idx.repo(member); m == nil || m.Class != repository.Remote {
		return true, false
	}
	d, _ := e.populate(idx, sub, mc)
	return true, d.Outcome == Allow
}
