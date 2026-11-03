package untrusted_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// A pass outside Inspect declares limits above the defaults of R-ARCH-028 (R-VULN-004 step 3: 1,000,000 entries, 4 GiB):
// NewZipDeclared uses them as given, and refuses a limit that is not finite.
func TestR_ARCH_028_ZipDeclaredLimits(t *testing.T) {
	ctx := context.Background()
	names := make([]string, 0, 12)
	for i := range 12 {
		names = append(names, string(rune('a'+i))+".json")
	}
	data := buildZip(t, names...)
	small := untrusted.Limits{Entries: 5, UncompressedBytes: 1 << 20, Ratio: 100, StreamBytes: 1 << 20, ManifestEntryBytes: 1 << 10}
	if _, err := untrusted.NewZipDeclared(ctx, bytes.NewReader(data), int64(len(data)), small); !untrusted.IsTooLarge(err) {
		t.Fatalf("12 entries against a limit of 5: %v, want too large", err)
	}
	wide := untrusted.Limits{Entries: 1_000_000, UncompressedBytes: 4 << 30, Ratio: 100, StreamBytes: 4 << 30, ManifestEntryBytes: 1 << 20}
	z, err := untrusted.NewZipDeclared(ctx, bytes.NewReader(data), int64(len(data)), wide)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(z.Entries()); got != 12 {
		t.Fatalf("entries = %d", got)
	}
	if _, err := z.ReadEntry(z.Entries()[0], 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.NewZipDeclared(ctx, bytes.NewReader(data), int64(len(data)), untrusted.Limits{Entries: 10}); err == nil {
		t.Fatal("an unset limit was accepted")
	}
}
