// Package project is the domain service of projects, the unit of delegated
// administration (R-AUTHZ-026, Q-AUTHZ-169 to Q-AUTHZ-185): the projects with their
// switches and administrators, the assignment of repositories to them, and the
// lookup of users and groups a project administrator names in policies.
//
// What it does not own: the grant index and every decision about what a project
// administrator may do (internal/authz answers ProjectsOn, AdministersOn and
// AdministersRepositoryOn on the index of the request); project policies
// (authz.Service, with their `project` member); repositories and their creation by
// a project administrator (internal/repository); project service accounts and
// their tokens (internal/auth). The service only reads and writes the rows of
// project, project_admin and the project column of repositories, notifies the
// change areas, and records the audit events of internal/audit (events_projects.go).
//
// The service does not decide whether the caller may call it: the management API's
// shared authorization step does (the declarations of api/openapi.yaml). It checks
// the rules of the data: a unique and non-overlapping key, an administrator that is
// a user or a group, a repository that exists, an assignment that does not take
// another project's repository without a move, a delete of an empty project only.
// docs/developer/projects.md lists the contract.
package project
