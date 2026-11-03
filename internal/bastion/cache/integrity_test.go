package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/iotest"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_BAS_032_AnEntryIsSignedAndAnEntryMovedOrRewrittenIsIgnoredAndDeleted(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a, b := payload(1, 100), payload(2, 100)
	r.fillObject(EntryArtifact, a)
	r.fillObject(EntryArtifact, b)
	ea := r.entry(EntryArtifact, a)
	if err := r.StoreEntry(ctx, "/a", ea); err != nil {
		t.Fatal(err)
	}
	got, info, err := r.LookupEntry(ctx, "/a")
	if err != nil || got.SHA256 != ea.SHA256 || info.Size != 100 {
		t.Fatalf("lookup: %+v %v", got, err)
	}

	// An entry rewritten to point at other bytes: the signature does not cover them any more.
	rec, _ := r.Backend().GetEntry(ctx, KeyHash("/a"))
	forged := bytes.Replace(rec, []byte(ea.SHA256), []byte(digest(b)), 1)
	if bytes.Equal(forged, rec) {
		t.Fatal("test setup: the record does not hold the address")
	}
	if err := r.Backend().PutEntry(ctx, KeyHash("/a"), forged); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LookupEntry(ctx, "/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rewritten entry was accepted: %v", err)
	}
	if evictions(t, r.m, metrics.EvictStale) != 1 {
		t.Error("the removal of a forged entry was not counted as stale")
	}
	if _, err := r.Backend().GetEntry(ctx, KeyHash("/a")); !errors.Is(err, ErrNotFound) {
		t.Error("a forged entry was kept")
	}

	// A valid record copied to another key's place is refused as well.
	if err := r.StoreEntry(ctx, "/a", ea); err != nil {
		t.Fatal(err)
	}
	rec, _ = r.Backend().GetEntry(ctx, KeyHash("/a"))
	if err := r.Backend().PutEntry(ctx, KeyHash("/other"), rec); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LookupEntry(ctx, "/other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entry moved to another cache key was accepted: %v", err)
	}

	// Garbage, an entry under another signing key (rotation) and a record without a signature are misses.
	for _, junk := range [][]byte{[]byte("not an entry"), nil, bytes.Repeat([]byte{'x'}, 5000)} {
		_ = r.Backend().PutEntry(ctx, KeyHash("/junk"), junk)
		if _, _, err := r.LookupEntry(ctx, "/junk"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("junk entry: %v", err)
		}
	}
	other, _ := NewSigner(bytes.Repeat([]byte{1}, MinKeyBytes))
	r.signer = other
	if _, _, err := r.LookupEntry(ctx, "/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an entry signed by the old key after a rotation: %v", err)
	}
}

func TestR_BAS_032_AReplacedObjectAbortsBeforeItsLastHoldBackAndTheNextReadIsAMiss(t *testing.T) {
	r := newRig(t, func(c *config.BastionCache) { c.MaxObjectBytes, c.MaxBytes, c.DocumentsMaxBytes = 4*mib, 8*mib, mib })
	ctx := context.Background()
	data := payload(11, 3*mib/2)
	r.fillObject(EntryArtifact, data)
	e := r.entry(EntryArtifact, data)
	if err := r.StoreEntry(ctx, "/big", e); err != nil {
		t.Fatal(err)
	}

	// Intact: all bytes arrive.
	got, _, err := r.LookupEntry(ctx, "/big")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := r.Open(ctx, "/big", got)
	if err != nil || !bytes.Equal(readAllT(t, rc), data) {
		t.Fatalf("intact read: %v", err)
	}

	// Replaced in the store by other bytes of the same size.
	evil := payload(12, len(data))
	if err := os.WriteFile(r.objectPath(EntryArtifact, e.SHA256), evil, 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err = r.Open(ctx, "/big", e)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, rc)
	_ = rc.Close()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a replaced object was served in full: %d bytes, %v", n, err)
	}
	if n > int64(len(data)-DefaultHoldBack) {
		t.Fatalf("%d bytes of %d were released: the last %d must be withheld", n, len(data), DefaultHoldBack)
	}
	if val(t, r.m.CacheCorrupt) != 1 {
		t.Errorf("corrupt counter %v", val(t, r.m.CacheCorrupt))
	}
	if _, err := r.Backend().HeadObject(ctx, e.SHA256); !errors.Is(err, ErrNotFound) {
		t.Error("the corrupt object was kept")
	}
	if _, err := r.Backend().GetEntry(ctx, KeyHash("/big")); !errors.Is(err, ErrNotFound) {
		t.Error("the entry of the corrupt object was kept")
	}
	// The following read is a miss: the caller fetches from the main installation.
	if _, _, err := r.LookupEntry(ctx, "/big"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the next read: %v", err)
	}
}

func TestR_BAS_032_AnObjectReplacedBySomethingOfAnotherSizeIsCaughtAtLookupOrAtTheEnd(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(13, 400)
	r.fillObject(EntryArtifact, data)
	e := r.entry(EntryArtifact, data)
	if err := r.StoreEntry(ctx, "/s", e); err != nil {
		t.Fatal(err)
	}
	p := r.objectPath(EntryArtifact, e.SHA256)
	if err := os.WriteFile(p, data[:399], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.LookupEntry(ctx, "/s"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a truncated object: %v", err)
	}
	if val(t, r.m.CacheCorrupt) != 1 {
		t.Errorf("corrupt counter %v", val(t, r.m.CacheCorrupt))
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Error("the truncated object was kept")
	}

	// Replaced after the lookup, longer than the entry says: no more than the entry's size is ever read.
	r.fillObject(EntryArtifact, data)
	if err := os.WriteFile(p, append(append([]byte{}, data...), make([]byte, 5000)...), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := r.Open(ctx, "/s", e)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, rc)
	_ = rc.Close()
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a longer object: %v", err)
	}
}

func TestR_BAS_032_AnObjectThatVanishesBetweenLookupAndOpenIsAMissAndItsEntryIsRemoved(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(14, 100)
	r.fillObject(EntryArtifact, data)
	e := r.entry(EntryArtifact, data)
	_ = r.StoreEntry(ctx, "/v", e)
	_ = os.Remove(r.objectPath(EntryArtifact, e.SHA256))
	if _, err := r.Open(ctx, "/v", e); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open: %v", err)
	}
	if _, err := r.Backend().GetEntry(ctx, KeyHash("/v")); !errors.Is(err, ErrNotFound) {
		t.Error("the entry stayed")
	}
}

func TestR_BAS_032_ARangeIsReadWithoutVerification(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	data := payload(15, 300)
	r.fillObject(EntryArtifact, data)
	e := r.entry(EntryArtifact, data)
	rc, err := r.OpenRange(ctx, e, 100, 50)
	if err != nil || !bytes.Equal(readAllT(t, rc), data[100:150]) {
		t.Fatalf("range: %v", err)
	}
}

func TestR_BAS_032_AnEntryOfAnUnknownKindOrAddressIsNotStored(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, e := range []Entry{
		{Kind: "other", SHA256: digest(nil)},
		{Kind: EntryArtifact, SHA256: "xyz"},
		{Kind: EntryArtifact, SHA256: digest(nil), Size: -1},
	} {
		if err := r.StoreEntry(ctx, "/x", e); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", e, err)
		}
	}
}

// oneByte returns a reader that gives data one byte at a time.
func oneByte(data []byte) io.ReadCloser {
	return io.NopCloser(iotest.OneByteReader(bytes.NewReader(data)))
}

func TestR_BAS_032_TheVerifyingReaderWithholdsTheTailUntilTheDigestMatched(t *testing.T) {
	for _, size := range []int{0, 1, 100, DefaultHoldBack - 1, DefaultHoldBack, DefaultHoldBack + 1, 256 * kib, 700*kib + 3} {
		data := payload(uint64(size), size)
		sha := digest(data)

		// Intact: every byte, in order, then EOF; Read and WriteTo agree.
		vr := newVerifyReader(io.NopCloser(bytes.NewReader(data)), sha, int64(size), DefaultHoldBack, nil)
		if got, err := io.ReadAll(vr); err != nil || !bytes.Equal(got, data) {
			t.Errorf("size %d: intact read: %d bytes, %v", size, len(got), err)
		}
		vw := newVerifyReader(io.NopCloser(bytes.NewReader(data)), sha, int64(size), DefaultHoldBack, nil)
		var out bytes.Buffer
		if n, err := vw.WriteTo(&out); err != nil || n != int64(size) || !bytes.Equal(out.Bytes(), data) {
			t.Errorf("size %d: WriteTo: %d %v", size, n, err)
		}

		if size == 0 {
			continue
		}
		// The last byte differs: the transfer aborts and the tail is not released.
		bad := append([]byte(nil), data...)
		bad[size-1] ^= 0xff
		var calls int
		vb := newVerifyReader(io.NopCloser(bytes.NewReader(bad)), sha, int64(size), DefaultHoldBack, func(error) { calls++ })
		got, err := io.ReadAll(vb)
		if !errors.Is(err, ErrCorrupt) || calls != 1 {
			t.Errorf("size %d: corrupt read: %v, hook calls %d", size, err, calls)
		}
		if limit := max(size-DefaultHoldBack, 0); len(got) > limit {
			t.Errorf("size %d: %d bytes released, at most %d allowed", size, len(got), limit)
		}
		_ = vb.Close()
	}
}

func TestR_BAS_032_TheVerifyingReaderRefusesAShortAndALongBodyAndPassesATransportError(t *testing.T) {
	data := payload(21, 1000)
	sha := digest(data)
	for name, body := range map[string][]byte{"short": data[:900], "long": append(append([]byte{}, data...), 1)} {
		var calls int
		v := newVerifyReader(io.NopCloser(bytes.NewReader(body)), sha, 1000, 100, func(error) { calls++ })
		if _, err := io.ReadAll(v); !errors.Is(err, ErrCorrupt) || calls != 1 {
			t.Errorf("%s: %v, hook calls %d", name, err, calls)
		}
	}
	// A transport error is no verdict about the object: no hook, the error as is.
	boom := errors.New("connection reset")
	src := io.NopCloser(io.MultiReader(bytes.NewReader(data[:500]), iotest.ErrReader(boom)))
	v := newVerifyReader(src, sha, 1000, 100, func(error) { t.Error("a transport error was judged corrupt") })
	if _, err := io.ReadAll(v); !errors.Is(err, boom) {
		t.Errorf("transport error: %v", err)
	}
	// Small reads from the source do not change the outcome.
	v = newVerifyReader(oneByte(data), sha, 1000, 100, nil)
	if got, err := io.ReadAll(v); err != nil || !bytes.Equal(got, data) {
		t.Errorf("one byte at a time: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Read(make([]byte, 1)); err == nil {
		t.Errorf("read after close: %v", err)
	}
}
