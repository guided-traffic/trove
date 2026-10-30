package repository

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Members, the deploy target and the pin rules as rows (R-REPO-017, R-REPO-021,
// R-REPO-023): the part of a virtual repository's write that needs PostgreSQL.
// The document-only rules are in validate_virtual.go.

// resolveVirtual checks the references of a virtual document against the other
// repositories and returns them as rows to write. It adds the violations of
// R-REPO-008 that only the database can decide to v: a member that is missing,
// being deleted, of another format or itself virtual (nesting is R-REPO-018,
// P1), and a deploy target that is not local. The members are locked FOR SHARE
// in id order, so a concurrent deletion of a member waits for this transaction
// instead of marking the repository after it was checked.
func (s *Service) resolveVirtual(ctx context.Context, qs *q.Queries, v *validation, d *Document) (*virtualRel, error) {
	if d.Class != Virtual || d.Virtual == nil {
		return nil, nil
	}
	vs := d.Virtual
	var keys []string
	add := func(k string) {
		if k != "" && k != d.Key && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, k := range vs.Members {
		add(k)
	}
	if vs.DeployTarget != nil {
		add(*vs.DeployTarget)
	}
	for _, r := range vs.Pinning.Rules {
		for _, k := range r.Members {
			add(k)
		}
	}
	byKey := map[string]q.LockRepositoriesByKeysRow{}
	if len(keys) > 0 {
		rows, err := qs.LockRepositoriesByKeys(ctx, keys)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			byKey[r.Key] = r
		}
	}
	if len(vs.Members) > s.maxMembers {
		v.add("/virtual/members", codeOutOfRange, "at most "+strconv.Itoa(s.maxMembers)+" members (repositories.max_virtual_members)")
	}
	rel := &virtualRel{}
	ref := func(k string) relRef { return relRef{id: byKey[k].ID, key: k} }
	for i, k := range vs.Members {
		ptr := "/virtual/members/" + strconv.Itoa(i)
		row, found := byKey[k]
		switch {
		case k == "" || k == d.Key:
			// reported with the document rules
		case !found:
			v.add(ptr, errs.CodeInvalidMember, "no repository has the key "+strconv.Quote(k))
		case Lifecycle(row.Lifecycle) == Deleting:
			v.add(ptr, errs.CodeInvalidMember, "the repository is being deleted")
		case row.Format != d.Format:
			v.add(ptr, errs.CodeInvalidMember, "the repository has the format "+row.Format+", not "+d.Format)
		case Class(row.Class) == Virtual:
			v.add(ptr, errs.CodeInvalidMember, "a virtual repository takes local and remote members; nesting arrives with R-REPO-018")
		default:
			rel.members = append(rel.members, ref(k))
		}
	}
	if vs.DeployTarget != nil {
		if row, found := byKey[*vs.DeployTarget]; found && slices.Contains(vs.Members, *vs.DeployTarget) {
			if Class(row.Class) != Local {
				v.add("/virtual/deployTarget", codeInvalidDeployTarget, "the deploy target must be a local repository")
			} else {
				t := ref(*vs.DeployTarget)
				rel.target = &t
			}
		}
	}
	for _, r := range vs.Pinning.Rules {
		pin := relPin{patterns: slices.Clone(r.Packages)}
		for _, k := range r.Members {
			if _, found := byKey[k]; found && slices.Contains(vs.Members, k) {
				pin.members = append(pin.members, ref(k))
			}
		}
		rel.pins = append(rel.pins, pin)
	}
	return rel, s.warnUnprotected(ctx, qs, v, rel, byKey)
}

// warnUnprotected raises dependency_confusion_unprotected (R-REPO-023): the
// virtual repository has local and remote members, no pin rule, and none of its
// local members claims anything automatically or by a claim of its own.
func (s *Service) warnUnprotected(ctx context.Context, qs *q.Queries, v *validation, rel *virtualRel,
	byKey map[string]q.LockRepositoriesByKeysRow,
) error {
	if len(rel.pins) > 0 {
		return nil
	}
	var locals []int64
	remotes := false
	for _, m := range rel.members {
		row := byKey[m.key]
		switch Class(row.Class) {
		case Local:
			locals = append(locals, m.id)
			if r, err := toRepository(q.Repository{ID: row.ID, Key: row.Key, Class: row.Class, Format: row.Format, Config: row.Config}, s.cat); err == nil && r.autoClaim {
				return nil
			}
		case Remote:
			remotes = true
		}
	}
	if len(locals) == 0 || !remotes {
		return nil
	}
	claiming, err := qs.ListClaimingRepositories(ctx, locals)
	if err != nil {
		return err
	}
	if len(claiming) > 0 {
		return nil
	}
	v.warnings = append(v.warnings, Warning{
		Code: warningUnprotected, Pointer: "/virtual",
		Detail: "the virtual repository has local and remote members, no pin rule, and its local members claim no names (local.autoClaim is off and no claim exists): a public package can shadow an internal name that is not published yet",
	})
	return nil
}

// writeVirtual replaces the rows of a virtual repository with rel. It writes
// only the parts that differ from old (nil on a create) and reports whether the
// member list or deploy target and whether the pin rules changed.
func writeVirtual(ctx context.Context, qs *q.Queries, id int64, rel, old *virtualRel) (membersChanged, pinsChanged bool, err error) {
	if old == nil {
		old = &virtualRel{}
	}
	sameRef := func(a, b relRef) bool { return a.id == b.id }
	if !slices.EqualFunc(old.members, rel.members, sameRef) {
		membersChanged = true
		if err = qs.DeleteVirtualMembers(ctx, id); err != nil {
			return
		}
		for i, m := range rel.members {
			if err = qs.InsertVirtualMember(ctx, q.InsertVirtualMemberParams{VirtualID: id, Position: int32(i), MemberID: m.id}); err != nil { //nolint:gosec // bounded by max_virtual_members
				return
			}
		}
	}
	if (old.target == nil) != (rel.target == nil) || (rel.target != nil && old.target.id != rel.target.id) {
		membersChanged = true
		if err = qs.DeleteVirtualDeployTarget(ctx, id); err != nil {
			return
		}
		if rel.target != nil {
			if err = qs.InsertVirtualDeployTarget(ctx, q.InsertVirtualDeployTargetParams{VirtualID: id, TargetID: rel.target.id}); err != nil {
				return
			}
		}
	}
	if !pinsEqual(old.pins, rel.pins) {
		pinsChanged = true
		if err = qs.DeleteVirtualPinRules(ctx, id); err != nil {
			return
		}
		for i, p := range rel.pins {
			if err = qs.InsertVirtualPinRule(ctx, q.InsertVirtualPinRuleParams{VirtualID: id, Position: int32(i), PackagePatterns: p.patterns}); err != nil { //nolint:gosec // bounded by MaxPinRules
				return
			}
			for _, m := range p.members {
				if err = qs.InsertVirtualPinMember(ctx, q.InsertVirtualPinMemberParams{VirtualID: id, Position: int32(i), MemberID: m.id}); err != nil { //nolint:gosec // bounded by MaxPinRules
					return
				}
			}
		}
	}
	return membersChanged, pinsChanged, nil
}

// pinsEqual compares two pin lists; the members of a rule compare as sets,
// because a read returns them in the order of the member list.
func pinsEqual(a, b []relPin) bool {
	return slices.EqualFunc(a, b, func(x, y relPin) bool {
		if !slices.Equal(x.patterns, y.patterns) || len(x.members) != len(y.members) {
			return false
		}
		for _, m := range x.members {
			if !slices.ContainsFunc(y.members, func(o relRef) bool { return o.id == m.id }) {
				return false
			}
		}
		return true
	})
}

// notifyPins tells the replicas that pin rules changed. They are part of the
// repository snapshot, which the repo_config change of the update drops; the
// area claims carries them as well (R-REPO-023), so that everything that routes
// reads one area.
func (s *Service) notifyPins(tx meta.Tx, r Repository) error {
	return s.notif.Notify(tx, cluster.AreaClaims, 1, claimsPayload{ID: r.ID, Key: r.Key, Op: "pins"})
}

// detachReferences handles the virtual repositories that list a repository when
// it is deleted (R-REPO-013). Without detach the delete answers 409
// repository_referenced naming them. With detach the same transaction removes
// the repository from every member list, deploy target and pin rule, moves the
// ETag of each virtual repository and records an update of it.
func (s *Service) detachReferences(ctx context.Context, tx meta.Tx, qs *q.Queries, c Caller, row q.Repository, detach bool) error {
	refs, err := qs.ListReferencingVirtuals(ctx, row.ID)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	if !detach {
		keys := make([]string, len(refs))
		for i, r := range refs {
			keys[i] = r.Key
		}
		return errs.E(errs.Conflict, errs.CodeRepositoryReferenced,
			"The repository is referenced by the virtual repositories "+strings.Join(keys, ", ")+"; delete with detach=true or remove the references first.")
	}
	if err := qs.DetachMember(ctx, row.ID); err != nil {
		return err
	}
	if err := qs.DetachDeployTarget(ctx, row.ID); err != nil {
		return err
	}
	if err := qs.DetachPinMember(ctx, row.ID); err != nil {
		return err
	}
	ids := make([]int64, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	if err := qs.TouchConfigVersions(ctx, ids); err != nil {
		return err
	}
	for _, ref := range refs {
		rows, err := qs.GetRepositoryByID(ctx, ref.ID)
		if err != nil || len(rows) == 0 {
			return err
		}
		v, _ := toRepository(rows[0], s.cat)
		if err := s.afterWrite(ctx, tx, c, EventUpdated, v, []string{"virtual"}); err != nil {
			return err
		}
	}
	return s.notif.Notify(tx, cluster.AreaClaims, 1, claimsPayload{ID: strconv.FormatInt(row.ID, 10), Key: row.Key, Op: "detach"})
}
