package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The two guards that keep the identity operations from reaching the role
// bindings, which belong to the authorization area (R-AUTHZ-011, R-AUTHZ-022).

// guardRoleHolder refuses a change of principal id when it holds a system role
// (a user or service account through a binding of its own or of a group, a
// group through its own binding) and the caller may not change role bindings
// (Q-AUTHZ-69). Without it identities:write would reach every capability: a
// membership in an administrator group, an identity link on an administrator
// that the next login claims, a rename that lets the identity provider's group
// sync fill an administrator group. A caller without the check (the admin
// command) passes.
func guardRoleHolder(ctx context.Context, qs *q.Queries, c Caller, id string) error {
	if c.RoleHolders == nil {
		return nil
	}
	held, err := qs.IdentityHoldsRole(ctx, q.IdentityHoldsRoleParams{ID: id})
	if err != nil || !held {
		return err
	}
	return c.RoleHolders(ctx)
}

// LastAdminGuard is the last_admin rule of R-AUTHZ-022 inside one transaction
// that may take the role admin from a principal: unbinding it, disabling or
// deleting its holder, deleting its group, removing a member from it. Arm runs
// before the change: when one of the principals holds the role it locks the
// admin bindings, so such changes run one after the other. Check runs after the
// change and refuses with 409 last_admin when no active user or service account
// holds the role any more and the break-glass administrator is disabled. The
// guard counts who can act as an administrator, not the bindings: a binding on
// an empty group or a disabled user keeps nobody in charge.
type LastAdminGuard struct{ armed bool }

// Arm checks the principals the change touches and, when one holds the role
// admin, takes the lock.
func (g *LastAdminGuard) Arm(ctx context.Context, qs *q.Queries, ids ...string) error {
	if g.armed {
		return nil
	}
	for _, id := range ids {
		held, err := qs.IdentityHoldsRole(ctx, q.IdentityHoldsRoleParams{Role: "admin", ID: id})
		if err != nil {
			return err
		}
		if held {
			g.armed = true
			return qs.IdentityLockAdminBindings(ctx)
		}
	}
	return nil
}

// Check counts the remaining holders, in a statement after the lock.
func (g *LastAdminGuard) Check(ctx context.Context, qs *q.Queries) error {
	if !g.armed {
		return nil
	}
	left, err := qs.IdentityAdminHolders(ctx)
	if err != nil {
		return err
	}
	if left.Holders == 0 && !left.BreakGlassActive {
		return errs.E(errs.Conflict, errs.CodeLastAdmin,
			"The change would leave no administrator who can sign in, and the break-glass administrator is disabled.")
	}
	return nil
}
