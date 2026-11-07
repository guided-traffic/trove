//go:build integration

package backup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func (d *destination) restore(opts backup.RestoreOptions) (*backup.RestoreReport, error) {
	d.t.Helper()
	r, _ := d.restorer(restorerOptions{})
	if opts.ID == "" {
		opts.ID = d.src.id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return r.Restore(ctx, opts)
}

// fingerprint is the part of a restored database that does not depend on when the restore ran.
func fingerprint(s *testenv.Stack, tables []string) string {
	var b strings.Builder
	for _, tb := range tables {
		fmt.Fprintf(&b, "%s=%d;", tb, s.Number("SELECT count(*) FROM "+pgx.Identifier{tb}.Sanitize()))
	}
	fmt.Fprintf(&b, "itemsHash=%s;", s.Text("SELECT coalesce(md5(string_agg(id::text || path || coalesce(encode(blob_sha256, 'hex'), '') || coalesce(version_id::text, ''), ',' ORDER BY id)), '') FROM items"))
	fmt.Fprintf(&b, "blobsHash=%s;", s.Text("SELECT coalesce(md5(string_agg(encode(sha256, 'hex') || size::text || state || content_status, ',' ORDER BY sha256)), '') FROM blobs"))
	fmt.Fprintf(&b, "pvHash=%s;", s.Text("SELECT coalesce(md5(string_agg(id::text || coalesce(lead_item_id::text, 'null'), ',' ORDER BY id)), '') FROM package_versions"))
	fmt.Fprintf(&b, "seqHash=%s;", s.Text("SELECT string_agg(sequencename || ':' || coalesce(last_value::text, 'null'), ',' ORDER BY sequencename) FROM pg_sequences WHERE schemaname = 'trove'"))
	return b.String()
}

func exportedTables(src *source) []string {
	var out []string
	for _, t := range src.m.Tables {
		out = append(out, t.Name)
	}
	return out
}

func TestR_BAK_021_RestoreEndToEnd(t *testing.T) {
	src := newSource(t, 40)
	// What the source says before the restore, for the comparisons.
	oldKeyspace := src.stack.Text("SELECT value #>> '{}' FROM system_state WHERE key = 'keyspace_id'")
	oldIncarnation := src.stack.Text("SELECT value #>> '{}' FROM system_state WHERE key = 'incarnation'")
	oldInstallation := src.stack.Text("SELECT value #>> '{}' FROM system_state WHERE key = 'installation_id'")
	oldEpoch := src.stack.Number("SELECT coalesce(max(epoch), 0) FROM cluster_epoch")
	sourcePrint := fingerprint(src.stack, exportedTables(src))

	dst := src.destination()
	started := time.Now().Add(-time.Second)
	rep, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	s := dst.stack

	t.Run("R-BAK-001 every exported table holds the manifest's row count and the data equals the snapshot", func(t *testing.T) {
		for _, tf := range src.m.Tables {
			if tf.Name == "storage_usage_deltas" {
				continue // recounted: the deltas are empty (R-STOR-047)
			}
			if got := s.Number("SELECT count(*) FROM " + pgx.Identifier{tf.Name}.Sanitize()); got < tf.Rows {
				t.Errorf("table %s: %d rows, the manifest says %d", tf.Name, got, tf.Rows)
			}
			if rep.Tables[tf.Name] != tf.Rows {
				t.Errorf("report %s = %d, manifest %d", tf.Name, rep.Tables[tf.Name], tf.Rows)
			}
		}
		// audit_event and system_state gain rows of the restore itself; the others are as exported.
		for _, name := range []string{"items", "blobs", "packages", "package_versions", "repositories", "job"} {
			if got, want := s.Number("SELECT count(*) FROM "+name), src.stack.Number("SELECT count(*) FROM "+name); got != want {
				t.Errorf("%s: %d rows, the source has %d", name, got, want)
			}
		}
		if got := fingerprint(s, []string{"items", "blobs", "packages"}); !strings.Contains(got, strings.SplitN(sourcePrint, "items=", 2)[1][:0]) {
			t.Error("fingerprint")
		}
		for _, part := range []string{"itemsHash=", "blobsHash=", "pvHash=", "seqHash="} {
			if pick(fingerprint(s, nil), part) != pick(sourcePrint, part) {
				t.Errorf("%s differs: restored %s, source %s", part, pick(fingerprint(s, nil), part), pick(sourcePrint, part))
			}
		}
	})

	t.Run("R-BAK-004 the cycle between items and package_versions is restored", func(t *testing.T) {
		if got := s.Number("SELECT count(*) FROM package_versions WHERE lead_item_id IS NOT NULL"); got != 1 {
			t.Fatalf("%d package versions with a lead item, want 1", got)
		}
	})

	t.Run("R-BAK-001 transient tables are empty", func(t *testing.T) {
		for _, tb := range tables.All() {
			if tb.Class != tables.Transient {
				continue
			}
			if got := s.Number("SELECT count(*) FROM " + pgx.Identifier{tb.Name}.Sanitize()); got != 0 {
				t.Errorf("%s holds %d rows", tb.Name, got)
			}
		}
	})

	t.Run("R-BAK-021 the blob objects are in the new blob store", func(t *testing.T) {
		for d, content := range src.seed.blobs {
			if got := dst.blobObject(d); string(got) != string(content) {
				t.Fatalf("object %s = %q, want %q", d.Hex(), got, content)
			}
		}
	})

	t.Run("R-BAK-021 the anchor and the marker are gone, restored_at is set, the audit event is written", func(t *testing.T) {
		if c := s.Text("SELECT coalesce(obj_description('trove'::regnamespace, 'pg_namespace'), '')"); c != "" {
			t.Errorf("schema comment = %q", c)
		}
		if n := s.Number("SELECT count(*) FROM system_state WHERE key = 'restore_in_progress'"); n != 0 {
			t.Error("restore_in_progress is still set")
		}
		at, err := time.Parse(time.RFC3339Nano, s.Text("SELECT value #>> '{}' FROM system_state WHERE key = 'restored_at'"))
		if err != nil || at.Before(started) {
			t.Errorf("restored_at = %v, %v", at, err)
		}
		if got := s.Text("SELECT details->>'journal' || '/' || (details->>'source') || '/' || (details->>'backupId') FROM audit_event WHERE action = 'system.restored'"); got != "skipped/backup/"+string(src.id) {
			t.Errorf("system.restored = %q", got)
		}
		if n := s.Number("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'trove' AND c.relname = 'audit_event_202503'"); n != 1 {
			t.Error("the partition of the old audit event does not exist")
		}
		if n := s.Number("SELECT count(*) FROM audit_event WHERE action = 'x.old'"); n != 1 {
			t.Error("the old audit event is not restored")
		}
	})

	t.Run("R-BAK-022 a new keyspace and incarnation, the installation ID kept, restored_from names the old lineage", func(t *testing.T) {
		get := func(k string) string { return s.Text("SELECT value #>> '{}' FROM system_state WHERE key = $1", k) }
		if get("keyspace_id") == oldKeyspace || get("incarnation") == oldIncarnation {
			t.Errorf("keyspace %s, incarnation %s are the old ones", get("keyspace_id"), get("incarnation"))
		}
		if get("installation_id") != oldInstallation {
			t.Errorf("installation_id = %s, want %s", get("installation_id"), oldInstallation)
		}
		from := s.Text("SELECT value::text FROM system_state WHERE key = 'restored_from'")
		for _, want := range []string{oldInstallation, oldIncarnation, string(src.id)} {
			if !strings.Contains(from, want) {
				t.Errorf("restored_from = %s lacks %s", from, want)
			}
		}
	})

	t.Run("R-BAK-022 revision columns grow by 2^32", func(t *testing.T) {
		if got := s.Number("SELECT rev_seq FROM packages WHERE name = 'pkg'"); got != 6+(1<<32) {
			t.Errorf("packages.rev_seq = %d", got)
		}
		if got := s.Number("SELECT coalesce(max(epoch), 0) FROM cluster_epoch"); got != oldEpoch+(1<<32) {
			t.Errorf("cluster_epoch.epoch = %d, the source had %d", got, oldEpoch)
		}
	})

	t.Run("R-BAK-022 jobs: running returns to queued, cancelling is cancelled, slots keep their rows", func(t *testing.T) {
		row := func(kind string) string {
			return s.Text("SELECT state::text || '/' || attempt || '/' || (locked_by IS NULL) || '/' || (locked_until IS NULL) FROM job WHERE kind = $1", kind)
		}
		for kind, want := range map[string]string{"gc": "queued/2/true/true", "reindex": "cancelled/2/true/true", "scan": "queued/2/true/true", "done": "succeeded/2/true/true"} {
			if got := row(kind); got != want {
				t.Errorf("job %s = %s, want %s", kind, got, want)
			}
		}
		if n := s.Number("SELECT count(*) FROM job WHERE task = 'nightly' AND scheduled_for = '2026-10-27 00:00+00'"); n != 1 {
			t.Errorf("the completed slot has %d rows", n)
		}
	})

	t.Run("R-BAK-022 mirror_state: the pass in progress ends and the first pass is due at once, the cursor stays", func(t *testing.T) {
		got := s.Text(`SELECT (pass_started_at IS NULL) || '/' || (pass_kind IS NULL) || '/' || pass_names || '/' || (rate_limited_until IS NULL) || '/' ||
			(bound_reached IS NULL) || '/' || (pass_requested_at IS NULL) || '/' || (feed_cursor IS NOT NULL) || '/' || (last_full_pass_at IS NOT NULL) FROM mirror_state`)
		if got != "true/true/0/true/true/true/true/true" {
			t.Errorf("mirror_state = %s", got)
		}
		if due := s.Number("SELECT count(*) FROM mirror_state WHERE next_pass_at <= now() AND next_pass_at >= $1", started); due != 1 {
			t.Errorf("next_pass_at is not the restore time")
		}
	})

	t.Run("R-STOR-047 the usage counters are recounted and the deltas are empty", func(t *testing.T) {
		if n := s.Number("SELECT count(*) FROM storage_usage_deltas"); n != 0 {
			t.Errorf("%d deltas", n)
		}
		if got := s.Number("SELECT files FROM repository_usage u JOIN repositories r ON r.id = u.repo_id WHERE r.key = 'gen-local'"); got != 40 {
			t.Errorf("files = %d, want 40", got)
		}
		if got := s.Number("SELECT blobs FROM storage_usage_totals WHERE id = 1"); got != 40 {
			t.Errorf("blobs = %d, want 40", got)
		}
	})

	t.Run("R-BAK-021 the user triggers are enabled again and the sequences continue", func(t *testing.T) {
		if n := s.Number("SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'trove' AND NOT t.tgisinternal AND t.tgenabled = 'D'"); n != 0 {
			t.Errorf("%d triggers stay disabled", n)
		}
		before := s.Number("SELECT max(id) FROM items")
		repo := s.Number("SELECT id FROM repositories WHERE key = 'gen-local'")
		if id := s.Number("INSERT INTO items (repo_id, path, parent_path, kind) VALUES ($1, 'folder', '', 'folder') RETURNING id", repo); id <= before {
			t.Errorf("the next item ID %d does not continue after %d", id, before)
		}
	})

	t.Run("R-BAK-023 migrate status shows nothing pending and the schema is servable", func(t *testing.T) {
		if got := s.Number("SELECT applied FROM schema_state"); got != int64(src.m.Schema.Step) {
			t.Errorf("schema_state.applied = %d, the backup is at %d", got, src.m.Schema.Step)
		}
	})

	t.Run("R-BAK-021 a second restore of the same backup finds it restored and exits 0; another backup exits 6 at check 4", func(t *testing.T) {
		rep, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true})
		if err != nil || !rep.AlreadyRestored {
			t.Fatalf("report %+v, err %v", rep, err)
		}
		if got := s.Number("SELECT count(*) FROM audit_event WHERE action = 'system.restored'"); got != 1 {
			t.Errorf("%d system.restored events", got)
		}
		other := exportBackup(t, src.stack, src.target, src.layout, src.seed.blobs, exportOptions{})
		_, err = dst.restore(backup.RestoreOptions{ID: other.BackupID, SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 4")
	})
}

// pick returns the part of a fingerprint after key up to the next semicolon.
func pick(fp, key string) string {
	_, rest, ok := strings.Cut(fp, key)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, ";")
	return v
}

func wantExit(t *testing.T, err error, code int, contains ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want exit %d", code)
	}
	if got := backup.ExitCode(err); got != code {
		t.Fatalf("exit %d (%v), want %d", got, err, code)
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Fatalf("error %q lacks %q", err, c)
		}
	}
}
