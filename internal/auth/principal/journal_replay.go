package principal

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replay of the identity events of the revocation journal (R-HA-054, R-AUTH-016, R-AUTH-018, R-AUTH-019, R-AUTH-020,
// R-AUTH-021, R-AUTH-039, R-BAS-004). The handlers remove or disable and nothing else:
//
//   - a deleted user or service account is disabled, not deleted, so a mistaken or forged entry stays reversible and the
//     account's tokens fail all the same (R-AUTH-016, R-AUTH-020);
//   - a disabled or deleted Bastion registration is disabled, which refuses its tokens (R-BAS-004, Q-BAS-145);
//   - a deleted group is deleted, which removes its memberships and the role bindings and policy entries that named it;
//   - a removed membership is removed with its source; added members are never replayed;
//   - a group update clears the auto-join flag when the entry cleared it, and never sets it (R-AUTH-019, Q-AUTH-198).
//
// Every entry names its principals by ID. The break-glass account lives in the configuration, is never journaled and is never
// touched by a replay (a forged entry naming it is skipped).
func init() {
	journal.Register(journal.Handler{
		Domain: "identity",
		Events: []string{
			audit.EventUserDisabled, audit.EventUserDeleted, audit.EventSADisabled, audit.EventSADeleted,
			audit.EventBastionDisabled, audit.EventBastionDeleted,
			audit.EventGroupDeleted, audit.EventGroupMembersChanged, audit.EventUserGroupsSynced, audit.EventGroupUpdated,
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
		return replayDisableAccount(ctx, tx, env, o, KindServiceAccount)
	case audit.EventBastionDisabled, audit.EventBastionDeleted:
		return replayDisableAccount(ctx, tx, env, o, KindBastion)
	case audit.EventGroupUpdated:
		return replayGroupUpdated(ctx, tx, o)
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

// replayDisableAccount disables a service account or a Bastion registration, the two kinds whose disable is the status
// alone: the status refuses every token of the principal (R-AUTH-020, R-BAS-004). An entry naming a principal of another
// kind is "subject unknown", so a bastion.* entry cannot disable a user and the reverse.
func replayDisableAccount(ctx context.Context, tx meta.Tx, env journal.Env, o journal.Object, kind Kind) (journal.Result, error) {
	var e struct {
		PrincipalID string `json:"principalId"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PrincipalID) {
		return o.Malformed("principalId is not an ID"), nil
	}
	qs := Queries(tx)
	sa, found, err := principalOf(ctx, qs, e.PrincipalID, kind)
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
		Name        string `json:"name"`
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
		// A group created and deleted after the restore point is not in the database, and a configuration that declares a
		// role binding for its name would create it again with the role. The roles it held are unknown, so the name is
		// marked for every system role (Q-AUTHZ-236).
		if e.Name != "" {
			for _, role := range systemRoles() {
				if err := qs.JournalSuppressBinding(ctx, role+"|group:"+e.Name); err != nil {
					return journal.Result{}, err
				}
			}
		}
		return o.Result(journal.SubjectUnknown, e.PrincipalID), nil
	}
	// The roles the group holds go with it (the binding rows cascade), and trove migrate must not bind them to the group it
	// would create again under a new ID: mark each by name (Q-AUTHZ-236).
	roles, err := qs.IdentityGroupRoles(ctx, g.ID)
	if err != nil {
		return journal.Result{}, err
	}
	for _, role := range roles {
		if err := qs.JournalSuppressBinding(ctx, role+"|group:"+g.Name); err != nil {
			return journal.Result{}, err
		}
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

// systemRoles are the roles a binding can hold.
func systemRoles() []string {
	return []string{"admin", "security-admin", "auditor", "repository-admin", "webhook-admin"}
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

// boolChange is a flag before and after a change, as a journal entry carries it.
type boolChange struct {
	Before bool `json:"before"`
	After  bool `json:"after"`
}

// groupUpdatedEntry is the journal entry of group.updated (R-AUTH-041, Q-AUTH-198): the group by ID, its name for display,
// and the auto-join flag before and after the update, on every update whether it changed the flag or not.
type groupUpdatedEntry struct {
	PrincipalID string     `json:"principalId"`
	Name        string     `json:"name"`
	AutoJoin    boolChange `json:"autoJoin"`
}

// readGroupUpdated reads a group.updated entry strictly: the ID and both values of the flag must be present.
func readGroupUpdated(o journal.Object) (groupUpdatedEntry, bool) {
	var e struct {
		PrincipalID string `json:"principalId"`
		Name        string `json:"name"`
		AutoJoin    *struct {
			Before *bool `json:"before"`
			After  *bool `json:"after"`
		} `json:"autoJoin"`
	}
	if !o.Into(&e) || !journal.IsUUID(e.PrincipalID) || e.AutoJoin == nil || e.AutoJoin.Before == nil || e.AutoJoin.After == nil {
		return groupUpdatedEntry{}, false
	}
	return groupUpdatedEntry{PrincipalID: e.PrincipalID, Name: e.Name, AutoJoin: boolChange{Before: *e.AutoJoin.Before, After: *e.AutoJoin.After}}, true
}

// autoJoinOrder is the comparator of the auto-join flag (R-AUTH-019): off gives the users created afterwards nothing that on
// does not, so a value narrows when it is off or equal to the current one.
type autoJoinOrder struct{}

// Narrows reports whether after grants nothing that current does not.
func (autoJoinOrder) Narrows(current, after bool) bool { return !after || current }

// replayGroupUpdated is the update rule of R-HA-054 on the auto-join flag: a flag the entry cleared is cleared (applied), one
// it set is never set (skipped, case 2). With two values the rule cannot reach case 3: the only change that is neither a
// narrowing nor contained in its before-state is an equal one, which is already in effect. The name and the description of
// a group are not access-bearing and are not replayed; replay never changes an identifying field.
func replayGroupUpdated(ctx context.Context, tx meta.Tx, o journal.Object) (journal.Result, error) {
	e, ok := readGroupUpdated(o)
	if !ok {
		return o.Malformed("principalId or autoJoin is missing"), nil
	}
	qs := Queries(tx)
	g, found, err := principalOf(ctx, qs, e.PrincipalID, KindGroup)
	if err != nil {
		return journal.Result{}, err
	}
	if !found {
		return o.Result(journal.SubjectUnknown, e.PrincipalID), nil
	}
	locked, err := qs.IdentityLockGroup(ctx, g.ID)
	if err != nil {
		return journal.Result{}, err
	}
	if len(locked) == 0 {
		return o.Result(journal.SubjectUnknown, g.Name), nil
	}
	current := locked[0].AutoJoin
	if current == e.AutoJoin.After {
		return o.Result(journal.AlreadyInEffect, g.Name), nil
	}
	// The flag is read when a user is created (applyAutoJoin); no replica caches it, so no change area is told.
	clearFlag := func() error {
		return qs.IdentityUpdateGroup(ctx, q.IdentityUpdateGroupParams{ID: g.ID, SetAutoJoin: true, AutoJoin: false})
	}
	act := journal.UpdateActions{
		Apply: clearFlag,
		Suspend: func() (json.RawMessage, error) {
			return json.RawMessage(`{"autoJoin":false}`), clearFlag()
		},
	}
	out, detail, err := journal.ApplyUpdate[bool](autoJoinOrder{}, current, e.AutoJoin.Before, e.AutoJoin.After, act)
	r := o.Result(out, g.Name)
	r.Detail = detail
	return r, err
}
