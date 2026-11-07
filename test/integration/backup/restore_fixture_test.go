//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // the blob rows carry all four digests
	"crypto/sha1" //nolint:gosec // the blob rows carry all four digests
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// seeded is what seedSource put into the source installation.
type seeded struct {
	blobs map[format.Digest][]byte
	// repo is the generic repository the files live in.
	repo string
}

func digestOf(content []byte) format.Digest { return format.Digest(sha256.Sum256(content)) }

// seedSource fills an installation with the state a restore must carry: a repository with files, a package with a version
// whose lead item closes the foreign-key cycle, a mirror's pass in progress, jobs in every state a restore resets, an audit
// event from a month without a partition, and the canary row of the key ring.
func seedSource(t *testing.T, s *testenv.Stack, files, bulkAudit int, ring *secrets.KeyRing) *seeded {
	t.Helper()
	out := &seeded{blobs: map[format.Digest][]byte{}, repo: "gen-local"}
	s.Repository(out.repo, "local", "generic", "{}")
	repoID := s.Number("SELECT id FROM repositories WHERE key = $1", out.repo)
	var leadItem int64
	for i := range files {
		content := []byte(fmt.Sprintf("restore fixture blob %d %s", i, strings.Repeat("x", i%7)))
		d := digestOf(content)
		out.blobs[d] = content
		sha1s, md5s, sha512s := sha1.Sum(content), md5.Sum(content), sha512.Sum512(content) //nolint:gosec // fixture digests
		s.Exec("INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING", d[:], sha1s[:], md5s[:], sha512s[:], int64(len(content)))
		id := s.Number(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, $2, '', 'file', $3, $4) RETURNING id`,
			repoID, fmt.Sprintf("f%05d.bin", i), d[:], int64(len(content)))
		if i == 0 {
			leadItem = id
		}
	}
	pkg := s.Number("INSERT INTO packages (repo_id, name, display_name, rev_seq) VALUES ($1, 'pkg', 'pkg', 6) RETURNING id", repoID)
	ver := s.Number("INSERT INTO package_versions (package_id, version, display_version, lead_item_id) VALUES ($1, '1.0.0', '1.0.0', $2) RETURNING id", pkg, leadItem)
	s.Exec("UPDATE items SET version_id = $1 WHERE id = $2", ver, leadItem)

	// A job running, one being cancelled, one queued and one done at the snapshot.
	for _, j := range []struct{ kind, state string }{{"gc", "running"}, {"reindex", "cancelling"}, {"scan", "queued"}, {"done", "succeeded"}} {
		s.Exec(`INSERT INTO job (id, kind, state, attempt, locked_by, locked_until)
			VALUES (gen_random_uuid(), $1, $2::job_state, 2, CASE WHEN $2 IN ('running', 'cancelling') THEN gen_random_uuid() END,
			        CASE WHEN $2 IN ('running', 'cancelling') THEN now() + interval '1 hour' END)`, j.kind, j.state)
	}
	// A scheduled slot that completed before the snapshot keeps its row.
	s.Exec(`INSERT INTO job (id, kind, state, task, scheduled_for, finished_at) VALUES (gen_random_uuid(), 'slot', 'succeeded', 'nightly', '2026-10-27 00:00+00', now())`)

	// An audit event in a month the fresh schema has no partition for.
	s.Exec("SELECT audit_create_partitions('2025-03-15 00:00+00', '2025-03-15 00:00+00')")
	s.Exec(`INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type) VALUES (gen_random_uuid(), '2025-03-15 10:00+00', 'x.old', 'system', 'success', 'system')`)

	if bulkAudit > 0 {
		// Enough rows in the current month's partition to make the load of audit_event take seconds.
		s.Exec(`INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type, details)
			SELECT gen_random_uuid(), date_trunc('month', now()) + (g || ' seconds')::interval, 'x.bulk', 'system', 'success', 'system', jsonb_build_object('n', g, 'pad', repeat('p', 80))
			  FROM generate_series(1, $1) g`, bulkAudit)
	}

	// The canary row the key ring check opens (R-ARCH-021): a replica seeds it at start-up.
	for _, id := range ring.KeyIDs() {
		sealed, err := ring.SealCanary(id)
		if err != nil {
			t.Fatal(err)
		}
		s.Exec("INSERT INTO secret_key_canary (key_id, ciphertext) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, sealed)
	}
	// A remote repository with a mirror pass in progress and a feed cursor.
	s.Repository("npm-remote", "remote", "npm", "{}")
	rid := s.Number("SELECT id FROM repositories WHERE key = 'npm-remote'")
	s.Exec(`INSERT INTO mirror_state (repo_id, next_pass_at, pass_started_at, pass_kind, pass_method, pass_job_id, pass_names, pass_packages_done,
			feed_cursor, last_full_pass_at, rate_limited_until, bound_reached, pass_requested_at)
		VALUES ($1, now() + interval '9 days', now(), 'full', 'feed', gen_random_uuid(), 40, 12, '{"seq": 7}', now(), now() + interval '1 hour', 'bytes', now())`, rid)
	return out
}

// keyRingOf writes a key file named <id>.key below dir and loads the ring.
func keyRingOf(t *testing.T, s *testenv.Stack, id string, fill byte) (*secrets.KeyRing, string) {
	t.Helper()
	file := s.KeyFile(id+".key", bytes.Repeat([]byte{fill}, 32))
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{file}, ActiveKeyID: id})
	if err != nil {
		t.Fatal(err)
	}
	return ring, file
}

type exportOptions struct {
	id      format.ID
	damaged []format.Digest
}

// exportBackup writes a backup of src's database the way trove backup create does, built by hand from the format package:
// every exported table in one REPEATABLE READ snapshot as zstd-compressed COPY text, the sequences, the blob list and the
// pool objects, the manifest last.
func exportBackup(t *testing.T, src *testenv.Stack, st target.Store, layout format.Layout, blobs map[format.Digest][]byte, o exportOptions) *format.Manifest {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testenv.PgDSN(t, src.Database()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "SET search_path TO trove"); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qs := q.New(tx)

	var snapshotAt time.Time
	var version string
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp(), current_setting('server_version')").Scan(&snapshotAt, &version); err != nil {
		t.Fatal(err)
	}
	if o.id == "" {
		o.id, _ = format.NewID(snapshotAt, nil)
	}
	m := &format.Manifest{
		Format: format.ManifestFormat, BackupID: o.id, Trove: format.TroveInfo{Version: "0.1.0", Revision: "test"},
		Postgres:  format.PostgresRef{ServerVersion: version, Isolation: "repeatable read"},
		StartedAt: snapshotAt.Add(-time.Second), SnapshotAt: snapshotAt,
		Source:  format.Source{Storage: format.StorageRef{Bucket: "blobs", Prefix: "it"}},
		Secrets: format.SecretsInfo{},
	}
	m.InstallationID = textOf(t, tx, "SELECT value #>> '{}' FROM system_state WHERE key = 'installation_id'")
	m.Incarnation = textOf(t, tx, "SELECT value #>> '{}' FROM system_state WHERE key = 'incarnation'")

	rows, err := tx.Query(ctx, "SELECT version, name, encode(checksum, 'hex') FROM schema_migration ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s format.SchemaStep
		if err := rows.Scan(&s.Step, &s.Name, &s.SHA256); err != nil {
			t.Fatal(err)
		}
		m.Schema.Steps = append(m.Schema.Steps, s)
		m.Schema.Step = s.Step
	}
	rows.Close()

	names, err := qs.SchemaTables(ctx, "trove")
	if err != nil {
		t.Fatal(err)
	}
	cols := map[string][][2]string{}
	colRows, err := qs.RestoreTableColumns(ctx, "trove")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range colRows {
		cols[c.TableName] = append(cols[c.TableName], [2]string{c.ColumnName, c.ColumnType})
	}
	for _, name := range names {
		class := tables.Data
		if reg, ok := tables.Lookup(name); ok {
			class = reg.Class
		}
		if !class.Exported() {
			continue
		}
		var names []string
		for _, c := range cols[name] {
			names = append(names, pgx.Identifier{c[0]}.Sanitize())
		}
		var raw bytes.Buffer
		zw, err := zstd.NewWriter(&raw)
		if err != nil {
			t.Fatal(err)
		}
		plain := &countWriter{w: zw}
		tag, err := tx.Conn().PgConn().CopyTo(ctx, plain, "COPY (SELECT "+strings.Join(names, ", ")+" FROM "+pgx.Identifier{name}.Sanitize()+") TO STDOUT")
		if err != nil {
			t.Fatalf("export %s: %v", name, err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		stored := raw.Bytes()
		sum := sha256.Sum256(stored)
		if _, err := st.Put(ctx, layout.Table(o.id, name), stored, target.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		cls := "data"
		if class == tables.Blobs {
			cls = "blobs"
		}
		m.Tables = append(m.Tables, format.TableFile{
			Name: name, Class: cls, File: format.TablesDir + "/" + name + format.TableSuffix, Columns: cols[name],
			Rows: tag.RowsAffected(), Bytes: plain.n, CompressedBytes: int64(len(stored)), SHA256: hex.EncodeToString(sum[:]),
		})
	}

	seqRows, err := tx.Query(ctx, "SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'trove' AND c.relkind = 'S' ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	var seqs []string
	for seqRows.Next() {
		var n string
		if err := seqRows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, n)
	}
	seqRows.Close()
	for _, n := range seqs {
		s := format.Sequence{Name: n}
		if err := tx.QueryRow(ctx, "SELECT last_value, is_called FROM "+pgx.Identifier{"trove", n}.Sanitize()).Scan(&s.LastValue, &s.IsCalled); err != nil {
			t.Fatal(err)
		}
		m.Sequences = append(m.Sequences, s)
	}

	keyRows, err := tx.Query(ctx, "SELECT key_id FROM secret_key_canary ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	for keyRows.Next() {
		var k string
		if err := keyRows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		m.Secrets.KeyIDs = append(m.Secrets.KeyIDs, k)
	}
	keyRows.Close()

	// The blob list: every live blob row, the pool: every one that is not damaged.
	damaged := map[format.Digest]bool{}
	for _, d := range o.damaged {
		damaged[d] = true
		m.Blobs.Damaged = append(m.Blobs.Damaged, format.DamagedBlob{SHA256: d.Hex(), Status: "missing"})
	}
	var entries []format.BlobEntry
	brows, err := tx.Query(ctx, "SELECT sha256, size FROM blobs WHERE state = 'live' ORDER BY sha256")
	if err != nil {
		t.Fatal(err)
	}
	for brows.Next() {
		var raw []byte
		var size int64
		if err := brows.Scan(&raw, &size); err != nil {
			t.Fatal(err)
		}
		d, _ := format.DigestOf(raw)
		entries = append(entries, format.BlobEntry{Digest: d, Size: size})
	}
	brows.Close()
	list, err := format.EncodeBlobList(entries, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, layout.BlobList(o.id), list, target.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	listSum := sha256.Sum256(list)
	m.Blobs.File, m.Blobs.SHA256, m.Blobs.Referenced = format.BlobListName, hex.EncodeToString(listSum[:]), int64(len(entries))
	for _, e := range entries {
		if damaged[e.Digest] {
			continue
		}
		content, ok := blobs[e.Digest]
		if !ok {
			t.Fatalf("no content for blob %s", e.Digest.Hex())
		}
		if _, err := st.Put(ctx, layout.Pool(e.Digest), content, target.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		m.Blobs.Copied++
		m.Blobs.CopiedBytes += e.Size
	}
	m.CompletedAt = time.Now()
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, layout.Manifest(o.id), body, target.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return m
}

func textOf(t *testing.T, tx pgx.Tx, sql string) string {
	t.Helper()
	var s string
	if err := tx.QueryRow(context.Background(), sql).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return c.w.Write(p) }
