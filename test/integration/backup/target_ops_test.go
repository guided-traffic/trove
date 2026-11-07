//go:build integration

package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
	"github.com/guided-traffic/trove/internal/backup/retention"
	"github.com/guided-traffic/trove/internal/backup/runstoretest"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
	"github.com/guided-traffic/trove/internal/backup/verify"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The target operations against the stack's S3 store through the real adapter. The stack's SeaweedFS rejects SSE-S3 writes, so
// the encryption mode is none; the runs are recorded in the in-memory run store (the run store has its own integration test).

type opsFacts struct{ inst, inc string }

func (f opsFacts) SystemFacts(context.Context) (backup.SystemFacts, error) {
	return backup.SystemFacts{InstallationID: f.inst, Incarnation: f.inc}, nil
}

type opsAudit struct {
	mu  sync.Mutex
	ids []string
}

func (a *opsAudit) BackupDeleted(_ context.Context, id format.ID, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids = append(a.ids, string(id)+":"+reason)
	return nil
}

type opsEnv struct {
	store  *s3.Target
	layout format.Layout
	mem    *targettest.Mem
	audit  *opsAudit
	runs   *runstoretest.Mem
	runner *retention.Runner
}

func newOpsEnv(t *testing.T, keepLast int) *opsEnv {
	t.Helper()
	cfg := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), "none")
	store := newTarget(t, cfg)
	layout := format.Layout{Prefix: cfg.Target.Prefix}
	cleanup(t, store, layout.Prefix+"/")
	ctx := context.Background()
	marker, err := format.NewMarker("inst-1", "inc-1", time.Now()).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, layout.Marker(), marker, target.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	audit := &opsAudit{}
	runs := &runstoretest.Mem{}
	n := 0
	stage := &retention.Stage{
		Store: store, Layout: layout, Policy: retention.Policy{KeepLast: keepLast}, SweepInterval: time.Hour,
		Facts: opsFacts{"inst-1", "inc-1"}, Audit: audit, Clock: retention.RunSweepClock{Runs: runs},
	}
	return &opsEnv{store: store, layout: layout, mem: targettest.New(), audit: audit, runs: runs, runner: &retention.Runner{
		Stage: stage, Runs: runs, Timing: backup.Timing{Lease: time.Minute, Heartbeat: time.Second, Stall: time.Hour, Finalize: time.Second},
		Target: "s3://it/" + layout.Prefix, Pod: "it", TroveVersion: "test",
		NewRunID: func() string { n++; return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) },
	}}
}

// put builds a backup in memory and uploads every object of it (and its pool objects) to the real store.
func (e *opsEnv) put(t *testing.T, s fixture.Spec) {
	t.Helper()
	before := map[string]bool{}
	for _, k := range e.mem.Keys() {
		before[k] = true
	}
	fixture.Put(t, e.mem, e.layout, s)
	for _, k := range e.mem.Keys() {
		body, _ := e.mem.Data(k)
		if _, err := e.store.Put(context.Background(), k, body, target.PutOptions{}); err != nil {
			t.Fatalf("upload %s: %v", k, err)
		}
	}
}

func (e *opsEnv) exists(t *testing.T, key string) bool {
	t.Helper()
	_, err := e.store.Head(context.Background(), key)
	if err != nil && !errors.Is(err, target.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// R-BAK-016 criterion 1 and R-BAK-017: three backups sharing blobs, keep_last 2.
func TestR_BAK_016_RetentionAndSweepAgainstTheS3Store(t *testing.T) {
	e := newOpsEnv(t, 2)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"shared", "only-a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"shared", "only-b"}})
	e.put(t, fixture.Spec{ID: fixture.ID(3), Blobs: []string{"shared", "only-c"}})
	// A hand-written pool object that no backup lists (R-BAK-017).
	e.mem.Set(e.layout.Pool(fixture.Digest("orphan")), []byte("orphan"))
	if _, err := e.store.Put(context.Background(), e.layout.Pool(fixture.Digest("orphan")), []byte("orphan"), target.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	res, err := e.runner.Pruner().Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.BackupsDeleted) != fmt.Sprint([]string{string(fixture.ID(1))}) {
		t.Fatalf("deleted %v", res.BackupsDeleted)
	}
	if e.exists(t, e.layout.Manifest(fixture.ID(1))) || e.exists(t, e.layout.Pool(fixture.Digest("only-a"))) || e.exists(t, e.layout.Pool(fixture.Digest("orphan"))) {
		t.Fatal("the deleted backup's objects or the orphan are still there")
	}
	for _, c := range []string{"shared", "only-b", "only-c"} {
		if !e.exists(t, e.layout.Pool(fixture.Digest(c))) {
			t.Fatalf("pool object %s of a retained backup is gone", c)
		}
	}
	if res.PoolObjectsDeleted != 2 {
		t.Fatalf("pool objects deleted = %d", res.PoolObjectsDeleted)
	}
	v := &verify.Verifier{Store: e.store, Layout: e.layout}
	rep, err := v.Verify(context.Background(), backup.VerifyOptions{All: true, Deep: true})
	if err != nil || len(rep.Findings) != 0 || len(rep.Verified) != 2 {
		t.Fatalf("verify: %v %+v", err, rep)
	}
	if fmt.Sprint(e.audit.ids) != fmt.Sprint([]string{string(fixture.ID(1)) + ":retention"}) {
		t.Fatalf("audit %v", e.audit.ids)
	}
}

// R-BAK-017 criterion 1: a junk key stops the sweep and is reported; nothing after it is deleted. The store lists in key order,
// the property the adapter's conformance suite proves.
func TestR_BAK_017_JunkKeyStopsTheSweepAgainstTheS3Store(t *testing.T) {
	e := newOpsEnv(t, 5)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"listed"}})
	for _, k := range []string{e.layout.Pool(fixture.Digest("orphan")), e.layout.PoolPrefix() + "junk"} {
		if _, err := e.store.Put(context.Background(), k, []byte("x"), target.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.runner.Pruner().Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed_with_warnings" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "junk") {
		t.Fatalf("result %+v", res)
	}
	if !e.exists(t, e.layout.PoolPrefix()+"junk") || !e.exists(t, e.layout.Pool(fixture.Digest("listed"))) {
		t.Fatal("the sweep deleted past the junk key")
	}
}

func TestR_BAK_016_DeleteAgainstTheS3Store(t *testing.T) {
	e := newOpsEnv(t, 7)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"x"}})
	if _, err := e.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(1)}); backup.ExitCode(err) != backup.ExitUsage {
		t.Fatalf("deleting the only backup without --allow-last: exit %d err %v", backup.ExitCode(err), err)
	}
	res, err := e.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(1), AllowLast: true})
	if err != nil || len(res.BackupsDeleted) != 1 {
		t.Fatalf("err %v res %+v", err, res)
	}
	if e.exists(t, e.layout.Pool(fixture.Digest("x"))) || e.exists(t, e.layout.Manifest(fixture.ID(1))) {
		t.Fatal("objects of the deleted backup are left")
	}
}

// R-BAK-018 against the store, and the incomplete directory of R-BAK-002.
func TestR_BAK_018_ListAgainstTheS3Store(t *testing.T) {
	e := newOpsEnv(t, 7)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"a", "b"}})
	e.put(t, fixture.Spec{ID: fixture.ID(3), Incomplete: true})
	l := &listing.Lister{Store: e.store, Layout: e.layout}
	out, err := l.List(context.Background(), backup.ListOptions{})
	if err != nil || len(out) != 2 || out[0].BackupID != fixture.ID(2) {
		t.Fatalf("err %v out %+v", err, out)
	}
	out, err = l.List(context.Background(), backup.ListOptions{All: true})
	if err != nil || len(out) != 3 || out[0].BackupID != fixture.ID(3) || out[0].Complete {
		t.Fatalf("--all: err %v out %+v", err, out)
	}
}

// R-BAK-019 criteria against the store: a removed pool object is missing; a same-length overwrite is found by --deep only.
func TestR_BAK_019_VerifyAgainstTheS3Store(t *testing.T) {
	e := newOpsEnv(t, 7)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"aaaa", "bbbb"}})
	v := &verify.Verifier{
		Store: e.store, Layout: e.layout, Runs: e.runs,
		Timing:   backup.Timing{Lease: time.Minute, Heartbeat: time.Second, Stall: time.Hour, Finalize: time.Second},
		NewRunID: func() string { return "00000000-0000-7000-8000-0000000000aa" },
	}
	ctx := context.Background()
	if rep, err := v.Verify(ctx, backup.VerifyOptions{Latest: true, Deep: true}); err != nil || len(rep.Findings) != 0 {
		t.Fatalf("clean: %v %+v", err, rep)
	}
	if _, err := e.store.Put(ctx, e.layout.Pool(fixture.Digest("aaaa")), []byte("aaab"), target.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if rep, err := v.Verify(ctx, backup.VerifyOptions{Latest: true}); err != nil || len(rep.Findings) != 0 {
		t.Fatalf("shallow verify must not see a same-length overwrite: %v %+v", err, rep)
	}
	v.NewRunID = func() string { return "00000000-0000-7000-8000-0000000000ab" }
	rep, err := v.Verify(ctx, backup.VerifyOptions{Latest: true, Deep: true, BytesPerSecond: 1 << 20})
	if backup.ExitCode(err) != backup.ExitFindings || len(rep.Findings) != 1 || rep.Findings[0].Kind != backup.FindingDigestMismatch {
		t.Fatalf("deep: exit %d %+v", backup.ExitCode(err), rep)
	}
	if err := e.store.Delete(ctx, []string{e.layout.Pool(fixture.Digest("bbbb"))}); err != nil {
		t.Fatal(err)
	}
	v.NewRunID = func() string { return "00000000-0000-7000-8000-0000000000ac" }
	rep, err = v.Verify(ctx, backup.VerifyOptions{Latest: true})
	if backup.ExitCode(err) != backup.ExitFindings || len(rep.Findings) != 1 || rep.Findings[0].Kind != backup.FindingMissing ||
		rep.Findings[0].Object != fixture.Digest("bbbb").Hex() {
		t.Fatalf("missing: exit %d %+v", backup.ExitCode(err), rep)
	}
}

// R-BAK-018 acceptance and R-BAK-028: a foreign marker stops a delete run with exit 4 against the real store.
func TestR_BAK_016_ForeignMarkerStopsPruneAgainstTheS3Store(t *testing.T) {
	e := newOpsEnv(t, 1)
	e.put(t, fixture.Spec{ID: fixture.ID(1)})
	e.put(t, fixture.Spec{ID: fixture.ID(2)})
	e.runner.Stage.Facts = opsFacts{"inst-2", "inc-1"}
	if _, err := e.runner.Pruner().Prune(context.Background()); backup.ExitCode(err) != backup.ExitForeignTarget {
		t.Fatalf("exit %d err %v", backup.ExitCode(err), err)
	}
	if !e.exists(t, e.layout.Manifest(fixture.ID(1))) {
		t.Fatal("a foreign run deleted something")
	}
}
