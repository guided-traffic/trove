package targettest

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/target"
)

func TestMem(t *testing.T) {
	ctx := context.Background()
	m := New()
	t.Run("R-BAK-002 a conditional create fails when the object exists", func(t *testing.T) {
		if _, err := m.Put(ctx, "p/marker", []byte("a"), target.PutOptions{IfAbsent: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Put(ctx, "p/marker", []byte("b"), target.PutOptions{IfAbsent: true}); !errors.Is(err, target.ErrExists) {
			t.Fatalf("err = %v", err)
		}
		if got, _ := m.Data("p/marker"); string(got) != "a" {
			t.Fatalf("data = %q", got)
		}
	})
	t.Run("R-BAK-017 a listing is in lexicographic key order", func(t *testing.T) {
		for _, k := range []string{"p/pool/c", "p/pool/a", "p/pool/b", "p/other"} {
			m.Set(k, []byte(k))
		}
		var keys []string
		if err := m.List(ctx, "p/pool/", func(o target.Object) error { keys = append(keys, o.Key); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(keys) != 3 || keys[0] != "p/pool/a" || keys[2] != "p/pool/c" {
			t.Fatalf("keys = %v", keys)
		}
	})
	t.Run("R-BAK-026 every write reports the configured server-side encryption", func(t *testing.T) {
		w, _ := m.Writer(ctx, "p/db/t.copy.zst", target.PutOptions{})
		_, _ = w.Write([]byte("rows"))
		o, err := w.Close(ctx)
		if err != nil || o.SSE != "AES256" || o.Size != 4 {
			t.Fatalf("Close = %+v, %v", o, err)
		}
		body, _, err := m.Get(ctx, "p/db/t.copy.zst")
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(body); string(b) != "rows" {
			t.Fatalf("body = %q", b)
		}
	})
	t.Run("R-BAK-006 a server-side copy needs a source and falls back to streaming otherwise", func(t *testing.T) {
		if err := m.Copy(ctx, target.ObjectRef{Bucket: "b", Key: "k"}, "p/pool/x"); !errors.Is(err, target.ErrServerCopyUnavailable) {
			t.Fatalf("err = %v", err)
		}
		m.Source = func(r target.ObjectRef) ([]byte, bool) { return []byte("blob"), r.Key == "k" }
		if err := m.Copy(ctx, target.ObjectRef{Bucket: "b", Key: "k"}, "p/pool/x"); err != nil {
			t.Fatal(err)
		}
		if m.Calls["copy"] != 2 {
			t.Fatalf("calls = %v", m.Calls)
		}
	})
	t.Run("R-BAK-016 a delete of a missing key is no error and at most 1000 keys", func(t *testing.T) {
		if err := m.Delete(ctx, []string{"nope"}); err != nil {
			t.Fatal(err)
		}
		if err := m.Delete(ctx, make([]string, 1001)); err == nil {
			t.Fatal("accepted 1001 keys")
		}
	})
}
