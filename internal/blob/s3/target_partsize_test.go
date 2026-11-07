package s3

import "testing"

func TestR_BAK_005_PartSizeFitsTheObjectInTenThousandParts(t *testing.T) {
	const mib, gib, tib = int64(1 << 20), int64(1 << 30), int64(1 << 40)
	t.Run("R-BAK-005 a hinted object gets parts so that it fits", func(t *testing.T) {
		for _, size := range []int64{0, 10 * mib, 100 * gib, 500 * gib, 2 * tib, 5 * tib} {
			base, err := basePartSize(16*mib, size)
			if err != nil {
				t.Fatalf("size %d: %v", size, err)
			}
			if size > 0 && size > base*maxParts {
				t.Fatalf("size %d with parts of %d needs more than %d parts", size, base, maxParts)
			}
			if base < 16*mib || base > maxPartSize {
				t.Fatalf("size %d: part size %d out of range", size, base)
			}
		}
	})
	t.Run("R-BAK-005 a hint above the object limit fails up front", func(t *testing.T) {
		if _, err := basePartSize(16*mib, 5*tib+1); err == nil {
			t.Fatal("accepted an object above 5 TiB")
		}
	})
	t.Run("R-BAK-005 an unhinted writer grows its parts and passes the old 156 GiB ceiling", func(t *testing.T) {
		var total int64
		for n := range maxParts {
			total += partSizeAt(16*mib, false, n)
		}
		if total < 800*gib {
			t.Fatalf("10,000 parts reach only %d GiB", total/gib)
		}
		if got := partSizeAt(16*mib, false, 0); got != 16*mib {
			t.Fatalf("first part = %d", got)
		}
		if got := partSizeAt(16*mib, true, 9999); got != 16*mib {
			t.Fatalf("a hinted writer grew its parts: %d", got)
		}
	})
}
