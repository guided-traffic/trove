package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// Package ownership (R-AUTHZ-018, R-AUTHZ-019). The first publish of a name in a
// checked repository claims it for the owners its grant designates; later
// mutating operations need an owner identity, manage on the coordinate or
// content:bypass. The rows are read and written in the writer's commit
// transaction and in no index: a claim is in force for the next write the moment
// it commits, on every replica (R-AUTHZ-015 "Ownership rows are read inside the
// write transaction"). The index a check decides on is the request's, taken before
// the transaction began, so nothing here waits for the index while a transaction
// is open (R-ARCH-019); the only wait inside one is the row lock of a name another
// transaction is claiming or changing.

// The values of a local repository's packageOwnership (R-AUTHZ-018), the ones
// the catalog's ownership default takes.
const (
	OwnershipEnforce = catalog.OwnershipEnforce
	OwnershipOff     = catalog.OwnershipOff
)

// The states of a name. A name without a claim row is unclaimed.
const (
	StateClaimed   = "claimed"
	StateReleased  = "released"
	StateUnclaimed = "unclaimed"
)

// The reasons of an owner entry (R-AUTHZ-018 data model).
const (
	OwnerFirstPublish = "first-publish"
	OwnerAdded        = "added"
	OwnerPreAssigned  = "pre-assigned"
)

// The audit events of ownership changes (internal/audit/events_authz.go).
const (
	EventOwnershipClaimed  = "authz.ownership.claimed"
	EventOwnershipAdded    = "authz.ownership.added"
	EventOwnershipRemoved  = "authz.ownership.removed"
	EventOwnershipReleased = "authz.ownership.released"
)

// SourceOwnership is the source of a decision an owner identity allowed
// (R-AUTHZ-029 sources).
const SourceOwnership = "ownership"

// OwnershipWrite is what the writer knows of a mutating operation (the
// "Ownership" column of R-AUTHZ-007) when it asks for the check, read under its
// package row lock in the commit transaction.
type OwnershipWrite struct {
	// Publish is true for an operation that stores a new version (npm publish,
	// NuGet push). Only a publish claims a name (rules 2 and 3).
	Publish bool
	// FirstVersion is true when the package held no live version before this
	// write: an unclaimed name without a stored version may be claimed by any
	// holder of the action (rule 1), one with versions needs manage (rule 3).
	FirstVersion bool
}

// Owner is one owner of a name.
type Owner struct {
	// Owner is the owner identity: user:<name>, group:<name>, sa:<name>,
	// ci:<issuer>/<mapping>, or ci:<issuer>:project:<id> (R-AUTHZ-018).
	Owner string
	// Display is the name at the time the owner was recorded, or the project path
	// of a CI project owner.
	Display string
	// Reason is first-publish, added or pre-assigned.
	Reason  string
	AddedAt time.Time
	// AddedBy is the principal reference of who added the owner; empty for a
	// claim by publish and for a principal deleted since.
	AddedBy string

	id          string
	principalID string
}

// PackageOwners is the ownership of one name in one repository (R-AUTHZ-019
// GET …/owners).
type PackageOwners struct {
	Repository string
	// Domain is the ownership domain as the API names it: the repository key in
	// the MVP, where every repository is its own domain.
	Domain  string
	Format  string
	Package string
	// State is claimed, released or unclaimed.
	State  string
	Owners []Owner
}

// OwnershipCheck is the answer of [Ownership.CheckTx]: whether the check applies,
// what it decided on, and whether the write claims the name.
type OwnershipCheck struct {
	// Applies is false when ownership is not checked for the write: not a local
	// repository, a format without package names, a coordinate without one, or
	// packageOwnership not enforce (R-AUTHZ-018 "When ownership is checked"). The
	// rest of the check is then zero and [Ownership.ClaimTx] does nothing.
	Applies bool
	// Claim says that the write claims the name (rules 2 and 3): ClaimTx records
	// the claim and its owners.
	Claim bool
	// State and Owners are the name's ownership as the commit transaction read it.
	State  string
	Owners []Owner
	// Decision is the decision of the check: Allow with the source that allowed
	// it, or the refusal (403 package_owned_by_other_owner, deny.ownership).
	Decision Decision

	key   ownKey
	write OwnershipWrite
	facts ownFacts
	sub   Subject
	c     Coordinate
	epoch int64
}

// ownKey is a name in its ownership domain.
type ownKey struct {
	repo                 *repoInfo
	domain, format, name string
}

func (k ownKey) lockParams() q.OwnershipLockStateParams {
	return q.OwnershipLockStateParams{Domain: k.domain, Format: k.format, PackageName: k.name}
}

// claimOwner is an owner a claim records (rule 2), with its reference.
type claimOwner struct {
	principalID string
	ref         Ref
}

// ownFacts is what the check derives from the subject on the request's index.
type ownFacts struct {
	bypass bool
	// manage is the decision of manage on the coordinate.
	manage Decision
	// identities are the principal IDs of the subject's owner identities.
	identities map[string]bool
	// claimers are the owners a claim by this subject records (rule 2).
	claimers []claimOwner
}

// OwnershipOptions configure [NewOwnership].
type OwnershipOptions struct {
	// Engine decides the grants, manage and content:bypass on the index of the
	// request; required.
	Engine *Engine
	// DB runs the transactions of the owner operations (R-AUTHZ-019). The check
	// and the claim of a write run on the writer's transaction.
	DB meta.Runner
	// Recorder records the ownership events in the transaction of the change; nil
	// is the engine's recorder.
	Recorder audit.Recorder
}

// Ownership is package ownership on top of the decision point: the check and the
// claim of a write in its commit transaction, and the owner operations of the
// management API.
type Ownership struct {
	e   *Engine
	db  meta.Runner
	rec audit.Recorder
}

// NewOwnership creates the ownership service.
func NewOwnership(o OwnershipOptions) (*Ownership, error) {
	if o.Engine == nil {
		return nil, errors.New("authz: ownership needs the engine")
	}
	if o.Recorder == nil {
		o.Recorder = o.Engine.rec
	}
	return &Ownership{e: o.Engine, db: o.DB, rec: o.Recorder}, nil
}

// domainOf is the ownership domain of a repository: the repository itself in the
// MVP (R-AUTHZ-018); shared domains are R-AUTHZ-020 (P1).
func domainOf(r *repoInfo) string { return "repo:" + strconv.FormatInt(r.ID, 10) }

// targetOfWrite is the repository a mutating operation on the coordinate writes
// to: the deploy target of a virtual repository (R-AUTHZ-009 rule 5), else the
// repository itself. Ownership is checked there, never on the virtual repository.
func targetOfWrite(idx *Index, key string) *repoInfo {
	r := idx.repo(key)
	if r != nil && r.Class == repository.Virtual {
		r = idx.repo(r.deployTarget)
	}
	return r
}

// keyOf finds the name of a write and whether ownership is checked for it. A
// repository the index does not know is an error: the writer decided the
// coordinate on this index before, so it cannot be missing.
func (o *Ownership) keyOf(idx *Index, c Coordinate) (ownKey, bool, error) {
	r := targetOfWrite(idx, c.Repository)
	if r == nil {
		return ownKey{}, false, fmt.Errorf("authz: the repository of %s is not in the index the write was decided on", targetOf(c))
	}
	if r.Class != repository.Local || c.Package == "" || o.e.namesOf(r.Format) == nil {
		return ownKey{}, false, nil
	}
	if r.ownership != OwnershipEnforce {
		return ownKey{}, false, nil
	}
	return ownKey{repo: r, domain: domainOf(r), format: r.Format, name: c.Package}, true, nil
}

// factsOf derives what the check needs from the subject on the request's index:
// content:bypass, manage on the coordinate, the owner identities (the principal
// set without the built-in principals, which own nothing) and the owners a claim
// would record (rule 2). A CI token's project identity joins with M4 (R-CI-015).
func (o *Ownership) factsOf(idx *Index, sub Subject, k ownKey, c Coordinate) ownFacts {
	e := o.e
	st := e.newEvalState(idx, sub)
	f := ownFacts{bypass: st.bypass, identities: map[string]bool{}}
	f.manage = e.evaluate(idx, sub, Coordinate{
		Repository: k.repo.Key, Class: k.repo.Class, Format: k.repo.Format, Kind: KindWrite,
		Package: c.Package, Path: c.Path, Actions: []Action{Manage}, Addressed: firstNonEmpty(c.Addressed, c.Repository),
	})
	for _, id := range st.pset {
		if id != AnonymousID && id != AuthenticatedID {
			f.identities[id] = true
		}
	}
	refs := identityRefs(idx, sub)
	if st.bypass {
		if sub.ID != "" {
			f.claimers = []claimOwner{{principalID: sub.ID, ref: refs[sub.ID]}}
		}
		return f
	}
	for _, id := range st.pset {
		if !f.identities[id] {
			continue
		}
		for _, g := range idx.grantsFor(k.repo.ID, id) {
			if g.actions.Has(Deploy) && g.res.matches(false, c.Package, c.Path) {
				f.claimers = append(f.claimers, claimOwner{principalID: id, ref: refs[id]})
				break
			}
		}
	}
	return f
}

// identityRefs maps the principal IDs of the subject's owner identities to their
// references, from the names the subject carries.
func identityRefs(idx *Index, sub Subject) map[string]Ref {
	out := map[string]Ref{}
	if sub.ID != "" {
		out[sub.ID] = Ref{Kind: subjectRefKind(sub), Name: sub.Name}
	}
	for _, g := range sub.Groups {
		out[g.ID] = Ref{Kind: principal.KindGroup, Name: g.Name}
	}
	for _, m := range sub.Mappings {
		name := strings.TrimPrefix(m, "ci:")
		if id := idx.ci[name]; id != "" {
			out[id] = Ref{Kind: principal.KindCI, Name: name}
		}
	}
	return out
}

// judge applies rules 1 to 3 of R-AUTHZ-018 to the name as the transaction read
// it. The order is the requirement's: content:bypass passes every check; a name
// with owners needs an owner identity or manage; an unclaimed name without a
// stored version is free to the holder of the action; every other name without
// owners (unclaimed with versions, released with versions, claimed after its last
// owner was deleted) needs manage (rule 3, rule 6, R-AUTHZ-031). Only a publish
// claims, and only a name without owners.
func judge(state string, owners []Owner, w OwnershipWrite, f ownFacts) (allowed, claim bool, sources []string) {
	hasOwners := len(owners) > 0
	switch {
	case f.bypass:
		return true, w.Publish && !hasOwners, []string{"role:admin"}
	case hasOwners:
		for _, ow := range owners {
			if ow.principalID != "" && f.identities[ow.principalID] {
				return true, false, []string{SourceOwnership}
			}
		}
		if f.manage.Outcome == Allow {
			return true, false, f.manage.Sources
		}
		return false, false, nil
	case state != StateClaimed && w.FirstVersion:
		return true, w.Publish, nil
	case f.manage.Outcome == Allow:
		return true, w.Publish, f.manage.Sources
	}
	return false, false, nil
}

// decide turns the judgement into the check: a claim needs owners to record
// (rule 2), so a publish that would claim and has no grant that designates one is
// refused rather than leaving a claim nobody owns.
func (o *Ownership) decide(chk OwnershipCheck) OwnershipCheck {
	allowed, claim, sources := judge(chk.State, chk.Owners, chk.write, chk.facts)
	if allowed && claim && len(chk.facts.claimers) == 0 {
		allowed, claim = false, false
	}
	chk.Claim = claim
	if allowed {
		chk.Decision = Decision{Outcome: Allow, Reason: ReasonAllowGrant, Epoch: chk.epoch, Target: targetOf(chk.c), Sources: sources}
		if chk.facts.bypass {
			chk.Decision.Reason = ReasonAllowRole
		}
		return chk
	}
	missing := Deploy
	if len(chk.c.Actions) > 0 {
		missing = chk.c.Actions[0]
	}
	chk.Decision = Decision{Outcome: DenyVisible, Reason: ReasonOwnership, Missing: missing, Target: targetOf(chk.c), Epoch: chk.epoch}
	return chk
}

// readOwnership loads the state and the owners of a name; lock takes the claim
// row FOR UPDATE first, and the owners are read by the next statement, which sees
// what a transaction that held the lock before committed.
func readOwnership(ctx context.Context, qs *q.Queries, k ownKey, lock bool) (string, []Owner, error) {
	var states []string
	var err error
	if lock {
		states, err = qs.OwnershipLockState(ctx, k.lockParams())
	} else {
		states, err = qs.OwnershipState(ctx, q.OwnershipStateParams(k.lockParams()))
	}
	if err != nil {
		return "", nil, err
	}
	state := StateUnclaimed
	if len(states) == 1 {
		state = states[0]
	}
	rows, err := qs.OwnershipOwners(ctx, q.OwnershipOwnersParams(k.lockParams()))
	if err != nil {
		return "", nil, err
	}
	owners := make([]Owner, 0, len(rows))
	for _, r := range rows {
		owners = append(owners, ownerOfRow(r))
	}
	return state, owners, nil
}

func ownerOfRow(r q.OwnershipOwnersRow) Owner {
	ow := Owner{
		Display: r.Display, Reason: r.Reason, AddedAt: time.UnixMicro(r.AddedUs).UTC(),
		AddedBy: refOfRow(r.AddedByKind, r.AddedByName), id: r.ID, principalID: r.PrincipalID,
	}
	switch r.OwnerKind {
	case "ci_project":
		// The issuer is named by ID until the issuers of M4 give it a name (R-CI-015).
		ow.Owner = "ci:" + r.CiIssuerID + ":project:" + r.CiProjectID
	default:
		ow.Owner = refOfRow(r.PrincipalKind, r.PrincipalName)
	}
	return ow
}

// CheckTx is the ownership check of a mutating operation (R-AUTHZ-018 rule 1),
// run in the writer's commit transaction after the actions were decided on idx
// and under the writer's package row lock. c is the coordinate the actions were
// decided on: the repository the client addressed (a virtual repository's deploy
// target is checked) and the normalized package name. A refusal is returned as
// the error, 403 package_owned_by_other_owner, and is audited as authz.denied
// with deny.ownership; the check then also holds the decision. The claim row,
// when there is one, stays locked until the transaction ends.
func (o *Ownership) CheckTx(ctx context.Context, tx meta.Tx, idx *Index, sub Subject, c Coordinate, w OwnershipWrite) (OwnershipCheck, error) {
	k, applies, err := o.keyOf(idx, c)
	if err != nil || !applies {
		return OwnershipCheck{}, err
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return OwnershipCheck{}, err
	}
	state, owners, err := readOwnership(ctx, qs, k, true)
	if err != nil {
		return OwnershipCheck{}, err
	}
	chk := o.decide(OwnershipCheck{
		Applies: true, State: state, Owners: owners,
		key: k, write: w, facts: o.factsOf(idx, sub, k, c), sub: sub, c: c, epoch: idx.Epoch(),
	})
	if chk.Decision.Outcome != Allow {
		o.refused(ctx, chk)
		return chk, chk.Decision.Err()
	}
	return chk, nil
}

// refused counts and audits a refused ownership check like a refused write.
func (o *Ownership) refused(ctx context.Context, chk OwnershipCheck) {
	c := chk.c
	c.Kind = KindWrite
	o.e.count(chk.Decision, chk.sub, c)
	o.e.audit(ctx, chk.sub, c, chk.Decision)
}

// maxClaimAttempts bounds the re-reads of a claim that lost the race of its
// insert: each lost race means another transaction committed a claim, after
// which the name has owners or a row this transaction can lock.
const maxClaimAttempts = 3

// ClaimTx records the claim the check decided, in the same transaction (rules 2
// and 5): the claim row is inserted with ON CONFLICT DO NOTHING, so of two
// concurrent first publishes, on one replica or on two, one inserts and the
// other waits for it, gets no row, reads the claim that committed and is
// decided again by rule 1; it is refused unless it is an owner. No advisory
// lock, no Valkey and no session state are involved, so the claim works through
// a transaction-mode pooler (R-ARCH-019). A check that claims nothing makes it a
// no-op; it records the claim the check's subject decided, on the facts of the
// check.
func (o *Ownership) ClaimTx(ctx context.Context, tx meta.Tx, chk OwnershipCheck) error {
	if !chk.Applies || !chk.Claim {
		return nil
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	k := chk.key
	for range maxClaimAttempts {
		claim := newID()
		if chk.State == StateUnclaimed {
			rows, err := qs.OwnershipInsertClaim(ctx, q.OwnershipInsertClaimParams{Domain: k.domain, Format: k.format, PackageName: k.name, ClaimEvent: claim})
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				// Another claim committed first: decide again on what it left.
				if chk.State, chk.Owners, err = readOwnership(ctx, qs, k, true); err != nil {
					return err
				}
				if chk = o.decide(chk); chk.Decision.Outcome != Allow {
					o.refused(ctx, chk)
					return chk.Decision.Err()
				}
				if !chk.Claim {
					return nil
				}
				continue
			}
		} else if err := qs.OwnershipReclaim(ctx, q.OwnershipReclaimParams{ClaimEvent: claim, Domain: k.domain, Format: k.format, PackageName: k.name}); err != nil {
			return err
		}
		return o.recordClaim(ctx, tx, qs, chk, claim)
	}
	return fmt.Errorf("authz: the claim of %s did not settle after %d attempts", targetOf(chk.c), maxClaimAttempts)
}

// recordClaim writes the owners of a claim and its audit event.
func (o *Ownership) recordClaim(ctx context.Context, tx meta.Tx, qs *q.Queries, chk OwnershipCheck, claim string) error {
	k := chk.key
	refs := make([]string, 0, len(chk.facts.claimers))
	for _, c := range chk.facts.claimers {
		if _, err := qs.OwnershipInsertOwner(ctx, q.OwnershipInsertOwnerParams{
			ID: newID(), Domain: k.domain, Format: k.format, PackageName: k.name, PrincipalID: c.principalID,
			Display: firstNonEmpty(c.ref.Name, c.principalID), Reason: OwnerFirstPublish,
		}); err != nil {
			return err
		}
		refs = append(refs, c.ref.String())
	}
	return o.rec.RecordTx(ctx, tx, ownershipEvent(EventOwnershipClaimed, chk.sub, k, map[string]any{
		"claim": claim, "reason": OwnerFirstPublish, "owners": refs,
	}, nil))
}

// subjectRefKind is the reference kind of the subject's own principal.
func subjectRefKind(sub Subject) principal.Kind {
	switch kindOf(sub) {
	case kindServiceAccount:
		return principal.KindServiceAccount
	case kindCI:
		return principal.KindCI
	}
	return principal.KindUser
}

// EnforceTx is the check and the claim of a mutating operation in one call, the
// form a writer uses (formats contract C-14): CheckTx, then ClaimTx with its
// answer.
func (o *Ownership) EnforceTx(ctx context.Context, tx meta.Tx, idx *Index, sub Subject, c Coordinate, w OwnershipWrite) (OwnershipCheck, error) {
	chk, err := o.CheckTx(ctx, tx, idx, sub, c, w)
	if err != nil {
		return chk, err
	}
	return chk, o.ClaimTx(ctx, tx, chk)
}

// ownershipEvent builds an ownership audit event; journal is the entry of
// R-AUTHZ-031 for the events that take access away.
func ownershipEvent(name string, actor Subject, k ownKey, extra map[string]any, journal map[string]any) audit.Event {
	details := map[string]any{"repository": k.repo.Key, "domain": k.domain, "format": k.format, "package": k.name}
	for key, v := range extra {
		details[key] = v
	}
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "package", Name: k.repo.Key + "/" + k.name},
		Repository: k.repo.Key, Path: k.name, Endpoint: actor.Endpoint,
		Details: mustJSON(details),
	}
	if journal != nil {
		ev.Journal = mustJSON(journal)
	}
	return ev
}

// The owner operations of R-AUTHZ-019. Each decides on the request's index and
// runs in a transaction of its own; the handler adds the HTTP conventions.

// errCIRestricted is the refusal of every owner change by a CI token: a pipeline
// runs the install scripts of its dependencies, so it must not be able to hand a
// package to another project (R-AUTHZ-019).
func errCIRestricted() error {
	return errs.E(errs.Forbidden, errs.CodeCITokenRestricted, "A CI token cannot change the owners of a package.")
}

// nameIn finds a name in its ownership domain, normalized with the format's name
// rule. ok is false where ownership does not exist: a repository the index does
// not hold, a remote or virtual repository, a format without package names, or a
// string that is no name of the format. norm is the name to decide on either way.
func (o *Ownership) nameIn(idx *Index, repoKey, name string) (k ownKey, norm string, ok bool) {
	r := idx.repo(repoKey)
	if r == nil || r.Class != repository.Local {
		return ownKey{}, name, false
	}
	names := o.e.namesOf(r.Format)
	if names == nil {
		return ownKey{}, name, false
	}
	if norm, ok = names.Normalize(name); !ok {
		return ownKey{}, name, false
	}
	return ownKey{repo: r, domain: domainOf(r), format: r.Format, name: norm}, norm, true
}

// manages reports whether the subject holds manage on the name, content:bypass
// included, under the ceiling of its endpoint. A repository the index does not
// hold has no grants.
func (o *Ownership) manages(idx *Index, sub Subject, repoKey, name string) bool {
	c := Coordinate{Repository: repoKey, Package: name, Kind: KindWrite, Actions: []Action{Manage}}
	return o.e.evaluate(idx, sub, c).Outcome == Allow
}

// owns reports whether one of the subject's owner identities is among the owners.
func owns(idx *Index, sub Subject, owners []Owner) bool {
	for _, id := range idx.principalIDs(sub) {
		if id == AnonymousID || id == AuthenticatedID {
			continue
		}
		for _, ow := range owners {
			if ow.principalID == id {
				return true
			}
		}
	}
	return false
}

// refuseChange is the refusal of an owner change: 403 naming manage, counted and
// audited like a refused write. It is the same for a name that does not exist,
// so a refused write reveals nothing (R-AUTHZ-016).
func (o *Ownership) refuseChange(ctx context.Context, idx *Index, sub Subject, repoKey, name string) error {
	c := Coordinate{Repository: repoKey, Package: name, Kind: KindWrite, Actions: []Action{Manage}}
	d := Decision{Outcome: DenyVisible, Reason: ReasonNoGrant, Missing: Manage, Target: targetOf(c), Epoch: idx.Epoch()}
	o.e.count(d, sub, c)
	o.e.audit(ctx, sub, c, d)
	return d.Err()
}

// changeTarget is the start of an owner change (POST and DELETE …/owners): an
// anonymous caller is 401, a CI token 403 ci_token_restricted, both before
// anything is looked up. Where ownership does not exist the answer is 404 for a
// caller that may manage the repository and the refusal of the change for every
// other caller, so the status of a write never depends on existence.
func (o *Ownership) changeTarget(ctx context.Context, idx *Index, sub Subject, repoKey, name string) (ownKey, error) {
	switch {
	case sub.Anonymous:
		return ownKey{}, Unauthenticated()
	case kindOf(sub) == kindCI:
		return ownKey{}, errCIRestricted()
	}
	k, norm, ok := o.nameIn(idx, repoKey, name)
	if ok {
		return k, nil
	}
	if o.manages(idx, sub, repoKey, norm) {
		return ownKey{}, NotFound()
	}
	return ownKey{}, o.refuseChange(ctx, idx, sub, repoKey, norm)
}

func (o *Ownership) packageOwners(k ownKey, state string, owners []Owner) PackageOwners {
	return PackageOwners{Repository: k.repo.Key, Domain: k.repo.Key, Format: k.format, Package: k.name, State: state, Owners: owners}
}

// Owners reads the state and the owners of a name (GET …/owners): read on the
// coordinate, refused as R-AUTHZ-016 says (404, byte-identical to a missing
// name). Where ownership does not exist the answer is the same 404.
func (o *Ownership) Owners(ctx context.Context, idx *Index, sub Subject, repoKey, name string) (PackageOwners, error) {
	k, norm, ok := o.nameIn(idx, repoKey, name)
	d := o.e.DecideOn(ctx, idx, sub, Coordinate{Repository: repoKey, Package: norm, Kind: KindMetadata, Actions: []Action{Read}})
	if d.Outcome != Allow {
		return PackageOwners{}, d.Err()
	}
	if !ok {
		return PackageOwners{}, NotFound()
	}
	var out PackageOwners
	err := o.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		state, owners, err := readOwnership(ctx, qs, k, false)
		out = o.packageOwners(k, state, owners)
		return err
	})
	return out, err
}

// ownerRef resolves the reference of an owner to a principal row. The owner
// identities are the principals of R-AUTHZ-018's table; anonymous and
// authenticated own nothing, and a CI project identity needs the CI issuers of
// M4. Each is 422 unknown_principal.
func ownerRef(ctx context.Context, qs *q.Queries, ref string) (Ref, string, error) {
	unknown := func(detail string) (Ref, string, error) {
		return Ref{}, "", problemError([]ProblemField{field("/owner", errs.CodeUnknownPrincipal, detail)})
	}
	if strings.HasPrefix(ref, "ci:") && strings.Contains(ref, ":project:") {
		return unknown("CI project owners arrive with the CI issuers (R-CI-015).")
	}
	r, err := ParseRef(ref)
	if err != nil {
		return unknown(err.Error())
	}
	kind := ""
	switch r.Kind {
	case principal.KindUser:
		kind = dbUser
	case principal.KindGroup:
		kind = dbGroup
	case principal.KindServiceAccount:
		kind = dbServiceAcct
	case principal.KindCI:
		kind = dbCIMapping
	default:
		return unknown(fmt.Sprintf("%s cannot own a package.", ref))
	}
	rows, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: kind, Name: r.Name})
	if err != nil {
		return Ref{}, "", err
	}
	if len(rows) == 0 {
		return unknown(fmt.Sprintf("No principal %s exists.", ref))
	}
	return r, rows[0].ID, nil
}

// changeAllowed decides an owner change (R-AUTHZ-019) on the owners the
// transaction read under the row lock: an owner, manage on the coordinate or
// content:bypass; with no owner left to ask (unclaimed, released, or claimed
// after the last owner was deleted) manage or content:bypass, and so for a
// release.
func (o *Ownership) changeAllowed(ctx context.Context, idx *Index, sub Subject, k ownKey, owners []Owner, needManage bool) error {
	if o.manages(idx, sub, k.repo.Key, k.name) || (!needManage && owns(idx, sub, owners)) {
		return nil
	}
	return o.refuseChange(ctx, idx, sub, k.repo.Key, k.name)
}

// AddOwner adds an owner to a name (POST …/owners) in a transaction of its own;
// see [Ownership.AddOwnerTx].
func (o *Ownership) AddOwner(ctx context.Context, idx *Index, sub Subject, repoKey, name, ref string) (Owner, error) {
	var out Owner
	err := o.db.InTx(ctx, func(tx meta.Tx) (err error) {
		out, err = o.AddOwnerTx(ctx, tx, idx, sub, repoKey, name, ref)
		return err
	})
	return out, err
}

// AddOwnerTx is AddOwner in a transaction the caller owns: the management API
// runs it in the transaction of an Idempotency-Key (R-API-014), so the owner,
// its audit event and the stored outcome commit together. On a name that is
// unclaimed and holds no version it pre-assigns the name: the claim is recorded
// for the given owner before anything is published. The permission is decided
// before the owner reference is looked up, so a refused caller learns nothing
// about principals; an unknown owner is 422 unknown_principal, and a principal
// that owns the name already is 409 already_exists.
func (o *Ownership) AddOwnerTx(ctx context.Context, tx meta.Tx, idx *Index, sub Subject, repoKey, name, ref string) (Owner, error) {
	k, err := o.changeTarget(ctx, idx, sub, repoKey, name)
	if err != nil {
		return Owner{}, err
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return Owner{}, err
	}
	state, owners, err := readOwnership(ctx, qs, k, true)
	if err != nil {
		return Owner{}, err
	}
	if err := o.changeAllowed(ctx, idx, sub, k, owners, len(owners) == 0); err != nil {
		return Owner{}, err
	}
	r, pid, err := ownerRef(ctx, qs, ref)
	if err != nil {
		return Owner{}, err
	}
	reason := OwnerAdded
	if state != StateClaimed {
		versions, err := qs.OwnershipHasVersions(ctx, q.OwnershipHasVersionsParams{RepoID: k.repo.ID, PackageName: k.name})
		if err != nil {
			return Owner{}, err
		}
		if !versions {
			reason = OwnerPreAssigned
		}
		if err := o.claimRow(ctx, qs, k, state); err != nil {
			return Owner{}, err
		}
	}
	added, err := qs.OwnershipInsertOwner(ctx, q.OwnershipInsertOwnerParams{
		ID: newID(), Domain: k.domain, Format: k.format, PackageName: k.name, PrincipalID: pid,
		Display: r.Name, Reason: reason, AddedBy: actorID(sub),
	})
	if err != nil {
		return Owner{}, err
	}
	if len(added) == 0 {
		return Owner{}, errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("%s owns %s already.", r, k.name))
	}
	out := Owner{Owner: r.String(), Display: r.Name, Reason: reason, AddedAt: time.UnixMicro(added[0]).UTC(), AddedBy: o.actorRef(sub)}
	return out, o.rec.RecordTx(ctx, tx, ownershipEvent(EventOwnershipAdded, sub, k, map[string]any{
		"owner": r.String(), "ownerId": pid, "reason": reason,
	}, nil))
}

// PackageName is the normalized name the owner operations address, for the
// Location of an answer: the format's name rule of the repository, the name as
// given where the index knows no rule for it.
func (o *Ownership) PackageName(idx *Index, repoKey, name string) string {
	_, norm, _ := o.nameIn(idx, repoKey, name)
	return norm
}

// claimRow makes the name claimed for an owner the API adds: a new row, or the
// row of a released name claimed again. A concurrent first publish that inserted
// the row in between makes the insert wait and do nothing; the owner is added to
// that claim, which the caller was allowed to change (manage or content:bypass).
func (o *Ownership) claimRow(ctx context.Context, qs *q.Queries, k ownKey, state string) error {
	claim := newID()
	if state == StateReleased {
		return qs.OwnershipReclaim(ctx, q.OwnershipReclaimParams{ClaimEvent: claim, Domain: k.domain, Format: k.format, PackageName: k.name})
	}
	_, err := qs.OwnershipInsertClaim(ctx, q.OwnershipInsertClaimParams{Domain: k.domain, Format: k.format, PackageName: k.name, ClaimEvent: claim})
	return err
}

// actorRef is the principal reference of the subject, as AddedBy shows it.
func (o *Ownership) actorRef(sub Subject) string {
	if sub.Anonymous || sub.Name == "" {
		return ""
	}
	return Ref{Kind: subjectRefKind(sub), Name: sub.Name}.String()
}

// RemoveOwner removes an owner of a name (DELETE …/owners/{owner}) in a
// transaction of its own; see [Ownership.RemoveOwnerTx].
func (o *Ownership) RemoveOwner(ctx context.Context, idx *Index, sub Subject, repoKey, name, ref string, release bool) error {
	return o.db.InTx(ctx, func(tx meta.Tx) error {
		return o.RemoveOwnerTx(ctx, tx, idx, sub, repoKey, name, ref, release)
	})
}

// RemoveOwnerTx is RemoveOwner in a transaction the caller owns. Removing the
// last owner is 409 last_owner unless release is true, which releases the name:
// it becomes unclaimed again (R-AUTHZ-018 rule 1, rule 3). release needs manage
// or content:bypass. Both events take access away and carry the journal entry
// of R-AUTHZ-031. The owners are read under the claim row's lock, so two removals
// of the last two owners run one after the other and the second is 409.
func (o *Ownership) RemoveOwnerTx(ctx context.Context, tx meta.Tx, idx *Index, sub Subject, repoKey, name, ref string, release bool) error {
	k, err := o.changeTarget(ctx, idx, sub, repoKey, name)
	if err != nil {
		return err
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	_, owners, err := readOwnership(ctx, qs, k, true)
	if err != nil {
		return err
	}
	if err := o.changeAllowed(ctx, idx, sub, k, owners, release || len(owners) == 0); err != nil {
		return err
	}
	if r, err := ParseRef(ref); err == nil {
		ref = r.String()
	}
	var gone *Owner
	for i := range owners {
		if owners[i].Owner == ref {
			gone = &owners[i]
			break
		}
	}
	if gone == nil {
		return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("%s is not an owner of %s.", ref, k.name))
	}
	last := len(owners) == 1
	if last && !release {
		return errs.E(errs.Conflict, errs.CodeLastOwner,
			fmt.Sprintf("%s is the only owner of %s; add the new owner first, or release the name.", ref, k.name))
	}
	if _, err := qs.OwnershipDeleteOwner(ctx, gone.id); err != nil {
		return err
	}
	event := EventOwnershipRemoved
	if last {
		event = EventOwnershipReleased
		if err := qs.OwnershipRelease(ctx, q.OwnershipReleaseParams{Domain: k.domain, Format: k.format, PackageName: k.name}); err != nil {
			return err
		}
	}
	journal := map[string]any{"domain": k.domain, "format": k.format, "package": k.name, "principalId": gone.principalID, "owner": gone.Owner}
	return o.rec.RecordTx(ctx, tx, ownershipEvent(event, sub, k, map[string]any{"owner": gone.Owner, "ownerId": gone.principalID}, journal))
}

// OwnedPackage is one name an identity owns (R-AUTHZ-019 GET /package-owners).
type OwnedPackage struct {
	// Repository is the key of the repository whose domain holds the name.
	Repository string
	Format     string
	Package    string
	State      string
	Reason     string
	AddedAt    time.Time
}

// OwnedCursor is the position after which a page of [Ownership.OwnedBy] starts:
// the key order of the ownership rows. The zero value is the first page.
type OwnedCursor struct{ Domain, Format, Package string }

// OwnedBy lists the names an identity owns, for offboarding and transfers: the
// entries that name the identity itself. It needs policies:read, or the identity
// is the caller's own or one of its groups; such a caller sees only the names of
// repositories it may read (R-AUTHZ-016), so a page may hold fewer items than the
// limit while a next cursor exists. repoKey narrows the list to one repository's
// domain; limit is at most 1000. One row more than the page is read: the cursor
// of the next page is returned when it exists, and it is the key of the last row
// read for the page, shown or not, so a filtered row is never read twice.
func (o *Ownership) OwnedBy(ctx context.Context, idx *Index, sub Subject, ref, repoKey string, after OwnedCursor, limit int) ([]OwnedPackage, *OwnedCursor, error) {
	full := o.e.HoldsOn(idx, sub, string(PoliciesRead))
	if !full && !o.ownIdentity(sub, ref) {
		return nil, nil, o.e.RequireOn(ctx, idx, sub, string(PoliciesRead))
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	domain := ""
	if repoKey != "" {
		r := idx.repo(repoKey)
		if r == nil {
			return []OwnedPackage{}, nil, nil
		}
		domain = domainOf(r)
	}
	var out []OwnedPackage
	var next *OwnedCursor
	err := o.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		_, pid, err := ownerRef(ctx, qs, ref)
		if err != nil {
			return err
		}
		rows, err := qs.OwnershipOwnedBy(ctx, q.OwnershipOwnedByParams{
			PrincipalID: pid, Domain: domain, AfterDomain: after.Domain, AfterFormat: after.Format, AfterName: after.Package,
			MaxRows: int32(limit + 1), //nolint:gosec // at most 1001
		})
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			last := rows[len(rows)-1]
			next = &OwnedCursor{Domain: last.Domain, Format: last.Format, Package: last.PackageName}
		}
		out = make([]OwnedPackage, 0, len(rows))
		for _, r := range rows {
			out = append(out, OwnedPackage{
				Repository: r.RepositoryKey, Format: r.Format, Package: r.PackageName, State: r.State, Reason: r.Reason,
				AddedAt: time.UnixMicro(r.AddedUs).UTC(),
			})
		}
		return nil
	})
	if err != nil || full {
		return out, next, err
	}
	keys := make([]string, 0, len(out))
	for _, p := range out {
		if p.Repository != "" && !slices.Contains(keys, p.Repository) {
			keys = append(keys, p.Repository)
		}
	}
	readable, err := o.e.ReadableOn(ctx, idx, sub, keys)
	if err != nil {
		return nil, nil, err
	}
	shown := out[:0]
	for _, p := range out {
		if slices.Contains(readable, p.Repository) {
			shown = append(shown, p)
		}
	}
	return shown, next, nil
}

// ownIdentity reports whether the reference names the subject itself or one of
// its groups, by name as the subject carries them.
func (o *Ownership) ownIdentity(sub Subject, ref string) bool {
	if sub.Anonymous {
		return false
	}
	r, err := ParseRef(ref)
	if err != nil {
		return false
	}
	switch r.Kind {
	case principal.KindGroup:
		for _, g := range sub.Groups {
			if g.Name == r.Name {
				return true
			}
		}
		return false
	case principal.KindCI:
		for _, m := range sub.Mappings {
			if m == r.String() {
				return true
			}
		}
		return false
	}
	return sub.Name != "" && r.Kind == subjectRefKind(sub) && r.Name == sub.Name
}

// OwnershipHook implements [repository.Hooks]: the ownership rows of a
// repository's domain carry no foreign key to it (the domain is text, so that
// shared domains of R-AUTHZ-020 fit the same table), so the deletion of the
// repository removes them in its own transaction (Q-AUTHZ-156). Repository IDs
// are never reused, so this keeps the tables and the backups small; it closes no
// access path.
type OwnershipHook struct{}

var _ repository.Hooks = OwnershipHook{}

// NewOwnershipHook returns the hook.
func NewOwnershipHook() OwnershipHook { return OwnershipHook{} }

// OnUpdated implements [repository.Hooks]: an update changes no ownership row.
func (OwnershipHook) OnUpdated(context.Context, meta.Tx, repository.Caller, repository.Repository, repository.Repository) error {
	return nil
}

// OnDelete implements [repository.Hooks].
func (OwnershipHook) OnDelete(ctx context.Context, tx meta.Tx, _ repository.Caller, r repository.Repository) error {
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("authz: the repository id %q is no number: %w", r.ID, err)
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	_, err = qs.OwnershipDeleteDomain(ctx, "repo:"+strconv.FormatInt(id, 10))
	return err
}
