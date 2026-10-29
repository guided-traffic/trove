package postgres

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// Registrations of the tables the M1 migrations create (00006_audit to
// 00012_endpoints) for the packages of milestone M1, so that every table carries
// its export class from its migration on (R-BAK-004) and the migration test passes
// before those packages exist. The classes are the specification's: sessions,
// login flows and idempotency keys are transient, everything else is data.
//
// The owning package takes its registrations over when it exists and deletes the
// lines here in the same change; a table registered twice panics at start-up, so a
// forgotten line is found at once. The sealed-column registry of internal/secrets
// and the change areas of internal/cluster (`auth`, `authz`, `repo_config`, whose
// cluster_epoch rows the migrations seed) are not part of this file.
func init() {
	// Audit and revocation journal (00006_audit, R-OPS-014 to R-OPS-017, R-HA-054).
	// audit_event is partitioned; its partitions export through the parent.
	tables.Register("audit_event", tables.Data, tables.Options{})
	tables.Register("audit_policy", tables.Data, tables.Options{})
	tables.Register("revocation_outbox", tables.Data, tables.Options{})

	// Identity (00007_identity, R-AUTH-016 to R-AUTH-023, R-AUTH-040). A principal
	// names its creator, a foreign key into its own table: a restore loads
	// created_by as NULL and fills it afterwards.
	tables.Register("principal", tables.Data, tables.Options{CycleBreak: "created_by"})
	tables.Register("app_user", tables.Data, tables.Options{})
	tables.Register("user_group", tables.Data, tables.Options{})
	tables.Register("group_membership", tables.Data, tables.Options{})
	tables.Register("service_account", tables.Data, tables.Options{})
	tables.Register("api_token", tables.Data, tables.Options{})
	tables.Register("auth_settings", tables.Data, tables.Options{})

	// Login state (00008_login, R-AUTH-005 to R-AUTH-014, R-AUTH-038). Sessions and
	// flows hold ID tokens and PKCE verifiers: a restore ends them all (R-AUTH-015).
	// The lockout survives a restore, so a locked account stays locked.
	tables.Register("user_identity", tables.Data, tables.Options{})
	tables.Register("ui_session", tables.Transient, tables.Options{})
	tables.Register("oidc_login_flow", tables.Transient, tables.Options{})
	tables.Register("cli_login", tables.Transient, tables.Options{})
	tables.Register("auth_lockout", tables.Data, tables.Options{})

	// Management API conventions (00009_api, R-API-014, R-API-021, R-ARCH-021).
	// A restore opens the backup's canary rows before it loads anything (R-BAK-020).
	tables.Register("api_idempotency_keys", tables.Transient, tables.Options{})
	tables.Register("user_preferences", tables.Data, tables.Options{})
	tables.Register("secret_key_canary", tables.Data, tables.Options{})

	// Authorization (00011_authz, R-AUTHZ-021, R-AUTHZ-022, R-AUTHZ-028).
	tables.Register("authz_policy", tables.Data, tables.Options{})
	tables.Register("authz_policy_selector", tables.Data, tables.Options{})
	tables.Register("authz_policy_pattern", tables.Data, tables.Options{})
	tables.Register("authz_policy_entry", tables.Data, tables.Options{})
	tables.Register("authz_role_binding", tables.Data, tables.Options{})

	// Endpoint exposure (00012_endpoints, R-EP-012).
	tables.Register("endpoint_exposure_rule", tables.Data, tables.Options{})

	// Revision columns that clients hold as an ETag (R-API-012, R-BAK-022): a
	// restore raises them by 2^32 so that no If-Match taken before it matches
	// different content afterwards. repositories.config_version is the ETag of
	// R-REPO-011; the tables that carry the others are new.
	meta.RegisterRevisionColumn("repositories", "config_version")
	meta.RegisterRevisionColumn("authz_policy", "version")
	meta.RegisterRevisionColumn("endpoint_exposure_rule", "revision")
	meta.RegisterRevisionColumn("user_preferences", "revision")
	// principal.revision is the ETag of a user, a group and a service account; auth_settings.revision of
	// the runtime authentication settings (00013_identity_revisions, R-API-012).
	meta.RegisterRevisionColumn("principal", "revision")
	meta.RegisterRevisionColumn("auth_settings", "revision")
}
