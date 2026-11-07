package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replay of the identity events of the revocation journal (R-HA-054, R-AUTH-016, R-AUTH-018, R-AUTH-020, R-AUTH-021,
// R-AUTH-039). The handlers remove or disable and nothing else:
//
//   - a deleted user or service account is disabled, not deleted, so a mistaken or forged entry stays reversible and the
//     account's tokens fail all the same (R-AUTH-016, R-AUTH-020);
//   - a deleted group is deleted, which removes its memberships and the role bindings and policy entries that named it;
//   - a removed membership is removed with its source; added members are never replayed.
//
// Every entry names its principals by ID. The break-glass account lives in the configuration, is never journaled and is never
// touched by a replay (a forged entry naming it is skipped).
func init() {
	journal.Register(journal.Handler{
		Domain: "identity",
		Events: []string{
			audit.EventUserDisabled, audit.EventUserDeleted, audit.EventSADisabled, audit.EventSADeleted,
			audit.EventGroupDeleted, audit.EventGroupMembersChanged, audit.EventUserGroupsSynced,
		},
		Apply: replayPrincipal,
	})
}

// validDisabledReason reports whether app_user.disabled_reason accepts the value.
func validDisabledReason(s string) bool { return s == "admin" || s == "dormant" || s == "scim" }

func replayPrincipal(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	switch o.Event {
	case audit.EventUserDisabled, audit.EventUserDeleted:
		return replayDisableUser(ctx, tx, env, o)
	case audit.EventSADisabled, audit.EventSADeleted:
		return replayDisableAccount(ctx, tx, env, o)
	case audit.EventGroupDeleted:
		return replayDeleteGroup(ctx, tx, env, o)
	case audit.EventGroupMembersChanged:
		return replayGroupMembers(ctx, tx, env, o)
	default:
		return replayUserGroups(ctx, tx, env, o)
	}
}

// principalOf reads the principal of an ID and checks its kind; found is false for an unknown ID and for another kind.
func principalOf(ctx context.Context, qs *q.Queries, id string, kind Kind) (row q.IdentityPrincipalByIDRow, found bool, err error) {
	rows, err := qs.IdentityPrincipalByID(ctx, id)
	if err != nil || len(rows) == 0 || rows[0].Kind != string(kind) {
		return q.IdentityPrincipalByIDRow{}, false, err
	}
	return rows[0], true, nil
}

func replayDisableUser(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		PrincipalID string `json:"principalId"`
		Reason      string `json:"disabledReason"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("principalId is not an ID"), nil
	}
	qs := Queries(tx)
	u, found, err := principalOf(ctx, qs, e.PrincipalID, KindUser)
	switch {
	case err != nil:
		return journal.Result{}, err
	case !found:
		return o.Result(journal.SubjectUnknown, e.PrincipalID), nil
	case u.IsBreakGlass:
		return o.Malformed("the break-glass account is not replayed"), nil
	case u.Status == string(StatusDisabled):
		return o.Result(journal.AlreadyInEffect, u.Name), nil
	}
	reason := e.Reason
	if !validDisabledReason(reason) {
		reason = "admin"
	}
	if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: u.ID, Status: string(StatusDisabled)}); err != nil {
		return journal.Result{}, err
	}
	if err := qs.IdentitySetUserDisabled(ctx, q.IdentitySetUserDisabledParams{ID: u.ID, Reason: reason}); err != nil {
		return journal.Result{}, err
	}
	if _, err := qs.IdentityEndSessions(ctx, q.IdentityEndSessionsParams{ID: u.ID, Reason: "principal_disabled"}); err != nil {
		return journal.Result{}, err
	}
	return o.Result(journal.Applied, u.Name), NotifyPrincipals(env, tx, ChangePrincipal, u.ID)
}

func replayDisableAccount(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		PrincipalID string `json:"principalId"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("principalId is not an ID"), nil
	}
	qs := Queries(tx)
	sa, found, err := principalOf(ctx, qs, e.PrincipalID, KindServiceAccount)
	switch {
	case err != nil:
		return journal.Result{}, err
	case !found:
		return o.Result(journal.SubjectUnknown, e.PrincipalID), nil
	case sa.Status == string(StatusDisabled):
		return o.Result(journal.AlreadyInEffect, sa.Name), nil
	}
	if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: sa.ID, Status: string(StatusDisabled)}); err != nil {
		return journal.Result{}, err
	}
	return o.Result(journal.Applied, sa.Name), NotifyPrincipals(env, tx, ChangePrincipal, sa.ID)
}

func replayDeleteGroup(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		PrincipalID string `json:"principalId"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("principalId is not an ID"), nil
	}
	qs := Queries(tx)
	g, found, err := principalOf(ctx, qs, e.PrincipalID, KindGroup)
	if err != nil {
		return journal.Result{}, err
	}
	if !found {
		return o.Result(journal.SubjectUnknown, e.PrincipalID), nil
	}
	// The policies that name the group lose its entry with the row: their versions move, as the live delete does.
	if _, err := qs.AuthzTouchPoliciesOfPrincipal(ctx, g.ID); err != nil {
		return journal.Result{}, err
	}
	if _, err := qs.IdentityDeletePrincipal(ctx, g.ID); err != nil {
		return journal.Result{}, err
	}
	if err := env.Notify(tx, cluster.AreaAuthz, 1, map[string]string{"type": "principal", "id": g.ID}); err != nil {
		return journal.Result{}, err
	}
	return o.Result(journal.Applied, g.Name), NotifyPrincipals(env, tx, ChangeGroup, g.ID)
}

// removedMembership is one removed membership of group.members.changed and user.groups.synced.
type removedMembership struct {
	GroupID  string `json:"groupId"`
	MemberID string `json:"memberId"`
	Source   string `json:"source"`
}

func replayGroupMembers(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		GroupID string `json:"groupId"`
		Removed []struct {
			MemberID string `json:"memberId"`
			Source   string `json:"source"`
		} `json:"removed"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.GroupID) || len(e.Removed) > 1000 {
		return o.Malformed("groupId is not an ID"), nil
	}
	rs := make([]removedMembership, 0, len(e.Removed))
	for _, r := range e.Removed {
		if !journal.IsUUID(r.MemberID) || r.Source == "" {
			return o.Malformed("a removed member is not an ID with a source"), nil
		}
		rs = append(rs, removedMembership{GroupID: e.GroupID, MemberID: r.MemberID, Source: r.Source})
	}
	return removeMemberships(ctx, tx, env, o, rs)
}

func replayUserGroups(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object) (journal.Result, error) {
	var e struct {
		UserID  string `json:"userId"`
		Removed []struct {
			GroupID string `json:"groupId"`
			Source  string `json:"source"`
		} `json:"removed"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.UserID) || len(e.Removed) > 1000 {
		return o.Malformed("userId is not an ID"), nil
	}
	rs := make([]removedMembership, 0, len(e.Removed))
	for _, r := range e.Removed {
		if !journal.IsUUID(r.GroupID) || r.Source == "" {
			return o.Malformed("a removed group is not an ID with a source"), nil
		}
		rs = append(rs, removedMembership{GroupID: r.GroupID, MemberID: e.UserID, Source: r.Source})
	}
	return removeMemberships(ctx, tx, env, o, rs)
}

// removeMemberships removes each membership as recorded. The outcome is applied when any row went, subject unknown when
// the restored database holds none of the groups or members, and already in effect otherwise.
func removeMemberships(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object, rs []removedMembership) (journal.Result, error) {
	qs := Queries(tx)
	var changed []string
	known := false
	for _, r := range rs {
		_, gOK, err := principalOf(ctx, qs, r.GroupID, KindGroup)
		if err != nil {
			return journal.Result{}, err
		}
		m, err := qs.IdentityPrincipalByID(ctx, r.MemberID)
		if err != nil {
			return journal.Result{}, err
		}
		if !gOK || len(m) == 0 {
			continue
		}
		known = true
		gone, err := qs.IdentityRemoveMembership(ctx, q.IdentityRemoveMembershipParams{GroupID: r.GroupID, MemberID: r.MemberID, Source: r.Source})
		if err != nil {
			return journal.Result{}, err
		}
		if len(gone) > 0 {
			changed = append(changed, r.MemberID)
		}
	}
	switch {
	case len(changed) > 0:
		return o.Result(journal.Applied, ""), NotifyPrincipals(env, tx, ChangeMembership, changed...)
	case known || len(rs) == 0:
		return o.Result(journal.AlreadyInEffect, ""), nil
	}
	return o.Result(journal.SubjectUnknown, ""), nil
}
