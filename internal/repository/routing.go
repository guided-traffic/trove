package repository

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// CacheClaims is the registered cache that holds the claim index (R-ARCH-018).
const CacheClaims = "claims"

// Route reasons: why a member takes no part in a request (R-REPO-019 steps 2 and
// 3). They are the decision names of the resolution explanation.
const (
	DropDisabled         = "disabled"
	DropNotAdmitted      = "not_admitted"
	DropPinnedOut        = "pinned-out"
	DropClaimedElsewhere = "claimed-elsewhere"
)

// The kinds of RouteDecision.
const (
	DecidedByPin   = "pin"
	DecidedByClaim = "claim"
)

// RoutingOptions configure [NewRouting].
type RoutingOptions struct {
	Runner meta.Runner
	// Reader is the replica's repository snapshot, which holds the members, the
	// deploy targets and the pin rules; the claim index is built beside it.
	Reader *SnapshotReader
	// Catalog is the format catalog; nil is catalog.Default.
	Catalog *catalog.Registry
	Caches  *cache.Registry
	// Notifier and Recorder are used by the auto claim; Recorder may be nil.
	Notifier Notifier
	Recorder audit.Recorder
	// RemoteRefusal reads proxy.claims.remote_refusal (R-REPO-023); nil means on.
	RemoteRefusal func() bool
	// SafetyTTL bounds the age of the claim index (cache.snapshot_safety_ttl).
	SafetyTTL time.Duration
	Log       *slog.Logger
}

// Routing is the caller-independent part of resolution in virtual repositories
// (R-REPO-019 steps 2, 3 and 5, R-REPO-023, R-REPO-048): which members take part
// in a request, from stored configuration only, never from the caller's
// permissions. The members, deploy targets and pin rules come from the
// repository snapshot, the claims from an index of their own; both are current
// within the change-log bound, with or without Valkey. It holds no state between
// requests beyond those two caches (R-ARCH-032).
type Routing struct {
	run      meta.Runner
	src      *SnapshotReader
	cat      *catalog.Registry
	notif    Notifier
	rec      audit.Recorder
	refusal  func() bool
	claims   *cache.Cache[struct{}, *claimIndex]
	log      *slog.Logger
	loadHook func() // tests: counts the loads of the claim index
}

// NewRouting registers the claim index on the cache registry, invalidated by the
// change area claims.
func NewRouting(o RoutingOptions) (*Routing, error) {
	switch {
	case o.Runner == nil:
		return nil, errors.New("repository: no transaction runner")
	case o.Reader == nil:
		return nil, errors.New("repository: routing needs the repository snapshot")
	case o.Caches == nil:
		return nil, errors.New("repository: no cache registry")
	case o.SafetyTTL <= 0:
		return nil, errors.New("repository: no claims safety TTL")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.RemoteRefusal == nil {
		o.RemoteRefusal = func() bool { return true }
	}
	rt := &Routing{
		run: o.Runner, src: o.Reader, cat: catalog.Or(o.Catalog), notif: o.Notifier, rec: o.Recorder,
		refusal: o.RemoteRefusal, log: o.Log,
	}
	rt.claims = cache.Register(o.Caches, CacheClaims, cache.Options[struct{}, *claimIndex]{
		MaxEntries: 1, SafetyTTL: o.SafetyTTL, Areas: []string{cluster.AreaClaims},
	})
	return rt, nil
}

func (rt *Routing) index(ctx context.Context) (*claimIndex, error) {
	return rt.claims.Load(ctx, struct{}{}, func(ctx context.Context) (*claimIndex, error) {
		if rt.loadHook != nil {
			rt.loadHook()
		}
		var rows []q.ListNamespaceClaimsRow
		err := rt.run.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			rows, err = qs.ListNamespaceClaims(ctx)
			return err
		})
		if err != nil {
			return nil, err
		}
		return buildClaimIndex(rows, rt.log), nil
	})
}

// RouteRequest is what routing needs of a request descriptor (R-REPO-003).
type RouteRequest struct {
	// Name is the package name, or the routing name of a package-less artifact,
	// as the format normalized it; empty for a request that has neither (service
	// index, search, ping).
	Name string
	// Path is the canonical path, for formats whose patterns match paths.
	Path string
}

// RouteDecision says what narrowed the members of a request.
type RouteDecision struct {
	// Kind is DecidedByPin, DecidedByClaim or "" when neither applies.
	Kind string
	// Pattern is the pin-rule or claim pattern that decided, in folded form.
	Pattern string
	// ClaimedBy lists the keys of the repositories whose claims are the most
	// specific match, for a claim decision. A repository the virtual repository
	// does not contain can be among them.
	ClaimedBy []string
}

// Route is the outcome of R-REPO-019 steps 2, 3 and 5 for one request.
type Route struct {
	// Admitted is false when the virtual repository's own patterns do not admit
	// the request (step 1: the answer is 404).
	Admitted bool
	// Members are the members that take part, in resolution order: priority
	// members first (R-REPO-022), else the order of the member list.
	Members []Repository
	// Dropped maps the key of a member that takes no part to the reason
	// (the Drop* constants).
	Dropped map[string]string
	// Decision is the pin rule or claim that narrowed the members.
	Decision RouteDecision
}

// Route evaluates a request to a virtual repository: it drops the members that
// are disabled, being deleted or whose patterns do not admit the request, applies
// the pin rule or the claims that cover the name, and orders what is left. The
// caller's permissions play no part (R-REPO-023 "Independence from the caller").
// A request without a name is only filtered by the patterns. Per-member `read`
// and the cache-fill check are the caller's, after this (R-REPO-019 step 4).
func (rt *Routing) Route(ctx context.Context, virtual Repository, req RouteRequest) (Route, error) {
	idx, err := rt.index(ctx)
	if err != nil {
		return Route{}, err
	}
	snap, err := rt.src.snapshot(ctx)
	if err != nil {
		return Route{}, err
	}
	return routeOf(rt.cat, idx, snap.keyOf, virtual, req), nil
}

// routeOf is Route on explicit inputs.
func routeOf(cat *catalog.Registry, idx *claimIndex, keyOf func(int64) string, virtual Repository, req RouteRequest) Route {
	out := Route{Dropped: map[string]string{}}
	info, known := cat.Lookup(virtual.Format)
	if !known || virtual.virt == nil {
		return out
	}
	out.Admitted = admits(virtual, info, req)
	name := req.Name
	if n, ok := cat.NormalizeName(virtual.Format, name); ok {
		name = n
	}

	var pin pinMatch
	var claimed []claimHit
	pinned := false
	if name != "" {
		if pin, pinned = virtual.virt.pins.best(name); pinned {
			out.Decision = RouteDecision{Kind: DecidedByPin, Pattern: pin.pattern.String()}
		} else if hits := mostSpecific(inEffect(idx.lookup(virtual.Format, name), virtual.virt)); len(hits) > 0 {
			out.Decision = RouteDecision{Kind: DecidedByClaim, Pattern: hits[0].pattern.String()}
			for _, h := range hits {
				out.Decision.ClaimedBy = append(out.Decision.ClaimedBy, keyOf(h.repoID))
			}
			claimed = hits
		}
	}
	for _, m := range virtual.virt.members {
		switch {
		case m.Disabled || m.Lifecycle != Active:
			out.Dropped[m.Key] = DropDisabled
		case !admits(m, info, req):
			out.Dropped[m.Key] = DropNotAdmitted
		case pinned && !pin.names(m.Key):
			out.Dropped[m.Key] = DropPinnedOut
		case claimed != nil && !claimedBy(claimed, m.rid):
			out.Dropped[m.Key] = DropClaimedElsewhere
		default:
			out.Members = append(out.Members, m)
		}
	}
	if info.PriorityMembers {
		// Stable: within the priority members and within the others the list order
		// stays (R-REPO-019 step 5).
		sort.SliceStable(out.Members, func(i, j int) bool { return out.Members[i].PriorityMember && !out.Members[j].PriorityMember })
	}
	return out
}

// inEffect keeps the claims that are in effect in a virtual repository: a
// global claim of any local repository of the format, and a members claim of a
// member of it (R-REPO-023).
func inEffect(hits []claimHit, v *virtualState) []claimHit {
	var out []claimHit
	for _, h := range hits {
		if h.scope == ScopeGlobal || v.memberIDs[h.repoID] {
			out = append(out, h)
		}
	}
	return out
}

func claimedBy(hits []claimHit, id int64) bool {
	for _, h := range hits {
		if h.repoID == id {
			return true
		}
	}
	return false
}

// admits applies the include and exclude patterns of a repository to a request
// (R-REPO-005): the normalized name for a format whose patterns match names, the
// canonical path otherwise. A request without that subject is admitted.
func admits(r Repository, info catalog.Entry, req RouteRequest) bool {
	if info.PatternSubject == SubjectName {
		return req.Name == "" || r.adm.Admits(req.Name)
	}
	return req.Path == "" || r.adm.Admits(req.Path)
}

// RemoteRefused reports whether a remote repository must answer 404 for a name
// without asking its upstream or serving a cached copy: a global claim covers the
// name and proxy.claims.remote_refusal is on, unless the request comes through a
// virtual repository (via) whose pin rule for the name lists the remote
// repository (R-REPO-023 "Remote refusal").
func (rt *Routing) RemoteRefused(ctx context.Context, remote Repository, name string, via *Repository) (bool, error) {
	if !rt.refusal() || remote.Class != Remote || name == "" {
		return false, nil
	}
	idx, err := rt.index(ctx)
	if err != nil {
		return false, err
	}
	return remoteRefused(rt.cat, idx, remote, name, via), nil
}

// remoteRefused is RemoteRefused on an explicit index, with the switch on.
func remoteRefused(cat *catalog.Registry, idx *claimIndex, remote Repository, name string, via *Repository) bool {
	if n, ok := cat.NormalizeName(remote.Format, name); ok {
		name = n
	}
	global := false
	for _, h := range idx.lookup(remote.Format, name) {
		if h.scope == ScopeGlobal {
			global = true
			break
		}
	}
	if !global {
		return false
	}
	if via != nil && via.virt != nil {
		if pin, ok := via.virt.pins.best(name); ok && pin.names(remote.Key) {
			return false
		}
	}
	return true
}

// CoveredByOwnClaim reports whether a claim of the local repository itself, of
// either scope and either source, covers the name: the precondition of the
// shadowing check (R-REPO-048).
func (rt *Routing) CoveredByOwnClaim(ctx context.Context, local Repository, name string) (bool, error) {
	idx, err := rt.index(ctx)
	if err != nil {
		return false, err
	}
	return coveredByOwn(rt.cat, idx, local, name), nil
}

func coveredByOwn(cat *catalog.Registry, idx *claimIndex, local Repository, name string) bool {
	if n, ok := cat.NormalizeName(local.Format, name); ok {
		name = n
	}
	for _, h := range idx.lookup(local.Format, name) {
		if h.repoID == local.rid {
			return true
		}
	}
	return false
}

// ClaimMatch is a claim that covers a name.
type ClaimMatch struct {
	// Repository is the key of the claiming repository.
	Repository string
	Pattern    string
	Scope      string
	Source     string
}

// ClaimsFor returns the claims of the format that cover the normalized name, the
// most specific first (R-REPO-023). Claims of equal specificity are ordered by
// repository id; use [MostSpecificClaims] for the ones that decide.
func (rt *Routing) ClaimsFor(ctx context.Context, format, name string) ([]ClaimMatch, error) {
	if n, ok := rt.cat.NormalizeName(format, name); ok {
		name = n
	}
	idx, err := rt.index(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := rt.src.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var out []ClaimMatch
	for _, h := range idx.lookup(format, name) {
		out = append(out, ClaimMatch{Repository: snap.keyOf(h.repoID), Pattern: h.pattern.String(), Scope: h.scope, Source: h.source})
	}
	return out, nil
}

// MostSpecificClaims keeps the claims of equal, highest specificity from the
// result of [Routing.ClaimsFor].
func MostSpecificClaims(matches []ClaimMatch) []ClaimMatch {
	if len(matches) == 0 {
		return nil
	}
	top, err := ParseClaimPattern(matches[0].Pattern)
	if err != nil {
		return matches[:1]
	}
	n := 0
	for n < len(matches) {
		p, err := ParseClaimPattern(matches[n].Pattern)
		if err != nil || !p.sameRank(top) {
			break
		}
		n++
	}
	return matches[:n]
}

// Exposure is a pair of the exposure set of R-REPO-048: a virtual repository
// that contains the local repository, and a remote member of it that would be
// consulted for the name together with the local repository.
type Exposure struct {
	Virtual Repository
	Remote  Repository
}

// ExposureSet returns the pairs (V, R) of R-REPO-048 for a first publish of name
// to the local repository: V is an active virtual repository that contains
// local, and R a remote member of V that steps 2 and 3 of R-REPO-019, evaluated
// for the name on stored configuration only, keep together with local. A pin
// rule of V that selects the name must name both, and where a claim in effect in
// V covers the name no remote member is consulted, so nothing is shadowed. An
// empty set lets the publish proceed without an upstream request. Nested virtual
// repositories (R-REPO-018) are not expanded.
func (rt *Routing) ExposureSet(ctx context.Context, local Repository, name string) ([]Exposure, error) {
	snap, err := rt.src.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	idx, err := rt.index(ctx)
	if err != nil {
		return nil, err
	}
	var out []Exposure
	for _, v := range snap.containing[local.rid] {
		if v.Disabled || v.Lifecycle != Active {
			continue
		}
		route := routeOf(rt.cat, idx, snap.keyOf, v, RouteRequest{Name: name})
		if !route.Admitted || !slices.ContainsFunc(route.Members, func(m Repository) bool { return m.rid == local.rid }) {
			continue
		}
		for _, m := range route.Members {
			if m.Class == Remote {
				out = append(out, Exposure{Virtual: v, Remote: m})
			}
		}
	}
	return out, nil
}

// AutoClaimResult is the outcome of [Routing.AutoClaim].
type AutoClaimResult struct {
	// Created is true when the call inserted the claim.
	Created bool
	// Pattern is the claim that was created.
	Pattern string
	// Reason says why nothing was created: "disabled" (local.autoClaim is false or
	// the format has no claims), "covered" (a claim of the repository already
	// covers the name), "skipped" (another repository's claim is in effect in a
	// virtual repository that contains this one; the audit event of the deploy
	// records autoClaim: skipped) or "limit" (the repository holds MaxClaims
	// claims). Empty when Created.
	Reason string
}

// The Reason values of AutoClaimResult.
const (
	AutoClaimDisabled = "disabled"
	AutoClaimCovered  = "covered"
	AutoClaimSkipped  = "skipped"
	AutoClaimLimit    = "limit"
)

// AutoClaim creates the exact claim of a first publish (R-REPO-023), in the
// transaction of the publish: when local.autoClaim is on, no claim of the
// repository covers the name, and no claim of another repository that is in
// effect in a virtual repository containing this one covers it (an auto claim
// must not take a name from another repository). It writes the area claims once
// per name and repository, never for a later version: a name that is claimed
// already changes nothing. The deploy's audit event records the result.
//
// The reads run in tx, so the decision is made on committed claims and a
// concurrent first publish of the same name inserts one claim, not two.
func (rt *Routing) AutoClaim(ctx context.Context, tx meta.Tx, local Repository, name string, by Caller) (AutoClaimResult, error) {
	info, ok := rt.cat.Lookup(local.Format)
	if !ok || local.Class != Local || !info.PinsAndClaims || !info.PackageNames || !local.autoClaim {
		return AutoClaimResult{Reason: AutoClaimDisabled}, nil
	}
	norm, ok := rt.cat.NormalizeName(local.Format, name)
	if !ok || norm == "" {
		return AutoClaimResult{}, errs.E(errs.Invalid, errs.CodeInvalid, "the package name is not a name of the "+local.Format+" format")
	}
	qs, err := queries(tx)
	if err != nil {
		return AutoClaimResult{}, err
	}
	own, err := qs.ListRepositoryClaims(ctx, local.rid)
	if err != nil {
		return AutoClaimResult{}, err
	}
	for _, c := range own {
		if p, err := ParseClaimPattern(c.Pattern); err == nil && p.Match(norm) {
			return AutoClaimResult{Reason: AutoClaimCovered}, nil
		}
	}
	others, err := qs.ListOtherClaims(ctx, q.ListOtherClaimsParams{RepoID: local.rid, Format: local.Format})
	if err != nil {
		return AutoClaimResult{}, err
	}
	for _, o := range others {
		inEffect := o.SharesVirtual || (o.Scope == ScopeGlobal && o.InVirtual)
		if !inEffect {
			continue
		}
		if p, err := ParseClaimPattern(o.Pattern); err == nil && p.Match(norm) {
			return AutoClaimResult{Reason: AutoClaimSkipped}, nil
		}
	}
	if len(own) >= MaxClaims {
		return AutoClaimResult{Reason: AutoClaimLimit}, nil
	}
	pat := ClaimPattern{literal: norm, kind: claimExact}.String()
	n, err := qs.InsertAutoClaim(ctx, q.InsertAutoClaimParams{RepoID: local.rid, Pattern: pat, Actor: by.principalID()})
	if err != nil {
		return AutoClaimResult{}, err
	}
	if n == 0 { // a concurrent first publish of the name inserted it
		return AutoClaimResult{Reason: AutoClaimCovered}, nil
	}
	version, err := qs.BumpClaimsVersion(ctx, local.rid)
	if err != nil {
		return AutoClaimResult{}, err
	}
	if err := recordClaims(ctx, tx, rt.rec, rt.notif, by, local, SourceAuto, []claimChange{{Pattern: pat, Scope: ScopeMembers}}, nil, version); err != nil {
		return AutoClaimResult{}, err
	}
	return AutoClaimResult{Created: true, Pattern: pat}, nil
}

// String is the readable form of a result, for audit details and logs.
func (r AutoClaimResult) String() string {
	if r.Created {
		return "created " + strconv.Quote(r.Pattern)
	}
	return r.Reason
}
