package principal

import (
	"context"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ServiceAccount is the record of R-AUTH-020.
type ServiceAccount struct {
	ID          string
	Name        string
	Description string
	Status      Status
	CreatedAt   time.Time
	// Revision is the ETag of the account (principal.revision, R-API-012); filled by GetServiceAccount.
	Revision int64
	// Project is the key of the project the account belongs to, "" for a global
	// account (R-AUTHZ-026, Q-AUTHZ-169). It is set when the account is created and
	// never changes.
	Project string
}

// NewServiceAccount is the request of CreateServiceAccount.
type NewServiceAccount struct {
	Name        string
	Description string
	// Admin binds the system role admin in the transaction that creates the
	// account (`trove admin service-account create --admin`, R-AUTH-015). It
	// needs an [AdminBinder]; without one the call fails with [ErrAdminBinding].
	Admin bool
	// Project makes the account a project service account (Q-AUTHZ-169): its name
	// starts with <Project>-, it is no group member and no role holder, and only the
	// project's own policies grant to it. The caller has checked the right to create
	// it (an administrator, or an administrator of the project with managePolicies).
	Project string
}

// CreateServiceAccount creates a service account (R-AUTH-020).
func (m *Manager) CreateServiceAccount(ctx context.Context, c Caller, in NewServiceAccount) (ServiceAccount, error) {
	if err := m.checkNewServiceAccount(in); err != nil {
		return ServiceAccount{}, err
	}
	var sa ServiceAccount
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		var err error
		sa, err = m.createServiceAccount(ctx, tx, c, in)
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
	if err := m.checkNewServiceAccount(in); err != nil {
		return ServiceAccount{}, err
	}
	sa, err := m.createServiceAccount(ctx, tx, c, in)
	return sa, DBErr("principal.CreateServiceAccount", err)
}

func (m *Manager) checkNewServiceAccount(in NewServiceAccount) error {
	if !ValidServiceAccountName(in.Name) {
		return errs.E(errs.Invalid, errs.CodeValidation, "The service-account name does not match ^[a-z0-9][a-z0-9-]{1,62}$.")
	}
	if in.Project != "" && in.Admin {
		return errs.E(errs.Invalid, errs.CodeRoleNotAllowed, "A project service account holds no system role.")
	}
	if in.Admin && m.admin == nil {
		return ErrAdminBinding
	}
	return nil
}

// bindProject applies the name rules of Q-AUTHZ-176 to a new account in tx: a
// project account is named <project>-… and the project exists (locked, so that it
// is not deleted under the account); a global account takes no name that starts
// with the key of a project and a hyphen. It returns the project's id, "" for a
// global account.
func bindProject(ctx context.Context, qs *q.Queries, in NewServiceAccount) (string, error) {
	if in.Project == "" {
		keys, err := qs.ProjectKeysPrefixing(ctx, in.Name)
		if err != nil {
			return "", err
		}
		if len(keys) > 0 {
			return "", errs.E(errs.Invalid, errs.CodeNameReservedByProject,
				"The name starts with the key of the project "+keys[0]+" and a hyphen, which a service account of that project takes.",
				errs.WithFields(errs.FieldError{Pointer: "/name", Code: errs.CodeNameReservedByProject, Detail: "reserved by the project " + keys[0]}))
		}
		return "", nil
	}
	rows, err := qs.IdentityProjectShare(ctx, in.Project)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errs.E(errs.Invalid, errs.CodeUnknownProject, "No project has this key.",
			errs.WithFields(errs.FieldError{Pointer: "/project", Code: errs.CodeUnknownProject, Detail: "no project has the key " + in.Project}))
	}
	// The key and the hyphen alone are no name in the project's space: the trigger AI003
	// refuses it as well (Q-AUTH-193).
	if !strings.HasPrefix(in.Name, in.Project+"-") || len(in.Name) == len(in.Project)+1 {
		return "", prefixRequired(in.Project)
	}
	return rows[0].ID, nil
}

// prefixRequired is the 422 of a project service account whose name is not
// <project>-… with at least one character after the hyphen (Q-AUTHZ-176).
func prefixRequired(project string) error {
	return errs.E(errs.Invalid, errs.CodeProjectPrefixRequired,
		"The name of a service account of the project "+project+" starts with "+project+"- and goes on after it.",
		errs.WithFields(errs.FieldError{Pointer: "/name", Code: errs.CodeProjectPrefixRequired, Detail: "starts with " + project + "- and goes on after it"}))
}

// createServiceAccount writes the account in tx and returns the record as tx sees it.
func (m *Manager) createServiceAccount(ctx context.Context, tx meta.Tx, c Caller, in NewServiceAccount) (ServiceAccount, error) {
	id := NewID()
	qs := Queries(tx)
	projectID, err := bindProject(ctx, qs, in)
	if err != nil {
		return ServiceAccount{}, err
	}
	rows, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindServiceAccount), Name: in.Name, CreatedBy: c.Actor.ID})
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
		if postgres.IsState(err, "AI003") { // the trigger behind bindProject's name rule
			return ServiceAccount{}, prefixRequired(in.Project)
		}
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
		Subject: audit.Subject{Kind: "service_account", ID: id, Name: in.Name},
		Details: details(withProject(map[string]any{"source": sourceOf(c), "admin": in.Admin}, in.Project)),
	}); err != nil {
		return ServiceAccount{}, err
	}
	return loadServiceAccount(ctx, qs, in.Name)
}

// withProject adds the key of the account's project to the details of an audit
// event of a service account; a global account adds nothing (Q-AUTHZ-169).
func withProject(d map[string]any, project string) map[string]any {
	if project != "" {
		d["project"] = project
	}
	return d
}

// GetServiceAccount returns the account, or a 404.
func (m *Manager) GetServiceAccount(ctx context.Context, name string) (ServiceAccount, error) {
	var sa ServiceAccount
	err := m.db.Read(ctx, func(db q.DBTX) error {
		var err error
		sa, err = loadServiceAccount(ctx, q.New(db), name)
		return err
	})
	if err == ErrNotFound { //nolint:errorlint // sentinel returned unwrapped by loadServiceAccount
		return ServiceAccount{}, NotFound("The service account")
	}
	return sa, DBErr("principal.GetServiceAccount", err)
}

// loadServiceAccount reads the account; ErrNotFound when there is none.
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

// ListServiceAccounts lists service accounts ordered by name; after is the name
// of the last account of the previous page. project keeps the accounts of the
// project with that key, empty keeps every account (R-AUTHZ-026).
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
		return nil
	})
	return out, DBErr("principal.ListServiceAccounts", err)
}

// UpdateServiceAccount changes the description (R-AUTH-020; owners are P1).
//
// expected is the revision the caller loaded (If-Match) or errs.AnyRevision for the
// deliberate blind write (R-API-012).
func (m *Manager) UpdateServiceAccount(ctx context.Context, c Caller, name, description string, expected int64) (ServiceAccount, error) {
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetServiceAccount(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The service account")
		}
		id := rows[0].ID
		if err := checkRevision(ctx, qs, id, expected, "The service account"); err != nil {
			return err
		}
		if err := guardRoleHolder(ctx, qs, c, id); err != nil {
			return err
		}
		if err := qs.IdentityUpdateServiceAccount(ctx, q.IdentityUpdateServiceAccountParams{ID: id, Description: description}); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSAUpdated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: name},
			Details: details(withProject(map[string]any{"fields": []string{"description"}}, rows[0].Project)),
		})
	})
	if err != nil {
		return ServiceAccount{}, DBErr("principal.UpdateServiceAccount", err)
	}
	return m.GetServiceAccount(ctx, name)
}

// DisableServiceAccount disables an account: all its tokens fail with
// principal_disabled (R-AUTH-020). With revokeTokens it also revokes them.
func (m *Manager) DisableServiceAccount(ctx context.Context, c Caller, name string, revokeTokens bool) error {
	return DBErr("principal.DisableServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetServiceAccount(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The service account")
		}
		id := rows[0].ID
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
			rs, err := qs.IdentityRevokeAllOfPrincipal(ctx, q.IdentityRevokeAllOfPrincipalParams{PrincipalID: id, RevokedBy: c.Actor.ID, Reason: "principal_disabled"})
			if err != nil {
				return err
			}
			revoked = len(rs)
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSADisabled, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: name},
			Details: details(withProject(map[string]any{"tokensRevoked": revoked}, rows[0].Project)),
			Journal: details(map[string]string{"principalId": id, "name": name}),
		})
	}))
}

// EnableServiceAccount enables an account. Revoked tokens stay revoked.
func (m *Manager) EnableServiceAccount(ctx context.Context, c Caller, name string) error {
	return DBErr("principal.EnableServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetServiceAccount(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The service account")
		}
		if err := guardRoleHolder(ctx, qs, c, rows[0].ID); err != nil {
			return err
		}
		if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: rows[0].ID, Status: string(StatusActive)}); err != nil {
			return err
		}
		return NotifyPrincipals(m.notify, tx, ChangePrincipal, rows[0].ID)
	}))
}

// DeleteServiceAccount deletes an account with its tokens and memberships.
func (m *Manager) DeleteServiceAccount(ctx context.Context, c Caller, name string) error {
	return DBErr("principal.DeleteServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetServiceAccount(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The service account")
		}
		id := rows[0].ID
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
		memberships, err := qs.IdentityCountMembershipsOfMember(ctx, id)
		if err != nil {
			return err
		}
		if err := notifyAuthz(ctx, m.authz, tx, id); err != nil {
			return err
		}
		if _, err := qs.IdentityDeletePrincipal(ctx, id); err != nil {
			return err
		}
		if err := last.Check(ctx, qs); err != nil {
			return err
		}
		if err := NotifyPrincipals(m.notify, tx, ChangePrincipal, id); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSADeleted, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: name},
			Details: details(withProject(map[string]any{"tokensDeleted": tokens, "membershipsDeleted": memberships}, rows[0].Project)),
			Journal: details(map[string]string{"principalId": id, "name": name}),
		})
	}))
}
