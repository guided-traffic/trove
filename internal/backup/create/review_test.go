package create

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// M4: a blob whose row is not 'ok' is recorded as damaged with its status and not copied, so that it is copied fresh once it is
// repaired (R-STOR-027), and no later backup keeps pointing at the corrupt pool object.
func TestR_BAK_006_ABlobThatIsNotOkIsRecordedAsDamagedAndNotCopied(t *testing.T) {
	for _, status := range []string{"corrupt", "missing"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			e.addBlob("alpha")
			bad := e.addBlob("beta")
			for i := range e.snap.blobs {
				if e.snap.blobs[i].Digest == bad {
					e.snap.blobs[i].Status = status
				}
			}
			res, err := e.create(backup.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			m := e.manifest(res.BackupID)
			if res.Outcome != "completed_with_warnings" || res.DamagedBlobs != 1 || m.Blobs.Referenced != 2 || m.Blobs.Copied != 1 {
				t.Fatalf("res %+v blobs %+v", res, m.Blobs)
			}
			if len(m.Blobs.Damaged) != 1 || m.Blobs.Damaged[0].SHA256 != bad.Hex() || m.Blobs.Damaged[0].Status != status {
				t.Fatalf("damaged = %+v", m.Blobs.Damaged)
			}
			if _, ok := e.mem.Data("trove/pool/sha256/" + bad.Hex()[:2] + "/" + bad.Hex()[2:4] + "/" + bad.Hex()); ok {
				t.Fatal("a blob that is not ok must not be copied into the pool")
			}
			// Repaired: the next run copies it, because the previous list names it as damaged.
			for i := range e.snap.blobs {
				e.snap.blobs[i].Status = ""
			}
			e.runs = newFakeRuns()
			e.snap.runs = e.runs
			e.now = e.now.Add(time.Minute)
			res2, err := e.create(backup.CreateOptions{})
			if err != nil || res2.BlobsCopied != 1 || res2.DamagedBlobs != 0 {
				t.Fatalf("after the repair: %+v %v", res2, err)
			}
			if _, ok := e.mem.Data("trove/pool/sha256/" + bad.Hex()[:2] + "/" + bad.Hex()[2:4] + "/" + bad.Hex()); !ok {
				t.Fatal("the repaired blob is not in the pool")
			}
		})
	}
}

// M4, the other half: the blob is already in the pool from an earlier backup, and now its row says corrupt. It is listed as damaged
// all the same, so the next backup after the repair copies it again over the old object.
func TestR_BAK_006_ABlobInThePoolThatTurnedCorruptIsDamagedAndCopiedAgainAfterTheRepair(t *testing.T) {
	e := newEnv(t)
	d := e.addBlob("alpha")
	if _, err := e.create(backup.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	e.snap.blobs[0].Status = "corrupt"
	e.runs = newFakeRuns()
	e.snap.runs = e.runs
	e.now = e.now.Add(time.Minute)
	res, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m := e.manifest(res.BackupID); len(m.Blobs.Damaged) != 1 || m.Blobs.Damaged[0].SHA256 != d.Hex() || m.Blobs.Damaged[0].Status != "corrupt" {
		t.Fatalf("damaged = %+v", m.Blobs.Damaged)
	}
	e.snap.blobs[0].Status = ""
	e.runs = newFakeRuns()
	e.snap.runs = e.runs
	e.now = e.now.Add(time.Minute)
	e.mem.Calls = map[string]int{}
	res3, err := e.create(backup.CreateOptions{})
	if err != nil || res3.BlobsCopied != 1 {
		t.Fatalf("after the repair: %+v %v", res3, err)
	}
}

// L1: the previous blob list is trusted only after its SHA-256 and entry count matched its manifest; a mismatch fails before the
// hold becomes 'pinned'.
func TestR_BAK_006_AnAlteredPreviousBlobListFailsBeforeThePinnedHold(t *testing.T) {
	e := newEnv(t)
	e.addBlob("alpha")
	e.addBlob("beta")
	first, err := e.create(backup.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A list with the same shape and one entry fewer: it decodes, it is sorted, and it is not the one the manifest names.
	key := "trove/backups/" + first.BackupID + "/blobs.idx.zst"
	raw, _ := e.mem.Data(key)
	rd, _ := format.NewBlobListReader(bytes.NewReader(raw))
	entries, _ := format.ReadAll(rd)
	short, _ := format.EncodeBlobList(entries[:1], 3)
	e.mem.Set(key, short)

	e.addBlob("gamma")
	e.runs = newFakeRuns()
	e.snap.runs = e.runs
	e.now = e.now.Add(time.Minute)
	_, err = e.create(backup.CreateOptions{})
	exitOf(t, err, backup.ExitFailed, backup.CodePoolDamaged)
	if slices.Contains(e.runs.holds, backup.HoldPinned) {
		t.Fatalf("holds = %v: the hold must not move to pinned on a list that does not match its manifest", e.runs.holds)
	}
	if e.runs.finished != backup.StateFailed || len(e.runs.pins) != 0 {
		t.Fatalf("state %s pins %d", e.runs.finished, len(e.runs.pins))
	}
}

// L3: a stalled run is exit 75 'stalled' even when the error it ended with carries an exit code of its own.
func TestR_BAK_013_StalledWinsOverAnInnerExitError(t *testing.T) {
	e := newEnv(t)
	e.cfg.HeartbeatInterval = 20 * time.Millisecond
	e.cfg.StallTimeout = 200 * time.Millisecond
	e.addBlob("alpha")
	e.snap.onExport = func(ctx context.Context) { <-ctx.Done() }
	e.snap.exportErr = backup.Fail(backup.ExitUsage, backup.CodeConfig, errors.New("an unrelated configuration error"))
	_, err := e.create(backup.CreateOptions{})
	exitOf(t, err, backup.ExitTemporary, backup.CodeStalled)
}

// L3: a marker that does not decode is a target this lane did not write: exit 4.
func TestR_BAK_002_AMarkerThatDoesNotDecodeIsExit4(t *testing.T) {
	e := newEnv(t)
	e.mem.Set("trove/trove-backup-target.json", []byte("<html>not a marker</html>"))
	_, err := e.create(backup.CreateOptions{})
	exitOf(t, err, backup.ExitForeignTarget, backup.CodeForeignTarget)
	if e.hasManifest() {
		t.Fatal("manifest written")
	}
}

// M2: the wait of the rate limiter is progress, so a run that is throttled for longer than the lease keeps its lease.
func TestR_BAK_013_TheLimiterTicksWhileItSleeps(t *testing.T) {
	ticks := 0
	l := newLimiter(1000)
	l.tick, l.slice = func() { ticks++ }, 20*time.Millisecond
	start := time.Now()
	for range 3 {
		if err := l.wait(context.Background(), 1000); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 800*time.Millisecond {
		t.Fatalf("waited only %s", d)
	}
	if ticks < 10 {
		t.Fatalf("%d ticks during a wait of about a second", ticks)
	}
}

// L6: on SIGTERM the open uploads are aborted under a short deadline of their own, so the run ends 'interrupted' inside the
// grace period even when the target does not answer the abort.
type hangingAbort struct{ target.Store }

func (h hangingAbort) Writer(ctx context.Context, key string, o target.PutOptions) (target.ObjectWriter, error) {
	w, err := h.Store.Writer(ctx, key, o)
	return hangingWriter{w}, err
}

type hangingWriter struct{ target.ObjectWriter }

func (hangingWriter) Abort(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestR_BAK_012_AbortsOnSIGTERMHaveADeadline(t *testing.T) {
	e := newEnv(t)
	e.addBlob("alpha")
	e.build()
	e.deps.Target = hangingAbort{e.mem}
	e.deps.AbortTimeout = 150 * time.Millisecond
	p, err := New(e.deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.snap.onExport = func(c context.Context) { cancel(); <-c.Done() }
	start := time.Now()
	_, err = p.Create(ctx, backup.CreateOptions{})
	exitOf(t, err, backup.ExitTemporary, backup.CodeInterrupted)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the run took %s to end after the signal", d)
	}
	if e.runs.finished != backup.StateInterrupted {
		t.Fatalf("state = %s", e.runs.finished)
	}
	_ = io.EOF
	_ = strings.Contains
}

// M2 and L2 of the review: a server-side copy carries the callback that ticks the run, and a streaming copy tells the adapter the
// size of the object so that it picks a part size that fits.
type spyTarget struct {
	target.Store
	mu        sync.Mutex
	progress  bool
	sizeHints map[string]int64
}

func (s *spyTarget) Copy(ctx context.Context, src target.ObjectRef, dst string) error {
	s.mu.Lock()
	s.progress = src.Progress != nil
	s.mu.Unlock()
	if src.Progress != nil {
		src.Progress(1)
	}
	return s.Store.Copy(ctx, src, dst)
}

func (s *spyTarget) Writer(ctx context.Context, key string, o target.PutOptions) (target.ObjectWriter, error) {
	s.mu.Lock()
	s.sizeHints[key] = o.SizeHint
	s.mu.Unlock()
	return s.Store.Writer(ctx, key, o)
}

func TestR_BAK_006_CopiesCarryTheProgressCallbackAndTheSizeHint(t *testing.T) {
	t.Run("server-side", func(t *testing.T) {
		e := newEnv(t)
		e.addBlob("alpha")
		e.build()
		spy := &spyTarget{Store: e.mem, sizeHints: map[string]int64{}}
		e.deps.Target = spy
		p, _ := New(e.deps)
		if _, err := p.Create(context.Background(), backup.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if !spy.progress {
			t.Fatal("the object reference of a server-side copy has no progress callback")
		}
	})
	t.Run("streaming", func(t *testing.T) {
		e := newEnv(t)
		d := e.addBlob("alpha-content")
		e.cfg.Copy.ServerSide = "never"
		e.build()
		spy := &spyTarget{Store: e.mem, sizeHints: map[string]int64{}}
		e.deps.Target = spy
		p, _ := New(e.deps)
		if _, err := p.Create(context.Background(), backup.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := spy.sizeHints[format.Layout{Prefix: "trove"}.Pool(d)]; got != int64(len("alpha-content")) {
			t.Fatalf("size hint = %d", got)
		}
	})
}
