package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
	"github.com/guided-traffic/trove/internal/backup/runstoretest"
	"github.com/guided-traffic/trove/internal/backup/verify"
)

func manifestAt(id format.ID, age time.Duration) listing.Backup {
	return listing.Backup{ID: id, Manifest: &format.Manifest{BackupID: id, SnapshotAt: testNow.Add(-age)}}
}

func TestR_BAK_016_PolicyTable(t *testing.T) {
	day := 24 * time.Hour
	mk := func(ages ...time.Duration) []listing.Backup {
		out := make([]listing.Backup, len(ages))
		for i, a := range ages {
			out[i] = manifestAt(fixture.ID(i+1), a)
		}
		return out
	}
	cases := []struct {
		name   string
		policy Policy
		in     []listing.Backup
		want   []int // days of the deleted backups, oldest first
	}{
		{"fewer than keep_last", Policy{KeepLast: 7}, mk(5*day, 4*day, 3*day), nil},
		{"count only", Policy{KeepLast: 2}, mk(3*day, 2*day, day), []int{1}},
		{"count only deletes all older ones, oldest first", Policy{KeepLast: 1}, mk(3*day, 2*day, day), []int{1, 2}},
		{"keep_within protects a recent backup beyond the count", Policy{KeepLast: 1, KeepWithin: 48 * time.Hour}, mk(10*day, 36*time.Hour, 2*time.Hour), []int{1}},
		{"older than keep_within and beyond the count is deleted", Policy{KeepLast: 2, KeepWithin: day}, mk(5*day, 3*day, 2*time.Hour, time.Hour), []int{1, 2}},
		{"exactly keep_within old is kept", Policy{KeepLast: 1, KeepWithin: day}, mk(day, time.Hour), nil},
		{"the newest is never deleted, even with keep_last 0", Policy{KeepLast: 0}, mk(9*day, 8*day), []int{1}},
		{"unsorted input", Policy{KeepLast: 1}, []listing.Backup{manifestAt(fixture.ID(3), day), manifestAt(fixture.ID(1), 3*day), manifestAt(fixture.ID(2), 2*day)}, []int{1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []int
			for _, b := range c.policy.Expired(c.in, testNow) {
				var d int
				if _, err := fmt.Sscanf(string(b.ID), "202611%02d", &d); err != nil {
					t.Fatal(err)
				}
				got = append(got, d)
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("deleted days = %v, want %v", got, c.want)
			}
		})
	}
}

// R-BAK-016 criterion 1: keep_last 2, three backups sharing blobs; the run that creates C deletes A; verify B and C is clean;
// a blob referenced only by A is gone from the pool.
func TestR_BAK_016_CreateRunDeletesOldestAndKeepsSharedBlobs(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 2})
	a := e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"shared", "only-a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"shared", "only-b"}})
	e.put(t, fixture.Spec{ID: fixture.ID(3), Blobs: []string{"shared", "only-b", "only-c"}})
	onlyA := e.l.Pool(fixture.Digest("only-a"))
	if !e.has(onlyA) {
		t.Fatal("fixture: only-a not in the pool")
	}
	_ = a

	f := &fakeFence{}
	res := &backup.Result{BackupID: string(fixture.ID(3))}
	if err := e.stage.prune(context.Background(), f, res, true); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.BackupsDeleted) != fmt.Sprint([]string{string(fixture.ID(1))}) {
		t.Fatalf("BackupsDeleted = %v", res.BackupsDeleted)
	}
	if e.has(onlyA) {
		t.Fatal("the blob referenced only by the deleted backup is still in the pool")
	}
	if res.PoolObjectsDeleted != 1 || res.PoolBytesReclaimed != int64(len("only-a")) {
		t.Fatalf("pool counters = %d objects, %d bytes", res.PoolObjectsDeleted, res.PoolBytesReclaimed)
	}
	if res.PoolSweptAt == nil || !res.PoolSweptAt.Equal(testNow) {
		t.Fatalf("PoolSweptAt = %v", res.PoolSweptAt)
	}
	for _, k := range e.mem.Keys() {
		if strings.HasPrefix(k, e.l.Dir(fixture.ID(1))) {
			t.Fatalf("object of the deleted backup left: %s", k)
		}
	}
	v := &verify.Verifier{Store: e.mem, Layout: e.l}
	rep, err := v.Verify(context.Background(), backup.VerifyOptions{All: true, Deep: true})
	if err != nil || len(rep.Findings) != 0 || len(rep.Verified) != 2 {
		t.Fatalf("verify after pruning: %v, %+v", err, rep)
	}
	if fmt.Sprint(e.audit.deleted) != fmt.Sprint([]string{string(fixture.ID(1)) + ":retention"}) {
		t.Fatalf("audit = %v", e.audit.deleted)
	}
	if res.Outcome != "completed" {
		t.Fatalf("outcome = %q", res.Outcome)
	}
}

func TestR_BAK_016_ManifestIsDeletedFirst(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 1})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"x"}, Tables: map[string]string{"a": "1", "b": "2", "c": "3"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"x"}})
	if err := e.stage.prune(context.Background(), &fakeFence{}, &backup.Result{}, false); err != nil {
		t.Fatal(err)
	}
	if len(e.rec.deletes) < 2 {
		t.Fatalf("delete requests: %v", e.rec.deletes)
	}
	first := e.rec.deletes[0]
	if len(first) != 1 || first[0] != e.l.Manifest(fixture.ID(1)) {
		t.Fatalf("the first delete request is %v, want the manifest alone", first)
	}
}

func TestR_BAK_016_ResultRecordsPolicyAndRetainedBackups(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 2, KeepWithin: 72 * time.Hour})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"a", "b"}, Damaged: []string{"lost"}})
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if res.Retention == nil || res.Retention.KeepLast != 2 || res.Retention.KeepWithinSeconds != 72*3600 {
		t.Fatalf("Retention = %+v", res.Retention)
	}
	if len(res.Retained) != 2 || res.Retained[0].BackupID != string(fixture.ID(2)) || !res.Retained[0].Damaged || res.Retained[1].Damaged {
		t.Fatalf("Retained = %+v", res.Retained)
	}
	if res.Retained[0].BlobCount != 3 || res.Retained[0].SchemaStep != 46 || res.Retained[0].TroveVersion != "0.1.0" || res.Retained[0].DatabaseBytes == 0 {
		t.Fatalf("Retained[0] = %+v", res.Retained[0])
	}
}

func TestR_BAK_016_IncompleteDirectories(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Incomplete: true, Blobs: []string{"orphan"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"kept"}})
	e.put(t, fixture.Spec{ID: fixture.ID(5), Incomplete: true})
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.IncompleteDeleted) != fmt.Sprint([]string{string(fixture.ID(1))}) {
		t.Fatalf("IncompleteDeleted = %v", res.IncompleteDeleted)
	}
	if e.has(e.l.BlobList(fixture.ID(1))) {
		t.Fatal("the older incomplete directory was not removed")
	}
	if !e.has(e.l.BlobList(fixture.ID(5))) {
		t.Fatal("an incomplete directory newer than the newest complete backup is the active run's and must stay")
	}
	if e.has(e.l.Pool(fixture.Digest("orphan"))) || !e.has(e.l.Pool(fixture.Digest("kept"))) {
		t.Fatal("the sweep after an incomplete deletion must reclaim the orphan and keep the listed blob")
	}
}

func TestR_BAK_016_OwnBackupDirectoryIsNeverIncomplete(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"kept"}})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Incomplete: true})
	res := &backup.Result{BackupID: string(fixture.ID(1))}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if len(res.IncompleteDeleted) != 0 || !e.has(e.l.BlobList(fixture.ID(1))) {
		t.Fatalf("the run's own directory was deleted: %v", res.IncompleteDeleted)
	}
}

func TestR_BAK_016_FenceRefusalDeletesNothing(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 1})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"b"}})
	before := len(e.mem.Keys())
	f := &fakeFence{err: backup.ErrSuperseded}
	err := e.stage.prune(context.Background(), f, &backup.Result{}, false)
	if !errors.Is(err, backup.ErrSuperseded) {
		t.Fatalf("err = %v", err)
	}
	if len(e.rec.deletes) != 0 || len(e.mem.Keys()) != before {
		t.Fatal("a run that fails the fence must not delete")
	}
}

func TestR_BAK_016_ForeignMarkerStopsBeforeAnyDeletion(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 1})
	e.put(t, fixture.Spec{ID: fixture.ID(1)})
	e.put(t, fixture.Spec{ID: fixture.ID(2)})
	e.stage.Facts = fakeFacts{backup.SystemFacts{InstallationID: "inst-1", Incarnation: "other"}}
	err := e.stage.prune(context.Background(), &fakeFence{}, &backup.Result{}, false)
	if backup.ExitCode(err) != backup.ExitForeignTarget || len(e.rec.deletes) != 0 {
		t.Fatalf("exit %d, deletes %v, err %v", backup.ExitCode(err), e.rec.deletes, err)
	}
}

func TestR_BAK_016_NoMarkerExitsUsage(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 1})
	e.mem.Remove(e.l.Marker())
	err := e.stage.prune(context.Background(), &fakeFence{}, &backup.Result{}, false)
	if backup.ExitCode(err) != backup.ExitUsage {
		t.Fatalf("exit %d, err %v", backup.ExitCode(err), err)
	}
}

func TestR_BAK_016_UnknownManifestFormatExits6AndDeletesNothing(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 1})
	e.put(t, fixture.Spec{ID: fixture.ID(1)})
	e.put(t, fixture.Spec{ID: fixture.ID(2)})
	e.mem.Set(e.l.Manifest(fixture.ID(2)), []byte(`{"format": 99}`))
	err := e.stage.prune(context.Background(), &fakeFence{}, &backup.Result{}, false)
	if backup.ExitCode(err) != backup.ExitPrecondition || len(e.rec.deletes) != 0 {
		t.Fatalf("exit %d, deletes %v, err %v", backup.ExitCode(err), e.rec.deletes, err)
	}
}

// R-BAK-017 criterion 1.
func TestR_BAK_017_SweepDeletesUnlistedPoolObjectAndJunkStopsIt(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"listed"}})
	orphan := fixture.Digest("orphan")
	e.mem.Set(e.l.Pool(orphan), []byte("orphan"))

	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if e.has(e.l.Pool(orphan)) || !e.has(e.l.Pool(fixture.Digest("listed"))) || res.PoolObjectsDeleted != 1 {
		t.Fatalf("hand-written orphan: still there %v, deleted %d", e.has(e.l.Pool(orphan)), res.PoolObjectsDeleted)
	}

	// A junk key: the sweep stops, reports it and ends with warnings; nothing after it is deleted.
	e.mem.Set(e.l.Pool(fixture.Digest("orphan-2")), []byte("orphan-2"))
	e.mem.Set(e.l.PoolPrefix()+"junk", []byte("x"))
	res = &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "pool/sha256/junk") || res.Outcome != "completed_with_warnings" {
		t.Fatalf("warnings = %v, outcome %q", res.Warnings, res.Outcome)
	}
	if res.PoolSweptAt != nil {
		t.Fatal("a stopped sweep is not a sweep: poolSweptAt must stay unset so the next run tries again")
	}
	if !e.has(e.l.Pool(fixture.Digest("orphan-2"))) {
		t.Fatal("the pending batch must not be deleted when the sweep stops")
	}
}

func TestR_BAK_017_OutOfOrderKeyStopsTheSweep(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"listed"}})
	for _, c := range []string{"o1", "o2", "o3"} {
		e.mem.Set(e.l.Pool(fixture.Digest(c)), []byte(c))
	}
	e.rec.list = func(prefix string, keys []string) []string {
		if prefix != e.l.PoolPrefix() {
			return keys
		}
		out := append([]string(nil), keys...)
		out[0], out[len(out)-1] = out[len(out)-1], out[0]
		return out
	}
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "does not follow") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if res.PoolObjectsDeleted != 0 || len(e.poolKeys()) != 4 {
		t.Fatalf("nothing may be deleted once the order is broken: deleted %d, pool %d", res.PoolObjectsDeleted, len(e.poolKeys()))
	}
}

func TestR_BAK_017_AKeyWhoseFanOutLiesStopsTheSweepAndDropsThePendingBatch(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"listed"}})
	e.mem.Set(e.l.Pool(fixture.Digest("o1")), []byte("o1"))
	e.mem.Set(e.l.Pool(format.Digest{0xff, 0xff}), []byte("tail"))
	// The hex name is valid but the fan-out directories do not match it, so the key is not a pool key.
	e.mem.Set(e.l.PoolPrefix()+"zz/zz/"+strings.Repeat("a", 64), []byte("liar"))
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.PoolObjectsDeleted != 0 || len(e.rec.deletes) != 0 {
		t.Fatalf("warnings %v, deleted %d, requests %v", res.Warnings, res.PoolObjectsDeleted, e.rec.deletes)
	}
}

func TestR_BAK_017_DamagedBlobsDoNotProtectPoolObjects(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"ok"}, Damaged: []string{"was-missing"}})
	// The object appeared in the pool later (for example from a repaired source): the list says damaged, so it is not
	// protected by this backup, and nothing else lists it.
	e.mem.Set(e.l.Pool(fixture.Digest("was-missing")), []byte("was-missing"))
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if e.has(e.l.Pool(fixture.Digest("was-missing"))) {
		t.Fatal("a digest the list marks damaged must not be in the keep set")
	}
}

func TestR_BAK_017_CorruptBlobListSkipsTheSweepWithAWarning(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"b"}})
	e.mem.Set(e.l.Pool(fixture.Digest("orphan")), []byte("orphan"))
	// A list that decodes but is not the one the manifest names (a truncated list would look the same).
	empty, err := format.EncodeBlobList(nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	e.mem.Set(e.l.BlobList(fixture.ID(2)), empty)
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "does not match its manifest") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if len(e.rec.deletes) != 0 || len(e.poolKeys()) != 3 {
		t.Fatalf("an untrusted list must delete nothing: %v", e.rec.deletes)
	}
}

func TestR_BAK_017_SweepRunsWhenDueAndOtherwiseNot(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.mem.Set(e.l.Pool(fixture.Digest("orphan")), []byte("orphan"))

	clock := &fixedClock{at: testNow.Add(-24 * time.Hour), ok: true}
	e.stage.Clock = clock
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if res.PoolObjectsDeleted != 0 || res.PoolSweptAt != nil || !e.has(e.l.Pool(fixture.Digest("orphan"))) {
		t.Fatal("a sweep 24h ago with a 168h interval must not sweep again when nothing was deleted")
	}
	clock.at = testNow.Add(-169 * time.Hour)
	res = &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if res.PoolObjectsDeleted != 1 || res.PoolSweptAt == nil {
		t.Fatalf("a sweep 169h ago is due: %+v", res)
	}
	clock.ok = false
	e.mem.Set(e.l.Pool(fixture.Digest("orphan-3")), []byte("orphan-3"))
	res = &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil {
		t.Fatal(err)
	}
	if res.PoolObjectsDeleted != 1 {
		t.Fatal("a pool that was never swept is swept")
	}
}

type fixedClock struct {
	at time.Time
	ok bool
}

func (c *fixedClock) LastSweep(context.Context) (time.Time, bool, error) { return c.at, c.ok, nil }

func TestR_BAK_017_SweepBatchesOf1000AndFencesEachRequest(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"keep"}})
	for i := range 2500 {
		c := fmt.Sprintf("orphan-%d", i)
		e.mem.Set(e.l.Pool(fixture.Digest(c)), []byte(c))
	}
	f := &fakeFence{}
	res := &backup.Result{}
	if err := e.stage.prune(context.Background(), f, res, false); err != nil {
		t.Fatal(err)
	}
	if res.PoolObjectsDeleted != 2500 || len(e.rec.deletes) != 3 || len(e.rec.deletes[0]) != 1000 || len(e.rec.deletes[2]) != 500 {
		t.Fatalf("deleted %d in %d requests", res.PoolObjectsDeleted, len(e.rec.deletes))
	}
	if f.fences != 3 {
		t.Fatalf("fences = %d, want one per request", f.fences)
	}
}

// Q-BAK-28 property in the spirit of the R-BAK-017 criterion: after any sequence of backups and prune runs, every undamaged
// digest a retained backup lists is in the pool.
func TestR_BAK_017_NoRetainedBackupLosesAListedDigest(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 3})
	contents := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for day := 1; day <= 12; day++ {
		var blobs []string
		for i, c := range contents {
			if (day+i)%3 != 0 {
				blobs = append(blobs, c)
			}
		}
		blobs = append(blobs, fmt.Sprintf("unique-%d", day))
		e.put(t, fixture.Spec{ID: fixture.ID(day), Blobs: blobs})
		if day%2 == 0 {
			if err := e.stage.prune(context.Background(), &fakeFence{}, &backup.Result{BackupID: string(fixture.ID(day))}, true); err != nil {
				t.Fatal(err)
			}
		}
		v := &verify.Verifier{Store: e.mem, Layout: e.l}
		rep, err := v.Verify(context.Background(), backup.VerifyOptions{All: true})
		if err != nil || len(rep.Findings) != 0 {
			t.Fatalf("day %d: %v %+v", day, err, rep.Findings)
		}
	}
}

// R-BAK-028: the reminder compares key IDs of the retained backups with those of the run's own backup.
func TestR_BAK_028_EscrowWarningNamesRetiredKeys(t *testing.T) {
	e := newEnv(t, Policy{KeepLast: 5})
	e.put(t, fixture.Spec{ID: fixture.ID(1), KeyIDs: []string{"2026-01"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), KeyIDs: []string{"2026-01", "2026-06"}})
	e.put(t, fixture.Spec{ID: fixture.ID(3), KeyIDs: []string{"2026-06"}})
	res := &backup.Result{BackupID: string(fixture.ID(3))}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, true); err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "2026-01 (needed by "+string(fixture.ID(1))+", "+string(fixture.ID(2))+")") || strings.Contains(res.Warnings[0], "2026-06") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if res.Outcome != "completed_with_warnings" {
		t.Fatalf("outcome = %q", res.Outcome)
	}

	// Every retained backup's keys are among the run's own: nothing to remind of. A standalone prune does not warn.
	res = &backup.Result{BackupID: string(fixture.ID(2))}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, true); err != nil || len(res.Warnings) != 0 {
		t.Fatalf("err %v warnings %v", err, res.Warnings)
	}
	res = &backup.Result{}
	if err := e.stage.prune(context.Background(), &fakeFence{}, res, false); err != nil || len(res.Warnings) != 0 {
		t.Fatalf("err %v warnings %v", err, res.Warnings)
	}
}

// --- the runs of delete and prune ---

type runEnv struct {
	*env
	runs   *runstoretest.Mem
	runner *Runner
}

func newRunEnv(t *testing.T, policy Policy) *runEnv {
	t.Helper()
	e := newEnv(t, policy)
	runs := &runstoretest.Mem{}
	n := 0
	return &runEnv{env: e, runs: runs, runner: &Runner{
		Stage: e.stage, Runs: runs,
		Timing: backup.Timing{Lease: time.Minute, Heartbeat: time.Second, Stall: time.Hour, Finalize: time.Second},
		Target: "s3://b/trove", Pod: "pod-1", TroveVersion: "0.1.0",
		NewRunID: func() string { n++; return fmt.Sprintf("00000000-0000-7000-8000-%012d", n) },
	}}
}

func TestR_BAK_016_DeleteOfTheOnlyCompleteBackupNeedsAllowLast(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 7})
	r.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"x"}})
	_, err := r.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(1), Yes: true})
	if backup.ExitCode(err) != backup.ExitUsage {
		t.Fatalf("exit %d, err %v", backup.ExitCode(err), err)
	}
	if !r.has(r.l.Manifest(fixture.ID(1))) || len(r.rec.deletes) != 0 {
		t.Fatal("a refused delete must not touch the target")
	}
	rows := r.runs.Runs()
	if len(rows) != 1 || rows[0].State != backup.StateFailed || !strings.Contains(string(rows[0].Error), backup.CodeDeletionNotAllowed) {
		t.Fatalf("rows = %+v", rows)
	}

	res, err := r.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(1), Yes: true, AllowLast: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.BackupsDeleted) != 1 || len(r.poolKeys()) != 0 || res.PoolObjectsDeleted != 1 {
		t.Fatalf("result %+v, pool %v", res, r.poolKeys())
	}
	if fmt.Sprint(r.audit.deleted) != fmt.Sprint([]string{string(fixture.ID(1)) + ":manual"}) {
		t.Fatalf("audit = %v", r.audit.deleted)
	}
}

func TestR_BAK_016_DeleteRunsRegardlessOfPolicyAndRecordsItsRow(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 7})
	r.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"shared", "one"}})
	r.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"shared"}})
	res, err := r.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(2)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed" || len(res.Retained) != 1 || res.Retained[0].BackupID != string(fixture.ID(1)) || res.Retention == nil {
		t.Fatalf("result %+v", res)
	}
	if !r.has(r.l.Pool(fixture.Digest("one"))) || !r.has(r.l.Pool(fixture.Digest("shared"))) {
		t.Fatal("blobs of the remaining backup were swept")
	}
	rows := r.runs.Runs()
	if len(rows) != 1 || rows[0].Operation != backup.OpDelete || rows[0].State != backup.StateCompleted || rows[0].BackupID != string(fixture.ID(2)) || len(rows[0].Result) == 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestR_BAK_016_DeleteOfAnUnknownOrIncompleteBackup(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 7})
	r.put(t, fixture.Spec{ID: fixture.ID(1)})
	r.put(t, fixture.Spec{ID: fixture.ID(2), Incomplete: true, Blobs: []string{"orphan"}})
	_, err := r.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(9)})
	if backup.ExitCode(err) != backup.ExitUsage {
		t.Fatalf("unknown id: exit %d", backup.ExitCode(err))
	}
	res, err := r.runner.Delete(context.Background(), backup.DeleteOptions{ID: fixture.ID(2)})
	if err != nil || fmt.Sprint(res.IncompleteDeleted) != fmt.Sprint([]string{string(fixture.ID(2))}) || len(res.BackupsDeleted) != 0 {
		t.Fatalf("err %v result %+v", err, res)
	}
	if len(r.audit.deleted) != 0 {
		t.Fatalf("an incomplete directory is not a backup: audit %v", r.audit.deleted)
	}
}

func TestR_BAK_016_PruneRunUsesTheSlotAndExitCodes(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 1})
	r.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	r.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"b"}})
	res, err := r.runner.Pruner().Prune(context.Background())
	if err != nil || len(res.BackupsDeleted) != 1 {
		t.Fatalf("err %v result %+v", err, res)
	}
	if rows := r.runs.Runs(); len(rows) != 1 || rows[0].State != backup.StateCompleted || rows[0].Operation != backup.OpDelete {
		t.Fatalf("rows = %+v", rows)
	}

	// A live create run holds the slot: exit 3, nothing deleted.
	r.put(t, fixture.Spec{ID: fixture.ID(3), Blobs: []string{"c"}})
	if _, err := r.runs.Begin(context.Background(), backup.NewRun{ID: "00000000-0000-7000-8000-00000000ffff", Operation: backup.OpCreate, Pod: "other", Lease: time.Hour}); err != nil {
		t.Fatal(err)
	}
	r.rec.deletes = nil
	if _, err := r.runner.Pruner().Prune(context.Background()); backup.ExitCode(err) != backup.ExitBusy || len(r.rec.deletes) != 0 {
		t.Fatalf("exit %d, deletes %v", backup.ExitCode(err), r.rec.deletes)
	}
}

func TestR_BAK_016_PruneRefusesWhileRestoreIsInProgress(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 1})
	r.put(t, fixture.Spec{ID: fixture.ID(1)})
	r.put(t, fixture.Spec{ID: fixture.ID(2)})
	r.stage.Facts = fakeFacts{backup.SystemFacts{InstallationID: "inst-1", Incarnation: "inc-1", RestoreInProgress: true}}
	_, err := r.runner.Pruner().Prune(context.Background())
	if backup.ExitCode(err) != backup.ExitTemporary || len(r.runs.Runs()) != 0 || len(r.rec.deletes) != 0 {
		t.Fatalf("exit %d, rows %d", backup.ExitCode(err), len(r.runs.Runs()))
	}
}

func TestR_BAK_016_PruneWithForeignMarkerExits4AndRecordsFailure(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 1})
	r.put(t, fixture.Spec{ID: fixture.ID(1)})
	r.put(t, fixture.Spec{ID: fixture.ID(2)})
	r.stage.Facts = fakeFacts{backup.SystemFacts{InstallationID: "someone-else", Incarnation: "inc-1"}}
	_, err := r.runner.Pruner().Prune(context.Background())
	if backup.ExitCode(err) != backup.ExitForeignTarget || len(r.rec.deletes) != 0 {
		t.Fatalf("exit %d", backup.ExitCode(err))
	}
	if rows := r.runs.Runs(); len(rows) != 1 || rows[0].State != backup.StateFailed || !strings.Contains(string(rows[0].Error), backup.CodeForeignTarget) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestR_BAK_016_FencedRunSupersededWritesNoDeletion(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 1})
	r.put(t, fixture.Spec{ID: fixture.ID(1)})
	r.put(t, fixture.Spec{ID: fixture.ID(2)})
	r.runs.FenceRefused = true
	_, err := r.runner.Pruner().Prune(context.Background())
	if !errors.Is(err, backup.ErrSuperseded) || len(r.rec.deletes) != 0 {
		t.Fatalf("err %v deletes %v", err, r.rec.deletes)
	}
}

func TestR_BAK_016_InterruptedRunIsRecordedAsInterruptedWithExit75(t *testing.T) {
	r := newRunEnv(t, Policy{KeepLast: 1})
	r.put(t, fixture.Spec{ID: fixture.ID(1)})
	r.put(t, fixture.Spec{ID: fixture.ID(2)})
	ctx, cancel := context.WithCancel(context.Background())
	r.rec.onList = func() error { cancel(); return ctx.Err() }
	_, err := r.runner.Pruner().Prune(ctx)
	if backup.ExitCode(err) != backup.ExitTemporary || backup.ErrorCode(err) != backup.CodeInterrupted {
		t.Fatalf("exit %d err %v", backup.ExitCode(err), err)
	}
	rows := r.runs.Runs()
	if len(rows) != 1 || rows[0].State != backup.StateInterrupted {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestR_BAK_017_RunSweepClockReadsTheNewestRecordedSweep(t *testing.T) {
	runs := &runstoretest.Mem{}
	ctx := context.Background()
	mk := func(id string, op backup.Operation, result string) {
		if _, err := runs.Begin(ctx, backup.NewRun{ID: id, Operation: op, Lease: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if err := runs.Finish(ctx, id, backup.StateCompleted, []byte(result), nil); err != nil {
			t.Fatal(err)
		}
	}
	c := RunSweepClock{Runs: runs}
	if _, ok, err := c.LastSweep(ctx); err != nil || ok {
		t.Fatalf("no runs: ok %v err %v", ok, err)
	}
	mk("a", backup.OpCreate, `{"poolSweptAt":"2026-11-10T00:00:00Z"}`)
	mk("b", backup.OpDelete, `{"poolSweptAt":"2026-11-12T00:00:00Z"}`)
	mk("c", backup.OpCreate, `{"backupId":"x"}`)
	at, ok, err := c.LastSweep(ctx)
	if err != nil || !ok || !at.Equal(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("at %v ok %v err %v", at, ok, err)
	}
}
