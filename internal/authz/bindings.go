package authz

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// codes of the identity area that the role-binding rules answer with; the
// identity area registers them.
const (
	codeAutoJoinAdminConflict = "auto_join_admin_conflict"
	codeLastAdmin             = "last_admin"
)

func roleProblem(pointer, code, detail string) error {
	return problemError([]ProblemField{field(pointer, code, detail)})
}

// bindable resolves the principal of a role binding and applies the rules of
// R-AUTHZ-010: a role is bound to users, groups and service accounts only.
func (s *Service) bindable(ctx context.Context, role Role, ref string) (Ref, string, error) {
	if !role.Valid() {
		return Ref{}, "", roleProblem("/role", errs.CodeUnknownRole, fmt.Sprintf("%q is not a system role.", role))
	}
	r, err := ParseRef(ref)
	if err != nil {
		return Ref{}, "", roleProblem("/principal", errs.CodeUnknownPrincipal, err.Error())
	}
	switch r.Kind {
	case principal.KindAnonymous, principal.KindAuthenticated, principal.KindCI:
		return Ref{}, "", roleProblem("/principal", errs.CodeRoleNotAllowed,
			"System roles are bound to users, groups and service accounts only.")
	}
	id, err := s.principalID(ctx, r)
	if err != nil {
		if errors.Is(err, principal.ErrNotFound) {
			return Ref{}, "", roleProblem("/principal", errs.CodeUnknownPrincipal, fmt.Sprintf("No principal %s exists.", ref))
		}
		return Ref{}, "", err
	}
	return r, id, nil
}

// BindRole binds a role to a principal (R-AUTHZ-022). It is idempotent: the
// result says whether the binding is new (201) or existed (200). Binding a role
// to an auto-join group is refused with 409 auto_join_admin_conflict. The second
// result is the epoch to return in Trove-Authz-Epoch: the epoch the write
// produced, or the current one when nothing changed.
func (s *Service) BindRole(ctx context.Context, actor Subject, role Role, ref string) (created bool, epoch int64, err error) {
	r, id, err := s.bindable(ctx, role, ref)
	if err != nil {
		return false, 0, err
	}
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		if r.Kind == principal.KindGroup {
			auto, err := qs.AuthzGroupAutoJoin(ctx, id)
			if err != nil {
				return err
			}
			if len(auto) == 1 && auto[0] {
				return errs.E(errs.Conflict, codeAutoJoinAdminConflict,
					fmt.Sprintf("The group %s is an auto-join group and takes no system role.", r.Name))
			}
		}
		n, err := qs.AuthzInsertBinding(ctx, q.AuthzInsertBindingParams{Role: string(role), PrincipalID: id, Actor: actorID(actor)})
		if err != nil {
			return err
		}
		created = n == 1
		if !created {
			return s.currentEpoch(ctx, qs, &epoch)
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "role_binding", ID: id}); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, bindingEvent(EventRoleBound, actor, role, r, id, epoch, false))
		})
		return nil
	})
	return created, epoch, err
}

// BindAdmin binds the system role admin to a principal inside the transaction
// that creates it (R-AUTH-015 `service-account create --admin`). It writes the
// binding, notifies the area authz and records role.bound in tx, exactly as
// [Service.BindRole] does (R-AUTHZ-028); a binding that exists is left as it is.
// grantedBy is the creating principal's ID, empty for the admin command.
// *Service implements principal.AdminBinder.
func (s *Service) BindAdmin(ctx context.Context, tx meta.Tx, principalID, grantedBy string) error {
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	rows, err := qs.IdentityPrincipalByID(ctx, principalID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return roleProblem("/principal", errs.CodeUnknownPrincipal, "The principal does not exist.")
	}
	ref, err := ParseRef(refOfRow(rows[0].Kind, rows[0].Name))
	if err != nil {
		return err
	}
	n, err := qs.AuthzInsertBinding(ctx, q.AuthzInsertBindingParams{Role: string(RoleAdmin), PrincipalID: principalID, Actor: grantedBy})
	if err != nil || n == 0 {
		return err
	}
	if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "role_binding", ID: principalID}); err != nil {
		return err
	}
	var epoch int64
	afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
		ev := bindingEvent(EventRoleBound, Subject{ID: grantedBy}, RoleAdmin, ref, principalID, epoch, false)
		if grantedBy == "" {
			ev.Actor = audit.Actor{Kind: "admin-cli"}
		}
		return s.o.Recorder.RecordTx(ctx, tx, ev)
	})
	return nil
}

// UnbindRole removes a role binding. It answers 404 for a binding that does not
// exist and 409 last_admin when it would remove the last admin binding while the
// break-glass administrator is disabled (R-AUTHZ-022).
func (s *Service) UnbindRole(ctx context.Context, actor Subject, role Role, ref string) (int64, error) {
	r, id, err := s.bindable(ctx, role, ref)
	if err != nil {
		return 0, err
	}
	var epoch int64
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		n, err := qs.AuthzDeleteBinding(ctx, q.AuthzDeleteBindingParams{Role: string(role), PrincipalID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, fmt.Sprintf("The role %s is not bound to %s.", role, ref))
		}
		if role == RoleAdmin {
			left, err := qs.AuthzAdminBindings(ctx)
			if err != nil {
				return err
			}
			if left.AdminBindings == 0 && !left.BreakGlassActive {
				return errs.E(errs.Conflict, codeLastAdmin,
					"This is the last admin binding and the break-glass administrator is disabled.")
			}
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "role_binding", ID: id}); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			return s.o.Recorder.RecordTx(ctx, tx, bindingEvent(EventRoleUnbound, actor, role, r, id, epoch, true))
		})
		return nil
	})
	return epoch, err
}

func (s *Service) currentEpoch(ctx context.Context, qs *q.Queries, out *int64) error {
	rows, err := qs.ListClusterEpochs(ctx, []string{AreaAuthz})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return errNoEpochRow
	}
	*out = rows[0].Epoch
	return nil
}

// ListBindings lists the role bindings, optionally of one role and one
// principal (R-AUTHZ-022).
func (s *Service) ListBindings(ctx context.Context, role Role, ref string) ([]Binding, error) {
	var pid string
	if ref != "" {
		r, err := ParseRef(ref)
		if err != nil {
			return nil, roleProblem("/principal", errs.CodeUnknownPrincipal, err.Error())
		}
		if pid, err = s.principalID(ctx, r); err != nil {
			if errors.Is(err, principal.ErrNotFound) {
				return []Binding{}, nil
			}
			return nil, err
		}
	}
	if role != "" && !role.Valid() {
		return nil, roleProblem("/role", errs.CodeUnknownRole, fmt.Sprintf("%q is not a system role.", role))
	}
	var out []Binding
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err := qs.AuthzListBindings(ctx, q.AuthzListBindingsParams{Role: string(role), PrincipalID: pid})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, Binding{
				Role: Role(r.Role), Principal: refOfRow(r.PrincipalKind, r.PrincipalName),
				GrantedAt: time.UnixMicro(r.GrantedUs).UTC(), GrantedBy: refOfRow(r.GrantedByKind, r.GrantedByName),
			})
		}
		return nil
	})
	if out == nil {
		out = []Binding{}
	}
	return out, err
}

func bindingEvent(name string, actor Subject, role Role, ref Ref, id string, epoch int64, journal bool) audit.Event {
	ev := audit.Event{
		Type: name, Actor: actorOf(actor), Outcome: audit.Success,
		Subject: audit.Subject{Kind: "role_binding", ID: id, Name: string(role) + "/" + ref.String()},
		Details: mustJSON(map[string]any{"role": string(role), "principal": ref.String(), "principalId": id, "epoch": epoch}),
	}
	if journal {
		ev.Journal = mustJSON(map[string]any{"role": string(role), "principalId": id, "principal": ref.String()})
	}
	return ev
}
