package targettest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/target"
)

// Options tune Conformance.
type Options struct {
	// Prefix is the key prefix the checks work under; every key they write is below it and is removed again.
	Prefix string
	// PartSize is the part size of the store's Writer. When it is set, the writer checks stream an object of two parts and a
	// bit, so that the multipart path runs.
	PartSize int64
	// SSE is the encryption every write is expected to report ("AES256", "aws:kms"); empty skips the check.
	SSE string
}

// Conformance holds a target.Store to the contract the backup lane relies on. The in-memory target and the S3 adapter run
// the same checks, so the fake cannot drift from the real store: lexicographic listings, a conditional create, a missing
// key that is no error on Delete, streaming writers and the SSE report of every write.
func Conformance(t *testing.T, s target.Store, o Options) {
	ctx := context.Background()
	p := o.Prefix
	t.Run("R-BAK-002 a missing object is ErrNotFound", func(t *testing.T) {
		if _, err := s.Head(ctx, p+"missing"); !errors.Is(err, target.ErrNotFound) {
			t.Fatalf("Head err = %v", err)
		}
		if _, _, err := s.Get(ctx, p+"missing"); !errors.Is(err, target.ErrNotFound) {
			t.Fatalf("Get err = %v", err)
		}
	})
	t.Run("R-BAK-002 a conditional create fails when the object exists and leaves it alone", func(t *testing.T) {
		defer del(ctx, t, s, p+"marker")
		if _, err := s.Put(ctx, p+"marker", []byte("a"), target.PutOptions{IfAbsent: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, p+"marker", []byte("b"), target.PutOptions{IfAbsent: true}); !errors.Is(err, target.ErrExists) {
			t.Fatalf("second create err = %v", err)
		}
		if got := read(ctx, t, s, p+"marker"); string(got) != "a" {
			t.Fatalf("object = %q", got)
		}
		if _, err := s.Put(ctx, p+"marker", []byte("c"), target.PutOptions{}); err != nil {
			t.Fatalf("unconditional overwrite: %v", err)
		}
		if got := read(ctx, t, s, p+"marker"); string(got) != "c" {
			t.Fatalf("object = %q", got)
		}
	})
	t.Run("R-BAK-017 a listing is in lexicographic key order, complete across pages", func(t *testing.T) {
		var keys []string
		for i := range 25 {
			keys = append(keys, fmt.Sprintf("%spool/%02x/%02x", p, (i*37)%256, i))
		}
		keys = append(keys, p+"pool-x", p+"other")
		for _, k := range keys {
			if _, err := s.Put(ctx, k, []byte(k), target.PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		defer del(ctx, t, s, keys...)
		var got []string
		if err := s.List(ctx, p+"pool/", func(o target.Object) error { got = append(got, o.Key); return nil }); err != nil {
			t.Fatal(err)
		}
		want := append([]string(nil), keys[:25]...)
		sort.Strings(want)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("keys = %v\nwant   %v", got, want)
		}
		stop := errors.New("stop")
		n := 0
		if err := s.List(ctx, p+"pool/", func(target.Object) error { n++; return stop }); !errors.Is(err, stop) || n != 1 {
			t.Fatalf("a callback error stops the listing: err = %v after %d", err, n)
		}
	})
	t.Run("R-BAK-002 Delete takes 1,000 keys and a missing key is no error", func(t *testing.T) {
		keys := make([]string, 0, 1000)
		for i := range 1000 {
			keys = append(keys, fmt.Sprintf("%sbulk/%04d", p, i))
		}
		for _, k := range keys[:5] {
			if _, err := s.Put(ctx, k, []byte("x"), target.PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Delete(ctx, keys); err != nil {
			t.Fatal(err)
		}
		n := 0
		_ = s.List(ctx, p+"bulk/", func(target.Object) error { n++; return nil })
		if n != 0 {
			t.Fatalf("%d objects left", n)
		}
	})
	t.Run("R-BAK-026 a write reports the configured encryption", func(t *testing.T) {
		defer del(ctx, t, s, p+"sse")
		out, err := s.Put(ctx, p+"sse", []byte("data"), target.PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		h, err := s.Head(ctx, p+"sse")
		if err != nil {
			t.Fatal(err)
		}
		if h.Size != 4 {
			t.Fatalf("Head size = %d", h.Size)
		}
		if o.SSE != "" && (out.SSE != o.SSE || h.SSE != o.SSE) {
			t.Fatalf("SSE: Put %q, Head %q, want %q", out.SSE, h.SSE, o.SSE)
		}
		pr, err := s.Probe(ctx, p+"probe")
		if err != nil {
			t.Fatal(err)
		}
		if o.SSE != "" && pr.SSE != o.SSE {
			t.Fatalf("Probe SSE = %q", pr.SSE)
		}
		if _, err := s.Head(ctx, p+"probe"); !errors.Is(err, target.ErrNotFound) {
			t.Fatalf("Probe left its object: %v", err)
		}
	})
	t.Run("R-BAK-005 a streaming writer stores what was written", func(t *testing.T) {
		sizes := []int{0, 10}
		if o.PartSize > 0 {
			sizes = append(sizes, int(o.PartSize), int(2*o.PartSize+777))
		}
		for _, n := range sizes {
			data := make([]byte, n)
			if _, err := rand.Read(data); err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("%sstream/%d", p, n)
			w, err := s.Writer(ctx, key, target.PutOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for rest := data; len(rest) > 0; {
				c := min(len(rest), 100_003)
				if _, err := w.Write(rest[:c]); err != nil {
					t.Fatal(err)
				}
				rest = rest[c:]
			}
			obj, err := w.Close(ctx)
			if err != nil {
				t.Fatalf("size %d: %v", n, err)
			}
			sum := sha256.Sum256(data)
			if obj.Size != int64(n) || (obj.SHA256 != "" && obj.SHA256 != hex.EncodeToString(sum[:])) {
				t.Fatalf("size %d: Close = %+v", n, obj)
			}
			if o.SSE != "" && obj.SSE != o.SSE {
				t.Fatalf("size %d: SSE = %q", n, obj.SSE)
			}
			if got := read(ctx, t, s, key); !bytes.Equal(got, data) {
				t.Fatalf("size %d: the object differs", n)
			}
			del(ctx, t, s, key)
		}
	})
	t.Run("R-BAK-002 a conditional streaming create fails when the object exists", func(t *testing.T) {
		key := p + "stream-cond"
		defer del(ctx, t, s, key)
		if _, err := s.Put(ctx, key, []byte("first"), target.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		w, _ := s.Writer(ctx, key, target.PutOptions{IfAbsent: true})
		_, _ = w.Write([]byte("second"))
		if _, err := w.Close(ctx); !errors.Is(err, target.ErrExists) {
			t.Fatalf("Close err = %v", err)
		}
		if got := read(ctx, t, s, key); string(got) != "first" {
			t.Fatalf("object = %q", got)
		}
	})
	t.Run("R-BAK-005 an aborted writer leaves no object", func(t *testing.T) {
		key := p + "stream-abort"
		w, _ := s.Writer(ctx, key, target.PutOptions{})
		_, _ = w.Write([]byte("partial"))
		if err := w.Abort(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Head(ctx, key); !errors.Is(err, target.ErrNotFound) {
			t.Fatalf("Head err = %v", err)
		}
	})
}

func read(ctx context.Context, t *testing.T, s target.Store, key string) []byte {
	t.Helper()
	rc, _, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func del(ctx context.Context, t *testing.T, s target.Store, keys ...string) {
	t.Helper()
	if err := s.Delete(ctx, keys); err != nil {
		t.Errorf("cleanup: %v", err)
	}
}
