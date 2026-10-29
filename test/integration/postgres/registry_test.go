//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func problems(t *testing.T, admin *pgx.Conn, reg *tables.Registry) []tables.Problem {
	t.Helper()
	ps, err := postgres.CheckTableRegistry(context.Background(), admin, schemaName, reg)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestTableRegistry(t *testing.T) {
	st := getStack(t)

	t.Run("R-BAK-004 every table of the embedded migrations has an export class and the FK graph is acyclic", func(t *testing.T) {
		_, _, admin := migrated(t, st)
		if ps := problems(t, admin, tables.Default()); len(ps) != 0 {
			t.Fatalf("problems: %v", ps)
		}
	})

	t.Run("R-BAK-004 every registered table exists in the migrated schema", func(t *testing.T) {
		_, _, admin := migrated(t, st)
		for _, tb := range tables.All() {
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+tb.Name) {
				t.Errorf("table %s is registered but no migration creates it", tb.Name)
			}
		}
	})

	t.Run("R-BAK-004 the initial export classes of the specification", func(t *testing.T) {
		want := map[string]tables.Class{
			"schema_migration": tables.Schema, "schema_state": tables.Schema, "system_state": tables.Data,
			"cluster_change": tables.Transient, "cluster_epoch": tables.Data, "replica": tables.Transient,
			"blob_gc_queue": tables.Transient, "blobs": tables.Blobs, "job": tables.Data,
		}
		for name, class := range want {
			got, ok := tables.Lookup(name)
			if !ok || got.Class != class {
				t.Errorf("%s = %+v (registered %v), want %s", name, got, ok, class)
			}
		}
		if pv, _ := tables.Lookup("package_versions"); pv.CycleBreak != "lead_item_id" {
			t.Errorf("package_versions CycleBreak = %q", pv.CycleBreak)
		}
	})

	t.Run("R-BAK-004 the migration test fails on a test migration that adds an unregistered table", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		extra := stepsWith(t, map[string]string{nextStep(1, "unregistered"): "-- trove:phase expand\nCREATE TABLE x_new (id bigint PRIMARY KEY);\n"})
		if _, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(extra)); err != nil {
			t.Fatal(err)
		}
		ps := problems(t, admin, tables.Default())
		if len(ps) != 1 || ps[0].Table != "x_new" || !strings.Contains(ps[0].Reason, "no export class") {
			t.Fatalf("problems = %v", ps)
		}
	})

	cycle := map[string]string{nextStep(1, "cycle"): `-- trove:phase expand
CREATE TABLE cyc_a (id bigint PRIMARY KEY, b_id bigint);
CREATE TABLE cyc_b (id bigint PRIMARY KEY, a_id bigint REFERENCES cyc_a (id));
ALTER TABLE cyc_a ADD FOREIGN KEY (b_id) REFERENCES cyc_b (id);
`}
	t.Run("R-BAK-004 the migration test fails on an FK cycle without a registered CycleBreak", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		if _, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(stepsWith(t, cycle))); err != nil {
			t.Fatal(err)
		}
		reg := tables.Default().Clone()
		reg.Register("cyc_a", tables.Data, tables.Options{})
		reg.Register("cyc_b", tables.Data, tables.Options{})
		ps := problems(t, admin, reg)
		if len(ps) != 1 || !strings.Contains(ps[0].Reason, "cyc_a, cyc_b") || !strings.Contains(ps[0].Reason, "CycleBreak") {
			t.Fatalf("problems = %v", ps)
		}
		// A registered CycleBreak on one edge of the cycle makes it pass.
		reg = tables.Default().Clone()
		reg.Register("cyc_a", tables.Data, tables.Options{CycleBreak: "b_id"})
		reg.Register("cyc_b", tables.Data, tables.Options{})
		if ps := problems(t, admin, reg); len(ps) != 0 {
			t.Fatalf("problems with CycleBreak = %v", ps)
		}
	})
}

func TestRevisionColumns(t *testing.T) {
	st := getStack(t)
	t.Run("every bigint rev_seq, revision, epoch and *_version column is in the revision-column registry", func(t *testing.T) {
		_, _, admin := migrated(t, st)
		missing, err := postgres.CheckRevisionColumns(context.Background(), admin, schemaName, meta.DefaultRevisionRegistry())
		if err != nil || len(missing) != 0 {
			t.Fatalf("unregistered: %v, %v", missing, err)
		}
		if !meta.IsRevisionColumn("packages", "rev_seq") || !meta.IsRevisionColumn("cluster_epoch", "epoch") {
			t.Fatal("packages.rev_seq and cluster_epoch.epoch are the first revision columns")
		}
	})
	t.Run("the revision-column check finds a test migration that adds an unregistered revision column", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		extra := stepsWith(t, map[string]string{nextStep(1, "rev"): "-- trove:phase expand\nALTER TABLE repositories ADD COLUMN revision bigint NOT NULL DEFAULT 1;\n"})
		if _, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(extra)); err != nil {
			t.Fatal(err)
		}
		missing, _ := postgres.CheckRevisionColumns(context.Background(), admin, schemaName, meta.DefaultRevisionRegistry())
		if len(missing) != 1 || missing[0] != "repositories.revision" {
			t.Fatalf("unregistered: %v", missing)
		}
	})
}

// TestBlobReferenceRules is the migration test of R-STOR-003 reference rule 4: a
// foreign key to blobs without its index or its triggers fails.
func TestBlobReferenceRules(t *testing.T) {
	st := getStack(t)
	t.Run("R-STOR-003 every foreign key to blobs has an index and both triggers", func(t *testing.T) {
		_, _, admin := migrated(t, st)
		rows, err := admin.Query(context.Background(), `
			SELECT cl.relname, att.attname
			  FROM pg_constraint c
			  JOIN pg_class cl ON cl.oid = c.conrelid
			  JOIN pg_namespace n ON n.oid = cl.relnamespace
			  JOIN pg_attribute att ON att.attrelid = c.conrelid AND att.attnum = c.conkey[1]
			 WHERE c.contype = 'f' AND c.confrelid = 'trove.blobs'::regclass AND n.nspname = 'trove'`)
		if err != nil {
			t.Fatal(err)
		}
		type ref struct{ table, column string }
		var refs []ref
		for rows.Next() {
			var r ref
			if err := rows.Scan(&r.table, &r.column); err != nil {
				t.Fatal(err)
			}
			refs = append(refs, r)
		}
		rows.Close()
		if len(refs) == 0 {
			t.Fatal("no foreign key to blobs: the check would pass on nothing")
		}
		for _, r := range refs {
			if r.column != "blob_sha256" {
				t.Errorf("%s: the referencing column must be blob_sha256, is %s", r.table, r.column)
			}
			indexed := scalar[bool](t, admin, `SELECT EXISTS (
				SELECT 1 FROM pg_index i WHERE i.indrelid = ('trove.' || $1)::regclass AND i.indisvalid
				   AND i.indkey[0] = (SELECT attnum FROM pg_attribute WHERE attrelid = i.indrelid AND attname = $2))`, r.table, r.column)
			if !indexed {
				t.Errorf("%s.%s has no index", r.table, r.column)
			}
			guard := scalar[bool](t, admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = ('trove.' || $1)::regclass
				AND NOT tgisinternal AND tgfoid = 'trove.blob_ref_guard'::regproc)`, r.table)
			release := scalar[int](t, admin, `SELECT count(*) FROM pg_trigger WHERE tgrelid = ('trove.' || $1)::regclass
				AND NOT tgisinternal AND tgfoid IN ('trove.blob_ref_release'::regproc, 'trove.blob_ref_release_update'::regproc)`, r.table)
			if !guard || release < 2 {
				t.Errorf("%s: guard trigger %v, release triggers %d (want the guard and a delete and an update release)", r.table, guard, release)
			}
		}
	})
}

// TestStorageSchema exercises the behaviour R-STOR-003 puts into the schema.
func TestStorageSchema(t *testing.T) {
	st := getStack(t)
	cfg, _, admin := migrated(t, st)
	_ = cfg
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
		}
	}
	blob := func(b byte, size int64, state string) []byte {
		sum := make([]byte, 32)
		for i := range sum {
			sum[i] = b
		}
		exec(`INSERT INTO trove.blobs (sha256, sha1, md5, sha512, size, state) VALUES ($1, $2, $3, $4, $5, $6)`,
			sum, make([]byte, 20), make([]byte, 16), make([]byte, 64), size, state)
		return sum
	}
	exec(`INSERT INTO trove.repositories (key, format, class) VALUES ('raw-local', 'raw', 'local')`)
	repo := scalar[int64](t, admin, `SELECT id FROM trove.repositories WHERE key = 'raw-local'`)
	file := func(path string, sha []byte, size int64) {
		exec(`INSERT INTO trove.items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, $2, '', 'file', $3, $4)`, repo, path, sha, size)
	}
	fold := func() (logical, files, folders, physical, blobs int64) {
		t.Helper()
		err := admin.QueryRow(ctx, `SELECT coalesce(sum(logical_bytes),0), coalesce(sum(files),0), coalesce(sum(folders),0),
			coalesce(sum(physical_bytes),0), coalesce(sum(blobs),0) FROM trove.storage_usage_deltas`).Scan(&logical, &files, &folders, &physical, &blobs)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	live := blob(1, 100, "live")
	dying := blob(2, 50, "deleting")

	t.Run("R-STOR-005 the reference guard refuses a blob that is not live, with SQLSTATE AS001", func(t *testing.T) {
		_, err := admin.Exec(ctx, `INSERT INTO trove.items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, 'dying', '', 'file', $2, 50)`, repo, dying)
		var pe *pgconn.PgError
		if err == nil || !asPg(err, &pe) || pe.Code != "AS001" {
			t.Fatalf("err = %v", err)
		}
		missing := make([]byte, 32)
		missing[0] = 0xEE
		_, err = admin.Exec(ctx, `INSERT INTO trove.items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, 'nothing', '', 'file', $2, 1)`, repo, missing)
		if err == nil || !asPg(err, &pe) || (pe.Code != "AS001" && pe.Code != "23503") {
			t.Fatalf("a reference to an unknown blob must be refused, err = %v", err)
		}
	})

	t.Run("R-STOR-003 usage deltas follow items and live blobs, statement by statement", func(t *testing.T) {
		// Two live blob inserts and one deleting insert so far: only live rows count.
		if l, f, fo, p, b := fold(); l != 0 || f != 0 || fo != 0 || p != 100 || b != 1 {
			t.Fatalf("after blobs: logical %d files %d folders %d physical %d blobs %d", l, f, fo, p, b)
		}
		file("a/one", live, 100)
		file("a/two", live, 100)
		exec(`INSERT INTO trove.items (repo_id, path, parent_path, kind, explicit_folder) VALUES ($1, 'a', '', 'folder', true)`, repo)
		if l, f, fo, _, _ := fold(); l != 200 || f != 2 || fo != 1 {
			t.Fatalf("after inserts: logical %d files %d folders %d", l, f, fo)
		}
		before := scalar[int](t, admin, `SELECT count(*) FROM trove.storage_usage_deltas`)
		exec(`UPDATE trove.items SET updated_at = now() WHERE path = 'a/one'`)
		if after := scalar[int](t, admin, `SELECT count(*) FROM trove.storage_usage_deltas`); after != before {
			t.Fatalf("a touch that changes nothing the counters see appended %d delta rows", after-before)
		}
		exec(`UPDATE trove.items SET trashed_at = now() WHERE path = 'a/one'`)
		if l, f, _, _, _ := fold(); l != 100 || f != 1 {
			t.Fatalf("after trashing: logical %d files %d", l, f)
		}
		exec(`DELETE FROM trove.items WHERE path = 'a/one'`) // trashed rows were already subtracted
		if l, f, _, _, _ := fold(); l != 100 || f != 1 {
			t.Fatalf("deleting a trashed row must not subtract twice: logical %d files %d", l, f)
		}
		exec(`UPDATE trove.blobs SET state = 'deleting' WHERE sha256 = $1`, live)
		if _, _, _, p, b := fold(); p != 0 || b != 0 {
			t.Fatalf("a blob leaving live subtracts: physical %d blobs %d", p, b)
		}
	})

	t.Run("R-STOR-024 deleting or overwriting a reference queues the blob for collection", func(t *testing.T) {
		b1 := blob(3, 10, "live")
		b2 := blob(4, 10, "live")
		file("g/x", b1, 10)
		exec(`UPDATE trove.items SET blob_sha256 = $1 WHERE path = 'g/x'`, b2)
		if scalar[int](t, admin, `SELECT count(*) FROM trove.blob_gc_queue WHERE sha256 = $1`, b1) != 1 {
			t.Fatal("an overwrite in place must queue the old blob")
		}
		if scalar[int](t, admin, `SELECT count(*) FROM trove.blob_gc_queue WHERE sha256 = $1`, b2) != 0 {
			t.Fatal("the new blob must not be queued")
		}
		exec(`DELETE FROM trove.items WHERE path = 'g/x'`)
		if scalar[int](t, admin, `SELECT count(*) FROM trove.blob_gc_queue WHERE sha256 = $1`, b2) != 1 {
			t.Fatal("a delete must queue the blob")
		}
	})

	t.Run("R-STOR-003 deleting a repository cascades to its items and queues their blobs", func(t *testing.T) {
		exec(`INSERT INTO trove.repositories (key, format, class) VALUES ('doomed', 'raw', 'local')`)
		doomed := scalar[int64](t, admin, `SELECT id FROM trove.repositories WHERE key = 'doomed'`)
		b := blob(5, 7, "live")
		exec(`INSERT INTO trove.items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, 'f', '', 'file', $2, 7)`, doomed, b)
		exec(`DELETE FROM trove.repositories WHERE id = $1`, doomed)
		if scalar[int](t, admin, `SELECT count(*) FROM trove.items WHERE repo_id = $1`, doomed) != 0 ||
			scalar[int](t, admin, `SELECT count(*) FROM trove.blob_gc_queue WHERE sha256 = $1`, b) != 1 {
			t.Fatal("the cascade must remove the items and queue the blob")
		}
		if scalar[int64](t, admin, `SELECT coalesce(sum(files), 0) FROM trove.storage_usage_deltas WHERE repo_id = $1`, doomed) != 0 {
			t.Fatal("the cascade must net the repository's file count to zero")
		}
	})

	t.Run("R-STOR-003 a property value of 2400 four-byte characters is stored and found by key and value", func(t *testing.T) {
		exec(`INSERT INTO trove.items (repo_id, path, parent_path, kind, explicit_folder) VALUES ($1, 'props', '', 'folder', true)`, repo)
		item := scalar[int64](t, admin, `SELECT id FROM trove.items WHERE path = 'props'`)
		value := strings.Repeat("\U0001F600", 2400)
		exec(`INSERT INTO trove.item_properties (item_id, key, value) VALUES ($1, 'build.note', $2)`, item, value)
		if scalar[int](t, admin, `SELECT count(*) FROM trove.item_properties WHERE key = 'build.note' AND value = $1`, value) != 1 {
			t.Fatal("the long value was not found by key and value")
		}
		if _, err := admin.Exec(ctx, `INSERT INTO trove.item_properties (item_id, key, value) VALUES ($1, 'build.note', $2)`, item, value); err == nil {
			t.Fatal("the same key and value twice must violate item_properties_unique")
		}
	})

	t.Run("R-STOR-003 paths sort bytewise: a subtree query for a/b does not return a/b0", func(t *testing.T) {
		for _, p := range []string{"s/a/b", "s/a/b/c", "s/a/b0", "s/a/b-"} {
			exec(`INSERT INTO trove.items (repo_id, path, parent_path, kind, explicit_folder) VALUES ($1, $2, '', 'folder', true)`, repo, p)
		}
		rows, err := admin.Query(ctx, `SELECT path FROM trove.items WHERE repo_id = $1 AND (path = 's/a/b' OR (path >= 's/a/b/' AND path < 's/a/b0')) ORDER BY path`, repo)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			got = append(got, p)
		}
		if strings.Join(got, ",") != "s/a/b,s/a/b/c" {
			t.Fatalf("subtree = %v", got)
		}
	})

	t.Run("R-STOR-003 a package row keeps its rev_seq default and the version cycle resolves through lead_item_id", func(t *testing.T) {
		exec(`INSERT INTO trove.packages (repo_id, name, display_name) VALUES ($1, 'left-pad', 'left-pad')`, repo)
		if scalar[int64](t, admin, `SELECT rev_seq FROM trove.packages WHERE name = 'left-pad'`) != 1 {
			t.Fatal("rev_seq must start at 1")
		}
		pkg := scalar[int64](t, admin, `SELECT id FROM trove.packages WHERE name = 'left-pad'`)
		exec(`INSERT INTO trove.package_versions (package_id, version, display_version) VALUES ($1, '1.0.0', '1.0.0')`, pkg)
		ver := scalar[int64](t, admin, `SELECT id FROM trove.package_versions WHERE package_id = $1`, pkg)
		file("pkg/left-pad-1.0.0.tgz", live2(t, blob, 9), 5)
		item := scalar[int64](t, admin, `SELECT id FROM trove.items WHERE path = 'pkg/left-pad-1.0.0.tgz'`)
		exec(`UPDATE trove.items SET version_id = $1 WHERE id = $2`, ver, item)
		exec(`UPDATE trove.package_versions SET lead_item_id = $1 WHERE id = $2`, item, ver)
		exec(`DELETE FROM trove.items WHERE id = $1`, item)
		if scalar[bool](t, admin, `SELECT lead_item_id IS NOT NULL FROM trove.package_versions WHERE id = $1`, ver) {
			t.Fatal("deleting the lead item must set lead_item_id to NULL")
		}
	})
}

func live2(t *testing.T, blob func(byte, int64, string) []byte, b byte) []byte {
	t.Helper()
	return blob(b, 5, "live")
}

func asPg(err error, target **pgconn.PgError) bool {
	for err != nil {
		if pe, ok := err.(*pgconn.PgError); ok { //nolint:errorlint // pgx returns the error unwrapped
			*target = pe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
