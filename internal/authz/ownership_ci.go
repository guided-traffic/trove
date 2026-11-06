package authz

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ciProjectRefRE is the owner reference of a CI project, "ci:<issuer>:project:<project_id>" (R-CI-015,
// R-AUTHZ-019): the issuer name of R-CI-001 and a project ID of at most 64 characters (package_owner's CHECK).
var ciProjectRefRE = regexp.MustCompile(`^ci:([a-z0-9][a-z0-9._-]{1,63}):project:([A-Za-z0-9._-]{1,64})$`)

// parseCIProjectRef parses a CI project owner reference; ok is false for any other string.
func parseCIProjectRef(ref string) (CIProject, bool) {
	m := ciProjectRefRE.FindStringSubmatch(ref)
	if m == nil {
		return CIProject{}, false
	}
	return CIProject{Issuer: m[1], ProjectID: m[2]}, true
}

// ciIssuerID resolves the issuer of a CI project reference by name; an unknown issuer is 422 unknown_principal,
// as an unknown principal is (Q-AUTHZ-157).
func ciIssuerID(ctx context.Context, qs *q.Queries, p CIProject) (string, error) {
	ids, err := qs.CiIssuerIDByName(ctx, p.Issuer)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", problemError([]ProblemField{field("/owner", errs.CodeUnknownPrincipal, fmt.Sprintf("No CI issuer %s exists.", p.Issuer))})
	}
	return ids[0], nil
}

// ciAddProjectOwner adds a CI project as an owner (POST …/owners with {"owner": "ci:<issuer>:project:<id>"},
// R-AUTHZ-019, R-CI-015): an administrator hands a name to the project, so its next publish succeeds. The caller
// decided the permission; the display value is the project ID until the project's first publish writes its path.
func (o *Ownership) ciAddProjectOwner(ctx context.Context, tx meta.Tx, qs *q.Queries, sub Subject, k ownKey, state string, p CIProject) (Owner, error) {
	issuerID, err := ciIssuerID(ctx, qs, p)
	if err != nil {
		return Owner{}, err
	}
	reason, err := o.claimForAdd(ctx, qs, k, state)
	if err != nil {
		return Owner{}, err
	}
	added, err := qs.CiOwnershipInsertProjectOwner(ctx, q.CiOwnershipInsertProjectOwnerParams{
		ID: newID(), Domain: k.domain, Format: k.format, PackageName: k.name, CiIssuerID: issuerID, CiProjectID: p.ProjectID,
		Display: p.ProjectID, Reason: reason, AddedBy: actorID(sub),
	})
	if err != nil {
		return Owner{}, err
	}
	if len(added) == 0 {
		return Owner{}, errs.E(errs.Conflict, errs.CodeAlreadyExists, fmt.Sprintf("%s owns %s already.", p.Ref(), k.name))
	}
	out := Owner{
		Owner: p.Ref(), Display: p.ProjectID, Reason: reason, AddedAt: time.UnixMicro(added[0]).UTC(), AddedBy: o.actorRef(sub),
		ciIssuerID: issuerID, ciProjectID: p.ProjectID,
	}
	return out, o.rec.RecordTx(ctx, tx, ownershipEvent(EventOwnershipAdded, sub, k, map[string]any{
		"owner": p.Ref(), "ciIssuerId": issuerID, "ciProjectId": p.ProjectID, "reason": reason,
	}, nil))
}

// Package ownership for CI tokens (R-CI-015, R-AUTHZ-018), work package W5. The general check, claim and owner
// operations are ownership.go's; this file is what a CI token adds to them:
//
//   - the owner identities of a CI token: its project "ci:<issuer>:project:<project_id>" (stored by issuer ID
//     and project ID), its mappings and the groups they list (R-AUTHZ-018 rule 1);
//   - the check also for a write that an inline grant with ownership bind_project allows in a repository whose
//     packageOwnership is off (keyFor);
//   - the claim of rule 2: the project only when a bind_project grant allowed the publish, otherwise the
//     principals of the grants that allowed it (a mapping's inline grant with ownership none makes the mapping
//     the owner);
//   - project_path as the display value, updated on every publish of the project (rule 7);
//   - the events ci.package_ownership.claimed and ci.package_ownership.conflict (R-CI-020).

// ciOwnFacts are the owner identities of a CI subject that the principal IDs of ownFacts cannot express.
type ciOwnFacts struct {
	// project is the token's project identity, nil for a token without one.
	project *CIProject
	// refs are the references of its mappings and listed groups, matched against owner rows by name.
	refs map[string]bool
}

// owns reports whether the owner row is one of the subject's CI identities; false for a subject that is no CI
// token.
func (f *ciOwnFacts) owns(ow Owner) bool {
	if f == nil {
		return false
	}
	if ow.ciIssuerID != "" {
		return f.project != nil && ow.ciIssuerID == f.project.IssuerID && ow.ciProjectID == f.project.ProjectID
	}
	return ow.principalID != "" && f.refs[ow.Owner]
}

// ciFacts fills the facts of a CI subject's write (factsOf): manage and content:bypass are never a CI token's,
// so the facts are its owner identities and the owners its claim records.
func (o *Ownership) ciFacts(idx *Index, st *evalState, f ownFacts, k ownKey, c Coordinate) ownFacts {
	f.identities = map[string]bool{}
	ids, refs := st.ci.ciIdentities()
	for _, id := range ids {
		f.identities[id] = true
	}
	f.ci = &ciOwnFacts{refs: map[string]bool{}}
	for _, r := range refs {
		f.ci.refs[r] = true
	}
	if p, ok := st.sub.CIProject(); ok {
		f.ci.project = &p
	}
	w := o.e.ciWriteOf(st.ci, idx, k.repo, c)
	if w.bindsDeploy {
		// Rule 2: a bind_project grant allowed the publish, so the project alone claims the name. A token
		// without a project claims nothing, and decide refuses a claim without owners.
		if f.ci.project != nil {
			f.claimers = []claimOwner{{project: f.ci.project}}
		}
		return f
	}
	refsByID := identityRefs(idx, st.sub)
	for _, cb := range st.ci.contributions {
		for i, gid := range cb.groups {
			refsByID[gid] = Ref{Kind: principal.KindGroup, Name: cb.groupNames[i]}
		}
		if cb.pid != "" {
			refsByID[cb.pid] = Ref{Kind: principal.KindCI, Name: cb.name}
		}
	}
	for _, id := range w.principals {
		f.claimers = append(f.claimers, claimOwner{principalID: id, ref: refsByID[id]})
	}
	for _, name := range w.mappings {
		f.claimers = append(f.claimers, claimOwner{mapping: name, ref: Ref{Kind: principal.KindCI, Name: name}})
	}
	return f
}

// ciResolveClaimers resolves the mapping principals a claim records that the index does not know (a mapping
// whose inline grant with ownership none allowed the publish and that no policy names), by name in the write's
// transaction. A principal that is gone since the exchange is dropped: its token is revoked, so the write is
// one of the last it could make, and a claim without owners is refused by decide.
func ciResolveClaimers(ctx context.Context, qs *q.Queries, f ownFacts) (ownFacts, error) {
	var names []string
	for _, c := range f.claimers {
		if c.mapping != "" {
			names = append(names, c.mapping)
		}
	}
	if len(names) == 0 {
		return f, nil
	}
	rows, err := qs.CiMappingPrincipalsByName(ctx, names)
	if err != nil {
		return f, err
	}
	byName := map[string]string{}
	for _, r := range rows {
		byName[r.Name] = r.ID
	}
	out := f.claimers[:0:0]
	for _, c := range f.claimers {
		if c.mapping != "" {
			id := byName[c.mapping]
			if id == "" || slices.ContainsFunc(out, func(x claimOwner) bool { return x.principalID == id }) {
				continue
			}
			c.principalID, c.mapping = id, ""
			f.identities[id] = true
		}
		out = append(out, c)
	}
	f.claimers = out
	return f, nil
}

// ciRecordProjectClaim records the project as the owner of a claimed name and writes
// ci.package_ownership.claimed (R-CI-015, R-CI-020); it returns the owner's reference for authz.ownership.claimed.
func (o *Ownership) ciRecordProjectClaim(ctx context.Context, tx meta.Tx, qs *q.Queries, chk OwnershipCheck, p CIProject) (string, error) {
	k := chk.key
	if _, err := qs.CiOwnershipInsertProjectOwner(ctx, q.CiOwnershipInsertProjectOwnerParams{
		ID: newID(), Domain: k.domain, Format: k.format, PackageName: k.name, CiIssuerID: p.IssuerID, CiProjectID: p.ProjectID,
		Display: firstNonEmpty(p.ProjectPath, p.ProjectID), Reason: OwnerFirstPublish,
	}); err != nil {
		return "", err
	}
	err := o.rec.RecordTx(ctx, tx, ciOwnershipEvent(audit.EventCIOwnershipClaimed, chk, p, nil))
	return p.Ref(), err
}

// ciTouchDisplay keeps project_path the display value of the publishing project (R-CI-015, R-AUTHZ-018 rule 7):
// an allowed publish of a CI token whose project owns the name writes its current path. It runs in the commit
// transaction, under the claim row's lock the check took.
func ciTouchDisplay(ctx context.Context, qs *q.Queries, chk OwnershipCheck) error {
	f := chk.facts.ci
	if f == nil || f.project == nil || f.project.ProjectPath == "" || chk.Decision.Outcome != Allow {
		return nil
	}
	k := chk.key
	_, err := qs.CiOwnershipUpdateDisplay(ctx, q.CiOwnershipUpdateDisplayParams{
		Display: f.project.ProjectPath, Domain: k.domain, Format: k.format, PackageName: k.name,
		CiIssuerID: f.project.IssuerID, CiProjectID: f.project.ProjectID,
	})
	return err
}

// ciConflict writes ci.package_ownership.conflict for a CI token's refused write (R-CI-015): through the buffer,
// because the refusal rolls the write back. owner is the project that was refused, owners the names' owners.
func (o *Ownership) ciConflict(ctx context.Context, chk OwnershipCheck) {
	if kindOf(chk.sub) != kindCI {
		return
	}
	var p CIProject
	if chk.facts.ci != nil && chk.facts.ci.project != nil {
		p = *chk.facts.ci.project
	}
	owners := make([]string, 0, len(chk.Owners))
	for _, ow := range chk.Owners {
		owners = append(owners, ow.Owner)
	}
	o.rec.Record(ctx, ciOwnershipEvent(audit.EventCIOwnershipConflict, chk, p, owners))
}

func ciOwnershipEvent(name string, chk OwnershipCheck, p CIProject, owners []string) audit.Event {
	k := chk.key
	d := map[string]any{"repository": k.repo.Name, "package": k.name, "tokenId": chk.sub.TokenID, "epoch": chk.epoch}
	if p.ProjectID != "" {
		d["owner"] = p.Ref()
	}
	outcome := audit.Success
	if owners != nil {
		d["owners"] = owners
		outcome = audit.Denied
	}
	return audit.Event{
		Type: name, Actor: actorOf(chk.sub), Outcome: outcome, Reason: chk.Decision.Reason,
		Subject:    audit.Subject{Kind: "package", Name: k.repo.Name.String() + "/" + k.name},
		Repository: k.repo.Name.String(), Path: k.name, Endpoint: chk.sub.Endpoint, Details: mustJSON(d),
	}
}
