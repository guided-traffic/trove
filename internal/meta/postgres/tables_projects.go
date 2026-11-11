package postgres

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// Registrations of the tables of 00019_projects (R-AUTHZ-026, R-BAK-004), so that
// the migration test passes and every table carries its export class from its
// migration on. Both are data: a restore brings back projects, their
// members with their roles and the grants of project policies (authz_project_policy_entry,
// Q-AUTHZ-174). project and project_member load before authz_policy rows that name
// a project, and after principal, which both reference; the restore orders tables
// by their foreign keys.
func init() {
	tables.Register("project", tables.Data, tables.Options{})
	tables.Register("project_member", tables.Data, tables.Options{})
	tables.Register("authz_project_policy_entry", tables.Data, tables.Options{})

	// visibility_settings (00073_visibility, R-API-056): the installation's default level, one row.
	tables.Register("visibility_settings", tables.Data, tables.Options{})

	// project.version is the ETag of the project (R-AUTHZ-026): a restore raises it
	// by 2^32 like every client-held revision (R-BAK-022).
	meta.RegisterRevisionColumn("project", "version")
}
