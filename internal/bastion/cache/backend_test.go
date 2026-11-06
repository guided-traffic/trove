package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_BAS_030_VolumeKeepsBytesOnceByContentAddressInTheLayout(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(1, 300)
	sha := digest(data)
	if err := r.Backend().PutObject(ctx, sha, int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(r.dir, "sha256", sha[0:2], sha[2:4], sha)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the bytes are not at %s: %v", want, err)
	}
	// Storing an existing address is not an error and keeps one object.
	if err := r.Backend().PutObject(ctx, sha, int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatalf("second put: %v", err)
	}
	info, err := r.Backend().HeadObject(ctx, sha)
	if err != nil || info.Size != 300 {
		t.Fatalf("head: %+v %v", info, err)
	}
	rc, err := r.Backend().GetObject(ctx, sha, 0, -1)
	if err != nil || !bytes.Equal(readAllT(t, rc), data) {
		t.Fatalf("get: %v", err)
	}
	for _, c := range []struct {
		off, n int64
		want   []byte
	}{{10, 20, data[10:30]}, {290, -1, data[290:]}, {290, 50, data[290:]}, {300, 5, nil}} {
		rc, err := r.Backend().GetObject(ctx, sha, c.off, c.n)
		if err != nil {
			t.Fatalf("range %d,%d: %v", c.off, c.n, err)
		}
		if got := readAllT(t, rc); !bytes.Equal(got, c.want) {
			t.Errorf("range %d,%d: %d bytes, want %d", c.off, c.n, len(got), len(c.want))
		}
	}
	if err := r.Backend().DeleteObject(ctx, sha); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backend().HeadObject(ctx, sha); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := r.Backend().DeleteObject(ctx, sha); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
}

func TestR_BAS_030_EntriesMarkersAndTheSpoolHaveTheirPlaces(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	kh := KeyHash("/api/npm/web/npm-all/x")
	if err := r.Backend().PutEntry(ctx, kh, []byte("record")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "entries", kh[0:2], kh+".json")); err != nil {
		t.Fatal(err)
	}
	sha := digest([]byte("x"))
	if err := r.Backend().Touch(ctx, sha); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(r.dir, "access", sha)); err != nil || fi.Size() != 0 {
		t.Fatalf("marker: %v", err)
	}
	if err := r.Backend().SpoolWrite(ctx, "bastion-0", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(filepath.Join(r.dir, "audit-spool", "bastion-0"))
	if len(files) != 1 || !strings.HasSuffix(files[0].Name(), ".jsonl") {
		t.Fatalf("spool files: %v", files)
	}
}

func TestR_BAS_030_ANameOutsideTheLayoutIsRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, sha := range []string{"", "../../etc/passwd", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if _, err := r.Backend().GetObject(ctx, sha, 0, -1); !errors.Is(err, ErrInvalid) {
			t.Errorf("GetObject(%q): %v", sha, err)
		}
		if err := r.Backend().Touch(ctx, sha); !errors.Is(err, ErrInvalid) {
			t.Errorf("Touch(%q): %v", sha, err)
		}
		if _, err := r.Backend().GetEntry(ctx, sha); !errors.Is(err, ErrInvalid) {
			t.Errorf("GetEntry(%q): %v", sha, err)
		}
	}
	for _, rep := range []string{"", "..", "a/b", ".hidden", strings.Repeat("x", 129)} {
		if err := r.Backend().SpoolWrite(ctx, rep, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("SpoolWrite(%q): %v", rep, err)
		}
	}
	if _, err := r.Backend().SpoolRead(ctx, SpoolFile{Replica: "a", Name: "../x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("SpoolRead: %v", err)
	}
}

func TestR_BAS_030_APutOfOtherBytesThanTheAddressIsRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(2, 100)
	for name, c := range map[string]struct {
		sha  string
		size int64
		body []byte
	}{
		"other digest": {digest([]byte("other")), 100, data},
		"short body":   {digest(data), 101, data},
		"long body":    {digest(data), 99, data},
	} {
		err := r.Backend().PutObject(ctx, c.sha, c.size, bytes.NewReader(c.body))
		if !errors.Is(err, ErrMismatch) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := r.Backend().HeadObject(ctx, c.sha); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: the object exists after a refused put: %v", name, err)
		}
	}
	if left, _ := os.ReadDir(filepath.Join(r.dir, "tmp")); len(left) != 0 {
		t.Errorf("spool files left: %d", len(left))
	}
}

func TestR_BAS_030_AFillIsInvisibleUntilItIsCommittedAfterVerification(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(3, 500)
	sha := digest(data)

	f, err := r.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if f.Size() != 500 || f.SHA256() != sha {
		t.Fatalf("running digest: %d %s", f.Size(), f.SHA256())
	}
	if _, err := r.Backend().HeadObject(ctx, sha); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a fill in progress is visible: %v", err)
	}
	if tmp, _ := os.ReadDir(filepath.Join(r.dir, "tmp")); len(tmp) != 1 {
		t.Fatalf("the fill is not spooled in the volume's tmp: %d files", len(tmp))
	}
	// Wrong digest: aborted, nothing committed.
	if err := f.Commit(ctx, digest([]byte("other"))); !errors.Is(err, ErrMismatch) {
		t.Fatalf("commit under another address: %v", err)
	}
	if _, err := r.Backend().HeadObject(ctx, sha); !errors.Is(err, ErrNotFound) {
		t.Fatal("bytes were committed under no address")
	}
	// A fresh fill with the right digest is committed by a rename.
	r.fillObject(EntryArtifact, data)
	if _, err := r.Backend().HeadObject(ctx, sha); err != nil {
		t.Fatal(err)
	}
	if tmp, _ := os.ReadDir(filepath.Join(r.dir, "tmp")); len(tmp) != 0 {
		t.Fatalf("spool files left: %d", len(tmp))
	}
	// Abort after commit is harmless; abort removes an unfinished fill.
	f2, _ := r.NewFill(EntryArtifact)
	_, _ = f2.Write([]byte("x"))
	f2.Abort()
	f2.Abort()
	if tmp, _ := os.ReadDir(filepath.Join(r.dir, "tmp")); len(tmp) != 0 {
		t.Fatalf("an aborted fill left %d files", len(tmp))
	}
}

func TestR_BAS_030_AFillStopsAtTheLimitOfItsKind(t *testing.T) {
	r := newRig(t)
	f, _ := r.NewFill(EntryArtifact)
	if _, err := f.Write(make([]byte, 600)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("artifact past max_object_bytes: %v", err)
	}
	f.Abort()
	d, _ := r.NewFill(EntryDocument)
	if _, err := d.Write(make([]byte, 201)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("document past documents_max_bytes: %v", err)
	}
	d.Abort()
}

func TestR_BAS_031_DocumentsAreAnAreaOfTheirOwn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	doc := payload(4, 80)
	art := payload(5, 90)
	r.fillObject(EntryDocument, doc)
	r.fillObject(EntryArtifact, art)
	if _, err := r.Backend().HeadObject(ctx, digest(doc)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a document is found as an artifact: %v", err)
	}
	if _, err := r.Backend().HeadObject(WithArea(ctx, EntryDocument), digest(doc)); err != nil {
		t.Fatal(err)
	}
	u, err := r.Backend().Usage(ctx)
	if err != nil || u.Artifacts != 90 || u.Documents != 80 {
		t.Fatalf("usage %+v %v", u, err)
	}
}

func TestR_BAS_039_TheAuditSpoolIsListedReadAndDeletedOldestFirst(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	b := r.Backend()
	if got, err := b.SpoolList(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	// Two writes in the same millisecond still get two names.
	for i, rep := range []string{"b-1", "a-0", "a-0"} {
		if err := b.SpoolWrite(ctx, rep, []byte{byte('0' + i), '\n'}); err != nil {
			t.Fatal(err)
		}
		r.clock.Advance(0)
	}
	r.clock.Advance(time.Second)
	if err := b.SpoolWrite(ctx, "b-1", []byte("late\n")); err != nil {
		t.Fatal(err)
	}
	files, err := b.SpoolList(ctx)
	if err != nil || len(files) != 4 {
		t.Fatalf("list: %v %v", files, err)
	}
	for i := 1; i < len(files); i++ {
		if spoolMs(files[i-1].Name) >= spoolMs(files[i].Name) {
			t.Errorf("not oldest first: %v", files)
		}
	}
	got, err := b.SpoolRead(ctx, files[3])
	if err != nil || string(got) != "late\n" {
		t.Fatalf("read: %q %v", got, err)
	}
	for _, f := range files {
		if err := b.SpoolDelete(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SpoolDelete(ctx, files[0]); err != nil {
		t.Fatalf("deleting a deleted file: %v", err)
	}
	if _, err := b.SpoolRead(ctx, files[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read after delete: %v", err)
	}
	if got, _ := b.SpoolList(ctx); len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestR_BAS_030_TheFirstCheckGatesReadinessAndLaterFailuresAreMissesCounted(t *testing.T) {
	r := newRig(t)
	fl := &Fault{}
	r.back.s = &faultStore{store: r.back.s, f: fl}
	ctx := context.Background()

	fl.SetDown(true)
	if err := r.Ready(ctx); err == nil {
		t.Fatal("a failing backend was ready at the first check")
	}
	fl.SetDown(false)
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("first readiness: %v", err)
	}

	data := payload(6, 100)
	r.fillObject(EntryArtifact, data)
	e := r.entry(EntryArtifact, data)
	if err := r.StoreEntry(ctx, "/k", e); err != nil {
		t.Fatal(err)
	}

	fl.SetDown(true)
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("readiness is gated only at the first check: %v", err)
	}
	before := errs(t, r.m, metrics.OpGet)
	if _, _, err := r.LookupEntry(ctx, "/k"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a failing backend must be an error the caller turns into a miss: %v", err)
	}
	if errs(t, r.m, metrics.OpGet) != before+1 {
		t.Errorf("get errors: %v -> %v", before, errs(t, r.m, metrics.OpGet))
	}
	if err := r.StoreEntry(ctx, "/k2", e); err == nil || errs(t, r.m, metrics.OpPut) != 1 {
		t.Errorf("put: %v, counted %v", err, errs(t, r.m, metrics.OpPut))
	}
	if _, err := r.Backend().Usage(ctx); err == nil || errs(t, r.m, metrics.OpList) != 1 {
		t.Errorf("list: %v, counted %v", err, errs(t, r.m, metrics.OpList))
	}
	if err := r.DeleteEntry(ctx, "/k"); err == nil || errs(t, r.m, metrics.OpDelete) != 1 {
		t.Errorf("delete: %v", err)
	}
	if err := r.Backend().SpoolWrite(ctx, "r", []byte("x")); err == nil || errs(t, r.m, metrics.OpSpool) != 1 {
		t.Errorf("spool: %v", err)
	}
	// A missing key and a cancelled context are no backend error.
	fl.SetDown(false)
	before = errs(t, r.m, metrics.OpGet)
	if _, _, err := r.LookupEntry(ctx, "/absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, _ = r.LookupEntry(cctx, "/absent")
	if errs(t, r.m, metrics.OpGet) != before {
		t.Error("a miss or a cancelled request was counted as a backend error")
	}
	fl.SetDown(false)
	if _, _, err := r.LookupEntry(ctx, "/k"); err != nil {
		t.Fatalf("the backend is back: %v", err)
	}
}

func TestR_BAS_030_KillingAnEphemeralVolumeLosesOnlyTheCache(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(7, 120)
	r.fillObject(EntryArtifact, data)
	if err := r.StoreEntry(ctx, "/k", r.entry(EntryArtifact, data)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LookupEntry(ctx, "/k"); err != nil {
		t.Fatal(err)
	}
	// The pod is deleted: the volume is gone, a new one is empty. A replica on it starts and misses.
	if err := os.RemoveAll(r.dir); err != nil {
		t.Fatal(err)
	}
	fresh, err := New(ctx, r.cfg, testSigner(t), Options{Metrics: r.m, Now: r.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh.LookupEntry(ctx, "/k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the cache survived the volume: %v", err)
	}
	fresh.fillObjectFor(t, data)
	if _, _, err := fresh.LookupEntry(ctx, "/k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entry appeared from nowhere: %v", err)
	}
}

// fillObjectFor commits data through a fill of c.
func (c *Cache) fillObjectFor(t *testing.T, data []byte) {
	t.Helper()
	f, err := c.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(data)
	if err := f.Commit(context.Background(), digest(data)); err != nil {
		t.Fatal(err)
	}
}

func TestR_BAS_030_APersistentVolumeKeepsItsCacheAcrossARestart(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(8, 120)
	r.fillObject(EntryArtifact, data)
	if err := r.StoreEntry(ctx, "/k", r.entry(EntryArtifact, data)); err != nil {
		t.Fatal(err)
	}
	// A leftover of an interrupted fill is removed at start once it is stale; a fresh one is left alone.
	stale := filepath.Join(r.dir, "tmp", "fill-stale")
	young := filepath.Join(r.dir, "tmp", "fill-young")
	for _, p := range []string{stale, young} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.setTime(stale, time.Now().Add(-2*time.Hour))
	again, err := New(ctx, r.cfg, testSigner(t), Options{Metrics: r.m})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := again.LookupEntry(ctx, "/k"); err != nil {
		t.Fatalf("the persistent volume lost its cache: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Error("a stale spool file was kept")
	}
	if _, err := os.Stat(young); err != nil {
		t.Error("a young spool file was removed")
	}
}

func TestR_BAS_030_ConcurrentFillsReadsAndSweepsAreRaceFree(t *testing.T) {
	r := newRig(t, func(c *config.BastionCache) { c.MaxBytes, c.DocumentsMaxBytes, c.MaxObjectBytes = 20_000, 1000, 600 })
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 30 {
				data := payload(uint64(g*100+i), 200+i)
				key := "/k/" + digest(data)
				f, err := r.NewFill(EntryArtifact)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = f.Write(data)
				if err := f.Commit(ctx, digest(data)); err != nil {
					t.Error(err)
					return
				}
				e := r.entry(EntryArtifact, data)
				if err := r.StoreEntry(ctx, key, e); err != nil {
					t.Error(err)
				}
				if got, _, err := r.LookupEntry(ctx, key); err == nil {
					r.Touch(ctx, got)
					if rc, err := r.Open(ctx, key, got); err == nil {
						_, _ = io.Copy(io.Discard, rc)
						_ = rc.Close()
					}
				}
				if i%10 == 0 {
					_ = r.SweepOnce(ctx, nil)
				}
			}
		}()
	}
	wg.Wait()
}
