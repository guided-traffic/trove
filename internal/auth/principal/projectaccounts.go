package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

// The service accounts of a project (Q-AUTHZ-169, Q-AUTHZ-209) are addressed by a [qname.Object]: the
// project key and the name within the project. The principal row stores the name as
// "<project key>/<name>" (principal.name), so [ServiceAccount.Name] of a project's account is that
// stored name and its Project the key; the API shows the name within the project and the principal
// reference "sa:<project>/<name>". The methods without a project in their name (CreateServiceAccount,
// GetServiceAccount, ...) address instance-wide accounts only: a bare name that belongs to a project's
// account answers not found there. An Object with an empty Project passed to a method below is a
// programming error and answers 404.
//
// The decision whether the caller may act on the project (an Owner of it, or a holder of the
// capability) is the API layer's, as for the instance-wide accounts.

func projectAccountNotImplemented(what string) error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The operation principal."+what+" is not implemented yet.")
}

// CreateProjectServiceAccount creates the service account in.Name of the project: unique within the
// project (409 already_exists), no group member, no role holder. The project must exist (404). in.Project
// and in.Admin are ignored; the project comes from the argument.
func (m *Manager) CreateProjectServiceAccount(ctx context.Context, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	return ServiceAccount{}, projectAccountNotImplemented("CreateProjectServiceAccount")
}

// CreateProjectServiceAccountTx is CreateProjectServiceAccount on the caller's transaction, for an
// idempotent create (R-API-014).
func (m *Manager) CreateProjectServiceAccountTx(ctx context.Context, tx meta.Tx, c Caller, project string, in NewServiceAccount) (ServiceAccount, error) {
	return ServiceAccount{}, projectAccountNotImplemented("CreateProjectServiceAccountTx")
}

// GetProjectServiceAccount reads the service account o of its project; 404 for an instance-wide account,
// for another project's and for none.
func (m *Manager) GetProjectServiceAccount(ctx context.Context, o qname.Object) (ServiceAccount, error) {
	return ServiceAccount{}, projectAccountNotImplemented("GetProjectServiceAccount")
}

// ListProjectServiceAccounts lists the accounts of one project ordered by name; after is the stored name
// of the last account of the previous page, query a case-insensitive substring of the name within the
// project.
func (m *Manager) ListProjectServiceAccounts(ctx context.Context, project, query, after string, limit int) ([]ServiceAccount, error) {
	return nil, projectAccountNotImplemented("ListProjectServiceAccounts")
}

// UpdateProjectServiceAccount changes the description; expected is the revision the caller loaded or
// errs.AnyRevision.
func (m *Manager) UpdateProjectServiceAccount(ctx context.Context, c Caller, o qname.Object, description string, expected int64) (ServiceAccount, error) {
	return ServiceAccount{}, projectAccountNotImplemented("UpdateProjectServiceAccount")
}

// DisableProjectServiceAccount disables the account; with revokeTokens its tokens are revoked too.
func (m *Manager) DisableProjectServiceAccount(ctx context.Context, c Caller, o qname.Object, revokeTokens bool) error {
	return projectAccountNotImplemented("DisableProjectServiceAccount")
}

// EnableProjectServiceAccount enables the account.
func (m *Manager) EnableProjectServiceAccount(ctx context.Context, c Caller, o qname.Object) error {
	return projectAccountNotImplemented("EnableProjectServiceAccount")
}

// DeleteProjectServiceAccount deletes the account with its tokens.
func (m *Manager) DeleteProjectServiceAccount(ctx context.Context, c Caller, o qname.Object) error {
	return projectAccountNotImplemented("DeleteProjectServiceAccount")
}
