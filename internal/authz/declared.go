package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Role bindings declared in the system configuration (R-AUTHZ-010, Q-AUTHZ-170,
// Q-AUTHZ-171). trove migrate applies authz.role_bindings in one transaction; no
// web or worker replica writes them. The API lists them with their source and
// refuses to remove them (bindings.go).

// The sources of a role binding, the column authz_role_binding.source.
const (
	// SourceAPI is a binding made through the management API or an
	// administration command.
	SourceAPI = "api"
	// SourceConfiguration is a binding authz.role_bindings declares.
	SourceConfiguration = "configuration"
)

// ConfigurationActor is the actor of every write that applies
// authz.role_bindings: system:configuration.
func ConfigurationActor() audit.Actor { return audit.Actor{Kind: "system", Name: "configuration"} }

// declaredGroupDescription is the description of a group trove migrate creates
// for a declaration, shown on the group's page.
const declaredGroupDescription = "Created for authz.role_bindings of the system configuration."

// GroupCreator creates a declared group that does not exist as a local group, in
// the caller's transaction (R-AUTH-018). *principal.Manager implements it.
type GroupCreator interface {
	CreateGroupTx(ctx context.Context, tx meta.Tx, c principal.Caller, in principal.NewGroup) (principal.Group, error)
}

// DeclaredOutcome is what one application changed, as role/reference pairs.
type DeclaredOutcome struct {
	// Bound are the declared bindings that did not exist.
	Bound []string
	// Adopted are bindings the API had made that the configuration keeps from now on.
	Adopted []string
	// Unbound are the configuration bindings no longer declared.
	Unbound []string
	// GroupsCreated are the declared groups that did not exist.
	GroupsCreated []string
}

// Changed reports whether the application wrote anything.
func (o DeclaredOutcome) Changed() bool {
	return len(o.Bound)+len(o.Adopted)+len(o.Unbound)+len(o.GroupsCreated) > 0
}

// ErrDeclared marks a declaration trove migrate cannot apply: a user or service
// account that does not exist, an auto-join group, or the removal of the last
// administrator. The message names every such entry.
var ErrDeclared = errors.New("authz.role_bindings cannot be applied")

// declaredBinding is one declaration resolved to its principal.
type declaredBinding struct {
	role Role
	ref  Ref
	id   string
}

func (d declaredBinding) String() string { return string(d.role) + "/" + d.ref.String() }

// declaredChange is one write of an application, audited once the epoch of the
// transaction is known.
type declaredChange struct {
	event   string
	binding declaredBinding
	adopted bool
}

// ApplyDeclared makes the bindings with source configuration exactly the
// declared ones, in one transaction (R-AUTHZ-010, Q-AUTHZ-170):
//
//   - a declared group that does not exist is created as a local group, so the
//     binding holds before the first login of a member; the login provider's
//     group sync then writes its memberships into it (R-AUTH-008);
//   - a declared user or service account that does not exist, and a declared
//     group that is an auto-join group (R-AUTHZ-022), stop the application with
//     an error that names each of them;
//   - a declared binding that does not exist is inserted with source
//     configuration, one the API made is taken over (Q-AUTHZ-171);
//   - a configuration binding no longer declared is removed, under the last_admin
//     guard of R-AUTHZ-022 (Q-AUTHZ-171).
//
// Every change notifies the area authz and is audited with the actor
// system:configuration in the same transaction, as an API bind or unbind is
// (R-AUTHZ-028, R-AUTHZ-029), so running replicas apply it within the bound of
// R-AUTHZ-028. The declarations are expected to have passed
// config.Authz.Validate; one that did not is refused the same way.
func (s *Service) ApplyDeclared(ctx context.Context, declared []config.AuthzRoleBinding, groups GroupCreator) (DeclaredOutcome, error) {
	var out DeclaredOutcome
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = DeclaredOutcome{}
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		want, err := resolveDeclared(ctx, tx, qs, declared, groups, &out)
		if err != nil {
			return err
		}
		current, err := qs.AuthzDeclaredBindings(ctx)
		if err != nil {
			return err
		}
		var changes []declaredChange
		for _, d := range want {
			c, err := s.declare(ctx, tx, qs, d, &out)
			if err != nil {
				return err
			}
			if c != nil {
				changes = append(changes, *c)
			}
		}
		removed, err := s.removeUndeclared(ctx, tx, qs, current, want, &out)
		if err != nil {
			return err
		}
		changes = append(changes, removed...)
		if len(changes) == 0 {
			return nil
		}
		var epoch int64
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			for _, c := range changes {
				if err := s.o.Recorder.RecordTx(ctx, tx, c.auditEvent(epoch)); err != nil {
					return err
				}
			}
			return nil
		})
		return nil
	})
	return out, err
}

// resolveDeclared finds the principal of every declaration and creates the
// missing groups. It collects every problem before it fails, so one run of
// trove migrate names all of them.
func resolveDeclared(ctx context.Context, tx meta.Tx, qs *q.Queries, declared []config.AuthzRoleBinding, groups GroupCreator, out *DeclaredOutcome) ([]declaredBinding, error) {
	var want []declaredBinding
	var problems []string
	for i, b := range declared {
		key := fmt.Sprintf("authz.role_bindings[%d]", i)
		d, problem, err := resolveOne(ctx, tx, qs, b, groups, out)
		switch {
		case err != nil:
			return nil, fmt.Errorf("%s: %w", key, err)
		case problem != "":
			problems = append(problems, key+": "+problem)
		default:
			want = append(want, d)
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrDeclared, strings.Join(problems, "; "))
	}
	return want, nil
}

// resolveOne resolves one declaration. A problem is a refusal to report with the
// others; an error ends the application at once.
func resolveOne(ctx context.Context, tx meta.Tx, qs *q.Queries, b config.AuthzRoleBinding, groups GroupCreator, out *DeclaredOutcome) (declaredBinding, string, error) {
	kind, name, ok := b.Principal()
	role := Role(b.Role)
	switch {
	case !role.Valid():
		return declaredBinding{}, fmt.Sprintf("%q is not a system role (unknown_role)", b.Role), nil
	case !ok:
		return declaredBinding{}, "names no principal or more than one", nil
	case name == dbAnonymous || name == dbAuthenticated:
		return declaredBinding{}, name + " is a built-in principal and holds no system role (role_not_allowed)", nil
	}
	d := declaredBinding{role: role, ref: Ref{Kind: principal.Kind(kind), Name: name}}
	rows, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: kind, Name: name})
	if err != nil {
		return d, "", err
	}
	switch {
	case len(rows) == 1:
		d.id = rows[0].ID
	case kind != config.DeclaredGroup:
		return d, d.ref.String() + " does not exist; create it, or remove the entry", nil
	case groups == nil:
		return d, "", errors.New("no group creator for a declared group")
	default:
		g, err := groups.CreateGroupTx(ctx, tx, principal.Caller{Actor: ConfigurationActor()},
			principal.NewGroup{Name: name, Description: declaredGroupDescription})
		if err != nil {
			return d, "", fmt.Errorf("creating group %s: %w", name, err)
		}
		d.id = g.ID
		out.GroupsCreated = append(out.GroupsCreated, name)
	}
	if kind != config.DeclaredGroup {
		return d, "", nil
	}
	// The lock an API bind and turning on auto-join take too (R-AUTH-019).
	if _, err := qs.IdentityLockGroup(ctx, d.id); err != nil {
		return d, "", err
	}
	auto, err := qs.AuthzGroupAutoJoin(ctx, d.id)
	if err != nil {
		return d, "", err
	}
	if len(auto) == 1 && auto[0] {
		return d, d.ref.String() + " is an auto-join group and takes no system role (auto_join_admin_conflict)", nil
	}
	return d, "", nil
}

// declare writes one declared binding: an insert, the take-over of a binding
// the API made, or nothing when the configuration keeps it already. It returns
// the change to audit, nil for none.
func (s *Service) declare(ctx context.Context, tx meta.Tx, qs *q.Queries, d declaredBinding, out *DeclaredOutcome) (*declaredChange, error) {
	n, err := qs.AuthzInsertDeclaredBinding(ctx, q.AuthzInsertDeclaredBindingParams{Role: string(d.role), PrincipalID: d.id})
	if err != nil {
		return nil, err
	}
	c := &declaredChange{event: EventRoleBound, binding: d}
	if n == 1 {
		out.Bound = append(out.Bound, d.String())
	} else {
		m, err := qs.AuthzAdoptBinding(ctx, q.AuthzAdoptBindingParams{Role: string(d.role), PrincipalID: d.id})
		if err != nil || m == 0 {
			return nil, err
		}
		c.adopted = true
		out.Adopted = append(out.Adopted, d.String())
	}
	// A take-over leaves the rights as they are; it notifies anyway, because every
	// write to the inputs of the grant index does (R-AUTHZ-028).
	if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "role_binding", ID: d.id}); err != nil {
		return nil, err
	}
	return c, nil
}

// removeUndeclared removes the configuration bindings that are no longer
// declared. The last_admin guard of R-AUTHZ-022 counts after every write of the
// application, the new bindings included.
func (s *Service) removeUndeclared(ctx context.Context, tx meta.Tx, qs *q.Queries, current []q.AuthzDeclaredBindingsRow, want []declaredBinding, out *DeclaredOutcome) ([]declaredChange, error) {
	keep := map[string]bool{}
	for _, d := range want {
		keep[string(d.role)+"|"+d.id] = true
	}
	var guard principal.LastAdminGuard
	var changes []declaredChange
	var admins []string
	for _, c := range current {
		if keep[c.Role+"|"+c.PrincipalID] {
			continue
		}
		ref, err := ParseRef(refOfRow(c.PrincipalKind, c.PrincipalName))
		if err != nil {
			return nil, err
		}
		d := declaredBinding{role: Role(c.Role), ref: ref, id: c.PrincipalID}
		if d.role == RoleAdmin {
			if err := guard.Arm(ctx, qs, d.id); err != nil {
				return nil, err
			}
			admins = append(admins, d.String())
		}
		if _, err := qs.AuthzDeleteBinding(ctx, q.AuthzDeleteBindingParams{Role: c.Role, PrincipalID: c.PrincipalID}); err != nil {
			return nil, err
		}
		if err := s.o.Changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "role_binding", ID: d.id}); err != nil {
			return nil, err
		}
		out.Unbound = append(out.Unbound, d.String())
		changes = append(changes, declaredChange{event: EventRoleUnbound, binding: d})
	}
	if err := guard.Check(ctx, qs); err != nil {
		if e, ok := errs.As(err); ok && e.Code == errs.CodeLastAdmin {
			return nil, fmt.Errorf("%w: removing %s would leave no active user or service account holding admin while the break-glass administrator is disabled (last_admin); declare the new administrator and let a member sign in first, or enable auth.break_glass",
				ErrDeclared, strings.Join(admins, ", "))
		}
		return nil, err
	}
	return changes, nil
}

// auditEvent is the event of the change: the event of an API bind or unbind,
// with the actor system:configuration and the source in its details; an unbind
// carries the journal entry of R-AUTHZ-031.
func (c declaredChange) auditEvent(epoch int64) audit.Event {
	d := c.binding
	ev := bindingEvent(c.event, Subject{}, d.role, d.ref, d.id, epoch, c.event == EventRoleUnbound)
	ev.Actor = ConfigurationActor()
	details := map[string]any{
		"role": string(d.role), "principal": d.ref.String(), "principalId": d.id, "epoch": epoch, "source": SourceConfiguration,
	}
	if c.adopted {
		details["previousSource"] = SourceAPI
	}
	ev.Details = mustJSON(details)
	return ev
}
