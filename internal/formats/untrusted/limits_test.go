package untrusted

import (
	"testing"
	"time"
)

// TestR_ARCH_028_DefaultLimitsAndTighten: the defaults are those of R-ARCH-028 and a
// format can only tighten them.
func TestR_ARCH_028_DefaultLimitsAndTighten(t *testing.T) {
	d := DefaultLimits()
	if d.Entries != 10_000 || d.UncompressedBytes != 64<<20 || d.Ratio != 100 || d.ManifestEntryBytes != 1<<20 || d.Deadline != 30*time.Second || d.JSONDepth != 64 {
		t.Fatalf("defaults = %+v", d)
	}
	t.Run("R-ARCH-028 a smaller bound replaces the default", func(t *testing.T) {
		got := d.Tighten(Limits{Entries: 500, ManifestEntryBytes: 1 << 10, Deadline: 5 * time.Second, DocumentBytes: 1 << 20})
		if got.Entries != 500 || got.ManifestEntryBytes != 1<<10 || got.Deadline != 5*time.Second || got.DocumentBytes != 1<<20 {
			t.Fatalf("tightened = %+v", got)
		}
		if got.UncompressedBytes != d.UncompressedBytes || got.Ratio != d.Ratio || got.JSONDepth != d.JSONDepth {
			t.Fatalf("an unset field changed: %+v", got)
		}
	})
	t.Run("R-ARCH-028 a larger bound never loosens", func(t *testing.T) {
		got := d.Tighten(Limits{Entries: 1_000_000, UncompressedBytes: 1 << 40, Ratio: 10_000, Deadline: time.Hour, JSONDepth: 10_000})
		if got != d {
			t.Fatalf("loosened: %+v", got)
		}
	})
	t.Run("an unset field of the base takes the tightening", func(t *testing.T) {
		got := Limits{}.Tighten(Limits{Entries: 7, DocumentBytes: 9})
		if got.Entries != 7 || got.DocumentBytes != 9 {
			t.Fatalf("got %+v", got)
		}
	})
}
