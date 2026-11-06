// Package project is the domain service of projects (R-AUTHZ-026, Q-AUTHZ-206 to
// Q-AUTHZ-209, Q-AUTHZ-215 to Q-AUTHZ-217): the projects, their members with one of
// the four roles, the move of a repository from one project to another (R-REPO-060,
// Q-REPO-217), and the lookup of users and groups an Owner names as members and in
// policies.
//
// What it does not own: the grant index and every decision about what a member may
// do (internal/authz answers MembershipsOn, RoleOn and OwnsOn on the index of the
// request and gives a role its actions in the decision path); project policies
// (authz.Service, with their `project` member); the repositories of a project and
// their creation (internal/repository); project service accounts and their tokens
// (internal/auth). The service reads and writes the rows of project and
// project_member and the project of a repository, notifies the change areas, and
// records the audit events of internal/audit (events_projects.go).
//
// The service does not decide whether the caller may call it: the management API's
// shared authorization step does (the declarations of api/openapi.yaml). It checks
// the rules of the data: a unique key, members that are users or groups and no
// auto-join group, a project that keeps an Owner unless an administrator of trove
// acts, a move that nothing of the source project names, a delete of an empty
// project only. docs/developer/projects.md lists the contract.
package project
