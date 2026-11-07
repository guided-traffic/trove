package listing_test

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
)

// The sweep and verify --all keep one blob-list reader open per retained backup. This measures the heap that n open readers
// hold, so the bound is a number and not a hope.
func TestR_BAK_017_OpenBlobListReadersStayInBoundedMemory(t *testing.T) {
	const n = 100
	var entries []format.BlobEntry
	for i := range 3000 {
		entries = append(entries, format.BlobEntry{Digest: fixture.Digest(fmt.Sprintf("blob-%06d", i)), Size: 10})
	}
	slices.SortFunc(entries, func(a, b format.BlobEntry) int { return strings.Compare(a.Digest.Hex(), b.Digest.Hex()) })
	raw, err := format.EncodeBlobList(entries, 3)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	readers := make([]*format.BlobListReader, n)
	for i := range readers {
		r, err := format.NewBlobListReader(io.NopCloser(bytes.NewReader(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
		readers[i] = r
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / n
	t.Logf("heap per open reader: %d KiB (GOMAXPROCS %d)", per/1024, runtime.GOMAXPROCS(0))
	if per > 3<<20 {
		t.Fatalf("an open blob-list reader holds %d KiB", per/1024)
	}
	for _, r := range readers {
		r.Close()
	}
}
