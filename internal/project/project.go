package project

import (
	"regexp"
	"time"
)

// KeyRE is the rule of a project key (R-AUTHZ-026): 2 to 32 characters, lower-case
// letters, digits and hyphens, starting with a letter. The key is immutable.
var KeyRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// Limits of the free-text fields, the CHECKs of the table project.
const (
	MaxDisplayName = 128
	MaxDescription = 1024
)

// Rights are the two privilege switches an administrator of trove sets per
// project (R-AUTHZ-026). They bound what the project's administrators may do.
type Rights struct {
	// ManagePolicies lets them create, change and delete project policies, create and
	// enable project service accounts and issue their tokens, and look up users and
	// groups. Default true.
	ManagePolicies bool
	// ManageRepositories lets them create, change and delete the project's
	// repositories of every class and test the upstream of its remotes. Default false.
	ManageRepositories bool
}

// DefaultRights are the switches of a project created without them.
func DefaultRights() Rights { return Rights{ManagePolicies: true, ManageRepositories: false} }

// PrincipalKind is the kind of a project administrator or of a lookup result: a user
// or a group (Q-AUTHZ-183). Service accounts and CI mappings never administer.
type PrincipalKind string

// The two kinds that administer a project.
const (
	KindUser  PrincipalKind = "user"
	KindGroup PrincipalKind = "group"
)

// PrincipalRef names a user or a group the way the API does: "user:alice",
// "group:web-leads".
type PrincipalRef struct {
	Kind PrincipalKind
	Name string
	// ID is the principal-table ID; empty in a lookup result.
	ID string
}

// String is the reference form of the API.
func (p PrincipalRef) String() string { return string(p.Kind) + ":" + p.Name }

// Input is what a client writes to create or replace a project. The key is read
// only on create and never changes.
type Input struct {
	Key         string
	DisplayName string
	Description string
	Rights
}

// Project is a stored project as the service returns it.
type Project struct {
	// ID is the immutable identifier; the API addresses the project by key.
	ID          string
	Key         string
	DisplayName string
	Description string
	Rights
	// Admins are the administrators, users and groups, ordered by kind and name.
	Admins []PrincipalRef
	// Repositories, ServiceAccounts, Policies and CIMappings count what belongs to the
	// project; a project with a repository (in any lifecycle state), a service account
	// or a CI mapping of its tier is not empty and cannot be deleted (Q-AUTHZ-169,
	// Q-CI-26).
	Repositories    int
	ServiceAccounts int
	Policies        int
	CIMappings      int
	// Version is the ETag source. It moves with the fields of the PUT (display name,
	// description, switches) and with nothing else: administrators and assignments
	// leave it (Q-AUTHZ-185).
	Version   int64
	CreatedAt time.Time
	CreatedBy string
	UpdatedAt time.Time
	UpdatedBy string
}

// Empty reports whether the project has nothing that keeps it from being deleted.
func (p Project) Empty() bool {
	return p.Repositories == 0 && p.ServiceAccounts == 0 && p.CIMappings == 0
}
