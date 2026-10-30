package blob_test

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestCommitAll(t *testing.T) {
	t.Run("R-ARCH-009 R-STOR-005 several blobs commit with one link transaction", func(t *testing.T) {
		f := newFixture(t, nil)
		a, b := f.ingest(t, content(10, 50)), f.ingest(t, content(20, 51))
		var links int
		res, err := f.store.CommitAll(context.Background(), []*blob.Spooled{a, b}, func(context.Context, meta.Tx) error {
			links++
			return nil
		})
		if err != nil || links != 1 || len(res) != 2 {
			t.Fatalf("res=%+v err=%v links=%d", res, err, links)
		}
		if !res[0].Inserted || !res[1].Inserted || f.mem.Count("put") != 2 || len(f.db.rows) != 2 {
			t.Fatalf("res=%+v puts=%d rows=%d", res, f.mem.Count("put"), len(f.db.rows))
		}
	})
	t.Run("R-ARCH-009 R-STOR-005 a failing link leaves no row and queues every written digest", func(t *testing.T) {
		f := newFixture(t, nil)
		a, b := f.ingest(t, content(10, 52)), f.ingest(t, content(20, 53))
		boom := errors.New("second file refused")
		_, err := f.store.CommitAll(context.Background(), []*blob.Spooled{a, b}, func(context.Context, meta.Tx) error { return boom })
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}
		if len(f.db.rows) != 0 || !f.db.queue[a.SHA256] || !f.db.queue[b.SHA256] {
			t.Fatalf("rows=%d queue=%v", len(f.db.rows), f.db.queue)
		}
	})
	t.Run("R-STOR-005 the same content twice is one blob, and the results follow the input", func(t *testing.T) {
		f := newFixture(t, nil)
		data := content(10, 54)
		a, b, c := f.ingest(t, data), f.ingest(t, content(12, 55)), f.ingest(t, data)
		res, err := f.store.CommitAll(context.Background(), []*blob.Spooled{a, b, c}, noLink)
		if err != nil || len(res) != 3 || f.mem.Count("put") != 2 || len(f.db.rows) != 2 {
			t.Fatalf("res=%+v err=%v puts=%d rows=%d", res, err, f.mem.Count("put"), len(f.db.rows))
		}
		if res[0] != res[2] {
			t.Fatalf("the repeated blob has two outcomes: %+v", res)
		}
	})
	t.Run("R-STOR-005 a blob that is there already needs no object write while the new one is written", func(t *testing.T) {
		f := newFixture(t, nil)
		old := content(10, 56)
		if _, err := f.store.Commit(context.Background(), f.ingest(t, old), noLink); err != nil {
			t.Fatal(err)
		}
		puts := f.mem.Count("put")
		res, err := f.store.CommitAll(context.Background(), []*blob.Spooled{f.ingest(t, old), f.ingest(t, content(11, 57))}, noLink)
		if err != nil || f.mem.Count("put") != puts+1 {
			t.Fatalf("res=%+v err=%v puts=%d", res, err, f.mem.Count("put"))
		}
		if !res[0].Deduplicated || res[1].Deduplicated {
			t.Fatalf("res=%+v", res)
		}
	})
}
