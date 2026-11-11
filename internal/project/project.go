package project

import "time"

// The rule of a project key (R-AUTHZ-026) is qname.ValidProjectKey: 2 to 32 characters, lower-case letters, digits and
// hyphens, starting with a letter. The key is immutable.

// Limits of the free-text fields, the CHECKs of the table project.
const (
	MaxDisplayName = 128
	MaxDescription = 1024
)

// PrincipalKind is the kind of a project member or of a lookup result: a user or a
// group (Q-AUTHZ-207, Q-AUTHZ-215). Service accounts and CI mappings are no members.
type PrincipalKind string

// The two kinds that are members of a project.
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
	// Owners are the references of the users and groups ("user:alice",
	// "group:web-leads") a create makes the first Owners of the project (Q-AUTHZ-208);
	// a replace ignores them, the members have operations of their own.
	Owners []string
}

// Project is a stored project as the service returns it.
type Project struct {
	// ID is the immutable identifier; the API addresses the project by key.
	ID          string
	Key         string
	DisplayName string
	Description string
	// Visibility is the project's level: private, internal or public (R-AUTHZ-033). A repository's levels are at most this.
	Visibility string
	// Members are the users and groups that are members, with their role, ordered by kind and name.
	Members []Member
	// Repositories, ServiceAccounts, Policies and CIMappings count what belongs to the
	// project; a project with a repository (in any lifecycle state), a service account
	// or a CI mapping of its tier is not empty and cannot be deleted (Q-AUTHZ-169,
	// Q-CI-26).
	Repositories    int
	ServiceAccounts int
	Policies        int
	CIMappings      int
	// LogicalBytes is the size the project is charged (Q-STOR-155): the sum of the folded logical sizes of its
	// repositories' files (R-STOR-003), each file in full whether another project holds the same content. It
	// lags a write by at most one fold interval.
	LogicalBytes int64
	// Version is the ETag source. It moves with the fields of the PUT (display name,
	// description) and with nothing else: members and repositories leave it (Q-AUTHZ-185).
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
