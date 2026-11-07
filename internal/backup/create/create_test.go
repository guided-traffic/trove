package create

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

func exitOf(t *testing.T, err error, want int, code string) {
	t.Helper()
	if got := backup.ExitCode(err); got != want {
		t.Fatalf("exit code = %d (%v), want %d", got, err, want)
	}
	if code != "" && backup.ErrorCode(err) != code {
		t.Fatalf("failure code = %q (%v), want %q", backup.ErrorCode(err), err, code)
	}
}

func TestR_BAK_009_PhasesAndManifest(t *testing.T) {
	e := newEnv(t)
	a := e.addBlob("alpha")
	e.addBlob("beta")
	res, err := e.create(backup.CreateOptions{Trigger: backup.TriggerManual})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed" || res.BlobsCopied != 2 || res.BlobCount != 2 {
		t.Fatalf("result = %+v", res)
	}
	m := e.manifest(res.BackupID)
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Blobs.Referenced != 2 || m.Blobs.Copied != 2 || len(m.Tables) != 2 || m.Tables[0].Name != "blobs" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Tables[1].Rows != 2 || m.Tables[1].File != "db/items.copy.zst" || len(m.Tables[1].SHA256) != 64 {
		t.Fatalf("table = %+v", m.Tables[1])
	}
	if m.Encryption.SSE != "AES256" || m.Secrets.KeyIDs[0] != "2026-10" || m.Postgres.Isolation != "repeatable read" {
		t.Fatalf("manifest = %+v", m)
	}
	if _, ok := e.mem.Data("trove/pool/sha256/" + a.Hex()[:2] + "/" + a.Hex()[2:4] + "/" + a.Hex()); !ok {
		t.Fatalf("pool object missing: %v", e.mem.Keys())
	}
	if _, ok := e.mem.Data("trove/trove-backup-target.json"); !ok {
		t.Fatal("marker not written")
	}
	if e.runs.finished != backup.StateCompleted || len(e.runs.pins) != 0 || !e.prune.called {
		t.Fatalf("run = %+v pins %d prune %v", e.runs.finished, len(e.runs.pins), e.prune.called)
	}
	if !slices.Equal(e.events.types(), []string{audit.EventBackupCompleted}) {
		t.Fatalf("events = %v", e.events.types())
	}
	if !e.snap.released || !e.snap.closed {
		t.Fatal("the migration lock and the snapshot must be released after the export")
	}
}

func TestR_BAK_007_HoldProtocolOrder(t *testing.T) {
	e := newEnv(t)
	e.addBlob("alpha")
	var seen []string
	e.build()
	e.deps.Fault = func(_ context.Context, point string) error {
		seen = append(seen, point+"="+string(e.runs.Hold()))
		return nil
	}
	p, _ := New(e.deps)
	if _, err := p.Create(context.Background(), backup.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"hold-all=all", "snapshot=all", "pins-committed=all", "hold-pinned=pinned", "copied=none", "finalizing=none"}
	if !slices.Equal(seen, want) {
		t.Fatalf("hold at the points = %v, want %v", seen, want)
	}
	if e.snap.holdAtOpen != backup.HoldAll {
		t.Fatalf("the hold was %s when the snapshot was taken, want all (R-BAK-007 step 2)", e.snap.holdAtOpen)
	}
	if got := e.runs.holds; !slices.Equal(got, []backup.Hold{backup.HoldNone, backup.HoldAll, backup.HoldPinned, backup.HoldNone, backup.HoldNone}) {
		t.Fatalf("holds = %v", got)
	}
}

func TestR_BAK_007_NoPinsMeansNoPinnedHold(t *testing.T) {
	e := newEnv(t)
	if _, err := e.create(backup.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, h := range e.runs.holds {
		if h == backup.HoldPinned {
			t.Fatalf("holds = %v: nothing to copy needs no pinned hold", e.runs.holds)
		}
	}
}

func TestR_BAK_006_IncrementalCopiesOnlyNewDigests(t *testing.T) {
	e := newEnv(t)
	e.addBlob("alpha")
	e.addBlob("beta")
	first, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.runs = newFakeRuns()
	e.snap.runs = e.runs
	e.mem.Calls = map[string]int{}
	e.now = e.now.Add(time.Minute) // the backup ID has a resolution of a second
	second, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.BlobsCopied != 0 || e.mem.Calls["copy"] != 0 {
		t.Fatalf("a second run without new blobs copied %d, copy calls %d", second.BlobsCopied, e.mem.Calls["copy"])
	}
	for _, k := range e.mem.Keys() {
		if strings.Contains(k, "/pool/") {
			t.Logf("pool: %s", k)
		}
	}
	e.runs = newFakeRuns()
	e.snap.runs = e.runs
	e.addBlob("gamma")
	e.now = e.now.Add(time.Minute)
	third, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if third.BlobsCopied != 1 || third.BlobCount != 3 {
		t.Fatalf("third = %+v, want one new blob of three", third)
	}
	m := e.manifest(third.BackupID)
	if m.PreviousBackupID == nil || string(*m.PreviousBackupID) != second.BackupID || first.BackupID == second.BackupID {
		t.Fatalf("previous = %v, want %s", m.PreviousBackupID, second.BackupID)
	}
}

func TestR_BAK_006_ServerSideAndStreamingCopy(t *testing.T) {
	t.Run("a store that copies server-side is used and counted", func(t *testing.T) {
		e := newEnv(t)
		e.addBlob("alpha")
		if _, err := e.create(backup.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if e.mem.Calls["copy"] != 1 || e.mem.Calls["writer"] != 3 { // blob list and two tables; the blob itself is not streamed
			t.Fatalf("calls = %v", e.mem.Calls)
		}
	})
	t.Run("a store that rejects it falls back to streaming once and the run completes", func(t *testing.T) {
		e := newEnv(t)
		for i := range 5 {
			e.addBlob(strings.Repeat("x", i+1))
		}
		e.mem.Source = nil
		res, err := e.create(backup.CreateOptions{})
		if err != nil || res.BlobsCopied != 5 {
			t.Fatalf("res %+v err %v", res, err)
		}
		if e.mem.Calls["copy"] > 4 { // at most one probe per worker before the first failure is seen
			t.Fatalf("copy calls = %d", e.mem.Calls["copy"])
		}
	})
	t.Run("server_side always fails when the store cannot", func(t *testing.T) {
		e := newEnv(t)
		e.addBlob("alpha")
		e.mem.Source = nil
		e.cfg.Copy.ServerSide = "always"
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitFailed, "")
	})
	t.Run("server_side never streams and hashes", func(t *testing.T) {
		e := newEnv(t)
		d := e.addBlob("alpha")
		e.cfg.Copy.ServerSide = "never"
		if _, err := e.create(backup.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if e.mem.Calls["copy"] != 0 {
			t.Fatalf("calls = %v", e.mem.Calls)
		}
		if b, _ := e.mem.Data("trove/pool/sha256/" + d.Hex()[:2] + "/" + d.Hex()[2:4] + "/" + d.Hex()); string(b) != "alpha" {
			t.Fatalf("pool object = %q", b)
		}
	})
}

func TestR_BAK_006_SourceDamage(t *testing.T) {
	t.Run("a live blob whose object is missing is recorded and the run completes with warnings", func(t *testing.T) {
		e := newEnv(t)
		e.addBlob("alpha")
		bad := e.addBlob("beta")
		delete(e.src.data, bad)
		res, err := e.create(backup.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != "completed_with_warnings" || res.DamagedBlobs != 1 || res.BlobsCopied != 1 {
			t.Fatalf("res = %+v", res)
		}
		m := e.manifest(res.BackupID)
		if len(m.Blobs.Damaged) != 1 || m.Blobs.Damaged[0].SHA256 != bad.Hex() || m.Blobs.Damaged[0].Status != "missing" {
			t.Fatalf("damaged = %+v", m.Blobs.Damaged)
		}
		if e.runs.finished != backup.StateCompletedWithWarning {
			t.Fatalf("state = %s", e.runs.finished)
		}
	})
	t.Run("an object of the right size with other content is corrupt", func(t *testing.T) {
		e := newEnv(t)
		d := e.addBlob("alpha")
		e.src.data[d] = []byte("alphb")
		e.cfg.Copy.ServerSide = "never"
		res, err := e.create(backup.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if m := e.manifest(res.BackupID); m.Blobs.Damaged[0].Status != "corrupt" {
			t.Fatalf("damaged = %+v", m.Blobs.Damaged)
		}
		if _, ok := e.mem.Data("trove/pool/sha256/" + d.Hex()[:2] + "/" + d.Hex()[2:4] + "/" + d.Hex()); ok {
			t.Fatal("a corrupt blob must not reach the pool")
		}
	})
	t.Run("a blob that left the source with its row gone is a lost hold: hold_lost, exit 1, no manifest", func(t *testing.T) {
		e := newEnv(t)
		d := e.addBlob("alpha")
		delete(e.src.data, d)
		delete(e.facts.states, d)
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitFailed, backup.CodeHoldLost)
		if e.hasManifest() || e.runs.finished != backup.StateFailed || len(e.runs.pins) != 0 || e.runs.Hold() != backup.HoldNone {
			t.Fatalf("manifest %v state %s pins %d hold %s", e.hasManifest(), e.runs.finished, len(e.runs.pins), e.runs.Hold())
		}
		if !slices.Equal(e.events.types(), []string{audit.EventBackupFailed}) {
			t.Fatalf("events = %v", e.events.types())
		}
	})
	t.Run("a row in state deleting is a lost hold too", func(t *testing.T) {
		e := newEnv(t)
		d := e.addBlob("alpha")
		delete(e.src.data, d)
		e.facts.states[d] = "deleting"
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitFailed, backup.CodeHoldLost)
	})
}

func TestR_BAK_013_ExitCodes(t *testing.T) {
	t.Run("0 success", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.create(backup.CreateOptions{}); backup.ExitCode(err) != 0 {
			t.Fatal(err)
		}
	})
	t.Run("2 a standby", func(t *testing.T) {
		e := newEnv(t)
		e.snap.openErr = backup.Fail(backup.ExitUsage, backup.CodeStandby, errorf("pg_is_in_recovery"))
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitUsage, backup.CodeStandby)
		if e.hasManifest() {
			t.Fatal("manifest written")
		}
	})
	t.Run("2 a missing grant, before any run row", func(t *testing.T) {
		e := newEnv(t)
		e.facts.grants = backup.Failf(backup.ExitUsage, backup.CodeBackupRoleGrant, "SELECT on items")
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitUsage, backup.CodeBackupRoleGrant)
		if e.runs.have {
			t.Fatal("a run row was written")
		}
	})
	t.Run("2 the target reports another encryption than configured", func(t *testing.T) {
		e := newEnv(t)
		e.mem.SSE = ""
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitUsage, backup.CodeEncryptionMismatch)
		if !strings.Contains(err.Error(), "backup.target.sse.mode") {
			t.Fatalf("the message must name the key: %v", err)
		}
	})
	t.Run("3 another live run holds the slot, nothing is written to the target", func(t *testing.T) {
		e := newEnv(t)
		e.runs.taken = &backup.Run{ID: "other", Operation: backup.OpCreate, State: backup.StateCopying, Pod: "pod-x", HeartbeatAt: time.Now(), StartedAt: time.Now()}
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitBusy, backup.CodeBusy)
		if !strings.Contains(err.Error(), "other") || len(e.mem.Keys()) != 0 {
			t.Fatalf("err %v keys %v", err, e.mem.Keys())
		}
	})
	t.Run("4 the marker names another installation and its ID", func(t *testing.T) {
		e := newEnv(t)
		m := format.NewMarker("someone-else", "inc-9", time.Now())
		b, _ := m.Encode()
		e.mem.Set("trove/trove-backup-target.json", b)
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitForeignTarget, backup.CodeForeignTarget)
		if !strings.Contains(err.Error(), "someone-else") || e.hasManifest() {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("4 a prefix with backups but no marker", func(t *testing.T) {
		e := newEnv(t)
		e.mem.Set("trove/backups/20260101T000000Z-aaaa/manifest.json", []byte("{}"))
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitForeignTarget, backup.CodeForeignTarget)
	})
	t.Run("6 a previous backup of a higher manifest format", func(t *testing.T) {
		e := newEnv(t)
		e.mem.Set("trove/backups/20260101T000000Z-aaaa/manifest.json", []byte(`{"format": 99}`))
		m := format.NewMarker("inst-1", "inc-1", time.Now())
		b, _ := m.Encode()
		e.mem.Set("trove/trove-backup-target.json", b)
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitPrecondition, backup.CodeFormatUnknown)
	})
	t.Run("75 a restore in progress writes nothing", func(t *testing.T) {
		e := newEnv(t)
		e.facts.facts.RestoreInProgress = true
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitTemporary, backup.CodeRestoreInProgress)
		if e.runs.have || len(e.mem.Keys()) != 0 {
			t.Fatal("something was written")
		}
	})
	t.Run("75 a schema behind the binary writes nothing", func(t *testing.T) {
		e := newEnv(t)
		e.facts.facts.SchemaBehind = true
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitTemporary, "")
	})
	t.Run("75 a migration wait that timed out", func(t *testing.T) {
		e := newEnv(t)
		e.snap.openErr = backup.Fail(backup.ExitTemporary, backup.CodeMigrationWait, errorf("migration"))
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitTemporary, backup.CodeMigrationWait)
		if e.runs.finished != backup.StateFailed {
			t.Fatalf("state = %s", e.runs.finished)
		}
	})
	t.Run("75 an unreachable target", func(t *testing.T) {
		e := newEnv(t)
		e.build()
		e.deps.Target = &downTarget{Store: e.mem}
		pp, err := New(e.deps)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pp.Create(context.Background(), backup.CreateOptions{})
		exitOf(t, err, backup.ExitTemporary, backup.CodeTemporary)
	})
	t.Run("1 the spot check finds a pool object missing", func(t *testing.T) {
		e := newEnv(t)
		a := e.addBlob("alpha")
		if _, err := e.create(backup.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		e.mem.Remove("trove/pool/sha256/" + a.Hex()[:2] + "/" + a.Hex()[2:4] + "/" + a.Hex())
		e.runs = newFakeRuns()
		e.snap.runs = e.runs
		e.now = e.now.Add(time.Minute)
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitFailed, backup.CodePoolDamaged)
		for _, k := range e.mem.Keys() {
			if strings.HasSuffix(k, "manifest.json") && strings.Contains(k, "backups/") && k != e.firstManifestKey() {
				t.Fatalf("a manifest was written by the failed run: %s", k)
			}
		}
	})
	t.Run("1 the run was superseded: the fence refuses and no manifest is written", func(t *testing.T) {
		e := newEnv(t)
		e.runs.fenceOK = false
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitFailed, backup.CodeSuperseded)
		if e.hasManifest() {
			t.Fatal("manifest written after the fence refused")
		}
	})
}

func (e *env) firstManifestKey() string {
	for _, k := range e.mem.Keys() {
		if strings.HasSuffix(k, "manifest.json") {
			return k
		}
	}
	return ""
}

type downTarget struct{ target.Store }

func (d *downTarget) Probe(context.Context, string) (target.Object, error) {
	return target.Object{}, fmt.Errorf("dial tcp 10.0.0.1:443: %w", syscall.ECONNREFUSED)
}
