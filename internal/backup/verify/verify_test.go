package verify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
	"github.com/guided-traffic/trove/internal/backup/runstoretest"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
)

type env struct {
	mem *targettest.Mem
	l   format.Layout
	v   *Verifier
}

func newEnv(t *testing.T) *env {
	t.Helper()
	mem := targettest.New()
	l := format.Layout{Prefix: "trove"}
	raw, err := format.NewMarker("inst-1", "inc-1", time.Now()).Encode()
	if err != nil {
		t.Fatal(err)
	}
	mem.Set(l.Marker(), raw)
	return &env{mem: mem, l: l, v: &Verifier{Store: mem, Layout: l}}
}

func (e *env) put(t *testing.T, s fixture.Spec) fixture.Backup {
	t.Helper()
	return fixture.Put(t, e.mem, e.l, s)
}

func (e *env) verify(opts backup.VerifyOptions) (*backup.VerifyReport, error) {
	return e.v.Verify(context.Background(), opts)
}

func findings(rep *backup.VerifyReport) []string {
	out := make([]string, len(rep.Findings))
	for i, f := range rep.Findings {
		out[i] = string(f.Kind) + " " + f.BackupID + " " + f.Object
	}
	return out
}

func wantFindings(t *testing.T, rep *backup.VerifyReport, want ...string) {
	t.Helper()
	got := findings(rep)
	if len(got) != len(want) {
		t.Fatalf("findings = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("findings = %q, want %q", got, want)
		}
	}
}

func TestR_BAK_019_CleanBackupsHaveNoFindings(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a", "b"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"b", "c"}, Damaged: []string{"gone"}})
	for _, opts := range []backup.VerifyOptions{{ID: fixture.ID(1)}, {Latest: true}, {All: true}, {All: true, Deep: true}} {
		rep, err := e.verify(opts)
		if err != nil || len(rep.Findings) != 0 {
			t.Fatalf("%+v: err %v findings %v", opts, err, rep.Findings)
		}
	}
	rep, _ := e.verify(backup.VerifyOptions{All: true})
	if len(rep.Verified) != 2 {
		t.Fatalf("verified = %v", rep.Verified)
	}
	rep, _ = e.verify(backup.VerifyOptions{Latest: true})
	if len(rep.Verified) != 1 || rep.Verified[0] != fixture.ID(2) {
		t.Fatalf("--latest verified %v", rep.Verified)
	}
}

// R-BAK-019 criterion 1: a pool object removed by hand is a missing finding, exit 5.
func TestR_BAK_019_MissingPoolObjectExits5(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a", "b"}})
	e.mem.Remove(e.l.Pool(fixture.Digest("b")))
	rep, err := e.verify(backup.VerifyOptions{Latest: true})
	if !errors.Is(err, backup.ErrFindings) || backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("err = %v, exit %d", err, backup.ExitCode(err))
	}
	wantFindings(t, rep, "missing "+string(fixture.ID(1))+" "+fixture.Digest("b").Hex())
}

// R-BAK-019 criterion 2: same-length overwrite passes verify, fails verify --deep with digest_mismatch.
func TestR_BAK_019_SameLengthOverwriteNeedsDeep(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"abcd"}})
	e.mem.Set(e.l.Pool(fixture.Digest("abcd")), []byte("abce"))
	if rep, err := e.verify(backup.VerifyOptions{Latest: true}); err != nil || len(rep.Findings) != 0 {
		t.Fatalf("shallow verify: %v %v", err, rep.Findings)
	}
	rep, err := e.verify(backup.VerifyOptions{Latest: true, Deep: true})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("deep verify exit %d", backup.ExitCode(err))
	}
	wantFindings(t, rep, "digest_mismatch "+string(fixture.ID(1))+" "+fixture.Digest("abcd").Hex())
}

func TestR_BAK_019_SizeMismatchOfAPoolObject(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"abcd"}})
	e.mem.Set(e.l.Pool(fixture.Digest("abcd")), []byte("abc"))
	rep, err := e.verify(backup.VerifyOptions{Latest: true, Deep: true})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("exit %d", backup.ExitCode(err))
	}
	wantFindings(t, rep, "size_mismatch "+string(fixture.ID(1))+" "+fixture.Digest("abcd").Hex())
}

func TestR_BAK_019_ManifestFilesAreCheckedAgainstTheManifest(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Tables: map[string]string{"a": "aaaa", "b": "bbbb", "c": "cccc"}})
	e.mem.Set(e.l.Table(fixture.ID(1), "a"), []byte("aaax")) // same size, other content
	e.mem.Set(e.l.Table(fixture.ID(1), "b"), []byte("bb"))   // other size
	e.mem.Remove(e.l.Table(fixture.ID(1), "c"))              // gone
	rep, err := e.verify(backup.VerifyOptions{ID: fixture.ID(1)})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("exit %d", backup.ExitCode(err))
	}
	id := string(fixture.ID(1))
	wantFindings(t, rep,
		"file_checksum_mismatch "+id+" "+e.l.Table(fixture.ID(1), "a"),
		"size_mismatch "+id+" "+e.l.Table(fixture.ID(1), "b"),
		"missing "+id+" "+e.l.Table(fixture.ID(1), "c"))
}

func TestR_BAK_019_BlobListMissingOrChangedIsAFindingAndItsEntriesAreNotTrusted(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"b"}})
	e.mem.Remove(e.l.Pool(fixture.Digest("a")))
	e.mem.Remove(e.l.BlobList(fixture.ID(1)))
	other, err := format.EncodeBlobList([]format.BlobEntry{{Digest: fixture.Digest("zzz"), Size: 3}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	e.mem.Set(e.l.BlobList(fixture.ID(2)), other)
	rep, err := e.verify(backup.VerifyOptions{All: true})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("exit %d", backup.ExitCode(err))
	}
	// --all goes newest first; neither list is trusted, so the pool object missing for backup 1 is not reported.
	wantFindings(t, rep,
		"file_checksum_mismatch "+string(fixture.ID(2))+" "+e.l.BlobList(fixture.ID(2)),
		"missing "+string(fixture.ID(1))+" "+e.l.BlobList(fixture.ID(1)),
	)
}

func TestR_BAK_019_FindingsOfTwoBackupsSharingAMissingObject(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"shared"}})
	e.put(t, fixture.Spec{ID: fixture.ID(2), Blobs: []string{"shared"}})
	e.mem.Remove(e.l.Pool(fixture.Digest("shared")))
	before := e.mem.Calls["head"]
	rep, err := e.verify(backup.VerifyOptions{All: true})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("exit %d", backup.ExitCode(err))
	}
	d := fixture.Digest("shared").Hex()
	wantFindings(t, rep, "missing "+string(fixture.ID(2))+" "+d, "missing "+string(fixture.ID(1))+" "+d)
	// One HEAD for the shared digest, plus the manifest probe of nothing: the pool object is checked once.
	if heads := e.mem.Calls["head"] - before; heads != 1 {
		t.Fatalf("head requests = %d, want 1 for a digest listed by two backups", heads)
	}
}

func TestR_BAK_019_DamagedBlobsAreNotLookedFor(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}, Damaged: []string{"never-copied"}})
	rep, err := e.verify(backup.VerifyOptions{Latest: true, Deep: true})
	if err != nil || len(rep.Findings) != 0 {
		t.Fatalf("err %v findings %v", err, rep.Findings)
	}
}

func TestR_BAK_019_ManifestOfAHigherFormatExits6(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1)})
	raw, _ := e.mem.Data(e.l.Manifest(fixture.ID(1)))
	e.mem.Set(e.l.Manifest(fixture.ID(1)), []byte(strings.Replace(string(raw), `"format": 1`, `"format": 2`, 1)))
	_, err := e.verify(backup.VerifyOptions{ID: fixture.ID(1)})
	if backup.ExitCode(err) != backup.ExitPrecondition || !strings.Contains(err.Error(), "format") {
		t.Fatalf("exit %d, err %v", backup.ExitCode(err), err)
	}
}

func TestR_BAK_019_UnreadableManifestIsAFinding(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1)})
	e.mem.Set(e.l.Manifest(fixture.ID(1)), []byte(`{not json`))
	rep, err := e.verify(backup.VerifyOptions{ID: fixture.ID(1)})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("exit %d err %v", backup.ExitCode(err), err)
	}
	wantFindings(t, rep, "file_checksum_mismatch "+string(fixture.ID(1))+" "+e.l.Manifest(fixture.ID(1)))
}

func TestR_BAK_019_UnknownBackupAndEmptyTargetExit2(t *testing.T) {
	e := newEnv(t)
	for _, opts := range []backup.VerifyOptions{{ID: fixture.ID(4)}, {Latest: true}, {All: true}} {
		if _, err := e.verify(opts); backup.ExitCode(err) != backup.ExitUsage {
			t.Fatalf("%+v: exit %d err %v", opts, backup.ExitCode(err), err)
		}
	}
}

func TestR_BAK_019_ManifestMustNameTheTargetsInstallation(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1), Installation: "someone-else"})
	rep, err := e.verify(backup.VerifyOptions{ID: fixture.ID(1)})
	if backup.ExitCode(err) != backup.ExitFindings || len(rep.Findings) != 1 || !strings.Contains(rep.Findings[0].Detail, "someone-else") {
		t.Fatalf("exit %d, %+v", backup.ExitCode(err), rep)
	}

	// After an adoption the marker names the new installation and the earlier backups are those of the predecessor.
	m := format.NewMarker("inst-1", "inc-1", time.Now()).Adopt("inst-2", "inc-2", time.Now())
	raw, _ := m.Encode()
	e.mem.Set(e.l.Marker(), raw)
	if rep, err := e.verify(backup.VerifyOptions{ID: fixture.ID(1)}); err != nil || len(rep.Findings) != 0 {
		t.Fatalf("adopted marker: %v %+v", err, rep)
	}
}

func TestR_BAK_019_MissingMarkerIsAFinding(t *testing.T) {
	e := newEnv(t)
	e.put(t, fixture.Spec{ID: fixture.ID(1)})
	e.mem.Remove(e.l.Marker())
	rep, err := e.verify(backup.VerifyOptions{ID: fixture.ID(1)})
	if backup.ExitCode(err) != backup.ExitFindings {
		t.Fatalf("exit %d", backup.ExitCode(err))
	}
	wantFindings(t, rep, "missing  "+e.l.Marker())
}

func TestR_BAK_019_ManyObjectsAreCheckedInParallelWithoutLosingFindings(t *testing.T) {
	e := newEnv(t)
	var blobs []string
	for i := range 300 {
		blobs = append(blobs, "blob-"+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: blobs})
	for i := 0; i < 300; i += 7 {
		e.mem.Remove(e.l.Pool(fixture.Digest(blobs[i])))
	}
	rep, err := e.verify(backup.VerifyOptions{Latest: true, Deep: true})
	if backup.ExitCode(err) != backup.ExitFindings || len(rep.Findings) != 43 {
		t.Fatalf("exit %d, %d findings", backup.ExitCode(err), len(rep.Findings))
	}
}

func TestR_BAK_019_RunIsRecordedWithItsFindings(t *testing.T) {
	e := newEnv(t)
	runs := &runstoretest.Mem{}
	e.v.Runs = runs
	e.v.NewRunID = func() string { return "00000000-0000-7000-8000-000000000001" }
	e.v.Timing = backup.Timing{Lease: time.Minute, Heartbeat: time.Second, Stall: time.Hour, Finalize: time.Second}
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	e.mem.Remove(e.l.Pool(fixture.Digest("a")))
	if _, err := e.verify(backup.VerifyOptions{Latest: true}); backup.ExitCode(err) != backup.ExitFindings {
		t.Fatal(err)
	}
	rows := runs.Runs()
	if len(rows) != 1 || rows[0].Operation != backup.OpVerify || rows[0].State != backup.StateCompleted {
		t.Fatalf("rows = %+v", rows)
	}
	var got backup.VerifyReport
	if err := json.Unmarshal(rows[0].Result, &got); err != nil || len(got.Findings) != 1 || got.Findings[0].Kind != backup.FindingMissing {
		t.Fatalf("result %s: %v", rows[0].Result, err)
	}

	// A verify that fails (unknown format) ends the row failed with the code.
	raw, _ := e.mem.Data(e.l.Manifest(fixture.ID(1)))
	e.mem.Set(e.l.Manifest(fixture.ID(1)), []byte(strings.Replace(string(raw), `"format": 1`, `"format": 2`, 1)))
	e.v.NewRunID = func() string { return "00000000-0000-7000-8000-000000000002" }
	if _, err := e.verify(backup.VerifyOptions{Latest: true}); backup.ExitCode(err) != backup.ExitPrecondition {
		t.Fatal(err)
	}
	rows = runs.Runs()
	if len(rows) != 2 || rows[1].State != backup.StateFailed || !strings.Contains(string(rows[1].Error), backup.CodeFormatUnknown) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestR_BAK_019_ADatabaseThatCannotRecordTheRunDoesNotStopTheVerification(t *testing.T) {
	e := newEnv(t)
	e.v.Runs = failingBegin{}
	e.v.NewRunID = func() string { return "x" }
	e.put(t, fixture.Spec{ID: fixture.ID(1), Blobs: []string{"a"}})
	if rep, err := e.verify(backup.VerifyOptions{Latest: true}); err != nil || len(rep.Verified) != 1 {
		t.Fatalf("err %v rep %+v", err, rep)
	}
}

type failingBegin struct{ backup.RunStore }

func (failingBegin) Begin(context.Context, backup.NewRun) (backup.Run, error) {
	return backup.Run{}, errors.New("database unreachable")
}

func TestR_BAK_019_ThrottleSpacesTheDownloads(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(1000)
	l.now = func() time.Time { return now }
	ctx := context.Background()
	// The first reservation is due now; each later one is due when the earlier bytes have been paid for.
	if err := l.wait(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(500 * time.Millisecond); !l.next.Equal(want) {
		t.Fatalf("next = %v, want %v", l.next, want)
	}
	if err := l.wait(ctx, 500); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if want := now.Add(time.Second); !l.next.Equal(want) {
		t.Fatalf("next = %v, want %v", l.next, want)
	}
	if newLimiter(0) != nil {
		t.Fatal("a rate of 0 is unlimited")
	}
	var unlimited *limiter
	if err := unlimited.wait(ctx, 1<<30); err != nil {
		t.Fatal(err)
	}
	// A cancelled context ends a wait.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	slow := newLimiter(1)
	slow.now = func() time.Time { return now }
	_ = slow.wait(ctx, 1)
	if err := slow.wait(cctx, 1); err == nil {
		t.Fatal("a cancelled wait must fail")
	}
}
