package principal

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Caller is who acts and from where. It is the actor and source of the audit
// events a Manager call records.
type Caller struct {
	Actor  audit.Actor
	Source audit.Source
}

// AdminBinder binds the system role admin to a principal in the transaction that
// creates it (R-AUTH-015 `service-account create --admin`). The authorization
// area implements it, because the write belongs to its change area and
// completeness test (R-AUTHZ-028): it inserts the role binding and notifies
// the area authz in tx.
type AdminBinder interface {
	BindAdmin(ctx context.Context, tx meta.Tx, principalID, grantedBy string) error
}

// ErrAdminBinding is returned when a service account is created with Admin and
// no [AdminBinder] is wired.
var ErrAdminBinding = errors.New("principal: administrator role binding is not available")

// ManagerOptions configure [NewManager].
type ManagerOptions struct {
	DB       DB
	Notifier Notifier
	// Recorder records the user.*, group.* and serviceaccount.* events in the
	// transaction of the change. Nil records nothing.
	Recorder audit.Recorder
	// BreakGlassName is auth.break_glass.username; it is a reserved username.
	BreakGlassName string
	// Admin binds the administrator role; nil disables the Admin option.
	Admin AdminBinder
}

// Manager is the write side of the principal table: users, groups, memberships
// and service accounts (R-AUTH-016, R-AUTH-018, R-AUTH-020). Every call is one
// transaction that changes the rows, records the change in the change area auth
// and writes the audit event, so a change exists with its record and is in force
// on every replica within the revocation bound.
type Manager struct {
	db       DB
	notify   Notifier
	rec      audit.Recorder
	reserved []string
	admin    AdminBinder
}

// NewManager creates the manager.
func NewManager(o ManagerOptions) *Manager {
	m := &Manager{db: o.DB, notify: o.Notifier, rec: o.Recorder, admin: o.Admin}
	if m.rec == nil {
		m.rec = audit.Nop{}
	}
	if o.BreakGlassName != "" {
		m.reserved = []string{o.BreakGlassName}
	}
	return m
}

// Identity is an external identity link of a user.
type Identity struct {
	Provider string
	Linked   bool
}

// PendingIdentity pre-provisions a link: the user's first login at the provider
// completes it (R-AUTH-007). Issuer is that of the provider's configuration.
type PendingIdentity struct {
	Provider string
	Issuer   string
}

// Membership is one source of one group membership.
type Membership struct {
	Group  string
	Source string
}

// ClaimMissing is the record of a login without a usable group claim
// (R-AUTH-008).
type ClaimMissing struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// User is the record of R-AUTH-016. isAdmin and adminVia are derived from the
// role bindings by the caller.
type User struct {
	ID             string
	Name           string
	Status         Status
	Email          string
	EmailVerified  bool
	DisplayName    string
	Origin         string
	BreakGlass     bool
	DisabledReason string
	SCIMExternalID string
	LastLoginAt    time.Time
	CreatedAt      time.Time
	Identities     []Identity
	Groups         []Membership
	// GroupClaimMissing is set while the last login carried no usable group claim.
	GroupClaimMissing *ClaimMissing
}

func (m *Manager) record(ctx context.Context, tx meta.Tx, ev audit.Event) error {
	return m.rec.RecordTx(ctx, tx, ev)
}

func details(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the values are maps and structs of strings and numbers
	}
	return b
}

func sourceOf(c Caller) string {
	if c.Actor.Kind == "admin-cli" {
		return "admin-cli"
	}
	return "admin"
}

// NewUser is the request of CreateUser (R-AUTH-016).
type NewUser struct {
	Name          string
	Email         string
	DisplayName   string
	EmailVerified bool
	// Origin is admin (default), scim or system.
	Origin     string
	Identities []PendingIdentity
}

// CreateUser creates a user with origin admin and pending identity links
// (R-AUTH-016). A reserved username answers 422 reserved_name, an invalid one
// 400 username_invalid, an existing one 409.
func (m *Manager) CreateUser(ctx context.Context, c Caller, in NewUser) (User, error) {
	if ReservedUserName(in.Name, m.reserved...) {
		return User{}, errs.E(errs.Invalid, "reserved_name", "The username is reserved.",
			errs.WithFields(errs.FieldError{Pointer: "/username", Code: "reserved_name", Detail: "reserved"}))
	}
	if !ValidUserName(in.Name, m.reserved...) {
		return User{}, errs.E(errs.Invalid, errs.CodeUsernameInvalid, "The username does not match ^[a-z0-9][a-z0-9._@+-]{0,127}$.")
	}
	origin := in.Origin
	if origin == "" {
		origin = "admin"
	}
	id := NewID()
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindUser), Name: in.Name, CreatedBy: c.Actor.ID})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.Conflict, errs.CodeConflict, "A user with this name exists.")
		}
		if err := qs.IdentityInsertUser(ctx, q.IdentityInsertUserParams{
			ID: id, Email: in.Email, EmailVerified: in.EmailVerified, DisplayName: in.DisplayName, Origin: origin,
		}); err != nil {
			return err
		}
		for _, pi := range in.Identities {
			if _, err := qs.IdentityInsertIdentity(ctx, q.IdentityInsertIdentityParams{UserID: id, ProviderID: pi.Provider, Issuer: pi.Issuer}); err != nil {
				return err
			}
		}
		if err := m.applyAutoJoin(ctx, qs, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventUserCreated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "user", ID: id, Name: in.Name},
			Details: details(map[string]string{"source": sourceOf(c)}),
		})
	})
	if err != nil {
		return User{}, DBErr("principal.CreateUser", err)
	}
	return m.GetUser(ctx, in.Name)
}

// applyAutoJoin gives a new user the auto memberships of R-AUTH-019.
func (m *Manager) applyAutoJoin(ctx context.Context, qs *q.Queries, userID string) error {
	groups, err := qs.IdentityAutoJoinGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := qs.IdentityAddMembership(ctx, q.IdentityAddMembershipParams{GroupID: g.ID, MemberID: userID, Source: "auto"}); err != nil {
			return err
		}
	}
	return nil
}

// GetUser returns the user record, or a 404.
func (m *Manager) GetUser(ctx context.Context, name string) (User, error) {
	var u User
	err := m.db.Read(ctx, func(db q.DBTX) error {
		var err error
		u, err = loadUser(ctx, q.New(db), name)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return User{}, NotFound("The user")
	}
	return u, DBErr("principal.GetUser", err)
}

func userFrom(id, name, status, email string, emailVerified bool, displayName, origin string, bg bool, reason, scim string,
	lastLogin, created time.Time, claim string,
) User {
	u := User{
		ID: id, Name: name, Status: Status(status), Email: email, EmailVerified: emailVerified, DisplayName: displayName,
		Origin: origin, BreakGlass: bg, DisabledReason: reason, SCIMExternalID: scim, LastLoginAt: lastLogin, CreatedAt: created,
	}
	if claim != "" && claim != "null" {
		var cm ClaimMissing
		if json.Unmarshal([]byte(claim), &cm) == nil && cm.Reason != "" {
			u.GroupClaimMissing = &cm
		}
	}
	return u
}

func loadUser(ctx context.Context, qs *q.Queries, name string) (User, error) {
	rows, err := qs.IdentityGetUser(ctx, name)
	if err != nil {
		return User{}, err
	}
	if len(rows) == 0 {
		return User{}, ErrNotFound
	}
	r := rows[0]
	u := userFrom(r.ID, r.Name, r.Status, r.Email, r.EmailVerified, r.DisplayName, r.Origin, r.IsBreakGlass, r.DisabledReason,
		r.ScimExternalID, r.LastLoginAt.Time, r.CreatedAt.Time, r.GroupClaimMissing)
	return u, fillUser(ctx, qs, &u)
}

func fillUser(ctx context.Context, qs *q.Queries, u *User) error {
	ids, err := qs.IdentityUserIdentities(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, i := range ids {
		u.Identities = append(u.Identities, Identity{Provider: i.ProviderID, Linked: i.Linked})
	}
	ms, err := qs.IdentityUserMemberships(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, g := range ms {
		u.Groups = append(u.Groups, Membership{Group: g.GroupName, Source: g.Source})
	}
	return nil
}

// UserFilter filters ListUsers (R-AUTH-016). Empty fields match everything.
type UserFilter struct {
	Query  string
	Status string
	Origin string
	Group  string
	// After is the name of the last user of the previous page.
	After string
	// Limit is the number of users to return at most; the caller asks for one
	// more than its page size to know whether another page follows.
	Limit int
}

// ListUsers lists users ordered by name.
func (m *Manager) ListUsers(ctx context.Context, f UserFilter) ([]User, error) {
	var out []User
	err := m.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.IdentityListUsers(ctx, q.IdentityListUsersParams{
			After: f.After, Query: f.Query, Status: f.Status, Origin: f.Origin, GroupName: f.Group, MaxRows: int32(clampLimit(f.Limit)), //nolint:gosec // bounded
		})
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range rows {
			u := userFrom(r.ID, r.Name, r.Status, r.Email, r.EmailVerified, r.DisplayName, r.Origin, r.IsBreakGlass, r.DisabledReason,
				r.ScimExternalID, r.LastLoginAt.Time, r.CreatedAt.Time, "")
			if err := fillUser(ctx, qs, &u); err != nil {
				return err
			}
			out = append(out, u)
		}
		return nil
	})
	return out, DBErr("principal.ListUsers", err)
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 100
	case n > 1001:
		return 1001
	}
	return n
}

// UserPatch is the request of UpdateUser; a nil field is unchanged.
type UserPatch struct {
	DisplayName *string
	Email       *string
	// Identities, when non-nil, replaces the pending links (a linked identity
	// stays).
	Identities *[]PendingIdentity
}

// UpdateUser changes the profile data an administrator may correct (R-AUTH-016).
func (m *Manager) UpdateUser(ctx context.Context, c Caller, name string, p UserPatch) (User, error) {
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		pr, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(KindUser), Name: name})
		if err != nil {
			return err
		}
		if len(pr) == 0 {
			return NotFound("The user")
		}
		id := pr[0].ID
		var fields []string
		if p.Email != nil || p.DisplayName != nil {
			arg := q.IdentityUpdateUserFieldsParams{ID: id}
			if p.Email != nil {
				arg.SetEmail, arg.Email = true, *p.Email
				fields = append(fields, "email")
			}
			if p.DisplayName != nil {
				arg.SetDisplayName, arg.DisplayName = true, *p.DisplayName
				fields = append(fields, "displayName")
			}
			if err := qs.IdentityUpdateUserFields(ctx, arg); err != nil {
				return err
			}
		}
		if p.Identities != nil {
			providers := make([]string, 0, len(*p.Identities))
			for _, pi := range *p.Identities {
				providers = append(providers, pi.Provider)
				if _, err := qs.IdentityInsertIdentity(ctx, q.IdentityInsertIdentityParams{UserID: id, ProviderID: pi.Provider, Issuer: pi.Issuer}); err != nil {
					return err
				}
			}
			if err := qs.IdentityReplaceIdentities(ctx, q.IdentityReplaceIdentitiesParams{UserID: id, Providers: providers}); err != nil {
				return err
			}
			fields = append(fields, "identities")
		}
		if len(fields) == 0 {
			return nil
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventUserUpdated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "user", ID: id, Name: name},
			Details: details(map[string]any{"source": sourceOf(c), "fields": fields}),
		})
	})
	if err != nil {
		return User{}, DBErr("principal.UpdateUser", err)
	}
	return m.GetUser(ctx, name)
}

// DisableOptions are the options of the disable operations.
type DisableOptions struct {
	// Reason is admin (default), dormant or scim.
	Reason string
	// RevokeTokens revokes the principal's tokens with reason principal_disabled
	// (R-AUTH-016 default true for the API; the caller decides).
	RevokeTokens bool
}

// DisableUser disables a user: it ends the sessions, makes every token of the
// user fail with principal_disabled and, with RevokeTokens, revokes them
// (R-AUTH-016). The break-glass account cannot be disabled here.
func (m *Manager) DisableUser(ctx context.Context, c Caller, name string, o DisableOptions) error {
	if o.Reason == "" {
		o.Reason = "admin"
	}
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		pr, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(KindUser), Name: name})
		if err != nil {
			return err
		}
		if len(pr) == 0 {
			return NotFound("The user")
		}
		if pr[0].IsBreakGlass {
			return ErrBreakGlass
		}
		id := pr[0].ID
		if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: id, Status: string(StatusDisabled)}); err != nil {
			return err
		}
		if err := qs.IdentitySetUserDisabled(ctx, q.IdentitySetUserDisabledParams{ID: id, Reason: o.Reason}); err != nil {
			return err
		}
		ended, err := qs.IdentityEndSessions(ctx, q.IdentityEndSessionsParams{ID: id, Reason: "principal_disabled"})
		if err != nil {
			return err
		}
		revoked := 0
		if o.RevokeTokens {
			rows, err := qs.IdentityRevokeAllOfPrincipal(ctx, q.IdentityRevokeAllOfPrincipalParams{
				PrincipalID: id, RevokedBy: c.Actor.ID, Reason: "principal_disabled",
			})
			if err != nil {
				return err
			}
			revoked = len(rows)
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventUserDisabled, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "user", ID: id, Name: name}, Reason: o.Reason,
			Details: details(map[string]any{"source": sourceOf(c), "reason": o.Reason, "tokensRevoked": revoked, "sessionsEnded": len(ended)}),
			Journal: details(map[string]string{"principalId": id, "username": name, "disabledReason": o.Reason}),
		})
	})
	return DBErr("principal.DisableUser", err)
}

// EnableUser enables a user (R-AUTH-016). It does not resurrect revoked tokens.
func (m *Manager) EnableUser(ctx context.Context, c Caller, name string) error {
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		pr, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(KindUser), Name: name})
		if err != nil {
			return err
		}
		if len(pr) == 0 {
			return NotFound("The user")
		}
		id := pr[0].ID
		rows, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: id, Status: string(StatusActive)})
		if err != nil {
			return err
		}
		if err := qs.IdentitySetUserDisabled(ctx, q.IdentitySetUserDisabledParams{ID: id, Reason: ""}); err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil // already active
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventUserEnabled, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "user", ID: id, Name: name},
			Details: details(map[string]string{"source": sourceOf(c)}),
		})
	})
	return DBErr("principal.EnableUser", err)
}

// DeleteUser deletes a user with its sessions, tokens, identity links and
// memberships; policy bindings go with the foreign keys (R-AUTH-016). The audit
// event records the counts. A later login of the same identity creates a new
// principal with a new ID.
func (m *Manager) DeleteUser(ctx context.Context, c Caller, name string) error {
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		pr, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(KindUser), Name: name})
		if err != nil {
			return err
		}
		if len(pr) == 0 {
			return NotFound("The user")
		}
		if pr[0].IsBreakGlass {
			return ErrBreakGlass
		}
		id := pr[0].ID
		tokens, err := qs.IdentityCountTokensOfPrincipal(ctx, id)
		if err != nil {
			return err
		}
		sessions, err := qs.IdentityCountSessions(ctx, id)
		if err != nil {
			return err
		}
		memberships, err := qs.IdentityCountMembershipsOfMember(ctx, id)
		if err != nil {
			return err
		}
		idents, err := qs.IdentityUserIdentities(ctx, id)
		if err != nil {
			return err
		}
		if _, err := qs.IdentityDeletePrincipal(ctx, id); err != nil {
			return err
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventUserDeleted, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "user", ID: id, Name: name},
			Details: details(map[string]any{
				"source": sourceOf(c), "tokensDeleted": tokens, "sessionsEnded": sessions,
				"membershipsDeleted": memberships, "identitiesDeleted": len(idents),
			}),
			Journal: details(map[string]string{"principalId": id, "username": name}),
		})
	})
	return DBErr("principal.DeleteUser", err)
}

// memberRef resolves a member reference of R-AUTH-018: a username, or
// "sa:<name>" for a service account. The built-in principals and unknown names
// resolve to nothing.
func memberRef(ctx context.Context, qs *q.Queries, ref string) (id string, kind Kind, err error) {
	kind, name := KindUser, ref
	if rest, ok := strings.CutPrefix(ref, "sa:"); ok {
		kind, name = KindServiceAccount, rest
	}
	rows, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(kind), Name: name})
	if err != nil {
		return "", "", err
	}
	if len(rows) == 0 {
		return "", "", NotFound("The member " + ref)
	}
	return rows[0].ID, kind, nil
}

// memberName renders a member for lists and events.
func memberName(kind, name string) string {
	if kind == string(KindServiceAccount) {
		return "sa:" + name
	}
	return name
}

// PatchUserGroups adds and removes manual memberships of a user
// (R-AUTH-016 PATCH /users/{username}/groups). An unknown user or group answers
// 404 and nothing is applied. The result is the user's memberships afterwards.
func (m *Manager) PatchUserGroups(ctx context.Context, c Caller, name string, add, remove []string) ([]Membership, error) {
	var out []Membership
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		pr, err := qs.IdentityPrincipalByName(ctx, q.IdentityPrincipalByNameParams{Kind: string(KindUser), Name: name})
		if err != nil {
			return err
		}
		if len(pr) == 0 {
			return NotFound("The user")
		}
		id := pr[0].ID
		groups, err := resolveGroups(ctx, qs, append(slices.Clone(add), remove...))
		if err != nil {
			return err
		}
		for _, g := range add {
			if err := m.changeMembers(ctx, tx, qs, c, groups[g], []memberChange{{id: id, name: name, kind: KindUser, add: true, source: "manual"}}); err != nil {
				return err
			}
		}
		for _, g := range remove {
			if err := m.changeMembers(ctx, tx, qs, c, groups[g], []memberChange{{id: id, name: name, kind: KindUser, add: false, source: "manual"}}); err != nil {
				return err
			}
		}
		ms, err := qs.IdentityUserMemberships(ctx, id)
		if err != nil {
			return err
		}
		out = out[:0]
		for _, g := range ms {
			out = append(out, Membership{Group: g.GroupName, Source: g.Source})
		}
		return nil
	})
	return out, DBErr("principal.PatchUserGroups", err)
}

type groupInfo struct {
	id, name, origin string
}

// resolveGroups looks up the named groups; an unknown one is a 404.
func resolveGroups(ctx context.Context, qs *q.Queries, names []string) (map[string]groupInfo, error) {
	out := map[string]groupInfo{}
	for _, n := range names {
		if _, ok := out[n]; ok {
			continue
		}
		rows, err := qs.IdentityGetGroup(ctx, n)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, NotFound("The group " + n)
		}
		out[n] = groupInfo{id: rows[0].ID, name: rows[0].Name, origin: rows[0].Origin}
	}
	return out, nil
}
