package project

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Service is the domain API of projects (R-AUTHZ-026). The HTTP operations are thin
// handlers over it (internal/api/v1/projects_api.go): they add ETags, idempotency,
// paging and the declared permission, and call these functions.
//
// Every other method that writes runs in one transaction; Delete (Q-AUTHZ-192: the
// policies go one by one through the policy store, then one transaction closes the
// project). A write transaction notifies the change area authz
// (an assignment also repo_config, and project administrators' effect reaches the
// index of every replica through it), records its audit event of events_projects.go
// in that transaction, and returns the epoch the transaction produced for the header
// Trove-Authz-Epoch (R-AUTHZ-032). expected is the version the client saw (If-Match);
// errs.AnyRevision is the deliberate blind write.
//
// Errors: 404 not_found for an unknown project, user, group or repository; 409
// already_exists for a key that exists; 409 project_key_overlap; 409
// project_not_empty; 409 repository_assigned; 412 precondition_failed; 422
// validation_failed with the field codes of R-AUTHZ-026.
type Service interface {
	// List returns every project with its administrators and counts, ordered by key.
	// The caller filters to the projects it may see (an administrator of a project
	// sees that project only, Q-AUTHZ-180).
	List(ctx context.Context) ([]Project, error)
	// Get returns one project.
	Get(ctx context.Context, key string) (Project, error)
	// CreateIn creates a project on the caller's transaction, for an idempotent
	// create (R-API-014). *epoch is set by a hook that runs after the change log
	// raised it. The key must not overlap another project's key or a global service
	// account's name (409 project_key_overlap, Q-AUTHZ-176).
	CreateIn(ctx context.Context, tx meta.Tx, actor authz.Subject, in Input, epoch *int64) (Project, error)
	// Replace changes display name, description and switches; the key is the
	// project's own. It returns the project and the epoch.
	Replace(ctx context.Context, actor authz.Subject, key string, expected int64, in Input) (Project, int64, error)
	// Delete deletes an empty project: first its policies, one by one through the
	// policy store so that each deletion is audited and journaled, then the project
	// and its administrators (Q-AUTHZ-185). 409 project_not_empty while a repository
	// or a service account belongs to it. It returns the epoch.
	Delete(ctx context.Context, actor authz.Subject, key string, expected int64) (int64, error)
	// AddAdmin makes a user or a group (a principal reference such as "group:web-leads")
	// an administrator. created is false when it already was one.
	AddAdmin(ctx context.Context, actor authz.Subject, key, ref string) (admin PrincipalRef, created bool, epoch int64, err error)
	// RemoveAdmin removes an administrator; 404 when it was none.
	RemoveAdmin(ctx context.Context, actor authz.Subject, key, ref string) (epoch int64, err error)
	// Assign assigns a repository to the project. A repository of another project is
	// refused with 409 repository_assigned unless move is true; a move records an
	// unassignment and an assignment in one transaction. Assigning a repository to
	// the project it already belongs to is a no-op that answers success.
	Assign(ctx context.Context, actor authz.Subject, key, repository string, move bool) (epoch int64, err error)
	// Unassign removes the repository from the project; 404 when it does not belong
	// to this project.
	Unassign(ctx context.Context, actor authz.Subject, key, repository string) (epoch int64, err error)
	// LookupPrincipals returns active users and groups whose name starts with prefix,
	// for a project administrator who names principals in policies (Q-AUTHZ-182). kind
	// is "", user or group; limit is at most MaxLookup. The answer holds the kind and
	// the name, nothing else.
	LookupPrincipals(ctx context.Context, prefix string, kind PrincipalKind, limit int) ([]PrincipalRef, error)
}

// MaxLookup is the most results a lookup returns, and MinLookupPrefix the shortest
// prefix it accepts: a project administrator cannot list every user of the
// installation in one call (Q-AUTHZ-182).
const (
	MaxLookup       = 20
	MinLookupPrefix = 2
)

// PolicyDeleter is the one function of the policy store a project delete needs: the
// deletion of a policy by name with its If-Match version (errs.AnyRevision for none).
type PolicyDeleter interface {
	Delete(ctx context.Context, actor authz.Subject, name string, expected int64, opts ...authz.WriteOption) (int64, error)
}

// Options configure [New].
type Options struct {
	// DB runs the writes in READ COMMITTED transactions.
	DB meta.Runner
	// Changes notifies the change areas authz and repo_config in the transaction of a
	// write.
	Changes authz.Changes
	// Principals and Repositories are the read sides of the principal and repository
	// areas. The service reads the principal of an administrator reference and the
	// repository of an assignment in the transaction of the write instead, so that
	// the answer is the one the write commits against and not a cached one; they
	// stay options so that the composition names the dependency.
	Principals   principal.Store
	Repositories repository.Reader
	// Policies deletes the policies of a deleted project through the policy store;
	// *authz.Service is the implementation, a test wraps it to act between two steps of
	// a delete.
	Policies PolicyDeleter
	// Recorder records the events of events_projects.go in the transaction of the write.
	Recorder audit.Recorder
}

// New creates the service.
func New(o Options) Service {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &service{o: o}
}

type service struct{ o Options }
