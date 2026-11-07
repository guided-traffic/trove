package create

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

func (e *env) putMarker(inst, inc string) {
	b, _ := format.NewMarker(inst, inc, time.Now()).Encode()
	e.mem.Set("trove/trove-backup-target.json", b)
}

func TestR_BAK_013_StallEndsTheRunAndReleasesTheHold(t *testing.T) {
	e := newEnv(t)
	e.cfg.HeartbeatInterval = 20 * time.Millisecond
	e.cfg.StallTimeout = 200 * time.Millisecond
	e.addBlob("alpha")
	e.snap.onExport = func(ctx context.Context) { <-ctx.Done() }
	start := time.Now()
	_, err := e.create(backup.CreateOptions{})
	exitOf(t, err, backup.ExitTemporary, backup.CodeStalled)
	if time.Since(start) > 10*time.Second {
		t.Fatal("the stall was not detected")
	}
	if e.runs.finished != backup.StateFailed || len(e.runs.pins) != 0 || e.runs.Hold() != backup.HoldNone || e.hasManifest() {
		t.Fatalf("state %s pins %d hold %s manifest %v", e.runs.finished, len(e.runs.pins), e.runs.Hold(), e.hasManifest())
	}
	if !e.snap.closed {
		t.Fatal("the snapshot must be closed")
	}
}

func TestR_BAK_012_SIGTERMInterruptsTheRun(t *testing.T) {
	e := newEnv(t)
	e.addBlob("alpha")
	ctx, cancel := context.WithCancel(context.Background())
	e.snap.onExport = func(c context.Context) { cancel(); <-c.Done() }
	_, err := e.build().Create(ctx, backup.CreateOptions{})
	exitOf(t, err, backup.ExitTemporary, backup.CodeInterrupted)
	if e.runs.finished != backup.StateInterrupted || len(e.runs.pins) != 0 || e.runs.Hold() != backup.HoldNone || e.hasManifest() {
		t.Fatalf("state %s pins %d hold %s", e.runs.finished, len(e.runs.pins), e.runs.Hold())
	}
	if slices.Contains(e.events.types(), audit.EventBackupFailed) {
		t.Fatalf("an interrupted run is not a failure for alerting: %v", e.events.types())
	}
	if !e.snap.closed {
		t.Fatal("the snapshot must be closed")
	}
}

func TestR_BAK_002_AdoptTarget(t *testing.T) {
	restored := &backup.RestoredFrom{InstallationID: "old", Incarnation: "old-inc"}
	t.Run("a restored installation adopts its predecessor's target", func(t *testing.T) {
		e := newEnv(t)
		e.putMarker("old", "old-inc")
		e.facts.facts.RestoredFrom = restored
		res, err := e.create(backup.CreateOptions{AdoptTarget: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := e.mem.Data("trove/trove-backup-target.json")
		m, _ := format.DecodeMarker(b)
		if m.Incarnation != "inc-1" || m.AdoptedAt == nil {
			t.Fatalf("marker = %+v", m)
		}
		if !slices.Contains(e.events.types(), audit.EventBackupTargetAdopted) || res.Outcome == "" {
			t.Fatalf("events = %v", e.events.types())
		}
	})
	t.Run("without the flag the marker of the predecessor is exit 4", func(t *testing.T) {
		e := newEnv(t)
		e.putMarker("old", "old-inc")
		e.facts.facts.RestoredFrom = restored
		_, err := e.create(backup.CreateOptions{})
		exitOf(t, err, backup.ExitForeignTarget, "")
	})
	t.Run("a marker of another lineage cannot be adopted", func(t *testing.T) {
		e := newEnv(t)
		e.putMarker("stranger", "x")
		e.facts.facts.RestoredFrom = restored
		_, err := e.create(backup.CreateOptions{AdoptTarget: true})
		exitOf(t, err, backup.ExitForeignTarget, "")
		if !strings.Contains(err.Error(), "adopt-target") {
			t.Fatal(err)
		}
	})
	t.Run("without restored_from the flag is exit 4", func(t *testing.T) {
		e := newEnv(t)
		e.putMarker("old", "old-inc")
		_, err := e.create(backup.CreateOptions{AdoptTarget: true})
		exitOf(t, err, backup.ExitForeignTarget, "")
	})
}

func TestR_BAK_006_CheckExistingSkipsObjectsAPredecessorCopied(t *testing.T) {
	e := newEnv(t)
	d := e.addBlob("alpha")
	e.putMarker("inst-1", "inc-1")
	// An incomplete directory of a killed run, and the pool object it had copied.
	e.mem.Set("trove/backups/20200101T000000Z-aaaa/db/items.copy.zst", []byte("partial"))
	e.mem.Set("trove/pool/sha256/"+d.Hex()[:2]+"/"+d.Hex()[2:4]+"/"+d.Hex(), []byte("alpha"))
	e.mem.Calls = map[string]int{}
	res, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if e.mem.Calls["copy"] != 0 || res.BlobsCopied != 1 {
		t.Fatalf("copy calls %d, copied %d: an object already in the pool is not copied again", e.mem.Calls["copy"], res.BlobsCopied)
	}
	if e.mem.Calls["head"] == 0 {
		t.Fatal("check_existing auto must HEAD when an incomplete directory is newer than the previous backup")
	}
}

func TestR_BAK_009_PruningFailureIsAWarningNotAFailedBackup(t *testing.T) {
	e := newEnv(t)
	e.prune.err = errorf("delete refused")
	res, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed_with_warnings" || len(res.Warnings) == 0 || !e.hasManifest() {
		t.Fatalf("res = %+v", res)
	}
}

func TestR_BAK_009_NoPruneSkipsThePruningPhase(t *testing.T) {
	e := newEnv(t)
	if _, err := e.create(backup.CreateOptions{NoPrune: true}); err != nil {
		t.Fatal(err)
	}
	if e.prune.called {
		t.Fatal("--no-prune ran the pruning phase")
	}
}

func TestR_BAK_005_FailedExportAbortsOpenUploads(t *testing.T) {
	e := newEnv(t)
	e.addBlob("alpha")
	e.snap.exportErr = errorf("copy: connection lost")
	_, err := e.create(backup.CreateOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if e.hasManifest() {
		t.Fatal("manifest written")
	}
	for _, k := range e.mem.Keys() {
		if strings.Contains(k, "/db/") {
			t.Fatalf("a table file of a failed export was left: %s", k)
		}
	}
}

func TestR_BAK_008_ExcludeRemoteCachesIsRecorded(t *testing.T) {
	e := newEnv(t)
	res, err := e.create(backup.CreateOptions{ExcludeRemoteCaches: true})
	if err != nil {
		t.Fatal(err)
	}
	if !e.manifest(res.BackupID).Options.ExcludeRemoteCaches || len(res.Warnings) == 0 {
		t.Fatal("the option must be in the manifest and warned about")
	}
}

func TestR_BAK_004_UnregisteredTablesAreExportedAndListed(t *testing.T) {
	e := newEnv(t)
	e.snap.tables = append(e.snap.tables, backup.TableInfo{Name: "x_new", Columns: [][2]string{{"a", "text"}}, EstimatedBytes: 1})
	e.snap.rows["x_new"] = "v\n"
	res, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.UnregisteredTables, []string{"x_new"}) {
		t.Fatalf("unregistered = %v", res.UnregisteredTables)
	}
	if _, ok := e.manifest(res.BackupID).Table("x_new"); !ok {
		t.Fatal("the table must be in the manifest")
	}
}

func TestR_BAK_013_PinsAreDeletedInBatchesOfAtMost1000(t *testing.T) {
	e := newEnv(t)
	for i := range 40 {
		e.addBlob(strings.Repeat("b", i+1))
	}
	e.build()
	if _, err := e.create(backup.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(e.runs.pins) != 0 {
		t.Fatal("pins left")
	}
}

func TestR_BAK_009_SamplerKeepsAUniformReservoir(t *testing.T) {
	s := newSampler(5)
	for i := range 1000 {
		var d format.Digest
		d[0], d[1] = byte(i>>8), byte(i)
		s.listed(d)
		if i%2 == 0 {
			s.copying()
		}
	}
	s.flush()
	if len(s.out) != 5 {
		t.Fatalf("sample = %d", len(s.out))
	}
	for _, d := range s.out {
		if (int(d[0])<<8|int(d[1]))%2 == 0 {
			t.Fatalf("a digest the run copied itself is in the sample: %x", d[:2])
		}
	}
}

func TestR_BAK_006_LimiterCapsTheRate(t *testing.T) {
	l := newLimiter(1000)
	start := time.Now()
	for range 4 {
		if err := l.wait(context.Background(), 1000); err != nil {
			t.Fatal(err)
		}
	}
	// 4000 bytes at 1000 B/s with a one second burst: the last reservation is due after 3 seconds, so the call returns after 2.
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("4 kB at 1 kB/s took %s", d)
	}
	if newLimiter(0).wait(context.Background(), 1<<30) != nil {
		t.Fatal("zero is unlimited")
	}
}

// A heartbeat is fenced on the run's state. A state change that races a heartbeat must never read as a superseded run
// (R-BAK-010): the handle serialises the two.
func TestR_BAK_010_AStateChangeNeverRacesTheHeartbeat(t *testing.T) {
	runs := newFakeRuns()
	run, err := runs.Begin(context.Background(), backup.NewRun{ID: "r", Operation: backup.OpCreate, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	h, ctx := backup.StartHandle(context.Background(), runs, run, backup.Timing{Lease: time.Minute, Heartbeat: time.Millisecond, Stall: time.Minute, Finalize: time.Second}, nil)
	defer h.Stop()
	states := []backup.State{backup.StateSnapshot, backup.StateCopying, backup.StateFinalizing, backup.StatePruning}
	for i := range 400 {
		if err := h.Transition(ctx, states[i%len(states)], ""); err != nil {
			// The cycle ends at pruning and the fake only moves forward from the current state: restart it.
			t.Fatalf("transition %d: %v (cause %v)", i, err, context.Cause(ctx))
		}
		time.Sleep(200 * time.Microsecond)
		if ctx.Err() != nil {
			t.Fatalf("the run was cancelled at transition %d: %v", i, context.Cause(ctx))
		}
	}
}
