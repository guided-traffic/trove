package principal

import (
	"context"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// ServiceAccount is the record of R-AUTH-020.
type ServiceAccount struct {
	ID string
	// Name is the principal name as stored: the bare name of an instance-wide account,
	// <project>/<name> of a project's (Q-AUTHZ-209).
	Name        string
	Description string
	Status      Status
	CreatedAt   time.Time
	// Revision is the ETag of the account (principal.revision, R-API-012); filled by GetServiceAccount.
	Revision int64
	// Project is the key of the project the account belongs to, "" for an instance-wide
	// account (R-AUTHZ-026, Q-AUTHZ-169). It is set when the account is created and
	// never changes.
	Project string
	// IsAdmin is whether the account holds the administrator role now (Q-AUTH-209), derived from the role bindings and
	// never stored. The read methods fill it ([Manager.GetServiceAccount], [Manager.ListServiceAccounts]); a record
	// from a write transaction carries false. A project's account holds no system role, so it is false there.
	IsAdmin bool
}

// AdminHolders returns the IDs among ids that hold the administrator role now: by a binding on the principal itself, by a
// binding on a group it belongs to, or as the break-glass account (Q-AUTH-209). One query however many IDs there are; an
// ID that is not a principal is not in the answer.
func (m *Manager) AdminHolders(ctx context.Context, ids []string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	err := m.db.Read(ctx, func(db q.DBTX) error {
		held, err := adminHolders(ctx, q.New(db), ids)
		out = held
		return err
	})
	return out, DBErr("principal.AdminHolders", err)
}

func adminHolders(ctx context.Context, qs *q.Queries, ids []string) (map[string]bool, error) {
	rows, err := qs.IdentityAdminHolderIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// Object is the account's address: its project and its name within the project.
func (sa ServiceAccount) Object() qname.Object {
	return qname.Object{Project: sa.Project, Name: strings.TrimPrefix(sa.Name, sa.Project+"/")}
}

// NewServiceAccount is the request of CreateServiceAccount.
type NewServiceAccount struct {
	Name        string
	Description string
	// Admin binds the system role admin in the transaction that creates the
	// account (`trove admin service-account create --admin`, R-AUTH-015). It
	// needs an [AdminBinder]; without one the call fails with [ErrAdminBinding].
	// A project service account holds no system role: 422 role_not_allowed.
	Admin bool
}

// CreateServiceAccount creates an instance-wide service account (R-AUTH-020).
func (m *Manager) CreateServiceAccount(ctx context.Context, c Caller, in NewServiceAccount) (ServiceAccount, error) {
	return m.createAccount(ctx, c, "", in)
}

func (m *Manager) createAccount(ctx context.Context, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	if err := m.checkNewServiceAccount(project, in); err != nil {
		return ServiceAccount{}, err
	}
	var sa ServiceAccount
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		var err error
		sa, err = m.createServiceAccount(ctx, tx, c, project, in)
		return err
	})
	if err != nil {
		return ServiceAccount{}, DBErr("principal.CreateServiceAccount", err)
	}
	return sa, nil
}

// CreateServiceAccountTx is [Manager.CreateServiceAccount] inside the caller's
// transaction, with the same rules as [Manager.CreateUserTx]; the administrator
// binding of the Admin option is part of tx too.
func (m *Manager) CreateServiceAccountTx(ctx context.Context, tx meta.Tx, c Caller, in NewServiceAccount) (ServiceAccount, error) {
	return m.createAccountTx(ctx, tx, c, "", in)
}

func (m *Manager) createAccountTx(ctx context.Context, tx meta.Tx, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	if err := m.checkNewServiceAccount(project, in); err != nil {
		return ServiceAccount{}, err
	}
	sa, err := m.createServiceAccount(ctx, tx, c, project, in)
	return sa, DBErr("principal.CreateServiceAccount", err)
}

func (m *Manager) checkNewServiceAccount(project string, in NewServiceAccount) error {
	if !ValidServiceAccountName(in.Name) {
		return errs.E(errs.Invalid, errs.CodeValidation, "The service-account name does not match ^[a-z0-9][a-z0-9-]{1,62}$.")
	}
	if project != "" && !qname.ValidProjectKey(project) {
		return NotFound("The project")
	}
	if project != "" && in.Admin {
		return errs.E(errs.Unprocessable, errs.CodeRoleNotAllowed, "A project service account holds no system role.")
	}
	if in.Admin && m.admin == nil {
		return ErrAdminBinding
	}
	return nil
}

// createServiceAccount writes the account in tx and returns the record as tx sees it. project is the key
// of the account's project, empty for an instance-wide account: the principal row is named
// <project>/<name> then (Q-AUTHZ-209), and the project is locked FOR SHARE so that a concurrent delete
// of it, which counts the accounts under its own lock, waits for this transaction.
func (m *Manager) createServiceAccount(ctx context.Context, tx meta.Tx, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	id := NewID()
	qs := Queries(tx)
	stored := qname.Object{Project: project, Name: in.Name}.String()
	var projectID string
	if project != "" {
		rows, err := qs.IdentityProjectShare(ctx, project)
		if err != nil {
			return ServiceAccount{}, err
		}
		if len(rows) == 0 {
			return ServiceAccount{}, NotFound("The project")
		}
		projectID = rows[0].ID
	}
	rows, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindServiceAccount), Name: stored, CreatedBy: c.Actor.ID})
	if err != nil {
		return ServiceAccount{}, err
	}
	if len(rows) == 0 {
		return ServiceAccount{}, errs.E(errs.Conflict, errs.CodeAlreadyExists, "A service account with this name exists.")
	}
	if projectID == "" {
		err = qs.IdentityInsertServiceAccount(ctx, q.IdentityInsertServiceAccountParams{ID: id, Description: in.Description})
	} else {
		err = qs.IdentityInsertProjectServiceAccount(ctx, q.IdentityInsertProjectServiceAccountParams{ID: id, Description: in.Description, ProjectID: projectID})
	}
	if err != nil {
		return ServiceAccount{}, err
	}
	if in.Admin {
		if err := m.admin.BindAdmin(ctx, tx, id, c.Actor.ID); err != nil {
			return ServiceAccount{}, err
		}
	}
	if err := m.record(ctx, tx, audit.Event{
		Type: audit.EventSACreated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
		Subject: audit.Subject{Kind: "service_account", ID: id, Name: stored},
		Details: details(withProject(map[string]any{"source": sourceOf(c), "admin": in.Admin}, project)),
	}); err != nil {
		return ServiceAccount{}, err
	}
	return loadServiceAccount(ctx, qs, stored)
}

// GetServiceAccount returns the instance-wide account, or a 404. The account of a project is stored as
// <project>/<name>, so no bare name finds it; [Manager.GetProjectServiceAccount] reads those.
func (m *Manager) GetServiceAccount(ctx context.Context, name string) (ServiceAccount, error) {
	return m.getAccount(ctx, qname.Object{Name: name})
}

func (m *Manager) getAccount(ctx context.Context, o qname.Object) (ServiceAccount, error) {
	var sa ServiceAccount
	err := m.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		var err error
		if sa, err = loadAccountOf(ctx, qs, o); err != nil || sa.Project != "" {
			return err
		}
		held, err := adminHolders(ctx, qs, []string{sa.ID})
		sa.IsAdmin = held[sa.ID]
		return err
	})
	if err == ErrNotFound { //nolint:errorlint // sentinel returned unwrapped by loadAccountOf
		return ServiceAccount{}, NotFound("The service account")
	}
	return sa, DBErr("principal.GetServiceAccount", err)
}

// loadAccountOf reads the account o, of its project or instance-wide when o.Project is empty;
// ErrNotFound when there is none there. A name that holds a "/" is no name within a project, so it
// finds nothing: the stored form of a project's account never addresses an account by a bare-name
// method.
func loadAccountOf(ctx context.Context, qs *q.Queries, o qname.Object) (ServiceAccount, error) {
	if o.Name == "" || strings.Contains(o.Name, "/") {
		return ServiceAccount{}, ErrNotFound
	}
	sa, err := loadServiceAccount(ctx, qs, o.String())
	if err != nil {
		return ServiceAccount{}, err
	}
	if sa.Project != o.Project {
		return ServiceAccount{}, ErrNotFound
	}
	return sa, nil
}

// loadServiceAccount reads the account by its stored name; ErrNotFound when there is none.
func loadServiceAccount(ctx context.Context, qs *q.Queries, name string) (ServiceAccount, error) {
	rows, err := qs.IdentityGetServiceAccount(ctx, name)
	if err != nil {
		return ServiceAccount{}, err
	}
	if len(rows) == 0 {
		return ServiceAccount{}, ErrNotFound
	}
	r := rows[0]
	return ServiceAccount{Revision: r.Revision, ID: r.ID, Name: r.Name, Description: r.Description, Status: Status(r.Status), CreatedAt: r.CreatedAt.Time, Project: r.Project}, nil
}

// ListServiceAccounts lists service accounts ordered by stored name; after is the stored name
// of the last account of the previous page. project keeps the accounts of the
// project with that key, empty keeps every account, instance-wide and of every project (R-AUTHZ-026).
func (m *Manager) ListServiceAccounts(ctx context.Context, query, project, after string, limit int) ([]ServiceAccount, error) {
	var out []ServiceAccount
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityListServiceAccounts(ctx, q.IdentityListServiceAccountsParams{
			After: after, Query: query, Project: project, MaxRows: int32(clampLimit(limit)), //nolint:gosec // bounded
		})
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range rows {
			out = append(out, ServiceAccount{Revision: r.Revision, ID: r.ID, Name: r.Name, Description: r.Description, Status: Status(r.Status), CreatedAt: r.CreatedAt.Time, Project: r.Project})
		}
		ids := make([]string, 0, len(out))
		for _, sa := range out {
			if sa.Project == "" {
				ids = append(ids, sa.ID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		held, err := adminHolders(ctx, q.New(db), ids)
		for i := range out {
			out[i].IsAdmin = held[out[i].ID]
		}
		return err
	})
	return out, DBErr("principal.ListServiceAccounts", err)
}

// UpdateServiceAccount changes the description of an instance-wide account (R-AUTH-020; owners are P1).
//
// expected is the revision the caller loaded (If-Match) or errs.AnyRevision for the
// deliberate blind write (R-API-012).
func (m *Manager) UpdateServiceAccount(ctx context.Context, c Caller, name, description string, expected int64) (ServiceAccount, error) {
	return m.updateAccount(ctx, c, qname.Object{Name: name}, description, expected)
}

func (m *Manager) updateAccount(ctx context.Context, c Caller, o qname.Object, description string, expected int64) (ServiceAccount, error) {
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		sa, err := accountOf(ctx, qs, o)
		if err != nil {
			return err
		}
		if err := checkRevision(ctx, qs, sa.ID, expected, "The service account"); err != nil {
			return err
		}
		if err := guardRoleHolder(ctx, qs, c, sa.ID); err != nil {
			return err
		}
		if err := qs.IdentityUpdateServiceAccount(ctx, q.IdentityUpdateServiceAccountParams{ID: sa.ID, Description: description}); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSAUpdated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: sa.ID, Name: sa.Name},
			Details: details(withProject(map[string]any{"fields": []string{"description"}}, sa.Project)),
		})
	})
	if err != nil {
		return ServiceAccount{}, DBErr("principal.UpdateServiceAccount", err)
	}
	return m.getAccount(ctx, o)
}

// accountOf is loadAccountOf with the 404 of the API for a missing account.
func accountOf(ctx context.Context, qs *q.Queries, o qname.Object) (ServiceAccount, error) {
	sa, err := loadAccountOf(ctx, qs, o)
	if err == ErrNotFound { //nolint:errorlint // sentinel returned unwrapped by loadAccountOf
		return ServiceAccount{}, NotFound("The service account")
	}
	return sa, err
}

// DisableServiceAccount disables an instance-wide account: all its tokens fail with
// principal_disabled (R-AUTH-020). With revokeTokens it also revokes them.
func (m *Manager) DisableServiceAccount(ctx context.Context, c Caller, name string, revokeTokens bool) error {
	return m.disableAccount(ctx, c, qname.Object{Name: name}, revokeTokens)
}

func (m *Manager) disableAccount(ctx context.Context, c Caller, o qname.Object, revokeTokens bool) error {
	return DBErr("principal.DisableServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		sa, err := accountOf(ctx, qs, o)
		if err != nil {
			return err
		}
		id := sa.ID
		if err := guardRoleHolder(ctx, qs, c, id); err != nil {
			return err
		}
		var last LastAdminGuard
		if err := last.Arm(ctx, qs, id); err != nil {
			return err
		}
		if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: id, Status: string(StatusDisabled)}); err != nil {
			return err
		}
		if err := last.Check(ctx, qs); err != nil {
			return err
		}
		revoked := 0
		if revokeTokens {
			// One token.revoked with its journal entry per token (Q-AUTH-198), as for a user.
			ids, err := RevokeAllTokensTx(ctx, tx, m.rec, c, id, "principal_disabled", false)
			if err != nil {
				return err
			}
			revoked = len(ids)
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSADisabled, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: sa.Name},
			Details: details(withProject(map[string]any{"tokensRevoked": revoked}, sa.Project)),
			Journal: details(map[string]string{"principalId": id, "name": sa.Name}),
		})
	}))
}

// EnableServiceAccount enables an instance-wide account. Revoked tokens stay revoked.
func (m *Manager) EnableServiceAccount(ctx context.Context, c Caller, name string) error {
	return m.enableAccount(ctx, c, qname.Object{Name: name})
}

func (m *Manager) enableAccount(ctx context.Context, c Caller, o qname.Object) error {
	return DBErr("principal.EnableServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		sa, err := accountOf(ctx, qs, o)
		if err != nil {
			return err
		}
		if err := guardRoleHolder(ctx, qs, c, sa.ID); err != nil {
			return err
		}
		if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: sa.ID, Status: string(StatusActive)}); err != nil {
			return err
		}
		return NotifyPrincipals(m.notify, tx, ChangePrincipal, sa.ID)
	}))
}

// DeleteServiceAccount deletes an instance-wide account with its tokens and memberships.
func (m *Manager) DeleteServiceAccount(ctx context.Context, c Caller, name string) error {
	return m.deleteAccount(ctx, c, qname.Object{Name: name})
}

// deleteAccount revokes the account's tokens first and removes the principal row after: the token rows
// go with the principal through their foreign key, so a revocation after the delete would find none
// and the caches of the replicas would learn of the account's end only from the change of the
// principal. The tokens are revoked, each with its token.revoked and journal entry (Q-AUTH-198), and
// the change notified, in the transaction that deletes the row.
func (m *Manager) deleteAccount(ctx context.Context, c Caller, o qname.Object) error {
	return DBErr("principal.DeleteServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		sa, err := accountOf(ctx, qs, o)
		if err != nil {
			return err
		}
		id := sa.ID
		if err := guardRoleHolder(ctx, qs, c, id); err != nil {
			return err
		}
		var last LastAdminGuard
		if err := last.Arm(ctx, qs, id); err != nil {
			return err
		}
		tokens, err := qs.IdentityCountTokensOfPrincipal(ctx, id)
		if err != nil {
			return err
		}
		if _, err := RevokeAllTokensTx(ctx, tx, m.rec, c, id, "principal_disabled", false); err != nil {
			return err
		}
		memberships, err := qs.IdentityCountMembershipsOfMember(ctx, id)
		if err != nil {
			return err
		}
		if err := notifyAuthz(ctx, m.authz, tx, id); err != nil {
			return err
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		if _, err := qs.IdentityDeletePrincipal(ctx, id); err != nil {
			return err
		}
		if err := last.Check(ctx, qs); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSADeleted, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: sa.Name},
			Details: details(withProject(map[string]any{"tokensDeleted": tokens, "membershipsDeleted": memberships}, sa.Project)),
			Journal: details(map[string]string{"principalId": id, "name": sa.Name}),
		})
	}))
}

// withProject adds the key of the account's project to the details of an audit
// event of a service account; an instance-wide account adds nothing (Q-AUTHZ-169).
func withProject(d map[string]any, project string) map[string]any {
	if project != "" {
		d["project"] = project
	}
	return d
}
