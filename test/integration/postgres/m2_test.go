//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// m2Tables are the tables of the M2 migrations (00014_remote_proxy to 00016_npm).
// Every one is data (R-REPO-049, R-NPM-047).
var m2Tables = []string{
	"remote_cache_entries", "remote_url_map", "remote_upstream_state", "remote_cache_invalidations", "repository_secrets",
	"virtual_members", "virtual_deploy_targets", "namespace_claims", "virtual_pin_rules", "virtual_pin_members",
	"package_ownership", "package_owner",
	"npm_packages", "npm_versions", "npm_tombstones", "npm_remote_packages", "npm_remote_versions",
}

const (
	m2Alice = "aaaaaaaa-0000-0000-0000-000000000001"
	m2Bob   = "aaaaaaaa-0000-0000-0000-000000000002"
)

// m2Fixture creates repositories and blobs for the checks below.
type m2Fixture struct {
	t     *testing.T
	admin *pgx.Conn
	next  byte
}

func (f *m2Fixture) repo(key, format, class string) int64 {
	f.t.Helper()
	mustExec(f.t, f.admin, "INSERT INTO trove.repositories (key, format, class) VALUES ($1, $2, $3)", key, format, class)
	return scalar[int64](f.t, f.admin, "SELECT id FROM trove.repositories WHERE key = $1", key)
}

func (f *m2Fixture) blob() []byte {
	f.t.Helper()
	f.next++
	sum := make([]byte, 32)
	for i := range sum {
		sum[i] = f.next
	}
	mustExec(f.t, f.admin, `INSERT INTO trove.blobs (sha256, sha1, md5, sha512, size) VALUES ($1, $2, $3, $4, 10)`,
		sum, make([]byte, 20), make([]byte, 16), make([]byte, 64))
	return sum
}

func (f *m2Fixture) item(repo int64, path string, blob []byte) int64 {
	f.t.Helper()
	mustExec(f.t, f.admin, `INSERT INTO trove.items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, $2, '', 'file', $3, 10)`, repo, path, blob)
	return scalar[int64](f.t, f.admin, "SELECT id FROM trove.items WHERE repo_id = $1 AND path = $2", repo, path)
}

func (f *m2Fixture) count(table string, where string, args ...any) int {
	f.t.Helper()
	return scalar[int](f.t, f.admin, fmt.Sprintf("SELECT count(*) FROM trove.%s WHERE %s", table, where), args...)
}

// TestM2Migrations checks the tables of milestone M2: their export classes, the
// compatibility with the migration gate, the revision registration and the integrity
// rules the database carries (docs/developer/database.md "M2 tables").
func TestM2Migrations(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	f := &m2Fixture{t: t, admin: admin}

	t.Run("R-BAK-004 every M2 table exists and is a data table", func(t *testing.T) {
		for _, name := range m2Tables {
			got, ok := tables.Lookup(name)
			if !ok || got.Class != tables.Data {
				t.Errorf("%s registered as %+v (%v), want data", name, got, ok)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
	})

	t.Run("R-ARCH-026 the M2 migrations are expand steps without a contract or app requirement", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		if postgres.RequiredSchema() < 16 {
			t.Fatalf("RequiredSchema = %d, want at least 16", postgres.RequiredSchema())
		}
		for _, s := range steps {
			if s.Version < 14 || s.Version > 16 {
				continue
			}
			if s.Phase != "expand" || s.NoTx || s.RequiresApp != "" {
				t.Errorf("%s: phase %q, no-transaction %v, requires-app %q", s.Name, s.Phase, s.NoTx, s.RequiresApp)
			}
		}
	})

	t.Run("R-ARCH-018 every registered change area has its cluster_epoch row", func(t *testing.T) {
		var want []string
		for _, a := range cluster.Areas(cluster.ChangeArea) {
			want = append(want, a.Name)
		}
		for _, name := range []string{cluster.AreaClaims, cluster.AreaCacheInvalidation, cluster.AreaUpstreamState} {
			a, ok := cluster.LookupArea(name)
			if !ok || a.Class != cluster.ChangeArea || a.PayloadVersion != 1 {
				t.Errorf("area %s = %+v (%v), want a registered change area of payload version 1", name, a, ok)
			}
		}
		if a, ok := cluster.LookupArea(cluster.AreaMetadataChanged); !ok || a.Class != cluster.SignalArea {
			t.Errorf("area metadata_changed = %+v (%v), want a registered signal area", a, ok)
		}
		for _, area := range want {
			if !scalar[bool](t, admin, "SELECT EXISTS (SELECT 1 FROM trove.cluster_epoch WHERE area = $1 AND epoch = 0)", area) {
				t.Errorf("no cluster_epoch row for the change area %s", area)
			}
		}
		if scalar[bool](t, admin, "SELECT EXISTS (SELECT 1 FROM trove.cluster_epoch WHERE area = 'metadata_changed')") {
			t.Error("a signal area has no cluster_epoch row")
		}
	})

	t.Run("R-ARCH-009 repositories.claims_version is a registered revision column that starts at 1", func(t *testing.T) {
		if !meta.IsRevisionColumn("repositories", "claims_version") {
			t.Fatal("repositories.claims_version is not registered")
		}
		missing, err := postgres.CheckRevisionColumns(context.Background(), admin, schemaName, meta.DefaultRevisionRegistry())
		if err != nil || len(missing) != 0 {
			t.Fatalf("unregistered revision columns: %v, %v", missing, err)
		}
		// The check is not vacuous: without the registration the column is found.
		bare := meta.NewRevisionRegistry()
		missing, _ = postgres.CheckRevisionColumns(context.Background(), admin, schemaName, bare)
		found := false
		for _, m := range missing {
			found = found || m == "repositories.claims_version"
		}
		if !found {
			t.Errorf("an empty registry did not report repositories.claims_version: %v", missing)
		}
		f.repo("rv-check", "npm", "local")
		if v := scalar[int64](t, admin, "SELECT claims_version FROM trove.repositories WHERE key = 'rv-check'"); v != 1 {
			t.Errorf("claims_version = %d, want 1", v)
		}
		wantState(t, admin, "23514", "UPDATE trove.repositories SET claims_version = 0 WHERE key = 'rv-check'")
	})

	t.Run("R-REPO-025 remote_cache_entries checks", func(t *testing.T) {
		remote := f.repo("npm-remote", "npm", "remote")
		b1, b2 := f.blob(), f.blob()
		item := f.item(remote, "lodash/-/lodash-4.17.21.tgz", b1)
		const cols = `repo_id, path, variant, kind, item_id, blob_sha256, upstream_url, fetched_at, validated_at, expires_at, last_used_at`
		ins := `INSERT INTO trove.remote_cache_entries (` + cols + `) VALUES ($1, $2, $3, $4, $5, $6, 'https://registry.example/x', now(), now(), $7, now())`

		mustExec(t, admin, ins, remote, "lodash/-/lodash-4.17.21.tgz", "", "artifact", item, b1, nil)
		mustExec(t, admin, ins, remote, "lodash", "full", "metadata", nil, b2, "2030-01-01T00:00:00Z")
		// A second variant of the same path is a second entry; the same variant is not.
		mustExec(t, admin, ins, remote, "lodash", "abbreviated", "metadata", nil, b2, "2030-01-01T00:00:00Z")
		wantState(t, admin, "23505", ins, remote, "lodash", "full", "metadata", nil, b2, "2030-01-01T00:00:00Z")

		t.Run("an artifact needs an item", func(t *testing.T) {
			wantState(t, admin, "23514", ins, remote, "a.tgz", "", "artifact", nil, b1, nil)
		})
		t.Run("a metadata entry has no item", func(t *testing.T) {
			wantState(t, admin, "23514", ins, remote, "b", "", "metadata", item, b1, "2030-01-01T00:00:00Z")
		})
		t.Run("a metadata entry needs an expiry and an artifact has none", func(t *testing.T) {
			wantState(t, admin, "23514", ins, remote, "c", "", "metadata", nil, b1, nil)
			wantState(t, admin, "23514", ins, remote, "d.tgz", "", "artifact", item, b1, "2030-01-01T00:00:00Z")
		})
		t.Run("the gone mark exists on metadata only", func(t *testing.T) {
			mustExec(t, admin, "UPDATE trove.remote_cache_entries SET upstream_gone_at = now() WHERE path = 'lodash' AND variant = 'full'")
			wantState(t, admin, "23514", "UPDATE trove.remote_cache_entries SET upstream_gone_at = now() WHERE kind = 'artifact'")
		})
		t.Run("the kind is artifact or metadata", func(t *testing.T) {
			wantState(t, admin, "23514", ins, remote, "e", "", "negative", nil, b1, "2030-01-01T00:00:00Z")
		})
		t.Run("the blob reference is guarded: a blob that is not live is refused", func(t *testing.T) {
			dying := f.blob()
			mustExec(t, admin, "UPDATE trove.blobs SET state = 'deleting' WHERE sha256 = $1", dying)
			wantState(t, admin, "AS001", ins, remote, "f", "", "metadata", nil, dying, "2030-01-01T00:00:00Z")
		})
		t.Run("the id is generated and unique", func(t *testing.T) {
			if n := scalar[int](t, admin, "SELECT count(DISTINCT id) FROM trove.remote_cache_entries"); n != f.count("remote_cache_entries", "true") {
				t.Errorf("distinct ids = %d", n)
			}
			wantState(t, admin, "428C9", "UPDATE trove.remote_cache_entries SET id = id + 1000 WHERE path = 'lodash' AND variant = 'full'")
		})
		t.Run("replacing the blob of an entry queues the replaced blob for collection", func(t *testing.T) {
			mustExec(t, admin, "DELETE FROM trove.blob_gc_queue")
			fresh := f.blob()
			mustExec(t, admin, "UPDATE trove.remote_cache_entries SET blob_sha256 = $1 WHERE path = 'lodash' AND variant = 'abbreviated'", fresh)
			if f.count("blob_gc_queue", "sha256 = $1", b2) != 1 {
				t.Error("the replaced blob was not queued by blob_ref_release_update")
			}
			if f.count("blob_gc_queue", "sha256 = $1", fresh) != 0 {
				t.Error("the new blob must not be queued")
			}
			mustExec(t, admin, "UPDATE trove.remote_cache_entries SET fetched_at = now() WHERE path = 'lodash' AND variant = 'abbreviated'")
			if f.count("blob_gc_queue", "true") != 1 {
				t.Error("an update that keeps the blob must queue nothing")
			}
		})
		t.Run("deleting an entry queues its blob; deleting the artifact item removes the entry", func(t *testing.T) {
			mustExec(t, admin, "DELETE FROM trove.blob_gc_queue")
			mustExec(t, admin, "DELETE FROM trove.remote_cache_entries WHERE path = 'lodash' AND variant = 'full'")
			if f.count("blob_gc_queue", "sha256 = $1", b2) != 1 {
				t.Error("deleting the entry did not queue its blob")
			}
			mustExec(t, admin, "DELETE FROM trove.items WHERE id = $1", item)
			if f.count("remote_cache_entries", "kind = 'artifact' AND repo_id = $1", remote) != 0 {
				t.Error("the entry of a deleted artifact item remains")
			}
		})
	})

	t.Run("R-REPO-029 remote_upstream_state holds one row per repository with bounded counters", func(t *testing.T) {
		remote := f.repo("npm-circuit", "npm", "remote")
		mustExec(t, admin, "INSERT INTO trove.remote_upstream_state (repo_id) VALUES ($1)", remote)
		if f.count("remote_upstream_state", "repo_id = $1 AND consecutive_failures = 0 AND open_until IS NULL", remote) != 1 {
			t.Error("a new row is closed with no failures")
		}
		wantState(t, admin, "23505", "INSERT INTO trove.remote_upstream_state (repo_id) VALUES ($1)", remote)
		wantState(t, admin, "23514", "UPDATE trove.remote_upstream_state SET consecutive_failures = -1 WHERE repo_id = $1", remote)
		wantState(t, admin, "23514", "UPDATE trove.remote_upstream_state SET last_failure = $2 WHERE repo_id = $1", remote, strings.Repeat("x", 1025))
		mustExec(t, admin, "UPDATE trove.remote_upstream_state SET last_failure = $2 WHERE repo_id = $1", remote, strings.Repeat("x", 1024))
	})

	t.Run("R-REPO-043 remote_url_map keeps one URL per repository and path", func(t *testing.T) {
		remote := f.repo("npm-urls", "npm", "remote")
		mustExec(t, admin, "INSERT INTO trove.remote_url_map (repo_id, path, upstream_url) VALUES ($1, 'a/-/a-1.tgz', 'https://cdn.example/a-1.tgz')", remote)
		wantState(t, admin, "23505", "INSERT INTO trove.remote_url_map (repo_id, path, upstream_url) VALUES ($1, 'a/-/a-1.tgz', 'https://other.example/a-1.tgz')", remote)
	})

	t.Run("R-REPO-045 remote_cache_invalidations: scope, fold bookkeeping, byte-wise prefixes", func(t *testing.T) {
		remote := f.repo("npm-inval", "npm", "remote")
		mustExec(t, admin, "INSERT INTO trove.remote_cache_invalidations (repo_id, prefix, scope) VALUES ($1, '@acme', 'metadata')", remote)
		mustExec(t, admin, "INSERT INTO trove.remote_cache_invalidations (repo_id, prefix, scope) VALUES ($1, '', 'all')", remote)
		wantState(t, admin, "23514", "INSERT INTO trove.remote_cache_invalidations (repo_id, prefix, scope) VALUES ($1, 'x', 'positive')", remote)
		if f.count("remote_cache_invalidations", "repo_id = $1 AND folded_at IS NULL", remote) != 2 {
			t.Error("a new invalidation is unfolded")
		}
		mustExec(t, admin, "UPDATE trove.remote_cache_invalidations SET folded_at = now() WHERE prefix = '@acme' AND repo_id = $1", remote)
		if f.count("remote_cache_invalidations", "repo_id = $1 AND folded_at IS NULL", remote) != 1 {
			t.Error("the fold mark did not take")
		}
		// "@acme" covers "@acme/util" and not "@acmecorp/util": the prefix match is on the "C" collation.
		if got := scalar[string](t, admin, "SELECT collation_name FROM information_schema.columns WHERE table_schema = 'trove' AND table_name = 'remote_cache_invalidations' AND column_name = 'prefix'"); got != "C" {
			t.Errorf("prefix collation = %q, want C", got)
		}
	})

	t.Run("R-REPO-033 repository_secrets holds a sealed value or a reference, never both", func(t *testing.T) {
		remote := f.repo("npm-secret", "npm", "remote")
		mustExec(t, admin, "INSERT INTO trove.repository_secrets (repo_id, field, sealed) VALUES ($1, '/remote/password', 'sealed-v1')", remote)
		mustExec(t, admin, `INSERT INTO trove.repository_secrets (repo_id, field, secret_ref) VALUES ($1, '/remote/token', '{"file":"npm-token"}')`, remote)
		wantState(t, admin, "23514", `INSERT INTO trove.repository_secrets (repo_id, field, sealed, secret_ref) VALUES ($1, '/remote/both', 's', '{}')`, remote)
		wantState(t, admin, "23514", "INSERT INTO trove.repository_secrets (repo_id, field) VALUES ($1, '/remote/none')", remote)
		wantState(t, admin, "23514", "INSERT INTO trove.repository_secrets (repo_id, field, sealed) VALUES ($1, 'password', 's')", remote)
		wantState(t, admin, "23514", "INSERT INTO trove.repository_secrets (repo_id, field, sealed) VALUES ($1, '/a b', 's')", remote)
		wantState(t, admin, "23505", "INSERT INTO trove.repository_secrets (repo_id, field, sealed) VALUES ($1, '/remote/password', 'again')", remote)
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", remote)
		if f.count("repository_secrets", "repo_id = $1", remote) != 0 {
			t.Error("the secrets of a deleted repository remain")
		}
	})

	t.Run("R-REPO-017 a member cannot be deleted while listed", func(t *testing.T) {
		virt := f.repo("npm-all", "npm", "virtual")
		local := f.repo("npm-local", "npm", "local")
		rem := f.repo("npm-rem", "npm", "remote")
		mustExec(t, admin, "INSERT INTO trove.virtual_members (virtual_id, position, member_id) VALUES ($1, 0, $2), ($1, 1, $3)", virt, local, rem)
		wantRestrict(t, admin, "DELETE FROM trove.repositories WHERE id = $1", local)
		wantState(t, admin, "23505", "INSERT INTO trove.virtual_members (virtual_id, position, member_id) VALUES ($1, 2, $2)", virt, local)
		wantState(t, admin, "23505", "INSERT INTO trove.virtual_members (virtual_id, position, member_id) VALUES ($1, 1, $2)", virt, f.repo("npm-third", "npm", "local"))
		wantState(t, admin, "23514", "INSERT INTO trove.virtual_members (virtual_id, position, member_id) VALUES ($1, 5, $1)", virt)
		wantState(t, admin, "23514", "INSERT INTO trove.virtual_members (virtual_id, position, member_id) VALUES ($1, -1, $2)", virt, rem)
		// Deleting the virtual repository takes its member rows and releases the members.
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", virt)
		if f.count("virtual_members", "virtual_id = $1", virt) != 0 {
			t.Error("member rows of a deleted virtual repository remain")
		}
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", local)
	})

	t.Run("R-REPO-021 a deploy target cannot be deleted while it is the target", func(t *testing.T) {
		virt := f.repo("npm-vt", "npm", "virtual")
		target := f.repo("npm-vt-local", "npm", "local")
		mustExec(t, admin, "INSERT INTO trove.virtual_deploy_targets (virtual_id, target_id) VALUES ($1, $2)", virt, target)
		wantRestrict(t, admin, "DELETE FROM trove.repositories WHERE id = $1", target)
		wantState(t, admin, "23505", "INSERT INTO trove.virtual_deploy_targets (virtual_id, target_id) VALUES ($1, $2)", virt, target)
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", virt)
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", target)
	})

	t.Run("R-REPO-023 an auto claim cannot be global", func(t *testing.T) {
		local := f.repo("npm-claims", "npm", "local")
		mustExec(t, admin, "INSERT INTO trove.namespace_claims (repo_id, pattern, source, scope) VALUES ($1, '@acme/*', 'manual', 'global')", local)
		mustExec(t, admin, "INSERT INTO trove.namespace_claims (repo_id, pattern, source) VALUES ($1, 'left-pad', 'auto')", local)
		if got := scalar[string](t, admin, "SELECT scope FROM trove.namespace_claims WHERE pattern = 'left-pad'"); got != "members" {
			t.Errorf("default scope = %q, want members", got)
		}
		wantState(t, admin, "23514", "INSERT INTO trove.namespace_claims (repo_id, pattern, source, scope) VALUES ($1, 'evil', 'auto', 'global')", local)
		wantState(t, admin, "23514", "INSERT INTO trove.namespace_claims (repo_id, pattern, source) VALUES ($1, 'x', 'import')", local)
		wantState(t, admin, "23514", "INSERT INTO trove.namespace_claims (repo_id, pattern, source) VALUES ($1, '', 'manual')", local)
		wantState(t, admin, "23514", "INSERT INTO trove.namespace_claims (repo_id, pattern, source) VALUES ($1, $2, 'manual')", local, strings.Repeat("a", 257))
		wantState(t, admin, "23505", "INSERT INTO trove.namespace_claims (repo_id, pattern, source) VALUES ($1, '@acme/*', 'manual')", local)
	})

	t.Run("R-REPO-023 pin rules hold 1 to 50 patterns and their members are protected from deletion", func(t *testing.T) {
		virt := f.repo("npm-pins", "npm", "virtual")
		pinned := f.repo("npm-pinned", "npm", "remote")
		mustExec(t, admin, "INSERT INTO trove.virtual_pin_rules (virtual_id, position, package_patterns) VALUES ($1, 0, ARRAY['@acme/*'])", virt)
		wantState(t, admin, "23514", "INSERT INTO trove.virtual_pin_rules (virtual_id, position, package_patterns) VALUES ($1, 1, ARRAY[]::text[])", virt)
		wantState(t, admin, "23514", "INSERT INTO trove.virtual_pin_rules (virtual_id, position, package_patterns) VALUES ($1, 1, $2)", virt, fiftyOne())
		wantState(t, admin, "23514", "INSERT INTO trove.virtual_pin_rules (virtual_id, position, package_patterns) VALUES ($1, -1, ARRAY['x'])", virt)
		mustExec(t, admin, "INSERT INTO trove.virtual_pin_members (virtual_id, position, member_id) VALUES ($1, 0, $2)", virt, pinned)
		wantState(t, admin, "23503", "INSERT INTO trove.virtual_pin_members (virtual_id, position, member_id) VALUES ($1, 9, $2)", virt, pinned)
		wantRestrict(t, admin, "DELETE FROM trove.repositories WHERE id = $1", pinned)
		mustExec(t, admin, "DELETE FROM trove.virtual_pin_rules WHERE virtual_id = $1", virt)
		if f.count("virtual_pin_members", "virtual_id = $1", virt) != 0 {
			t.Error("deleting a pin rule must delete its members")
		}
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", pinned)
	})

	t.Run("R-AUTHZ-018 the owner unique index", func(t *testing.T) {
		repo := f.repo("npm-owned", "npm", "local")
		domain := fmt.Sprintf("repo:%d", repo)
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'user', 'alice'), ($2, 'user', 'bob')", m2Alice, m2Bob)
		mustExec(t, admin, "INSERT INTO trove.package_ownership (domain, format, package_name, state) VALUES ($1, 'npm', '@acme/ui', 'claimed')", domain)
		wantState(t, admin, "23505", "INSERT INTO trove.package_ownership (domain, format, package_name, state) VALUES ($1, 'npm', '@acme/ui', 'claimed')", domain)
		wantState(t, admin, "23514", "INSERT INTO trove.package_ownership (domain, format, package_name, state) VALUES ('repo:abc', 'npm', 'x', 'claimed')")
		wantState(t, admin, "23514", "INSERT INTO trove.package_ownership (domain, format, package_name, state) VALUES ($1, 'npm', 'y', 'owned')", domain)
		mustExec(t, admin, "INSERT INTO trove.package_ownership (domain, format, package_name, state) VALUES ('domain:acme', 'npm', 'z', 'released')")

		ownerSQL := `INSERT INTO trove.package_owner (id, domain, format, package_name, owner_kind, principal_id, ci_issuer_id, ci_project_id, display, reason)
			VALUES (gen_random_uuid(), $1, 'npm', '@acme/ui', $2, $3, $4, $5, 'x', 'first-publish')`
		mustExec(t, admin, ownerSQL, domain, "principal", m2Alice, nil, nil)
		// Two replicas inserting the same owner of the same name: exactly one succeeds.
		wantState(t, admin, "23505", ownerSQL, domain, "principal", m2Alice, nil, nil)
		mustExec(t, admin, ownerSQL, domain, "principal", m2Bob, nil, nil)
		issuer := "cccccccc-0000-0000-0000-000000000001"
		mustExec(t, admin, ownerSQL, domain, "ci_project", nil, issuer, "4711")
		wantState(t, admin, "23505", ownerSQL, domain, "ci_project", nil, issuer, "4711")
		mustExec(t, admin, ownerSQL, domain, "ci_project", nil, issuer, "4712")
		// The kind decides which columns are set.
		wantState(t, admin, "23514", ownerSQL, domain, "principal", nil, nil, nil)
		wantState(t, admin, "23514", ownerSQL, domain, "principal", m2Alice, issuer, "1")
		wantState(t, admin, "23514", ownerSQL, domain, "ci_project", nil, issuer, nil)
		wantState(t, admin, "23514", ownerSQL, domain, "ci_project", nil, issuer, strings.Repeat("9", 65))
		wantState(t, admin, "23514", `INSERT INTO trove.package_owner (id, domain, format, package_name, owner_kind, principal_id, display, reason)
			VALUES (gen_random_uuid(), $1, 'npm', '@acme/ui', 'principal', $2, 'x', 'guess')`, domain, m2Bob)
		// An owner needs the ownership row.
		wantState(t, admin, "23503", `INSERT INTO trove.package_owner (id, domain, format, package_name, owner_kind, principal_id, display, reason)
			VALUES (gen_random_uuid(), $1, 'npm', 'unclaimed', 'principal', $2, 'x', 'added')`, domain, m2Alice)

		// Deleting a principal removes its owner entries (R-AUTHZ-018 rule 6) and leaves the name.
		mustExec(t, admin, "DELETE FROM trove.principal WHERE id = $1", m2Bob)
		if f.count("package_owner", "principal_id = $1", m2Bob) != 0 || f.count("package_ownership", "domain = $1", domain) != 1 {
			t.Error("deleting a principal must remove its owner rows and keep the ownership row")
		}
		// The added_by reference is cleared, not cascaded.
		mustExec(t, admin, "UPDATE trove.package_owner SET added_by = $1 WHERE principal_id = $1", m2Alice)
		mustExec(t, admin, "DELETE FROM trove.principal WHERE id = $1", m2Alice)
		// Removing the ownership row removes its owners.
		mustExec(t, admin, "DELETE FROM trove.package_ownership WHERE domain = $1", domain)
		if f.count("package_owner", "domain = $1", domain) != 0 {
			t.Error("owners of a removed ownership row remain")
		}
	})

	t.Run("R-NPM-002 name_fold is unique per repository", func(t *testing.T) {
		a := f.repo("npm-fold-a", "npm", "local")
		b := f.repo("npm-fold-b", "npm", "local")
		pkg := func(repo int64, name string) int64 {
			mustExec(t, admin, "INSERT INTO trove.packages (repo_id, name, display_name) VALUES ($1, $2, $2)", repo, name)
			return scalar[int64](t, admin, "SELECT id FROM trove.packages WHERE repo_id = $1 AND name = $2", repo, name)
		}
		ins := "INSERT INTO trove.npm_packages (package_id, repo_id, name_fold) VALUES ($1, $2, $3)"
		mustExec(t, admin, ins, pkg(a, "acme-ui"), a, "acmeui")
		// acme_ui is a different package name that folds to the same string: the look-alike race rule.
		wantState(t, admin, "23505", ins, pkg(a, "acme_ui"), a, "acmeui")
		mustExec(t, admin, ins, pkg(b, "acme-ui"), b, "acmeui")
		if got := scalar[string](t, admin, "SELECT dist_tags::text FROM trove.npm_packages WHERE repo_id = $1", a); got != "{}" {
			t.Errorf("dist_tags default = %q", got)
		}
		// A second row for one package is refused; deleting the package removes the npm row.
		wantState(t, admin, "23505", ins, scalar[int64](t, admin, "SELECT id FROM trove.packages WHERE repo_id = $1 AND name = 'acme-ui'", a), a, "other")
		mustExec(t, admin, "DELETE FROM trove.packages WHERE repo_id = $1 AND name = 'acme-ui'", a)
		if f.count("npm_packages", "repo_id = $1 AND name_fold = 'acmeui'", a) != 0 {
			t.Error("the npm row outlived its package")
		}
	})

	t.Run("R-NPM-006 npm_versions rows follow their version and carry the limits", func(t *testing.T) {
		repo := f.repo("npm-versions", "npm", "local")
		mustExec(t, admin, "INSERT INTO trove.packages (repo_id, name, display_name) VALUES ($1, 'v', 'v')", repo)
		pid := scalar[int64](t, admin, "SELECT id FROM trove.packages WHERE repo_id = $1", repo)
		mustExec(t, admin, "INSERT INTO trove.package_versions (package_id, version, display_version) VALUES ($1, '1.0.0', '1.0.0')", pid)
		vid := scalar[int64](t, admin, "SELECT id FROM trove.package_versions WHERE package_id = $1", pid)
		const ins = `INSERT INTO trove.npm_versions (version_id, manifest, integrity, shasum, deprecated, file_count, unpacked_size, has_install_script, has_shrinkwrap, readme, publish_tag, published_at)
			VALUES ($1, '{}', 'sha512-AAAA', $2, $3, $4, $5, false, false, $6, 'latest', now())`
		sha := strings.Repeat("a", 40)
		wantState(t, admin, "23514", ins, vid, sha, strings.Repeat("d", 1025), 1, 1, nil)
		wantState(t, admin, "23514", ins, vid, sha, nil, -1, 1, nil)
		wantState(t, admin, "23514", ins, vid, sha, nil, 1, -1, nil)
		wantState(t, admin, "23514", ins, vid, sha, nil, 1, 1, strings.Repeat("r", 65537))
		mustExec(t, admin, ins, vid, sha, nil, 3, 1000, strings.Repeat("r", 65536))
		wantState(t, admin, "23505", ins, vid, sha, nil, 3, 1000, nil)
		mustExec(t, admin, "DELETE FROM trove.package_versions WHERE id = $1", vid)
		if f.count("npm_versions", "version_id = $1", vid) != 0 {
			t.Error("the npm row outlived its version")
		}
	})

	t.Run("R-NPM-008 a tombstone outlives the version row", func(t *testing.T) {
		repo := f.repo("npm-tomb", "npm", "local")
		mustExec(t, admin, "INSERT INTO trove.packages (repo_id, name, display_name) VALUES ($1, 'gone', 'gone')", repo)
		pid := scalar[int64](t, admin, "SELECT id FROM trove.packages WHERE repo_id = $1", repo)
		mustExec(t, admin, "INSERT INTO trove.package_versions (package_id, version, display_version) VALUES ($1, '1.0.0', '1.0.0')", pid)
		const tomb = `INSERT INTO trove.npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by)
			VALUES ($1, 'gone', '1.0.0', 'sha512-AAAA', $2, now(), $3)`
		mustExec(t, admin, tomb, repo, "unpublish", m2Alice)
		wantState(t, admin, "23505", tomb, repo, "journal", m2Alice)
		wantState(t, admin, "23514", `INSERT INTO trove.npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by)
			VALUES ($1, 'gone', '1.0.1', 'sha512-AAAA', 'purge', now(), $2)`, repo, m2Alice)
		// The actor is a uuid without a foreign key: it survives the deletion of the principal.
		mustExec(t, admin, "DELETE FROM trove.package_versions WHERE package_id = $1", pid)
		mustExec(t, admin, "DELETE FROM trove.packages WHERE id = $1", pid)
		if f.count("npm_tombstones", "repo_id = $1 AND name = 'gone' AND version = '1.0.0'", repo) != 1 {
			t.Error("the tombstone went with the version or the package")
		}
		// Only the deletion of the repository itself removes it.
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", repo)
		if f.count("npm_tombstones", "repo_id = $1", repo) != 0 {
			t.Error("the tombstone of a deleted repository remains")
		}
	})

	t.Run("R-NPM-024 npm_remote_versions follow their package document and the digest has 32 bytes", func(t *testing.T) {
		remote := f.repo("npm-remote-docs", "npm", "remote")
		const pkg = "INSERT INTO trove.npm_remote_packages (repo_id, name, doc_sha256) VALUES ($1, 'evil', $2)"
		wantState(t, admin, "23514", pkg, remote, make([]byte, 31))
		mustExec(t, admin, pkg, remote, make([]byte, 32))
		wantState(t, admin, "23505", pkg, remote, make([]byte, 32))
		mustExec(t, admin, "INSERT INTO trove.npm_remote_versions (repo_id, name, version) VALUES ($1, 'evil', '1.0.1')", remote)
		if !scalar[bool](t, admin, "SELECT listed FROM trove.npm_remote_versions WHERE repo_id = $1", remote) {
			t.Error("a version is listed by default")
		}
		wantState(t, admin, "23503", "INSERT INTO trove.npm_remote_versions (repo_id, name, version) VALUES ($1, 'unknown', '1.0.0')", remote)
		mustExec(t, admin, "DELETE FROM trove.npm_remote_packages WHERE repo_id = $1 AND name = 'evil'", remote)
		if f.count("npm_remote_versions", "repo_id = $1", remote) != 0 {
			t.Error("version records outlived their package document")
		}
	})

	t.Run("R-REPO-049 deleting a repository removes every M2 row that names it", func(t *testing.T) {
		repo := f.repo("npm-sweep", "npm", "remote")
		blob := f.blob()
		mustExec(t, admin, "INSERT INTO trove.remote_cache_entries (repo_id, path, kind, blob_sha256, upstream_url, fetched_at, validated_at, expires_at, last_used_at) VALUES ($1, 'p', 'metadata', $2, 'https://r.example/p', now(), now(), now(), now())", repo, blob)
		mustExec(t, admin, "INSERT INTO trove.remote_url_map (repo_id, path, upstream_url) VALUES ($1, 'p', 'https://r.example/p')", repo)
		mustExec(t, admin, "INSERT INTO trove.remote_upstream_state (repo_id) VALUES ($1)", repo)
		mustExec(t, admin, "INSERT INTO trove.remote_cache_invalidations (repo_id, prefix, scope) VALUES ($1, '', 'all')", repo)
		mustExec(t, admin, "INSERT INTO trove.namespace_claims (repo_id, pattern, source) VALUES ($1, 'x', 'manual')", repo)
		mustExec(t, admin, "INSERT INTO trove.npm_remote_packages (repo_id, name, doc_sha256) VALUES ($1, 'p', $2)", repo, make([]byte, 32))
		mustExec(t, admin, "INSERT INTO trove.npm_remote_versions (repo_id, name, version) VALUES ($1, 'p', '1.0.0')", repo)
		mustExec(t, admin, "DELETE FROM trove.blob_gc_queue")
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE id = $1", repo)
		for _, table := range []string{"remote_cache_entries", "remote_url_map", "remote_upstream_state", "remote_cache_invalidations", "namespace_claims", "npm_remote_packages", "npm_remote_versions"} {
			if n := f.count(table, "repo_id = $1", repo); n != 0 {
				t.Errorf("%s: %d rows of the deleted repository remain", table, n)
			}
		}
		if f.count("blob_gc_queue", "sha256 = $1", blob) != 1 {
			t.Error("the cache entry's blob was not queued when the repository went")
		}
	})
}

// fiftyOne is an array literal of 51 patterns, one more than a pin rule may hold.
func fiftyOne() []string {
	out := make([]string, 51)
	for i := range out {
		out[i] = fmt.Sprintf("p%d", i)
	}
	return out
}
