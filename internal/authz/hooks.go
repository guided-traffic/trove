package authz

import (
	"context"
	"fmt"
	"strconv"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// PrincipalHook implements [principal.AuthzHook]: a deleted or renamed principal
// changes inputs of the grant index (its entries and role bindings go with the
// row; a policy's representation shows the name), so the identity manager calls
// it in the transaction of the write and the hook makes the change what
// R-AUTHZ-028 requires of every write to those inputs: the epoch of the area
// moves, and so does the version of every policy whose entries name the
// principal, which is the ETag of its representation.
type PrincipalHook struct{ changes Changes }

var _ principal.AuthzHook = (*PrincipalHook)(nil)

// NewPrincipalHook creates the hook over the change log.
func NewPrincipalHook(changes Changes) *PrincipalHook { return &PrincipalHook{changes: changes} }

// OnPrincipalChanged implements [principal.AuthzHook]. It must run before the
// delete of the principal row, while its entries still exist. A principal that
// no policy and no role binding names changes no grant, so it moves nothing.
func (h *PrincipalHook) OnPrincipalChanged(ctx context.Context, tx meta.Tx, principalID string) error {
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	touched, err := qs.AuthzTouchPoliciesOfPrincipal(ctx, principalID)
	if err != nil {
		return err
	}
	bound, err := qs.AuthzPrincipalHasBinding(ctx, principalID)
	if err != nil {
		return err
	}
	if len(touched) == 0 && !bound {
		return nil
	}
	return h.changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "principal", ID: principalID})
}

// PolicyHook implements [repository.Hooks]: the deletion of a repository removes
// the key selectors on it with the row (foreign key), which changes the grant
// index and the representation of the policies that named it, so the hook moves
// the epoch of the area and the version of those policies in the transaction of
// the deletion (R-AUTHZ-028). An update changes no selector.
type PolicyHook struct{ changes Changes }

var _ repository.Hooks = (*PolicyHook)(nil)

// NewPolicyHook creates the hook over the change log.
func NewPolicyHook(changes Changes) *PolicyHook { return &PolicyHook{changes: changes} }

// OnUpdated implements [repository.Hooks].
func (h *PolicyHook) OnUpdated(context.Context, meta.Tx, repository.Caller, repository.Repository, repository.Repository) error {
	return nil
}

// OnDelete implements [repository.Hooks].
func (h *PolicyHook) OnDelete(ctx context.Context, tx meta.Tx, _ repository.Caller, r repository.Repository) error {
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("authz: the repository id %q is no number: %w", r.ID, err)
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return err
	}
	touched, err := qs.AuthzTouchPoliciesOfRepository(ctx, id)
	if err != nil {
		return err
	}
	if len(touched) == 0 {
		return nil
	}
	return h.changes.Notify(tx, AreaAuthz, payloadVersion, changePayload{Type: "repository", ID: r.ID})
}
