package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
)

// The service accounts of a project (Q-AUTHZ-169, Q-AUTHZ-209) are addressed by a [qname.Object]: the
// project key and the name within the project. The principal row stores the name as
// "<project key>/<name>" (principal.name), so [ServiceAccount.Name] of a project's account is that
// stored name and its Project the key; [ServiceAccount.Object] gives the address back. The API shows the
// name within the project and the principal reference "sa:<project>/<name>". The methods without a
// project in their name (CreateServiceAccount, GetServiceAccount, ...) address instance-wide accounts
// only: the account of a project does not answer to them. An Object with an empty Project passed to a
// method below is a programming error and answers 404.
//
// The decision whether the caller may act on the project (an Owner of it, or a holder of
// identities:write) is the API layer's, as for the instance-wide accounts. The tokens of a project's
// account are the token service's: it creates them with CreateRequest.OwnerID of the account and
// Delegated set for an Owner, and revokes them with RevokeAll.

// projectOnly turns an address without a project into the 404 of an account that is not there.
func projectOnly(o qname.Object) (qname.Object, error) {
	if o.Project == "" {
		return o, NotFound("The service account")
	}
	return o, nil
}

// CreateProjectServiceAccount creates the service account in.Name of the project: unique within the
// project (409 already_exists), no group member, no role holder (in.Admin: 422 role_not_allowed). The
// project must exist (404).
func (m *Manager) CreateProjectServiceAccount(ctx context.Context, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	if project == "" {
		return ServiceAccount{}, NotFound("The project")
	}
	return m.createAccount(ctx, c, project, in)
}

// CreateProjectServiceAccountTx is CreateProjectServiceAccount on the caller's transaction, for an
// idempotent create (R-API-014).
func (m *Manager) CreateProjectServiceAccountTx(ctx context.Context, tx meta.Tx, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	if project == "" {
		return ServiceAccount{}, NotFound("The project")
	}
	return m.createAccountTx(ctx, tx, c, project, in)
}

// GetProjectServiceAccount reads the service account o of its project; 404 for an instance-wide account,
// for another project's and for none.
func (m *Manager) GetProjectServiceAccount(ctx context.Context, o qname.Object) (ServiceAccount, error) {
	o, err := projectOnly(o)
	if err != nil {
		return ServiceAccount{}, err
	}
	return m.getAccount(ctx, o)
}

// ListProjectServiceAccounts lists the accounts of one project ordered by name; after is the stored name
// of the last account of the previous page, query a case-insensitive substring of the name within the
// project.
func (m *Manager) ListProjectServiceAccounts(ctx context.Context, project, query, after string, limit int) ([]ServiceAccount, error) {
	var out []ServiceAccount
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityListProjectServiceAccounts(ctx, q.IdentityListProjectServiceAccountsParams{
			Project: project, After: after, Query: query, MaxRows: int32(clampLimit(limit)), //nolint:gosec // bounded
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, ServiceAccount{Revision: r.Revision, ID: r.ID, Name: r.Name, Description: r.Description, Status: Status(r.Status), CreatedAt: r.CreatedAt.Time, Project: r.Project})
		}
		return nil
	})
	return out, DBErr("principal.ListProjectServiceAccounts", err)
}

// UpdateProjectServiceAccount changes the description; expected is the revision the caller loaded or
// errs.AnyRevision.
func (m *Manager) UpdateProjectServiceAccount(ctx context.Context, c Caller, o qname.Object, description string, expected int64) (ServiceAccount, error) {
	o, err := projectOnly(o)
	if err != nil {
		return ServiceAccount{}, err
	}
	return m.updateAccount(ctx, c, o, description, expected)
}

// DisableProjectServiceAccount disables the account; with revokeTokens its tokens are revoked too.
func (m *Manager) DisableProjectServiceAccount(ctx context.Context, c Caller, o qname.Object, revokeTokens bool) error {
	o, err := projectOnly(o)
	if err != nil {
		return err
	}
	return m.disableAccount(ctx, c, o, revokeTokens)
}

// EnableProjectServiceAccount enables the account.
func (m *Manager) EnableProjectServiceAccount(ctx context.Context, c Caller, o qname.Object) error {
	o, err := projectOnly(o)
	if err != nil {
		return err
	}
	return m.enableAccount(ctx, c, o)
}

// DeleteProjectServiceAccount deletes the account with its tokens: they are revoked in the transaction
// that removes the row, before it goes.
func (m *Manager) DeleteProjectServiceAccount(ctx context.Context, c Caller, o qname.Object) error {
	o, err := projectOnly(o)
	if err != nil {
		return err
	}
	return m.deleteAccount(ctx, c, o)
}
