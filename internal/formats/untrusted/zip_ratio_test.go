package untrusted

import (
	"math"
	"testing"
)

// R-ARCH-028: the ratio comparison does not overflow: a declared compressed size so large that ratio times it wraps is not read as
// an allowance below every size.
func TestR_ARCH_028_ZipRatioComparisonIsOverflowSafe(t *testing.T) {
	for _, tt := range []struct {
		name             string
		size, compressed int64
		ratio            int
		over             bool
	}{
		{"within the ratio", 100 << 10, 1 << 10, 100, false},
		{"above the ratio", 1 << 30, 1 << 10, 100, true},
		{"inside the read-ahead allowance", 100, 1, 100, false},
		{"a compressed size that would wrap", 1 << 40, 1 << 62, 2048, false},
		{"the largest compressed size", 1 << 40, math.MaxInt64, 100, false},
		{"a ratio of one entry with nothing compressed", 1 << 30, 0, 100, true},
	} {
		if got := overRatio(tt.size, tt.compressed, tt.ratio); got != tt.over {
			t.Errorf("%s: overRatio = %v, want %v", tt.name, got, tt.over)
		}
	}
}
