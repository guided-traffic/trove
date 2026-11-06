package project

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The members of a project (R-AUTHZ-026, Q-AUTHZ-207, Q-AUTHZ-208, Q-AUTHZ-217). A member is a user or a
// group with one of the four roles; the grant index reads the rows (authz.Index) and decides the role's
// actions from them, so a write here changes what a member may do on every replica within the bound of
// R-AUTHZ-028, through the change area authz this file notifies in the transaction of the write.
//
// Every write runs under the project's row lock (ProjectLock): the count of the last-Owner rule and the
// write are one moment, so two Owners who remove or demote each other at the same instant run one after the
// other and the second is refused.

// parseMemberRef reads the reference of a member, "user:<name>" or "group:<name>". A reference that does
// not parse is 422 validation_failed with invalid_principal; a kind that is never a member (a service
// account, a CI mapping, a Bastion registration, anonymous, authenticated) is 422 validation_failed with
// principal_not_allowed at /principalRef (R-AUTHZ-026, Q-AUTHZ-215, R-BAS-004; the code of a violation lives in
// errors[], Q-API-136).
func parseMemberRef(ref string) (PrincipalKind, string, error) {
	r, err := authz.ParseRef(ref)
	if err != nil {
		return "", "", errs.E(errs.Invalid, errs.CodeValidation, err.Error(), errs.WithFields(errs.FieldError{
			Pointer: "/principalRef", Code: "invalid_principal", Detail: err.Error(),
		}))
	}
	switch r.Kind {
	case principal.KindUser:
		return KindUser, r.Name, nil
	case principal.KindGroup:
		return KindGroup, r.Name, nil
	case principal.KindBastion:
		return "", "", principalNotAllowed(ref, "a Bastion registration holds no grant, role, ownership or membership") // R-BAS-004
	}
	return "", "", principalNotAllowed(ref, "only users and groups are members of a project; a service account or a CI mapping reaches it through its policies")
}

func principalNotAllowed(ref, why string) error {
	detail := fmt.Sprintf("%s cannot be a member of a project: %s.", ref, why)
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(errs.FieldError{
		Pointer: "/principalRef", Code: errs.CodePrincipalNotAllowed, Detail: detail,
	}))
}

// memberPrincipal reads the user or group a member reference names, in the writer's transaction: 404 for a
// principal that does not exist, 422 principal_not_allowed for an auto-join group, which every user is in,
// so that it would make every user a member (what authenticated in a global policy says, Q-AUTHZ-200).
func memberPrincipal(ctx context.Context, qs *q.Queries, ref string) (PrincipalRef, error) {
	kind, name, err := parseMemberRef(ref)
	if err != nil {
		return PrincipalRef{}, err
	}
	rows, err := qs.ProjectPrincipalByRef(ctx, q.ProjectPrincipalByRefParams{Kind: string(kind), Name: name})
	if err != nil {
		return PrincipalRef{}, err
	}
	if len(rows) == 0 {
		return PrincipalRef{}, errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The principal %s does not exist.", ref))
	}
	if rows[0].AutoJoin {
		return PrincipalRef{}, principalNotAllowed(ref, "it is an auto-join group, which every user is in")
	}
	return PrincipalRef{Kind: kind, Name: rows[0].Name, ID: rows[0].ID}, nil
}

// lockProject takes the project's row lock and returns its ID; 404 for a project that does not exist.
func lockProject(ctx context.Context, qs *q.Queries, key string) (string, error) {
	ids, err := qs.ProjectLock(ctx, key)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", notFound(key)
	}
	return ids[0], nil
}

// roleOfRow reads the stored role of a principal in the project; ProjectRoleNone when it is no member.
func roleOfRow(ctx context.Context, qs *q.Queries, projectID, principalID string) (Role, error) {
	rows, err := qs.ProjectMemberRole(ctx, q.ProjectMemberRoleParams{ProjectID: projectID, PrincipalID: principalID})
	if err != nil || len(rows) == 0 {
		return authz.ProjectRoleNone, err
	}
	r, _ := authz.ParseProjectRole(rows[0])
	return r, nil
}

// checkLastOwner refuses, unless bypass (the caller holds projects:write), a write that takes the role owner
// from a member that holds it while it is the only membership with that role (409 last_owner, Q-AUTHZ-217).
// The caller holds ProjectLock.
func checkLastOwner(ctx context.Context, qs *q.Queries, key, projectID string, before, after Role, bypass bool) error {
	if bypass || before != RoleOwner || after == RoleOwner {
		return nil
	}
	n, err := qs.ProjectOwnerCount(ctx, projectID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errs.E(errs.Conflict, errs.CodeLastOwner,
			fmt.Sprintf("This is the last Owner of the project %q; make another member an Owner first (an administrator of trove may remove it).", key))
	}
	return nil
}

func validRole(role Role) error {
	if role.Valid() {
		return nil
	}
	return errs.E(errs.Invalid, errs.CodeValidation, "The role is reader, developer, maintainer or owner.",
		errs.WithFields(errs.FieldError{Pointer: "/role", Code: "invalid_role", Detail: "not one of reader, developer, maintainer, owner"}))
}

// Members implements [Service].
func (s *service) Members(ctx context.Context, key string) ([]Member, error) {
	var out []Member
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) error {
		p, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		out = p.Members
		return nil
	})
	return out, err
}

// AddMember implements [Service]: the idempotent PUT of a membership. A principal that is no member is
// added (created), a member's role is set; the same role changes nothing, records no event and answers the
// current epoch (Q-AUTHZ-193).
func (s *service) AddMember(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypassLastOwner bool) (Member, bool, int64, error) {
	return s.putMember(ctx, actor, key, ref, role, bypassLastOwner, true)
}

// SetRole implements [Service]: like AddMember for a principal that is a member already; 404 otherwise.
func (s *service) SetRole(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypassLastOwner bool) (Member, int64, error) {
	m, _, epoch, err := s.putMember(ctx, actor, key, ref, role, bypassLastOwner, false)
	return m, epoch, err
}

func (s *service) putMember(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypass, mayAdd bool) (Member, bool, int64, error) {
	if err := validRole(role); err != nil {
		return Member{}, false, 0, err
	}
	var out Member
	var created bool
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		projectID, err := lockProject(ctx, qs, key)
		if err != nil {
			return err
		}
		pr, err := memberPrincipal(ctx, qs, ref)
		if err != nil {
			return err
		}
		out = Member{Project: key, Principal: pr, Role: role}
		before, err := roleOfRow(ctx, qs, projectID, pr.ID)
		if err != nil {
			return err
		}
		switch {
		case before == role:
			epoch, err = epochOf(ctx, qs)
			return err
		case before == authz.ProjectRoleNone && !mayAdd:
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("%s is no member of the project %q.", ref, key))
		case before == authz.ProjectRoleNone:
			created = true
			if _, err := qs.ProjectMemberAdd(ctx, q.ProjectMemberAddParams{ProjectID: projectID, PrincipalID: pr.ID, Role: role.String()}); err != nil {
				return err
			}
		default:
			if err := checkLastOwner(ctx, qs, key, projectID, before, role, bypass); err != nil {
				return err
			}
			if _, err := qs.ProjectMemberSetRole(ctx, q.ProjectMemberSetRoleParams{ProjectID: projectID, PrincipalID: pr.ID, Role: role.String()}); err != nil {
				return err
			}
		}
		if err := s.notifyAuthz(tx, "project", projectID); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			if created {
				return s.o.Recorder.RecordTx(ctx, tx, memberEvent(audit.EventProjectMemberAdded, actor, key, projectID, pr, epoch, authz.ProjectRoleNone, role))
			}
			return s.o.Recorder.RecordTx(ctx, tx, memberEvent(audit.EventProjectMemberChanged, actor, key, projectID, pr, epoch, before, role))
		})
		return nil
	})
	if err != nil {
		return Member{}, false, 0, err
	}
	return out, created, epoch, nil
}

// RemoveMember implements [Service].
func (s *service) RemoveMember(ctx context.Context, actor authz.Subject, key, ref string, bypassLastOwner bool) (int64, error) {
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		projectID, err := lockProject(ctx, qs, key)
		if err != nil {
			return err
		}
		kind, name, err := parseMemberRef(ref)
		if err != nil {
			return err
		}
		rows, err := qs.ProjectPrincipalByRef(ctx, q.ProjectPrincipalByRefParams{Kind: string(kind), Name: name})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The principal %s does not exist.", ref))
		}
		pr := PrincipalRef{Kind: kind, Name: rows[0].Name, ID: rows[0].ID}
		before, err := roleOfRow(ctx, qs, projectID, pr.ID)
		if err != nil {
			return err
		}
		if before == authz.ProjectRoleNone {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("%s is no member of the project %q.", ref, key))
		}
		if err := checkLastOwner(ctx, qs, key, projectID, before, authz.ProjectRoleNone, bypassLastOwner); err != nil {
			return err
		}
		if _, err := qs.ProjectMemberRemove(ctx, q.ProjectMemberRemoveParams{ProjectID: projectID, PrincipalID: pr.ID}); err != nil {
			return err
		}
		if err := s.notifyAuthz(tx, "project", projectID); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, memberEvent(audit.EventProjectMemberRemoved, actor, key, projectID, pr, epoch, before, authz.ProjectRoleNone))
		})
		return nil
	})
	if err != nil {
		return 0, err
	}
	return epoch, nil
}

// memberEvent builds the event of a membership write (R-AUTHZ-029). An addition records the role, a
// removal the role it had, a change the role before and after; a change and a removal carry the journal
// entry of R-AUTHZ-031: the project and the principal by ID and reference, the roles of a change.
func memberEvent(name string, actor authz.Subject, key, projectID string, pr PrincipalRef, epoch int64, before, after Role) audit.Event {
	details := map[string]any{"epoch": epoch, "project": key, "projectId": projectID, "principal": pr.String(), "principalId": pr.ID}
	journal := map[string]any{"projectId": projectID, "project": key, "principalId": pr.ID, "principal": pr.String()}
	switch name {
	case audit.EventProjectMemberAdded:
		details["role"] = after.String()
	case audit.EventProjectMemberRemoved:
		details["role"] = before.String()
	default:
		details["before"], details["after"] = before.String(), after.String()
		journal["before"], journal["after"] = before.String(), after.String()
	}
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success, Project: key,
		Subject: audit.Subject{Kind: string(pr.Kind), ID: pr.ID, Name: pr.Name},
		Details: mustJSON(details),
	}
	if name != audit.EventProjectMemberAdded {
		ev.Journal = mustJSON(journal)
	}
	return ev
}

// LookupPrincipals answers the lookup of Q-AUTHZ-182. The bounds are the service's
// own, so that no caller can widen them: a prefix of at least MinLookupPrefix
// characters, at most MaxLookup results.
func (s *service) LookupPrincipals(ctx context.Context, prefix string, kind PrincipalKind, limit int) ([]PrincipalRef, error) {
	var fields []errs.FieldError
	if utf8.RuneCountInString(prefix) < MinLookupPrefix {
		fields = append(fields, errs.FieldError{
			Pointer: "/q", Code: "prefix_too_short",
			Detail: fmt.Sprintf("The prefix has at least %d characters.", MinLookupPrefix),
		})
	}
	if kind != "" && kind != KindUser && kind != KindGroup {
		fields = append(fields, errs.FieldError{Pointer: "/kind", Code: "invalid_kind", Detail: "The kind is user or group."})
	}
	if limit < 0 {
		fields = append(fields, errs.FieldError{Pointer: "/limit", Code: "invalid_limit", Detail: fmt.Sprintf("The limit is 1 to %d.", MaxLookup)})
	}
	if len(fields) > 0 {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, fields[0].Detail, errs.WithFields(fields...))
	}
	if limit == 0 || limit > MaxLookup {
		limit = MaxLookup
	}
	var out []PrincipalRef
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) error {
		rows, err := qs.ProjectLookupPrincipals(ctx, q.ProjectLookupPrincipalsParams{Kind: string(kind), Prefix: prefix, MaxRows: int32(limit)}) //nolint:gosec // at most MaxLookup
		if err != nil {
			return err
		}
		out = make([]PrincipalRef, 0, len(rows))
		for _, r := range rows {
			out = append(out, PrincipalRef{Kind: PrincipalKind(r.Kind), Name: r.Name})
		}
		return nil
	})
	return out, err
}
