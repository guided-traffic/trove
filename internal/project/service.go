package project

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// Service is the domain API of projects (R-AUTHZ-026). The HTTP operations are thin
// handlers over it (internal/api/v1/projects_api.go): they add ETags, idempotency,
// paging and the declared permission, and call these functions.
//
// Every method that writes runs in one transaction except Delete (Q-AUTHZ-192: the
// policies go one by one through the policy store, then one transaction closes the
// project). A write transaction notifies the change area authz (a move also
// repo_config; a member's role reaches the grant index of every replica through it),
// records its audit event of internal/audit/events_projects.go in that transaction,
// and returns the epoch the transaction produced for the header Trove-Authz-Epoch
// (R-AUTHZ-032). expected is the version the client saw (If-Match);
// errs.AnyRevision is the deliberate blind write.
//
// The service does not decide whether the caller may call it: the management API's
// shared step does (the declarations of api/openapi.yaml, R-API-008) with the
// engine's questions (authz.Decider RoleOn, OwnsOn, MembershipsOn, HoldsOn). It
// checks the rules of the data, and takes from the caller the one fact that changes
// a rule: whether it holds projects:write (bypassLastOwner).
//
// Errors: 404 not_found for an unknown project, user, group or repository; 409
// already_exists for a key that exists; 409 project_not_empty; 409 last_owner; 409
// repository_in_use; 412 precondition_failed; 422 principal_not_allowed; 422
// validation_failed with the field codes of R-AUTHZ-026.
type Service interface {
	// List returns every project with its members and counts, ordered by key. The
	// caller filters to the projects it may see (a member sees its projects only,
	// Q-AUTHZ-207).
	List(ctx context.Context) ([]Project, error)
	// Get returns one project.
	Get(ctx context.Context, key string) (Project, error)
	// CreateIn creates a project on the caller's transaction, for an idempotent
	// create (R-API-014), with the users and groups of in.Owners as its first Owners.
	// *epoch is set by a hook that runs after the change log raised it.
	CreateIn(ctx context.Context, tx meta.Tx, actor authz.Subject, in Input, epoch *int64) (Project, error)
	// Replace changes display name and description; the key is the project's own.
	// It returns the project and the epoch.
	Replace(ctx context.Context, actor authz.Subject, key string, expected int64, in Input) (Project, int64, error)
	// Delete deletes an empty project: first its policies, one by one through the
	// policy store so that each deletion is audited and journaled, then the project
	// and its member entries (Q-AUTHZ-185). 409 project_not_empty while a repository,
	// a service account or a CI mapping of its tier belongs to it. It returns the epoch.
	Delete(ctx context.Context, actor authz.Subject, key string, expected int64) (int64, error)
	// Members returns the members of a project with their role, ordered by kind and name
	// (Q-AUTHZ-207); 404 for an unknown project.
	Members(ctx context.Context, key string) ([]Member, error)
	// AddMember makes a user or a group (a principal reference such as "group:web-leads") a member
	// with the role, or, when it already is one, sets the role (created is false): the idempotent
	// PUT of /projects/{projectKey}/members/{principalRef}. A reference to a service account, a CI
	// mapping, a Bastion registration, anonymous, authenticated or an auto-join group is 422
	// principal_not_allowed; an unknown user or group 404. Demoting the last Owner is 409
	// last_owner unless bypassLastOwner (the caller holds projects:write, Q-AUTHZ-217). The same
	// role again records nothing and answers the current epoch (Q-AUTHZ-193).
	AddMember(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypassLastOwner bool) (member Member, created bool, epoch int64, err error)
	// SetRole changes the role of a member; 404 when the principal is no member. The last-Owner
	// rule of AddMember applies.
	SetRole(ctx context.Context, actor authz.Subject, key, ref string, role Role, bypassLastOwner bool) (member Member, epoch int64, err error)
	// RemoveMember removes a member; 404 when it was none. Removing the last Owner is 409
	// last_owner unless bypassLastOwner.
	RemoveMember(ctx context.Context, actor authz.Subject, key, ref string, bypassLastOwner bool) (epoch int64, err error)
	// SetVisibility sets the project's level (R-API-057, R-AUTHZ-033): lowering it takes every repository whose artifacts or wiki
	// lie above it down to it in the same transaction, each with a new configuration version. expected is the version the client
	// saw; 422 validation_failed for a value that is no level, 409 public_not_allowed while the default endpoint admits no request
	// without a credential, 412 for a stale version. It returns the project, the lowered repositories with their levels before,
	// and the epoch. The level it already has changes nothing.
	SetVisibility(ctx context.Context, actor authz.Subject, key string, expected int64, level string) (Project, []Lowered, int64, error)
	// MoveRepository moves a repository to another project (R-REPO-060, Q-REPO-217): the address
	// changes, the id and the content do not. It locks both projects and the repository, refuses
	// with 409 repository_in_use (problem member `references`, [Reference]) while an object of the
	// source project names the repository (a virtual repository's member list, pin rules or
	// deployment target, a key selector of a project policy, an inline grant of a CI mapping of the
	// project's tier) or while a virtual repository has members or a deployment target, and with
	// 409 already_exists when the target has the key. A target equal to the source changes nothing.
	// The caller has decided that the actor may move it: repositories:write-config, or Owner of both
	// projects, which is restricted: a restricted move is refused as well while a key selector of a
	// global policy names the repository (kind global_policy, Q-REPO-232), whose grant would follow it.
	// It returns the new name and the epoch.
	MoveRepository(ctx context.Context, actor authz.Subject, from qname.Repo, toProject string, restricted bool) (to qname.Repo, epoch int64, err error)
	// LookupPrincipals returns active users and groups whose name starts with prefix,
	// for an Owner of a project who names principals in policies (Q-AUTHZ-182). kind
	// is "", user or group; limit is at most MaxLookup. The answer holds the kind and
	// the name, nothing else.
	LookupPrincipals(ctx context.Context, prefix string, kind PrincipalKind, limit int) ([]PrincipalRef, error)
}

// MaxLookup is the most results a lookup returns, and MinLookupPrefix the shortest
// prefix it accepts: a project Owner cannot list every user of the
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
	// areas. The service reads the principal of a member reference and the repository
	// of a move in the transaction of the write instead, so that the answer is the one
	// the write commits against and not a cached one; they stay options so that the
	// composition names the dependency.
	Principals   principal.Store
	Repositories repository.Reader
	// Policies deletes the policies of a deleted project through the policy store;
	// *authz.Service is the implementation, a test wraps it to act between two steps of
	// a delete.
	Policies PolicyDeleter
	// Recorder records the events of events_projects.go in the transaction of the write.
	Recorder audit.Recorder
	// PublicAvailable reports whether the default endpoint admits requests without a credential (R-EP-010): while it does not, a
	// write that sets a level to public answers 409 public_not_allowed (R-AUTHZ-033 rule 5). Nil means it does not.
	PublicAvailable func() bool
}

// New creates the service.
func New(o Options) Service {
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &service{o: o}
}

type service struct{ o Options }
