package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// fakeSession records what the restore does to the database and answers the inspections from its fields.
type fakeSession struct {
	mu        sync.Mutex
	calls     []string
	lockErr   error
	lockBlock bool
	inspect   postgres.RestoreInspection
	server    error
	steps     []postgres.RestoreStep
	migrated  []postgres.RestoreMigration
	loaded    bool
	loadErr   error
}

func (f *fakeSession) rec(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeSession) called(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}
func (f *fakeSession) InTx(context.Context, func(meta.Tx) error) error { return nil }
func (f *fakeSession) Read(context.Context, func(q.DBTX) error) error  { return nil }
func (f *fakeSession) DBTX() q.DBTX                                    { return nil }
func (f *fakeSession) Lock(ctx context.Context, _ time.Duration) error {
	f.rec("lock")
	if f.lockBlock {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.lockErr
}
func (f *fakeSession) Close(context.Context) { f.rec("close") }
func (f *fakeSession) Schema() string        { return "trove" }
func (f *fakeSession) Inspect(context.Context) (postgres.RestoreInspection, error) {
	return f.inspect, nil
}
func (f *fakeSession) ServerCheck(context.Context) error { return f.server }
func (f *fakeSession) Steps() ([]postgres.RestoreStep, error) {
	return f.steps, nil
}
func (f *fakeSession) SetAnchor(context.Context, string) error { f.rec("anchor"); return nil }
func (f *fakeSession) MigrateTo(_ context.Context, m postgres.RestoreMigration) (postgres.Result, error) {
	f.rec("migrate")
	f.migrated = append(f.migrated, m)
	return postgres.Result{}, nil
}

func (f *fakeSession) Load(context.Context, postgres.LoadPlan, postgres.LoadSource) (postgres.LoadResult, error) {
	f.rec("load")
	f.loaded = true
	return postgres.LoadResult{}, f.loadErr
}
func (f *fakeSession) Fix(context.Context, func(meta.Tx) error) error { f.rec("fix"); return nil }
func (f *fakeSession) Finish(context.Context, func(meta.Tx) error) error {
	f.rec("finish")
	return nil
}
func (f *fakeSession) EnsureAuditPartitions(context.Context) error { return nil }
func (f *fakeSession) PendingSteps(context.Context) ([]postgres.StepInfo, error) {
	return nil, nil
}
func (f *fakeSession) LiveBlobs(context.Context, func([]byte) error) error { return nil }
func (f *fakeSession) SecretCanaries(context.Context) (map[string]string, error) {
	return nil, nil
}

// fakeBlobs is the new installation's object store in memory.
type fakeBlobs struct {
	mu         sync.Mutex
	objs       map[format.Digest][]byte
	foreign    string
	probeErr   error
	serverCopy bool
	copies     int
	streams    int
	allowSeen  bool
}

func (b *fakeBlobs) Has(_ context.Context, d format.Digest, size int64) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.objs[d]
	return ok && int64(len(o)) == size, nil
}

func (b *fakeBlobs) Put(_ context.Context, d format.Digest, size int64, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if sha256.Sum256(data) != [32]byte(d) {
		return errors.New("digest mismatch")
	}
	b.mu.Lock()
	b.objs[d] = data
	b.streams++
	b.mu.Unlock()
	return nil
}

func (b *fakeBlobs) CopyFrom(context.Context, target.ObjectRef, format.Digest, int64) error {
	if !b.serverCopy {
		return target.ErrServerCopyUnavailable
	}
	b.mu.Lock()
	b.copies++
	b.mu.Unlock()
	return nil
}

func (b *fakeBlobs) FirstForeign(_ context.Context, allow bool) (string, error) {
	b.allowSeen = allow
	return b.foreign, nil
}
func (b *fakeBlobs) Probe(context.Context, string) error { return b.probeErr }

// fakeJournal is a revocation journal.
type fakeJournal struct {
	reach  error
	replay int
}

func (j *fakeJournal) Reachable(context.Context) error { return j.reach }
func (j *fakeJournal) Replay(context.Context, journal.ReplayOptions) (*journal.Summary, error) {
	j.replay++
	return &journal.Summary{}, nil
}

type env struct {
	t      *testing.T
	mem    *targettest.Mem
	layout format.Layout
	id     format.ID
	m      *format.Manifest
	ring   *secrets.KeyRing
	sess   *fakeSession
	blobs  *fakeBlobs
	jrn    *fakeJournal
	pool   map[format.Digest][]byte
	out    bytes.Buffer
}

func zstdBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func keyRing(t *testing.T, id string, key byte) *secrets.KeyRing {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, id+".key")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{key}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{p}, ActiveKeyID: id})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// newEnv writes a small backup into an in-memory target: the secret_key_canary file sealed by key 2026-10, one items file and
// a blob list with two blobs in the pool.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, mem: targettest.New(), layout: format.Layout{Prefix: "trove"}, pool: map[format.Digest][]byte{}}
	e.id = "20261028T020000Z-7f3a"
	e.ring = keyRing(t, "2026-10", 7)
	sealed, err := e.ring.SealCanary("2026-10")
	if err != nil {
		t.Fatal(err)
	}
	file := func(name string, cols [][2]string, text string) format.TableFile {
		z := zstdBytes(t, []byte(text))
		e.mem.Set(e.layout.Table(e.id, name), z)
		return format.TableFile{
			Name: name, Class: "data", File: "db/" + name + ".copy.zst", Columns: cols, Rows: int64(strings.Count(text, "\n")),
			Bytes: int64(len(text)), CompressedBytes: int64(len(z)), SHA256: sum(z),
		}
	}
	var entries []format.BlobEntry
	for _, c := range []string{"alpha", "beta"} {
		d := format.Digest(sha256.Sum256([]byte(c)))
		e.pool[d] = []byte(c)
		e.mem.Set(e.layout.Pool(d), []byte(c))
		entries = append(entries, format.BlobEntry{Digest: d, Size: int64(len(c))})
	}
	if entries[1].Digest.Less(entries[0].Digest) {
		entries[0], entries[1] = entries[1], entries[0]
	}
	list, err := format.EncodeBlobList(entries, 3)
	if err != nil {
		t.Fatal(err)
	}
	e.mem.Set(e.layout.BlobList(e.id), list)
	snap := time.Date(2026, 10, 28, 2, 0, 1, 0, time.UTC)
	e.m = &format.Manifest{
		Format: 1, BackupID: e.id, InstallationID: "inst", Incarnation: "inc", Trove: format.TroveInfo{Version: "0.4.0"},
		Schema:    format.SchemaInfo{Step: 2, Steps: []format.SchemaStep{{Step: 1, Name: "00001_a", SHA256: "aa"}, {Step: 2, Name: "00002_b", SHA256: "bb"}}},
		StartedAt: snap, SnapshotAt: snap, CompletedAt: snap.Add(time.Minute),
		Source: format.Source{Storage: format.StorageRef{Bucket: "blobs", Prefix: "trove"}, RevocationJournal: true},
		Tables: []format.TableFile{
			file("secret_key_canary", [][2]string{{"key_id", "text"}, {"ciphertext", "text"}, {"created_at", "timestamp with time zone"}},
				"2026-10\t"+sealed+"\t2026-10-01 00:00:00+00\n"),
			file("items", [][2]string{{"id", "bigint"}}, "1\n2\n"),
		},
		Blobs:   format.BlobsInfo{File: "blobs.idx.zst", SHA256: sum(list), Referenced: 2},
		Secrets: format.SecretsInfo{KeyIDs: []string{"2026-10"}},
	}
	e.putManifest()
	e.sess = &fakeSession{
		steps:   []postgres.RestoreStep{{Version: 1, Name: "00001_a", SHA256: "aa"}, {Version: 2, Name: "00002_b", SHA256: "bb"}},
		inspect: postgres.RestoreInspection{SchemaExists: true, OwnsSchema: true},
	}
	e.blobs = &fakeBlobs{objs: map[format.Digest][]byte{}}
	e.jrn = &fakeJournal{}
	return e
}

func (e *env) putManifest() {
	b, err := e.m.Encode()
	if err != nil {
		e.t.Fatal(err)
	}
	e.mem.Set(e.layout.Manifest(e.id), b)
}

func (e *env) restorer() *Restorer {
	cfg := config.Backup{}
	cfg.Database.MigrationWait = time.Second
	cfg.Copy.Concurrency = 3
	cfg.Copy.ServerSide = "auto"
	return New(Deps{
		Session: e.sess, Target: e.mem, Layout: e.layout, Blobs: e.blobs, Keys: e.ring, Config: cfg, Out: &e.out, Pod: "pod",
		TargetBucket: "backups",
		Journal:      func(context.Context, JournalEnv) (Journal, error) { return e.jrn, nil },
		Registry:     nil,
	})
}

func (e *env) check(opts backup.RestoreOptions) error {
	opts.ID = e.id
	opts.Check = true
	_, err := e.restorer().Restore(context.Background(), opts)
	return err
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

func TestR_BAK_020_Checks(t *testing.T) {
	t.Run("R-BAK-020 a backup whose preconditions hold passes --check and changes nothing", func(t *testing.T) {
		e := newEnv(t)
		rep, err := e.restorer().Restore(context.Background(), backup.RestoreOptions{ID: e.id, Check: true})
		if err != nil || rep == nil || !rep.Checked {
			t.Fatalf("report = %+v, err = %v", rep, err)
		}
		for _, c := range []string{"anchor", "migrate", "load", "fix", "finish"} {
			if e.sess.called(c) {
				t.Errorf("--check called %s", c)
			}
		}
		if !e.sess.called("lock") || !e.sess.called("close") {
			t.Errorf("calls = %v, want the lock taken and the session closed", e.sess.calls)
		}
	})
	t.Run("R-BAK-020 check 1: a missing manifest, a missing file and a file of the wrong size", func(t *testing.T) {
		e := newEnv(t)
		e.mem.Remove(e.layout.Manifest(e.id))
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 1")

		e = newEnv(t)
		e.mem.Remove(e.layout.Table(e.id, "items"))
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 1", "db/items.copy.zst")

		e = newEnv(t)
		e.m.Tables[1].CompressedBytes++
		e.putManifest()
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 1", "db/items.copy.zst")
	})
	t.Run("R-BAK-003 a manifest of a higher format exits 6 and names the format", func(t *testing.T) {
		e := newEnv(t)
		b, _ := e.mem.Data(e.layout.Manifest(e.id))
		e.mem.Set(e.layout.Manifest(e.id), bytes.Replace(b, []byte(`"format": 1`), []byte(`"format": 2`), 1))
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "format 2")
	})
	t.Run("R-BAK-023 check 2: a changed or a missing migration step exits 6 and names the step and a release", func(t *testing.T) {
		e := newEnv(t)
		e.sess.steps[1].SHA256 = "changed"
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 2", "step 2 (00002_b)", "trove 0.4.0")

		e = newEnv(t)
		e.sess.steps = e.sess.steps[:1]
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 2", "migration step 2", "trove 0.4.0")
	})
	t.Run("R-BAK-020 check 3: a key ring without the backup's key ID", func(t *testing.T) {
		e := newEnv(t)
		e.ring = keyRing(t, "2026-09", 1)
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 3", "key ring does not match backup: missing key 2026-10")
		if e.sess.called("anchor") {
			t.Error("a check wrote")
		}
	})
	t.Run("R-BAK-020 check 3: the same key ID with other key material does not open the canary", func(t *testing.T) {
		e := newEnv(t)
		e.ring = keyRing(t, "2026-10", 9)
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 3", "key ring does not match backup")
	})
	t.Run("R-BAK-020 check 4: a database with a relation, another restore's anchor, and the anchor of this restore", func(t *testing.T) {
		e := newEnv(t)
		e.sess.inspect.RelationCount, e.sess.inspect.Relations = 1, []string{"repositories"}
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 4", "repositories")

		e = newEnv(t)
		e.sess.inspect.Anchor = "20250101T000000Z-aaaa"
		e.sess.inspect.RelationCount = 5
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 4", "20250101T000000Z-aaaa", "20261028T020000Z-7f3a")

		e = newEnv(t)
		e.sess.inspect.Anchor = string(e.id)
		e.sess.inspect.RelationCount = 40
		if err := e.check(backup.RestoreOptions{}); err != nil {
			t.Fatalf("the anchor of this restore is accepted whatever the schema holds: %v", err)
		}
		if !e.blobs.allowSeen {
			t.Error("check 5 did not accept the blobs of the interrupted restore")
		}

		e = newEnv(t)
		e.sess.inspect.SchemaExists = false
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 4", "does not exist")
	})
	t.Run("R-BAK-020 check 5: a foreign object under the blob prefix", func(t *testing.T) {
		e := newEnv(t)
		e.blobs.foreign = "trove/stray"
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 5", "trove/stray")
	})
	t.Run("R-BAK-020 check 6: an unsupported server and a credential that does not own the schema", func(t *testing.T) {
		e := newEnv(t)
		e.sess.server = errors.New("PostgreSQL 14 is older than the supported minimum 16")
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 6", "minimum 16")

		e = newEnv(t)
		e.sess.inspect.OwnsSchema = false
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 6", "does not own schema")
	})
	t.Run("R-BAK-020 check 7: a blob store that takes no probe object", func(t *testing.T) {
		e := newEnv(t)
		e.blobs.probeErr = errors.New("AccessDenied")
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 7", "AccessDenied")
	})
	t.Run("R-BAK-020 check 8: an unreachable journal exits 6, --skip-revocation-journal passes", func(t *testing.T) {
		e := newEnv(t)
		e.jrn.reach = errors.New("AccessDenied")
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 8", "--skip-revocation-journal")
		if err := e.check(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
			t.Fatalf("skip: %v", err)
		}
		e.m.Source.RevocationJournal = false
		e.putManifest()
		if err := e.check(backup.RestoreOptions{}); err != nil {
			t.Fatalf("a source without a journal: %v", err)
		}
	})
	t.Run("R-BAK-020 the first failed check is the one named", func(t *testing.T) {
		e := newEnv(t)
		e.blobs.foreign = "x"
		e.sess.inspect.OwnsSchema = false
		e.jrn.reach = errors.New("down")
		wantExit(t, e.check(backup.RestoreOptions{}), backup.ExitPrecondition, "check 5")
	})
}

func TestR_BAK_021_Exits(t *testing.T) {
	t.Run("R-BAK-021 a migration lock held by another session exits 75 and writes nothing", func(t *testing.T) {
		e := newEnv(t)
		e.sess.lockErr = &postgres.LockWaitError{Wait: time.Second, Holders: "trove:other:migrate"}
		_, err := e.restorer().Restore(context.Background(), backup.RestoreOptions{ID: e.id})
		wantExit(t, err, backup.ExitTemporary, "backup.database.migration_wait", "trove:other:migrate")
		if e.sess.called("anchor") {
			t.Error("wrote while waiting")
		}
	})
	t.Run("R-BAK-013 a restore without progress for backup.stall_timeout exits 75 as stalled", func(t *testing.T) {
		e := newEnv(t)
		e.sess.lockBlock = true
		r := e.restorer()
		r.d.Config.StallTimeout = 50 * time.Millisecond
		_, err := r.Restore(context.Background(), backup.RestoreOptions{ID: e.id})
		wantExit(t, err, backup.ExitTemporary, "stall_timeout")
		var ee *backup.ExitError
		if !errors.As(err, &ee) || ee.Failure != backup.CodeStalled {
			t.Fatalf("failure = %+v", ee)
		}
	})
	t.Run("R-BAK-021 SIGTERM exits 75, interrupted", func(t *testing.T) {
		e := newEnv(t)
		e.sess.lockBlock = true
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		_, err := e.restorer().Restore(ctx, backup.RestoreOptions{ID: e.id})
		wantExit(t, err, backup.ExitTemporary, "interrupted")
	})
	t.Run("R-BAK-021 a schema that differs from the backup exits 6", func(t *testing.T) {
		e := newEnv(t)
		e.sess.inspect.Marker = &postgres.RestoreMarker{BackupID: string(e.id), Phase: postgres.PhaseBlobs}
		e.sess.loadErr = &postgres.RestoreSchemaError{Problems: []string{"table items has the columns (id bigint, x text)"}}
		_, err := e.restorer().Restore(context.Background(), backup.RestoreOptions{ID: e.id, SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "table items has the columns")
	})
}

func TestR_BAK_021_Phases(t *testing.T) {
	t.Run("R-BAK-021 a marker in phase loaded skips the blobs and the load; fixed skips the fix-ups", func(t *testing.T) {
		e := newEnv(t)
		r := e.restorer()
		st := &state{id: e.id, m: e.m}
		rep := &backup.RestoreReport{}
		for _, phase := range []string{postgres.PhaseLoaded, postgres.PhaseFixed} {
			marker := &postgres.RestoreMarker{BackupID: string(e.id), Phase: phase}
			if err := r.stepBlobs(context.Background(), st, marker); err != nil {
				t.Fatal(err)
			}
			if err := r.stepLoad(context.Background(), st, marker, rep); err != nil {
				t.Fatal(err)
			}
			if phase == postgres.PhaseFixed {
				if err := r.stepFixups(context.Background(), st, marker); err != nil {
					t.Fatal(err)
				}
			}
		}
		if e.sess.loaded || e.sess.called("fix") || len(e.blobs.objs) != 0 {
			t.Fatalf("a phase was repeated: calls %v, objects %d", e.sess.calls, len(e.blobs.objs))
		}
	})
	t.Run("R-BAK-021 step 4 runs once in phase blobs and moves the marker to loaded; step 6 runs once in loaded", func(t *testing.T) {
		e := newEnv(t)
		r := e.restorer()
		st := &state{id: e.id, m: e.m}
		marker := &postgres.RestoreMarker{BackupID: string(e.id), Phase: postgres.PhaseBlobs}
		if err := r.stepLoad(context.Background(), st, marker, &backup.RestoreReport{}); err != nil || !e.sess.loaded || marker.Phase != postgres.PhaseLoaded {
			t.Fatalf("load: %v, phase %s", err, marker.Phase)
		}
		if err := r.stepFixups(context.Background(), st, marker); err != nil || !e.sess.called("fix") || marker.Phase != postgres.PhaseFixed {
			t.Fatalf("fix-ups: %v, phase %s", err, marker.Phase)
		}
	})
	t.Run("R-BAK-021 step 2 anchors, migrates to the backup's step with the marker, and a resumed run keeps its anchor", func(t *testing.T) {
		e := newEnv(t)
		e.sess.inspect.Anchor = string(e.id)
		e.sess.inspect.Marker = &postgres.RestoreMarker{BackupID: string(e.id), Phase: postgres.PhaseBlobs}
		st := &state{id: e.id, m: e.m, inspect: e.sess.inspect}
		marker, err := e.restorer().stepSchema(context.Background(), st)
		if err != nil || marker.Phase != postgres.PhaseBlobs {
			t.Fatalf("marker = %+v, %v", marker, err)
		}
		if e.sess.called("anchor") {
			t.Error("the anchor was set again")
		}
		if len(e.sess.migrated) != 1 || e.sess.migrated[0].UpTo != 2 || e.sess.migrated[0].Marker == nil {
			t.Fatalf("migration = %+v", e.sess.migrated)
		}
	})
}

func TestR_BAK_021_Blobs(t *testing.T) {
	run := func(e *env) error {
		r := e.restorer()
		return r.stepBlobs(context.Background(), &state{id: e.id, m: e.m}, &postgres.RestoreMarker{Phase: postgres.PhaseBlobs})
	}
	t.Run("R-BAK-021 step 3 streams from the pool when the server-side copy is unavailable, and skips what exists", func(t *testing.T) {
		e := newEnv(t)
		if err := run(e); err != nil {
			t.Fatal(err)
		}
		if len(e.blobs.objs) != 2 || e.blobs.streams != 2 {
			t.Fatalf("objects %d streams %d", len(e.blobs.objs), e.blobs.streams)
		}
		before := e.mem.Calls["get"]
		if err := run(e); err != nil {
			t.Fatal(err)
		}
		if e.blobs.streams != 2 {
			t.Fatalf("a resumed copy streamed again: %d", e.blobs.streams)
		}
		// Only the blob list was read again.
		if got := e.mem.Calls["get"] - before; got != 1 {
			t.Fatalf("%d reads of the target on a repeated copy, want the blob list only", got)
		}
	})
	t.Run("R-BAK-021 step 3 uses the server-side copy when the store allows it", func(t *testing.T) {
		e := newEnv(t)
		e.blobs.serverCopy = true
		if err := run(e); err != nil {
			t.Fatal(err)
		}
		if e.blobs.copies != 2 || e.blobs.streams != 0 {
			t.Fatalf("copies %d streams %d", e.blobs.copies, e.blobs.streams)
		}
	})
	t.Run("R-BAK-021 server_side always fails when the copy is unavailable; never streams without asking", func(t *testing.T) {
		e := newEnv(t)
		r := e.restorer()
		r.d.Config.Copy.ServerSide = "always"
		err := r.stepBlobs(context.Background(), &state{id: e.id, m: e.m}, &postgres.RestoreMarker{Phase: postgres.PhaseBlobs})
		wantExit(t, err, backup.ExitFailed, "always")

		e = newEnv(t)
		e.blobs.serverCopy = true
		r = e.restorer()
		r.d.Config.Copy.ServerSide = "never"
		if err := r.stepBlobs(context.Background(), &state{id: e.id, m: e.m}, &postgres.RestoreMarker{Phase: postgres.PhaseBlobs}); err != nil {
			t.Fatal(err)
		}
		if e.blobs.copies != 0 || e.blobs.streams != 2 {
			t.Fatalf("copies %d streams %d", e.blobs.copies, e.blobs.streams)
		}
	})
	t.Run("R-BAK-021 a missing pool object stops the restore with exit 1 and the digest", func(t *testing.T) {
		e := newEnv(t)
		for d := range e.pool {
			e.mem.Remove(e.layout.Pool(d))
			err := run(e)
			wantExit(t, err, backup.ExitFailed, d.Hex(), "trove backup verify")
			return
		}
	})
	t.Run("R-BAK-006 a damaged blob is not copied", func(t *testing.T) {
		e := newEnv(t)
		var d format.Digest
		for d = range e.pool {
			break
		}
		e.mem.Remove(e.layout.Pool(d))
		e.m.Blobs.Damaged = []format.DamagedBlob{{SHA256: d.Hex(), Status: "missing"}}
		if err := run(e); err != nil {
			t.Fatal(err)
		}
		if len(e.blobs.objs) != 1 {
			t.Fatalf("objects = %d", len(e.blobs.objs))
		}
	})
	t.Run("R-BAK-003 a blob list that does not match the manifest's SHA-256 fails the step", func(t *testing.T) {
		e := newEnv(t)
		e.m.Blobs.SHA256 = strings.Repeat("0", 64)
		err := run(e)
		wantExit(t, err, backup.ExitFailed, "blob list")
	})
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{`plain`: "plain", `a\tb`: "a\tb", `a\\b`: `a\b`, `line\nnext`: "line\nnext", `x\.y`: "x.y"} {
		if got, null := unescape(in); got != want || null {
			t.Errorf("unescape(%q) = %q, %v", in, got, null)
		}
	}
	if _, null := unescape(`\N`); !null {
		t.Error(`\N is null`)
	}
}

func TestFileReaderVerifies(t *testing.T) {
	mem := targettest.New()
	z := zstdBytes(t, []byte("a\nb\n"))
	mem.Set("k", z)
	read := func(size int64, digest string) error {
		f, err := openFile(context.Background(), mem, "k", "k", size, digest, nil)
		if err != nil {
			t.Fatal(err)
		}
		if b, err := io.ReadAll(f); err != nil || string(b) != "a\nb\n" {
			t.Fatalf("read %q, %v", b, err)
		}
		return f.Close()
	}
	if err := read(int64(len(z)), sum(z)); err != nil {
		t.Fatalf("good file: %v", err)
	}
	if err := read(int64(len(z))+1, sum(z)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("size: %v", err)
	}
	if err := read(int64(len(z)), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("digest: %v", err)
	}
	if _, err := openFile(context.Background(), mem, "missing", "missing", 0, "", nil); backup.ExitCode(err) != backup.ExitPrecondition {
		t.Fatalf("missing: %v", err)
	}
	_ = fmt.Sprint
}
