package nupkg

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// TestR_NUGET_020_HeadReadsTheStartOfAnEntryOnly: the first bytes of a large entry come back
// without the archive being read, a shorter entry returns what it has, and an entry of
// another archive and a directory are refused.
func TestR_NUGET_020_HeadReadsTheStartOfAnEntryOnly(t *testing.T) {
	big := append([]byte("BSJB"), bytes.Repeat([]byte{0xA5, 0x5A, 0x11, 0xEE}, 16<<20/4)...)
	data := mustBytes(t, good().with(ent{name: "lib/big.pdb", body: big, stored: true}, ent{name: "lib/tiny.pdb", body: []byte("ab")}))
	c := &countingReaderAt{r: bytes.NewReader(data)}
	res, err := Inspect(context.Background(), c, int64(len(data)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	before := c.read.Load()
	e, _ := res.Archive.Lookup("lib/big.pdb")
	head, err := res.Archive.Head(context.Background(), e, 32)
	if err != nil || len(head) != 32 || !bytes.HasPrefix(head, []byte("BSJB")) {
		t.Fatalf("head = %q, err %v", head, err)
	}
	if got := c.read.Load() - before; got > 64<<10 {
		t.Errorf("Head read %d bytes of a %d byte entry", got, len(big))
	}
	tiny, _ := res.Archive.Lookup("lib/tiny.pdb")
	if head, err := res.Archive.Head(context.Background(), tiny, 32); err != nil || string(head) != "ab" {
		t.Errorf("a short entry: %q, err %v", head, err)
	}
	if _, err := res.Archive.Head(context.Background(), Entry{Name: "forged"}, 32); !errors.Is(err, ErrEntry) {
		t.Errorf("an entry of another archive: %v", err)
	}
	dir, _ := res.Archive.Lookup("lib")
	if _, err := res.Archive.Head(context.Background(), dir, 32); !errors.Is(err, ErrEntry) {
		t.Errorf("a directory has no content: %v", err)
	}
}

// TestR_NUGET_020_HeadHoldsAnEntryToTheCompressionRatio: an entry that declares more than
// the ratio allows is refused before a byte is inflated, as for Read.
func TestR_NUGET_020_HeadHoldsAnEntryToTheCompressionRatio(t *testing.T) {
	data := mustBytes(t, good().with(ent{name: "lib/zeros.pdb", body: bytes.Repeat([]byte{0}, 4<<20)}))
	res, err := inspect(t, data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := res.Archive.Lookup("lib/zeros.pdb")
	if _, err := res.Archive.Head(context.Background(), e, 32); !errors.Is(err, ErrLimit) {
		t.Errorf("err = %v, want ErrLimit", err)
	}
}

// TestR_NUGET_020_HeadStopsWithItsContext: a cancelled context is its own error.
func TestR_NUGET_020_HeadStopsWithItsContext(t *testing.T) {
	data := mustBytes(t, good().with(ent{name: "lib/a.pdb", body: []byte("BSJB")}))
	res, err := inspect(t, data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := res.Archive.Lookup("lib/a.pdb")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := res.Archive.Head(ctx, e, 4); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
