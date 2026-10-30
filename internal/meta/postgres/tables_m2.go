package postgres

import (
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// Registrations of the tables the M2 migrations create (00014_remote_proxy to
// 00016_npm), so that every table carries its export class from its migration on
// (R-BAK-004) and the migration test passes before the packages that own them
// exist. Every one of them is data: a restore must bring back the cache entries
// with their blobs (unless backup.exclude.remote_caches drops them), the circuit
// state, the claims, the ownership and above all the npm tombstones, which exist
// to refuse a republish (R-REPO-049, R-NPM-047).
//
// The owning package takes its registrations over when it exists and deletes the
// lines here in the same change; a table registered twice panics at start-up, so a
// forgotten line is found at once. The sealed column of internal/secrets
// (repository_secrets.sealed, internal/repository/secret_columns.go) and the change
// areas of internal/cluster (cache_invalidation, upstream_state, claims,
// metadata_changed; internal/cluster/areas.go) are not part of this file.
func init() {
	// Remote repositories (00014_remote_proxy, R-REPO-024 to R-REPO-045).
	// remote_cache_entries references items and blobs, both of which load before it.
	tables.Register("remote_cache_entries", tables.Data, tables.Options{})
	tables.Register("remote_url_map", tables.Data, tables.Options{})
	tables.Register("remote_upstream_state", tables.Data, tables.Options{})
	tables.Register("remote_cache_invalidations", tables.Data, tables.Options{})
	tables.Register("repository_secrets", tables.Data, tables.Options{})

	// Virtual repositories, claims and ownership (00015_virtual_claims_ownership,
	// R-REPO-017 to R-REPO-023, R-AUTHZ-018).
	tables.Register("virtual_members", tables.Data, tables.Options{})
	tables.Register("virtual_deploy_targets", tables.Data, tables.Options{})
	tables.Register("namespace_claims", tables.Data, tables.Options{})
	tables.Register("virtual_pin_rules", tables.Data, tables.Options{})
	tables.Register("virtual_pin_members", tables.Data, tables.Options{})
	tables.Register("package_ownership", tables.Data, tables.Options{})
	tables.Register("package_owner", tables.Data, tables.Options{})

	// The npm format (00016_npm, R-NPM-006, R-NPM-008, R-NPM-024, R-NPM-047).
	tables.Register("npm_packages", tables.Data, tables.Options{})
	tables.Register("npm_versions", tables.Data, tables.Options{})
	tables.Register("npm_tombstones", tables.Data, tables.Options{})
	tables.Register("npm_remote_packages", tables.Data, tables.Options{})
	tables.Register("npm_remote_versions", tables.Data, tables.Options{})

	// repositories.claims_version is the ETag of the claims resource (R-REPO-023):
	// a restore raises it by 2^32 like every client-held revision (R-BAK-022).
	meta.RegisterRevisionColumn("repositories", "claims_version")
}
