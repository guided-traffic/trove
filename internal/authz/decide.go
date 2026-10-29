package authz

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

type subjectKind int

const (
	kindUser subjectKind = iota
	kindServiceAccount
	kindCI
)

// kindOf classifies the subject for its principal set. A subject whose kind is
// empty (a test that sets only a name) is a user.
func kindOf(sub Subject) subjectKind {
	switch sub.Kind {
	case principal.KindServiceAccount:
		return kindServiceAccount
	case principal.KindCI:
		return kindCI
	}
	return kindUser
}

func isBreakGlass(sub Subject) bool { return sub.Kind == principal.KindBreakGlass }

// readType reports whether the request kind is read-type (R-REPO-003,
// R-AUTHZ-016): a refused read answers 404 instead of 403. A coordinate without
// a kind is read-type when it asks for nothing but read.
func readType(c Coordinate) bool {
	switch c.Kind {
	case KindArtifact, KindMetadata, KindSearch:
		return true
	case KindWrite:
		return false
	}
	for _, a := range c.Actions {
		if a != Read {
			return false
		}
	}
	return true
}

// targetOf is the coordinate as the caller sent it, for the text of a 403.
func targetOf(c Coordinate) string {
	t := c.Repository
	switch {
	case c.Package != "":
		t += "/" + c.Package
	case c.Path != "":
		t += "/" + c.Path
	}
	return t
}

// Decide implements [Authorizer]: it decides every action of the coordinate on
// the compiled index and refuses with the outcome of R-AUTHZ-016.
//
// Every call takes the installed index at that moment. A request that decides
// several coordinates (the members of a virtual repository, a copy) calls
// [Engine.Begin] once and [Engine.DecideOn] for each coordinate, so that all of
// them are decided on one index (R-AUTHZ-015 `index.ForRequest`).
func (e *Engine) Decide(ctx context.Context, sub Subject, c Coordinate) Decision {
	c = onEndpoint(ctx, c)
	idx, refused := e.begin(ctx, sub, c.Repository)
	if refused != nil {
		e.count(*refused, c)
		e.audit(ctx, sub, c, *refused)
		return *refused
	}
	return e.DecideOn(ctx, idx, sub, c)
}

// Begin returns the index a request decides on, waiting for a confirmation or
// for the epoch of Trove-Authz-Min-Epoch as R-AUTHZ-028 and R-AUTHZ-032 say. The
// error is the refusal: 503 authz_stale or 412 authz_epoch_unknown. The keys are
// the repositories the request is about: a key the index does not hold yet is
// looked up and the index brought up to it before the index is taken
// ([Engine.Know]).
func (e *Engine) Begin(ctx context.Context, sub Subject, keys ...string) (*Index, error) {
	idx, refused := e.begin(ctx, sub, keys...)
	if refused != nil {
		return nil, refused.Err()
	}
	return idx, nil
}

// begin is the start of every decision: the repositories the request names are
// known to the index (R-ARCH-011 step 2, R-API-052), then the index is taken.
func (e *Engine) begin(ctx context.Context, sub Subject, keys ...string) (*Index, *Decision) {
	for _, key := range keys {
		if err := e.Know(ctx, key); err != nil {
			d := Decision{Outcome: DenyStale, Reason: ReasonStale}
			return nil, &d
		}
	}
	return e.forRequest(ctx, sub)
}

// DecideOn decides a coordinate on the index Begin returned. Requests in flight
// finish on the index they started with, so a swap of the installed index never
// changes the decisions of one request (R-AUTHZ-028).
func (e *Engine) DecideOn(ctx context.Context, idx *Index, sub Subject, c Coordinate) Decision {
	c = onEndpoint(ctx, c)
	sub, refused := e.withAnonymous(ctx, sub)
	if refused != nil {
		e.count(*refused, c)
		e.audit(ctx, sub, c, *refused)
		return *refused
	}
	start := time.Now()
	d := e.evaluate(idx, sub, c)
	e.m.eval.Observe(time.Since(start).Seconds())
	e.count(d, c)
	e.audit(ctx, sub, c, d)
	return d
}

func (e *Engine) count(d Decision, c Coordinate) {
	outcome := "deny"
	if d.Outcome == Allow {
		outcome = "allow"
	}
	action := string(Read)
	if len(c.Actions) > 0 {
		action = string(c.Actions[0])
	}
	e.m.decisions.WithLabelValues(outcome, d.Reason, action).Inc()
	if d.Reason == ReasonExposure || d.Reason == ReasonEndpointReadOnly {
		e.m.exposureHidden(c.Endpoint, "request")
	}
}

// withAnonymous reads the runtime switch of R-AUTH-037 for an anonymous subject,
// once for the request, with the request's context and deadline, and keeps the
// answer on the returned subject; a subject that is not anonymous, or whose switch
// was read already, comes back as it is. A switch that cannot be read is no "off":
// the answer is 503 like every other state the decision point cannot confirm.
func (e *Engine) withAnonymous(ctx context.Context, sub Subject) (Subject, *Decision) {
	if !sub.Anonymous || sub.anon != anonUnknown {
		return sub, nil
	}
	sub.anon = anonOff
	if e.o.AnonymousAccess == nil {
		return sub, nil
	}
	on, err := e.o.AnonymousAccess(ctx)
	if err != nil {
		e.log.Warn("authz: the anonymous access switch could not be read", "err", err)
		d := Decision{Outcome: DenyStale, Reason: ReasonStale}
		return sub, &d
	}
	if on {
		sub.anon = anonOn
	}
	return sub, nil
}

// evalState is what one decision derives once from the subject.
type evalState struct {
	idx    *Index
	sub    Subject
	pset   []string
	caps   capSet
	bypass bool
}

func (e *Engine) newEvalState(idx *Index, sub Subject) *evalState {
	st := &evalState{idx: idx, sub: sub, pset: idx.principalIDs(sub)}
	st.caps = e.capabilities(idx, sub)
	st.bypass = st.caps.has(ContentBypass)
	return st
}

// rolesOf returns the system roles the subject holds: the roles bound to the
// subject and its groups, and admin for the break-glass administrator
// (R-AUTHZ-010). A CI identity, anonymous and authenticated never hold one.
func (x *Index) rolesOf(sub Subject) roleSet {
	if sub.Anonymous {
		return 0
	}
	var rs roleSet
	if isBreakGlass(sub) {
		rs |= RoleAdmin.bit()
	}
	if kindOf(sub) == kindCI {
		return rs
	}
	if sub.ID != "" {
		rs |= x.bindings[sub.ID]
	}
	for _, g := range sub.Groups {
		rs |= x.bindings[g.ID]
	}
	return rs
}

// capabilities returns the capabilities the credential exercises (R-AUTHZ-010,
// R-AUTHZ-013): a session exercises every role of its user; a token exercises
// every role only with admin: true and only if the subject holds admin at use
// time, otherwise just the system items that one of the subject's roles grants.
// content:bypass is never a system item.
func (e *Engine) capabilities(idx *Index, sub Subject) capSet {
	if sub.Anonymous {
		return 0
	}
	roles := idx.rolesOf(sub)
	full := capsOfRoles(roles)
	if sub.Scope == nil {
		return full
	}
	if sub.Scope.Admin && roles.has(RoleAdmin) {
		return full
	}
	var out capSet
	for _, item := range sub.Scope.System {
		c := Capability(item)
		if c == ContentBypass || !c.Valid() {
			continue
		}
		if full.has(c) {
			out |= c.bit()
		}
	}
	return out
}

// scopeAdmits applies the repository part of a token scope as a ceiling
// (R-AUTHZ-013): inherit admits everything; otherwise an element must match the
// coordinate and list the action. A malformed element admits nothing.
func (e *Engine) scopeAdmits(sc *token.Scope, a Action, r *repoInfo, repoLevel bool, pkg, path string) bool {
	if sc == nil || sc.Inherit {
		return true
	}
	for _, el := range sc.Repositories {
		acts, bad := ParseActions(el.Actions)
		if len(bad) > 0 || !acts.Has(a) {
			continue
		}
		if !e.compile.selectorsMatch(el.Repositories, r.repoView) {
			continue
		}
		pkgs, err := e.compile.set(dimPackage, el.Packages.Include, el.Packages.Exclude, foldFor(r.Format))
		if err != nil {
			continue
		}
		paths, err := e.compile.set(dimPath, el.Paths.Include, el.Paths.Exclude, false)
		if err != nil {
			continue
		}
		if (resourceSet{packages: pkgs, paths: paths}).matches(repoLevel, pkg, path) {
			return true
		}
	}
	return false
}

// grantedBy returns the names of the policies whose grants give the action on
// the coordinate to a principal of the set (R-AUTHZ-015 `granted`).
func (st *evalState) grantedBy(a Action, r *repoInfo, repoLevel bool, pkg, path string) []string {
	var out []string
	for _, p := range st.pset {
		for _, g := range st.idx.grantsFor(r.ID, p) {
			if g.actions.Has(a) && g.res.matches(repoLevel, pkg, path) {
				out = append(out, g.policy)
			}
		}
	}
	return out
}

// allowedAction is `Allowed` of R-AUTHZ-015 after the endpoint ceiling, which
// evaluate has applied to every action of the coordinate before it looked the
// repository up: content:bypass, then the grants intersected with the token
// scope. The reason is the one of R-AUTHZ-029.
func (e *Engine) allowedAction(st *evalState, a Action, r *repoInfo, c Coordinate) (ok bool, reason string, sources []string) {
	if st.bypass {
		return true, ReasonAllowRole, []string{"role:admin"}
	}
	repoLevel := c.Package == "" && c.Path == ""
	by := st.grantedBy(a, r, repoLevel, c.Package, c.Path)
	if len(by) == 0 {
		return false, ReasonNoGrant, nil
	}
	if !e.scopeAdmits(st.sub.Scope, a, r, repoLevel, c.Package, c.Path) {
		return false, ReasonScope, nil
	}
	return true, ReasonAllowGrant, by
}

// anonReadsAllMembers reports whether anonymous holds read on every member of a
// virtual repository, recursively (R-AUTHZ-009 rule 6).
func (st *evalState) anonReadsAllMembers(r *repoInfo, depth int) bool {
	if depth > 16 {
		return false
	}
	for _, key := range r.members {
		m := st.idx.repo(key)
		if m == nil {
			continue
		}
		var ok bool
		for _, g := range st.idx.grantsFor(m.ID, AnonymousID) {
			if g.actions.Has(Read) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
		if m.Class == repository.Virtual && !st.anonReadsAllMembers(m, depth+1) {
			return false
		}
	}
	return true
}

// evaluate is `Decide` of R-AUTHZ-015 on a given index.
func (e *Engine) evaluate(idx *Index, sub Subject, c Coordinate) Decision {
	epoch := idx.label.AuthzEpoch
	deny := func(o Outcome, reason string, missing Action) Decision {
		return Decision{Outcome: o, Reason: reason, Missing: missing, Target: targetOf(c), Epoch: epoch}
	}
	if sub.Anonymous && sub.anon != anonOn {
		return deny(DenyUnauthenticated, ReasonUnauthenticated, "")
	}
	actions := c.Actions
	if len(actions) == 0 {
		actions = []Action{Read}
	}
	first := actions[0]
	if d, refused := e.ceilingRefuses(idx, sub, c, actions, epoch); refused {
		return d
	}
	r := idx.repo(c.Repository)
	if r == nil {
		// A repository the index does not know has no grants; refusing before any
		// lookup keeps the answer identical to a repository the caller may not see.
		return e.refusal(sub, c, nil, ReasonNoGrant, first, epoch)
	}
	if sub.Anonymous && r.requireAuth {
		return deny(DenyUnauthenticated, ReasonUnauthenticated, "")
	}
	st := e.newEvalState(idx, sub)

	target := r
	var sources []string
	if r.Class == repository.Virtual {
		ok, reason, by := e.allowedAction(st, Read, r, Coordinate{Repository: c.Repository, Endpoint: c.Endpoint, Addressed: c.Addressed})
		if !ok {
			if reason == ReasonNoGrant || reason == ReasonScope {
				reason = ReasonVirtualGate
			}
			return e.refusal(sub, c, r, reason, Read, epoch)
		}
		sources = append(sources, by...)
		if sub.Anonymous && !st.anonReadsAllMembers(r, 0) {
			return deny(DenyUnauthenticated, ReasonAnonymousPartialVirtual, Read)
		}
		if c.Kind == KindWrite && writesContent(actions) {
			// Writes are decided on the deploy target; the gate applies as well
			// (R-AUTHZ-009 rule 5). manage on the virtual repository itself
			// (invalidating its merged-metadata cache) is not a content write.
			target = idx.repo(r.deployTarget)
			if target == nil {
				return deny(DenyVisible, ReasonNoGrant, first)
			}
		}
	}

	reason := ReasonAllowGrant
	for _, a := range actions {
		if target == r && r.Class == repository.Virtual && a == Read {
			// The gate above was this very check: content is decided on the
			// member that holds it (R-AUTHZ-009 rule 2), not on the virtual
			// repository.
			continue
		}
		ok, why, by := e.allowedAction(st, a, target, c)
		if !ok {
			return e.refusal(sub, c, target, why, a, epoch)
		}
		if why == ReasonAllowRole {
			reason = ReasonAllowRole
		}
		sources = append(sources, by...)
	}
	return Decision{Outcome: Allow, Reason: reason, Epoch: epoch, Target: targetOf(c), Sources: dedupStrings(sources)}
}

// ceilingRefuses applies the endpoint's exposure (R-EP-013) to every action of
// the coordinate, first and before the repository lookup: the refusal is the same
// for every subject, administrators and content:bypass included (Q-EP-12), and
// the same for a repository that does not exist, so neither the answer nor the
// work done depends on existence (Q-EP-4). A write-type action is refused as
// read-only before a read is refused as hidden, so a write answers 403
// endpoint_read_only whether or not its target exists. The anonymous gate of
// R-AUTH-037 has run before, so a request without a credential the endpoint
// requires is 401 first.
func (e *Engine) ceilingRefuses(idx *Index, sub Subject, c Coordinate, actions []Action, epoch int64) (Decision, bool) {
	cl := e.o.Ceiling
	if cl == nil {
		return Decision{}, false
	}
	for _, a := range actions {
		if a != Read && cl.Admits(idx, c, a) == CeilingReadOnly {
			return e.refusal(sub, c, nil, ReasonEndpointReadOnly, a, epoch), true
		}
	}
	for _, a := range actions {
		switch cl.Admits(idx, c, a) {
		case CeilingAdmits:
		case CeilingReadOnly:
			return e.refusal(sub, c, nil, ReasonEndpointReadOnly, a, epoch), true
		default:
			return e.refusal(sub, c, nil, ReasonExposure, a, epoch), true
		}
	}
	return Decision{}, false
}

// writesContent reports whether the actions change content: anything besides
// read and manage, the only actions with an effect on a virtual repository.
func writesContent(actions []Action) bool {
	for _, a := range actions {
		if a != Read && a != Manage {
			return true
		}
	}
	return false
}

// refusal maps a refused action to the outcome of R-AUTHZ-016's table (first
// matching row wins). r is nil for a repository the index does not know.
func (e *Engine) refusal(sub Subject, c Coordinate, r *repoInfo, reason string, missing Action, epoch int64) Decision {
	d := Decision{Reason: reason, Missing: missing, Target: targetOf(c), Epoch: epoch}
	switch {
	case reason == ReasonEndpointReadOnly:
		d.Outcome = DenyVisible
	case reason == ReasonExposure:
		// Exposure binds every subject and answers 404; R-AUTHZ-017 does not
		// change it.
		d.Outcome = DenyHidden
	case sub.Anonymous:
		d.Outcome = DenyUnauthenticated
	case readType(c) && missing == Read:
		d.Outcome = DenyHidden
		if r != nil && !e.hides(r) {
			d.Outcome = DenyVisible
		}
	default:
		d.Outcome = DenyVisible
	}
	return d
}

// hides reports whether a refused read of the repository answers 404: the
// repository's hideExistence, or authz.hide_existence for "inherit"
// (R-AUTHZ-017).
func (e *Engine) hides(r *repoInfo) bool {
	switch r.hide {
	case "true":
		return true
	case "false":
		return false
	}
	return e.o.Config.HideExistence
}

func dedupStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Readable implements [Authorizer]: the keys the subject may read, in the order
// given (R-AUTHZ-016: a listing contains only what the subject may read). It
// decides at repository level, so patterns of a grant do not narrow it
// (R-AUTHZ-005). On an endpoint with exposure rules (the endpoint of ctx, see
// [WithEndpoint]) a repository without an enabled rule is left out as well
// (R-EP-014). A refusal to decide (503, 412) is returned as an error.
func (e *Engine) Readable(ctx context.Context, sub Subject, keys []string) ([]string, error) {
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	sub, refused = e.withAnonymous(ctx, sub)
	if refused != nil {
		return nil, refused.Err()
	}
	if sub.Anonymous && sub.anon != anonOn {
		return nil, nil
	}
	endpoint := EndpointOf(ctx)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		d := e.evaluate(idx, sub, Coordinate{Repository: k, Kind: KindMetadata, Actions: []Action{Read}, Endpoint: endpoint})
		switch {
		case d.Outcome == Allow:
			out = append(out, k)
		case d.Reason == ReasonExposure:
			// An item removed from a listing by exposure (R-EP-014, R-EP-009).
			e.m.exposureHidden(endpoint, "item")
		}
	}
	return out, nil
}

// roleNeeded reads the "role:<name>" form of a permission.
const rolePrefix = "role:"

// Require implements [Authorizer]: it checks a system capability for a
// management operation (R-API-008). The permission is a capability name of
// R-AUTHZ-010, or "role:<role>" for the declaration {role: <role>}, which needs
// the role itself. A refusal is 401 for an anonymous subject, 403 otherwise; it
// runs before any lookup, so it does not depend on whether the object exists.
func (e *Engine) Require(ctx context.Context, sub Subject, permission string) error {
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		e.auditCapability(ctx, sub, permission, *refused)
		return refused.Err()
	}
	start := time.Now()
	d := e.requireOn(idx, sub, permission)
	e.m.eval.Observe(time.Since(start).Seconds())
	e.m.decisions.WithLabelValues(map[bool]string{true: "allow", false: "deny"}[d.Outcome == Allow], d.Reason, permission).Inc()
	if d.Outcome != Allow {
		e.auditCapability(ctx, sub, permission, d)
		if d.Outcome == DenyVisible {
			return errs.E(errs.Forbidden, errs.CodeForbidden, fmt.Sprintf("The permission %s is required.", permission))
		}
	}
	return d.Err()
}

// Holds reports whether the subject holds a permission, as [Engine.Require]
// would decide it, but as a question and not as a request for an operation: it
// records no authz.denied event and counts no denial, because nothing was refused
// (R-AUTHZ-029). A handler that only chooses between two answers, and the rate
// limit that exempts administrators, use it. An error is a refusal to decide (503,
// 412), not a "no". *Engine implements [Prober].
func (e *Engine) Holds(ctx context.Context, sub Subject, permission string) (bool, error) {
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return false, refused.Err()
	}
	return e.requireOn(idx, sub, permission).Outcome == Allow, nil
}

func (e *Engine) requireOn(idx *Index, sub Subject, permission string) Decision {
	epoch := idx.label.AuthzEpoch
	if sub.Anonymous {
		return Decision{Outcome: DenyUnauthenticated, Reason: ReasonUnauthenticated, Epoch: epoch}
	}
	deny := Decision{Outcome: DenyVisible, Reason: ReasonCapability, Epoch: epoch}
	if len(permission) > len(rolePrefix) && permission[:len(rolePrefix)] == rolePrefix {
		role := Role(permission[len(rolePrefix):])
		if !role.Valid() {
			e.log.Warn("authz: an operation declares an unknown role", "role", role)
			return deny
		}
		if idx.rolesOf(sub).has(role) && (sub.Scope == nil || sub.Scope.Admin) {
			return Decision{Outcome: Allow, Reason: ReasonAllowRole, Epoch: epoch, Sources: []string{"role:" + string(role)}}
		}
		return deny
	}
	c := Capability(permission)
	if !c.Valid() {
		e.log.Warn("authz: an operation declares an unknown capability", "permission", permission)
		return deny
	}
	if e.capabilities(idx, sub).has(c) {
		return Decision{Outcome: Allow, Reason: ReasonAllowRole, Epoch: epoch, Sources: []string{"capability:" + permission}}
	}
	return deny
}

// CheckItem is one check of POST /api/v1/authz/check (R-AUTHZ-023).
type CheckItem struct {
	Repository string
	Package    string
	Path       string
	Action     Action
}

// MaxChecks is the most checks one call may carry (R-AUTHZ-023).
const MaxChecks = 100

// Check evaluates the caller's own rights, token scope included, for up to
// MaxChecks coordinates (R-AUTHZ-023). A repository the caller cannot see
// answers false exactly like a missing one. It audits nothing: the caller
// asked about itself, and the server enforces every operation regardless. The
// second result is the epoch of the index that answered.
func (e *Engine) Check(ctx context.Context, sub Subject, items []CheckItem) ([]bool, int64, error) {
	if sub.Anonymous {
		return nil, 0, Unauthenticated()
	}
	if len(items) > MaxChecks {
		return nil, 0, errs.E(errs.Invalid, errs.CodeValidation, fmt.Sprintf("A call holds at most %d checks.", MaxChecks),
			errs.WithFields(errs.FieldError{Pointer: "/checks", Code: "too_many", Detail: fmt.Sprintf("at most %d", MaxChecks)}))
	}
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return nil, 0, refused.Err()
	}
	out := make([]bool, len(items))
	for i, it := range items {
		if !it.Action.Valid() {
			return nil, 0, errs.E(errs.Invalid, errs.CodeValidation, fmt.Sprintf("%q is not a repository action.", it.Action),
				errs.WithFields(errs.FieldError{Pointer: fmt.Sprintf("/checks/%d/action", i), Code: errs.CodeInvalidAction, Detail: "not one of the six actions"}))
		}
		kind := KindMetadata
		if it.Action != Read {
			kind = KindWrite
		}
		d := e.evaluate(idx, sub, onEndpoint(ctx, Coordinate{Repository: it.Repository, Package: it.Package, Path: it.Path, Kind: kind, Actions: []Action{it.Action}}))
		out[i] = d.Outcome == Allow
	}
	return out, idx.label.AuthzEpoch, nil
}

// Effective returns the capabilities, roles and principal set of the subject
// (R-AUTHZ-022 GET /authz/me).
func (e *Engine) Effective(ctx context.Context, sub Subject) (Me, error) {
	idx, refused := e.forRequest(ctx, sub)
	if refused != nil {
		return Me{}, refused.Err()
	}
	me := Me{Epoch: idx.label.AuthzEpoch}
	if sub.Anonymous {
		me.Principals = []string{"anonymous"}
		return me, nil
	}
	rs := idx.rolesOf(sub)
	for _, r := range Roles() {
		if rs.has(r) {
			me.Roles = append(me.Roles, r)
		}
	}
	me.Capabilities = e.capabilities(idx, sub).list()
	switch kindOf(sub) {
	case kindServiceAccount:
		me.Principals = append(me.Principals, "sa:"+sub.Name)
	case kindCI:
		me.Principals = append(me.Principals, sub.Mappings...)
	default:
		me.Principals = append(me.Principals, "user:"+sub.Name)
	}
	for _, g := range sub.Groups {
		me.Principals = append(me.Principals, "group:"+g.Name)
	}
	if kindOf(sub) == kindUser {
		me.Principals = append(me.Principals, "authenticated")
	}
	return me, nil
}

// patternCache compiles the patterns and selectors of token scopes, which arrive
// with every request. It is bounded and lives in the engine, never in package
// state (R-ARCH-032).
type patternCache struct {
	mu   sync.RWMutex
	sels map[string]selector
	bad  map[string]bool
	res  map[string]*globSet
}

const patternCacheMax = 4096

func newPatternCache() *patternCache {
	return &patternCache{sels: map[string]selector{}, bad: map[string]bool{}, res: map[string]*globSet{}}
}

func (c *patternCache) full() bool { return len(c.sels)+len(c.res) > patternCacheMax }

// selectorsMatch reports whether any selector text covers the repository.
func (c *patternCache) selectorsMatch(texts []string, r repoView) bool {
	for _, t := range texts {
		c.mu.RLock()
		s, ok := c.sels[t]
		bad := c.bad[t]
		c.mu.RUnlock()
		if bad {
			continue
		}
		if !ok {
			var err error
			s, err = parseSelector(t)
			c.mu.Lock()
			if c.full() {
				c.sels, c.bad, c.res = map[string]selector{}, map[string]bool{}, map[string]*globSet{}
			}
			if err != nil {
				c.bad[t] = true
				c.mu.Unlock()
				continue
			}
			c.sels[t] = s
			c.mu.Unlock()
		}
		if s.matches(r) {
			return true
		}
	}
	return false
}

// set compiles one dimension's lists, from the cache when seen before.
func (c *patternCache) set(dim dimension, include, exclude []string, fold bool) (globSet, error) {
	if len(include) == 0 && len(exclude) == 0 {
		return globSet{}, nil
	}
	key := fmt.Sprintf("%d|%t|%q|%q", dim, fold, include, exclude)
	c.mu.RLock()
	g := c.res[key]
	c.mu.RUnlock()
	if g != nil {
		return *g, nil
	}
	got, err := compileSet(dim, include, exclude, fold)
	if err != nil {
		return globSet{}, err
	}
	c.mu.Lock()
	if c.full() {
		c.sels, c.bad, c.res = map[string]selector{}, map[string]bool{}, map[string]*globSet{}
	}
	c.res[key] = &got
	c.mu.Unlock()
	return got, nil
}
