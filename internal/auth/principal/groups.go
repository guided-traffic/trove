package principal

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Group is the group record of R-AUTH-018. The role bindings of the group
// ("roles") are the authorization area's to add.
type Group struct {
	ID          string
	Name        string
	Description string
	AutoJoin    bool
	// Origin is admin, oidc or scim.
	Origin      string
	MemberCount int
	CreatedAt   time.Time
}

// NewGroup is the request of CreateGroup.
type NewGroup struct {
	Name        string
	Description string
	AutoJoin    bool
	// Members are usernames and "sa:<name>" references; all are added with
	// source manual.
	Members []string
	// Origin is admin (default), oidc or scim.
	Origin string
}

type memberChange struct {
	id     string
	name   string
	kind   Kind
	add    bool
	source string
}

// MemberResult is one member after a PatchMembers call: its remaining sources
// show why a removed member is still in the group (R-AUTH-018).
type MemberResult struct {
	Member  string
	Kind    Kind
	Sources []string
}

// CreateGroup creates a group with origin admin (R-AUTH-018). An unknown member
// answers 404 and nothing is created.
func (m *Manager) CreateGroup(ctx context.Context, c Caller, in NewGroup) (Group, error) {
	if !ValidGroupName(in.Name) {
		return Group{}, errs.E(errs.Invalid, errs.CodeValidation, "The group name does not match ^[a-z0-9][a-z0-9._-]{0,127}$.")
	}
	origin := in.Origin
	if origin == "" {
		origin = "admin"
	}
	id := NewID()
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindGroup), Name: in.Name, CreatedBy: c.Actor.ID})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.Conflict, errs.CodeConflict, "A group with this name exists.")
		}
		if err := qs.IdentityInsertGroup(ctx, q.IdentityInsertGroupParams{ID: id, Description: in.Description, AutoJoin: in.AutoJoin, Origin: origin}); err != nil {
			return err
		}
		var changes []memberChange
		for _, ref := range in.Members {
			mid, kind, err := memberRef(ctx, qs, ref)
			if err != nil {
				return err
			}
			changes = append(changes, memberChange{id: mid, name: ref, kind: kind, add: true, source: "manual"})
		}
		if err := m.changeMembers(ctx, tx, qs, c, groupInfo{id: id, name: in.Name}, changes); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventGroupCreated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "group", ID: id, Name: in.Name},
			Details: details(map[string]any{"origin": origin, "autoJoin": in.AutoJoin, "members": in.Members}),
		})
	})
	if err != nil {
		return Group{}, DBErr("principal.CreateGroup", err)
	}
	return m.GetGroup(ctx, in.Name)
}

func groupFrom(id, name, desc string, autoJoin bool, origin string, created time.Time, members int64) Group {
	return Group{ID: id, Name: name, Description: desc, AutoJoin: autoJoin, Origin: origin, CreatedAt: created, MemberCount: int(members)}
}

// GetGroup returns the group, or a 404.
func (m *Manager) GetGroup(ctx context.Context, name string) (Group, error) {
	var g Group
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityGetGroup(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNotFound
		}
		r := rows[0]
		g = groupFrom(r.ID, r.Name, r.Description, r.AutoJoin, r.Origin, r.CreatedAt.Time, r.MemberCount)
		return nil
	})
	if err == ErrNotFound { //nolint:errorlint // sentinel returned unwrapped above
		return Group{}, NotFound("The group")
	}
	return g, DBErr("principal.GetGroup", err)
}

// GroupFilter filters ListGroups.
type GroupFilter struct {
	Query  string
	Origin string
	After  string
	Limit  int
}

// ListGroups lists groups ordered by name, with member counts.
func (m *Manager) ListGroups(ctx context.Context, f GroupFilter) ([]Group, error) {
	var out []Group
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityListGroups(ctx, q.IdentityListGroupsParams{
			After: f.After, Query: f.Query, Origin: f.Origin, MaxRows: int32(clampLimit(f.Limit)), //nolint:gosec // bounded
		})
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range rows {
			out = append(out, groupFrom(r.ID, r.Name, r.Description, r.AutoJoin, r.Origin, r.CreatedAt.Time, r.MemberCount))
		}
		return nil
	})
	return out, DBErr("principal.ListGroups", err)
}

func managedBySCIM() error {
	return errs.E(errs.Conflict, errs.CodeManagedBySCIM, "The group is owned by the identity provider and cannot be changed here.")
}

// GroupPatch is the request of UpdateGroup; a nil field is unchanged.
type GroupPatch struct {
	Name        *string
	Description *string
	AutoJoin    *bool
}

// UpdateGroup renames a group or changes its description or auto-join flag
// (R-AUTH-018, R-AUTH-019). A group with origin scim answers 409
// managed_by_scim; auto-join on a group with an administration role binding
// answers 409 auto_join_admin_conflict.
func (m *Manager) UpdateGroup(ctx context.Context, c Caller, name string, p GroupPatch) (Group, error) {
	final := name
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetGroup(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The group")
		}
		g := rows[0]
		if g.Origin == "scim" && (p.Name != nil || p.Description != nil) {
			return managedBySCIM()
		}
		var fields []string
		if p.AutoJoin != nil && *p.AutoJoin {
			admin, err := qs.IdentityGroupHasAdminRole(ctx, g.ID)
			if err != nil {
				return err
			}
			if admin {
				return errs.E(errs.Conflict, errs.CodeAutoJoinAdminConflict, "A group with an administration role binding cannot be an auto-join group.")
			}
		}
		if p.Description != nil || p.AutoJoin != nil {
			arg := q.IdentityUpdateGroupParams{ID: g.ID}
			if p.Description != nil {
				arg.SetDescription, arg.Description = true, *p.Description
				fields = append(fields, "description")
			}
			if p.AutoJoin != nil {
				arg.SetAutoJoin, arg.AutoJoin = true, *p.AutoJoin
				fields = append(fields, "autoJoin")
			}
			if err := qs.IdentityUpdateGroup(ctx, arg); err != nil {
				return err
			}
		}
		if p.Name != nil && *p.Name != name {
			if !ValidGroupName(*p.Name) {
				return errs.E(errs.Invalid, errs.CodeValidation, "The group name does not match ^[a-z0-9][a-z0-9._-]{0,127}$.")
			}
			renamed, err := qs.IdentityRenamePrincipal(ctx, q.IdentityRenamePrincipalParams{ID: g.ID, Name: *p.Name})
			if err != nil {
				return err
			}
			if len(renamed) == 0 {
				return errs.E(errs.Conflict, errs.CodeConflict, "A group with this name exists.")
			}
			final = *p.Name
			fields = append(fields, "name")
			if err := NotifyPrincipals(m.notify, tx, ChangeGroup, g.ID); err != nil {
				return err
			}
		}
		if len(fields) == 0 {
			return nil
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventGroupUpdated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "group", ID: g.ID, Name: final},
			Details: details(map[string]any{"fields": fields}),
		})
	})
	if err != nil {
		return Group{}, DBErr("principal.UpdateGroup", err)
	}
	return m.GetGroup(ctx, final)
}

// DeleteGroup deletes a group with its memberships and bindings. A group with
// origin scim answers 409 managed_by_scim (R-AUTH-018).
func (m *Manager) DeleteGroup(ctx context.Context, c Caller, name string) error {
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetGroup(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The group")
		}
		g := rows[0]
		if g.Origin == "scim" {
			return managedBySCIM()
		}
		if _, err := qs.IdentityDeletePrincipal(ctx, g.ID); err != nil {
			return err
		}
		if err := NotifyPrincipals(m.notify, tx, ChangeGroup, g.ID); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventGroupDeleted, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "group", ID: g.ID, Name: name},
			Details: details(map[string]any{"membershipsDeleted": g.MemberCount}),
			Journal: details(map[string]string{"principalId": g.ID, "name": name}),
		})
	})
	return DBErr("principal.DeleteGroup", err)
}

// MemberEntry is one line of a member list: the member and one of its sources.
type MemberEntry struct {
	Member string
	Kind   Kind
	Source string
}

// ListMembers lists the members of a group ordered by kind and name, one entry
// per source. After is "<kind>/<name>" of the last entry of the previous page.
func (m *Manager) ListMembers(ctx context.Context, group, source, afterKind, afterName string, limit int) ([]MemberEntry, error) {
	var out []MemberEntry
	err := m.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		g, err := qs.IdentityGetGroup(ctx, group)
		if err != nil {
			return err
		}
		if len(g) == 0 {
			return ErrNotFound
		}
		rows, err := qs.IdentityGroupMembers(ctx, q.IdentityGroupMembersParams{
			GroupID: g[0].ID, AfterKind: afterKind, AfterName: afterName, Source: source, MaxRows: int32(clampLimit(limit)), //nolint:gosec // bounded
		})
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range rows {
			out = append(out, MemberEntry{Member: memberName(r.MemberKind, r.MemberName), Kind: Kind(r.MemberKind), Source: r.Source})
		}
		return nil
	})
	if err == ErrNotFound { //nolint:errorlint // sentinel returned unwrapped above
		return nil, NotFound("The group")
	}
	return out, DBErr("principal.ListMembers", err)
}

// PatchMembers adds and removes memberships of one source (manual by default,
// auto from R-AUTH-019) in one transaction. An unknown member answers 404 and
// nothing is applied; a group with origin scim answers 409 managed_by_scim. The
// result names every touched member with its remaining sources.
func (m *Manager) PatchMembers(ctx context.Context, c Caller, group, source string, add, remove []string) ([]MemberResult, error) {
	if source == "" {
		source = "manual"
	}
	if source != "manual" && source != "auto" {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, "Only manual and auto memberships can be changed through the API.")
	}
	var out []MemberResult
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetGroup(ctx, group)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The group")
		}
		g := groupInfo{id: rows[0].ID, name: rows[0].Name, origin: rows[0].Origin}
		var changes []memberChange
		for _, ref := range add {
			id, kind, err := memberRef(ctx, qs, ref)
			if err != nil {
				return err
			}
			changes = append(changes, memberChange{id: id, name: ref, kind: kind, add: true, source: source})
		}
		for _, ref := range remove {
			id, kind, err := memberRef(ctx, qs, ref)
			if err != nil {
				return err
			}
			changes = append(changes, memberChange{id: id, name: ref, kind: kind, add: false, source: source})
		}
		if err := m.changeMembers(ctx, tx, qs, c, g, changes); err != nil {
			return err
		}
		out = out[:0]
		for _, ch := range changes {
			srcs, err := qs.IdentityMemberSources(ctx, q.IdentityMemberSourcesParams{GroupID: g.id, MemberID: ch.id})
			if err != nil {
				return err
			}
			out = append(out, MemberResult{Member: ch.name, Kind: ch.kind, Sources: srcs})
		}
		return nil
	})
	return out, DBErr("principal.PatchMembers", err)
}

type memberRefJSON struct {
	Member string `json:"member"`
	Source string `json:"source"`
}

// changeMembers applies membership changes of one group, records the change in
// the change area auth and writes group.members.changed with the journal entry
// of the removed memberships (R-AUTH-041). It writes nothing when no row
// changed.
func (m *Manager) changeMembers(ctx context.Context, tx meta.Tx, qs *q.Queries, c Caller, g groupInfo, changes []memberChange) error {
	if len(changes) == 0 {
		return nil
	}
	if g.origin == "scim" {
		return managedBySCIM()
	}
	var added, removed []memberRefJSON
	var ids []string
	for _, ch := range changes {
		if ch.add {
			rows, err := qs.IdentityAddMembership(ctx, q.IdentityAddMembershipParams{GroupID: g.id, MemberID: ch.id, Source: ch.source})
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				added = append(added, memberRefJSON{Member: ch.name, Source: ch.source})
				ids = append(ids, ch.id)
			}
			continue
		}
		rows, err := qs.IdentityRemoveMembership(ctx, q.IdentityRemoveMembershipParams{GroupID: g.id, MemberID: ch.id, Source: ch.source})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			removed = append(removed, memberRefJSON{Member: ch.name, Source: ch.source})
			ids = append(ids, ch.id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := NotifyPrincipals(m.notify, tx, ChangeMembership, ids...); err != nil {
		return err
	}
	type entry struct {
		Group   string          `json:"group"`
		Removed []memberRefJSON `json:"removed"`
	}
	return m.record(ctx, tx, audit.Event{
		Type: audit.EventGroupMembersChanged, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
		Subject: audit.Subject{Kind: "group", ID: g.id, Name: g.name},
		Details: details(map[string]any{"added": orEmpty(added), "removed": orEmpty(removed)}),
		Journal: details(entry{Group: g.name, Removed: orEmpty(removed)}),
	})
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
