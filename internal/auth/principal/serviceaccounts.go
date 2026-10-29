package principal

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
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
}

// NewServiceAccount is the request of CreateServiceAccount.
type NewServiceAccount struct {
	Name        string
	Description string
	// Admin binds the system role admin in the transaction that creates the
	// account (`trove admin service-account create --admin`, R-AUTH-015). It
	// needs an [AdminBinder]; without one the call fails with [ErrAdminBinding].
	Admin bool
}

// CreateServiceAccount creates a service account (R-AUTH-020).
func (m *Manager) CreateServiceAccount(ctx context.Context, c Caller, in NewServiceAccount) (ServiceAccount, error) {
	if !ValidServiceAccountName(in.Name) {
		return ServiceAccount{}, errs.E(errs.Invalid, errs.CodeValidation, "The service-account name does not match ^[a-z0-9][a-z0-9-]{1,62}$.")
	}
	if in.Admin && m.admin == nil {
		return ServiceAccount{}, ErrAdminBinding
	}
	id := NewID()
	err := m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityInsertPrincipal(ctx, q.IdentityInsertPrincipalParams{ID: id, Kind: string(KindServiceAccount), Name: in.Name, CreatedBy: c.Actor.ID})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errs.E(errs.Conflict, errs.CodeConflict, "A service account with this name exists.")
		}
		if err := qs.IdentityInsertServiceAccount(ctx, q.IdentityInsertServiceAccountParams{ID: id, Description: in.Description}); err != nil {
			return err
		}
		if in.Admin {
			if err := m.admin.BindAdmin(ctx, tx, id, c.Actor.ID); err != nil {
				return err
			}
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSACreated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: in.Name},
			Details: details(map[string]any{"source": sourceOf(c), "admin": in.Admin}),
		})
	})
	if err != nil {
		return ServiceAccount{}, DBErr("principal.CreateServiceAccount", err)
	}
	return m.GetServiceAccount(ctx, in.Name)
}

// GetServiceAccount returns the account, or a 404.
func (m *Manager) GetServiceAccount(ctx context.Context, name string) (ServiceAccount, error) {
	var sa ServiceAccount
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityGetServiceAccount(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNotFound
		}
		r := rows[0]
		sa = ServiceAccount{Revision: r.Revision, ID: r.ID, Name: r.Name, Description: r.Description, Status: Status(r.Status), CreatedAt: r.CreatedAt.Time}
		return nil
	})
	if err == ErrNotFound { //nolint:errorlint // sentinel returned unwrapped above
		return ServiceAccount{}, NotFound("The service account")
	}
	return sa, DBErr("principal.GetServiceAccount", err)
}

// ListServiceAccounts lists service accounts ordered by name; after is the name
// of the last account of the previous page.
func (m *Manager) ListServiceAccounts(ctx context.Context, query, after string, limit int) ([]ServiceAccount, error) {
	var out []ServiceAccount
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityListServiceAccounts(ctx, q.IdentityListServiceAccountsParams{
			After: after, Query: query, MaxRows: int32(clampLimit(limit)), //nolint:gosec // bounded
		})
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range rows {
			out = append(out, ServiceAccount{Revision: r.Revision, ID: r.ID, Name: r.Name, Description: r.Description, Status: Status(r.Status), CreatedAt: r.CreatedAt.Time})
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
		if err := qs.IdentityUpdateServiceAccount(ctx, q.IdentityUpdateServiceAccountParams{ID: id, Description: description}); err != nil {
			return err
		}
		return m.record(ctx, tx, audit.Event{
			Type: audit.EventSAUpdated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: name},
			Details: details(map[string]any{"fields": []string{"description"}}),
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
		if _, err := qs.IdentitySetPrincipalStatus(ctx, q.IdentitySetPrincipalStatusParams{ID: id, Status: string(StatusDisabled)}); err != nil {
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
			Details: details(map[string]any{"tokensRevoked": revoked}),
			Journal: details(map[string]string{"principalId": id, "name": name}),
		})
	}))
}

// EnableServiceAccount enables an account. Revoked tokens stay revoked.
func (m *Manager) EnableServiceAccount(ctx context.Context, name string) error {
	return DBErr("principal.EnableServiceAccount", m.db.InTx(ctx, func(tx meta.Tx) error {
		qs := Queries(tx)
		rows, err := qs.IdentityGetServiceAccount(ctx, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return NotFound("The service account")
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
		tokens, err := qs.IdentityCountTokensOfPrincipal(ctx, id)
		if err != nil {
			return err
		}
		memberships, err := qs.IdentityCountMembershipsOfMember(ctx, id)
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
			Type: audit.EventSADeleted, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
			Subject: audit.Subject{Kind: "service_account", ID: id, Name: name},
			Details: details(map[string]any{"tokensDeleted": tokens, "membershipsDeleted": memberships}),
			Journal: details(map[string]string{"principalId": id, "name": name}),
		})
	}))
}
