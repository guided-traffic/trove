//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// m3Tables are the tables of the M3 migration (00017_nuget). Every one is data and
// NuGet has no transient table (R-NUGET-033).
var m3Tables = []string{"nuget_version", "nuget_tombstone", "nuget_symbol_package", "nuget_symbol_file"}

// TestM3Migrations checks the tables of milestone M3: their export classes, the
// absence of the trigram index and of any extension, the indexes, the integrity
// rules the database carries and the statements of the data port
// (docs/developer/database.md "M3 tables").
func TestM3Migrations(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	f := &m2Fixture{t: t, admin: admin}
	mustExec(t, admin, "SET search_path = "+schemaName)
	ctx := context.Background()
	qs := q.New(admin)

	t.Run("R-BAK-004 every M3 table exists and is a data table", func(t *testing.T) {
		for _, name := range m3Tables {
			got, ok := tables.Lookup(name)
			if !ok || got.Class != tables.Data {
				t.Errorf("%s registered as %+v (%v), want data", name, got, ok)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
	})

	t.Run("R-ARCH-026 the NuGet migration is an expand step without a contract or app requirement", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		if postgres.RequiredSchema() < 17 {
			t.Fatalf("RequiredSchema = %d, want at least 17", postgres.RequiredSchema())
		}
		found := false
		for _, s := range steps {
			if s.Version != 17 {
				continue
			}
			found = true
			if s.Phase != "expand" || s.NoTx || s.RequiresApp != "" {
				t.Errorf("%s: phase %q, no-transaction %v, requires-app %q", s.Name, s.Phase, s.NoTx, s.RequiresApp)
			}
		}
		if !found {
			t.Error("no migration 00017")
		}
	})

	t.Run("Q-NUGET-12 the migration creates no extension and no trigram index", func(t *testing.T) {
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm'"); n != 0 {
			t.Errorf("pg_trgm is installed (%d rows)", n)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexdef ILIKE '%trgm%'", schemaName); n != 0 {
			t.Errorf("%d trigram indexes", n)
		}
	})

	t.Run("R-NUGET-005 the search and latest-version indexes exist", func(t *testing.T) {
		search := scalar[string](t, admin, "SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = 'nuget_version_search'", schemaName)
		if !strings.Contains(search, "USING gin (search_doc)") {
			t.Errorf("nuget_version_search = %s", search)
		}
		latest := scalar[string](t, admin, "SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND indexname = 'nuget_version_latest'", schemaName)
		if !strings.Contains(latest, "(repository_id, lower_id, sort_key DESC)") || !strings.Contains(latest, "WHERE listed") {
			t.Errorf("nuget_version_latest = %s", latest)
		}
	})

	repo := f.repo("nuget-local", "nuget", "local")
	lead := f.blob()
	mustExec(t, admin, "UPDATE blobs SET sha512 = $1 WHERE sha256 = $2", sha512Of(7), lead)
	mustExec(t, admin, `INSERT INTO packages (repo_id, name, display_name) VALUES ($1, 'acme.widgets', 'Acme.Widgets')`, repo)
	pkg := scalar[int64](t, admin, "SELECT id FROM packages WHERE repo_id = $1 AND name = 'acme.widgets'", repo)
	version := func(v string) (versionID, itemID int64) {
		mustExec(t, admin, `INSERT INTO package_versions (package_id, version, display_version) VALUES ($1, $2, $2)`, pkg, v)
		versionID = scalar[int64](t, admin, "SELECT id FROM package_versions WHERE package_id = $1 AND version = $2", pkg, v)
		itemID = f.item(repo, "acme.widgets/"+v+"/acme.widgets."+v+".nupkg", lead)
		mustExec(t, admin, "UPDATE package_versions SET lead_item_id = $1 WHERE id = $2", itemID, versionID)
		return versionID, itemID
	}
	v1, _ := version("1.2.3")
	insert := func(id int64, p q.NuGetInsertVersionParams) error {
		p.VersionID = id
		p.RepositoryID = repo
		return qs.NuGetInsertVersion(ctx, p)
	}
	base := q.NuGetInsertVersionParams{
		LowerID: "acme.widgets", DeclaredID: "Acme.Widgets", OriginalVersion: "1.2.3", SortKey: []byte{1, 2, 3},
		PublishedAt: "2026-11-01T10:00:00Z", PackageTypes: []string{"dependency"}, Metadata: `{"title":"Widgets"}`,
		IDTokens: "acme widgets", Title: "Widget toolkit", Tags: "ui controls", Authors: "Acme", Summary: "Widgets", Description: "Buttons and sliders",
		Origin: "push",
	}

	t.Run("R-NUGET-010 NuGetInsertVersion builds a weighted search_doc that matches ID tokens and prefixes", func(t *testing.T) {
		if err := insert(v1, base); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			query string
			want  bool
		}{
			{"acme", true},
			{"widgets", true},
			{"wid:*", true},
			{"sliders", true},
			{"ui", true},
			{"tonsoft", false},
			{"idget:*", false},
		} {
			got := scalar[bool](t, admin, "SELECT search_doc @@ to_tsquery('simple', $1) FROM nuget_version WHERE version_id = $2", tc.query, v1)
			if got != tc.want {
				t.Errorf("search_doc @@ %q = %v, want %v", tc.query, got, tc.want)
			}
		}
		if w := scalar[string](t, admin, "SELECT search_doc::text FROM nuget_version WHERE version_id = $1", v1); !strings.Contains(w, "'acme':1A") {
			t.Errorf("search_doc = %s, want the ID token with weight A", w)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM nuget_version WHERE listed AND version_id = $1", v1); n != 1 {
			t.Errorf("a new version is listed: %d", n)
		}
	})

	t.Run("R-NUGET-005 the checks of nuget_version", func(t *testing.T) {
		v2, _ := version("1.2.4")
		bad := base
		bad.Origin = "import"
		if err := insert(v2, bad); sqlState(err) != "23514" {
			t.Errorf("origin import: %v", err)
		}
		bad = base
		bad.SortKey = []byte{}
		if err := insert(v2, bad); sqlState(err) != "23514" {
			t.Errorf("empty sort key: %v", err)
		}
		bad = base
		bad.LowerID = ""
		if err := insert(v2, bad); sqlState(err) != "23514" {
			t.Errorf("empty lower_id: %v", err)
		}
		// One NuGet row per version, and the version row has to exist.
		if err := insert(v1, base); sqlState(err) != "23505" {
			t.Errorf("second row for one version: %v", err)
		}
		if err := insert(v2+1000, base); sqlState(err) != "23503" {
			t.Errorf("row for a missing version: %v", err)
		}
	})

	t.Run("R-NUGET-007 NuGetSetListed drops the version from the latest-version index predicate", func(t *testing.T) {
		n, err := qs.NuGetSetListed(ctx, q.NuGetSetListedParams{Listed: false, VersionID: v1})
		if err != nil || n != 1 {
			t.Fatalf("unlist: %d, %v", n, err)
		}
		if scalar[bool](t, admin, "SELECT listed FROM nuget_version WHERE version_id = $1", v1) {
			t.Error("still listed")
		}
		if n, _ := qs.NuGetSetListed(ctx, q.NuGetSetListedParams{Listed: true, VersionID: v1}); n != 1 {
			t.Error("relist changed no row")
		}
	})

	t.Run("R-NUGET-006 NuGetVersionFacts reads the digest of the lead item's blob and ignores a trashed version", func(t *testing.T) {
		args := q.NuGetVersionFactsParams{RepositoryID: repo, LowerID: "acme.widgets", LowerVersion: "1.2.3"}
		rows, err := qs.NuGetVersionFacts(ctx, args)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%v, %v", rows, err)
		}
		if !rows[0].Listed || string(rows[0].Sha512) != string(sha512Of(7)) || !rows[0].Now.Valid {
			t.Errorf("facts = %+v", rows[0])
		}
		mustExec(t, admin, "UPDATE package_versions SET trashed_at = now() WHERE id = $1", v1)
		if rows, _ := qs.NuGetVersionFacts(ctx, args); len(rows) != 0 {
			t.Errorf("a trashed version has facts: %v", rows)
		}
		mustExec(t, admin, "UPDATE package_versions SET trashed_at = NULL WHERE id = $1", v1)
	})

	const by = "aaaaaaaa-0000-0000-0000-000000000001"
	t.Run("R-NUGET-006 the tombstone outlives the version, its item and its removal", func(t *testing.T) {
		got, err := qs.NuGetInsertTombstone(ctx, q.NuGetInsertTombstoneParams{RepositoryID: repo, At: "2026-11-01T11:00:00Z", By: by, VersionID: v1})
		if err != nil || string(got) != string(sha512Of(7)) {
			t.Fatalf("insert: %x, %v", got, err)
		}
		// A second removal keeps the first tombstone.
		if _, err := qs.NuGetInsertTombstone(ctx, q.NuGetInsertTombstoneParams{RepositoryID: repo, At: "2026-11-01T12:00:00Z", By: by, VersionID: v1}); err != nil {
			t.Fatal(err)
		}
		if n := f.count("nuget_tombstone", "repository_id = $1", repo); n != 1 {
			t.Fatalf("%d tombstones, want 1", n)
		}
		mustExec(t, admin, "DELETE FROM package_versions WHERE id = $1", v1)
		mustExec(t, admin, "DELETE FROM items WHERE repo_id = $1 AND path LIKE 'acme.widgets/1.2.3/%'", repo)
		rows, err := qs.NuGetTombstone(ctx, q.NuGetTombstoneParams{RepositoryID: repo, LowerID: "acme.widgets", LowerVersion: "1.2.3"})
		if err != nil || len(rows) != 1 || rows[0].Kind != "removed" || string(rows[0].Sha512) != string(sha512Of(7)) {
			t.Fatalf("tombstone after the version is gone: %+v, %v", rows, err)
		}
		if f.count("nuget_version", "version_id = $1", v1) != 0 {
			t.Error("the NuGet row outlived its version")
		}
	})

	t.Run("R-NUGET-006 NuGetDeleteTombstone removes a tombstone only for the same bytes", func(t *testing.T) {
		v, _ := version("1.2.3")
		if err := insert(v, base); err != nil {
			t.Fatal(err)
		}
		other := f.blob()
		mustExec(t, admin, "UPDATE blobs SET sha512 = $1 WHERE sha256 = $2", sha512Of(9), other)
		mustExec(t, admin, "UPDATE items SET blob_sha256 = $1 WHERE id = (SELECT lead_item_id FROM package_versions WHERE id = $2)", other, v)
		if n, err := qs.NuGetDeleteTombstone(ctx, v); err != nil || n != 0 {
			t.Fatalf("other bytes removed %d tombstones, %v", n, err)
		}
		mustExec(t, admin, "UPDATE items SET blob_sha256 = $1 WHERE id = (SELECT lead_item_id FROM package_versions WHERE id = $2)", lead, v)
		if n, err := qs.NuGetDeleteTombstone(ctx, v); err != nil || n != 1 {
			t.Fatalf("the same bytes removed %d tombstones, %v", n, err)
		}
	})

	t.Run("R-NUGET-006 the checks of nuget_tombstone", func(t *testing.T) {
		const ins = `INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind) VALUES ($1, $2, $3, $4, now(), $5, $6)`
		mustExec(t, admin, ins, repo, "acme.widgets", "5.0.0", sha512Of(1), by, "reserved")
		wantState(t, admin, "23505", ins, repo, "acme.widgets", "5.0.0", sha512Of(1), by, "removed")
		wantState(t, admin, "23514", ins, repo, "acme.widgets", "5.0.1", sha512Of(1), by, "revived")
		wantState(t, admin, "23514", ins, repo, "acme.widgets", "5.0.2", []byte{1, 2}, by, "removed")
		wantState(t, admin, "23503", ins, repo+1000, "acme.widgets", "5.0.3", sha512Of(1), by, "removed")
		// Deleting the repository removes its tombstones.
		other := f.repo("nuget-other", "nuget", "local")
		mustExec(t, admin, ins, other, "x", "1.0.0", sha512Of(1), by, "removed")
		mustExec(t, admin, "DELETE FROM repositories WHERE id = $1", other)
		if n := f.count("nuget_tombstone", "repository_id = $1", other); n != 0 {
			t.Errorf("%d tombstones of a deleted repository", n)
		}
	})

	t.Run("R-NUGET-020 symbol packages and files", func(t *testing.T) {
		v, _ := version("2.0.0")
		if err := insert(v, base); err != nil {
			t.Fatal(err)
		}
		sym := f.item(repo, "acme.widgets/2.0.0/acme.widgets.2.0.0.snupkg", f.blob())
		arg := q.NuGetInsertSymbolPackageParams{VersionID: v, ItemID: sym}
		if n, err := qs.NuGetInsertSymbolPackage(ctx, arg); err != nil || n != 1 {
			t.Fatalf("insert: %d, %v", n, err)
		}
		if n, err := qs.NuGetInsertSymbolPackage(ctx, arg); err != nil || n != 0 {
			t.Fatalf("a repeated push inserted %d rows, %v", n, err)
		}
		const file = `INSERT INTO nuget_symbol_file (repository_id, symbol_key, version_id, item_id, pdb_kind, checksum) VALUES ($1, $2, $3, $4, $5, $6)`
		pdb := f.item(repo, "acme.widgets/2.0.0/.symbols/acme.widgets.pdb", f.blob())
		mustExec(t, admin, file, repo, "acme.widgets.pdb/abc/acme.widgets.pdb", v, pdb, "portable", make([]byte, 32))
		wantState(t, admin, "23505", file, repo, "acme.widgets.pdb/abc/acme.widgets.pdb", v, pdb, "portable", nil)
		wantState(t, admin, "23514", file, repo, "k2", v, pdb, "managed", nil)
		wantState(t, admin, "23514", file, repo, "k3", v, pdb, "portable", []byte{1})
		wantState(t, admin, "23503", file, repo, "k4", v+1000, pdb, "portable", nil)
		// The pending index covers the unindexed packages only.
		if n := scalar[int](t, admin, "SELECT count(*) FROM nuget_symbol_package WHERE indexed_at IS NULL"); n != 1 {
			t.Errorf("%d pending symbol packages, want 1", n)
		}
		// The symbol rows follow the version when it is removed permanently.
		mustExec(t, admin, "DELETE FROM package_versions WHERE id = $1", v)
		if f.count("nuget_symbol_package", "version_id = $1", v) != 0 || f.count("nuget_symbol_file", "version_id = $1", v) != 0 {
			t.Error("symbol rows outlived their version")
		}
	})
}

// sha512Of returns a 64-byte digest filled with b.
func sha512Of(b byte) []byte {
	out := make([]byte, 64)
	for i := range out {
		out[i] = b
	}
	return out
}
