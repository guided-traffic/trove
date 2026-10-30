package untrusted

import (
	"encoding/binary"
	"testing"
	"time"
)

// TestR_ARCH_028_DefaultsOfQ_ARCH_217: the bounds R-ARCH-028 leaves without a
// number have the defaults Q-ARCH-217 fixes, and a format can only tighten them.
func TestR_ARCH_028_DefaultsOfQ_ARCH_217(t *testing.T) {
	d := DefaultLimits()
	if d.JSONMembers != 2_000_000 || d.XMLDepth != 64 || d.StreamBytes != 2<<30 {
		t.Fatalf("defaults = %+v", d)
	}
	got := d.Tighten(Limits{JSONMembers: 10, XMLDepth: 8, StreamBytes: 1 << 20, Deadline: time.Second})
	if got.JSONMembers != 10 || got.XMLDepth != 8 || got.StreamBytes != 1<<20 {
		t.Fatalf("tightened = %+v", got)
	}
	if loose := d.Tighten(Limits{JSONMembers: 1 << 30, XMLDepth: 1000, StreamBytes: 1 << 50}); loose != d {
		t.Fatalf("loosened: %+v", loose)
	}
	if (Limits{DocumentBytes: 10, ManifestEntryBytes: 20}).Bound() != 10 || (Limits{ManifestEntryBytes: 20}).Bound() != 20 {
		t.Fatal("Bound")
	}
}

// TestR_ARCH_028_Zip64Offset: the local header offset of an entry that starts
// beyond 4 GiB comes out of the zip64 extra field, after the sizes that are also
// stored there.
func TestR_ARCH_028_Zip64Offset(t *testing.T) {
	le := binary.LittleEndian
	extra := func(fields ...uint64) []byte {
		b := le.AppendUint16(nil, 0x0001)
		b = le.AppendUint16(b, uint16(8*len(fields)))
		for _, f := range fields {
			b = le.AppendUint64(b, f)
		}
		return b
	}
	other := le.AppendUint16(le.AppendUint16(nil, 0x5455), 5)
	other = append(other, 1, 2, 3, 4, 5)
	cases := []struct {
		name         string
		extra        []byte
		usize, csize bool
		want         uint64
		ok           bool
	}{
		{"offset alone", extra(7 << 32), false, false, 7 << 32, true},
		{"after the uncompressed size", extra(1, 7<<32), true, false, 7 << 32, true},
		{"after both sizes", extra(1, 2, 7<<32), true, true, 7 << 32, true},
		{"after another field", append(append([]byte(nil), other...), extra(7<<32)...), false, false, 7 << 32, true},
		{"too short", extra(1), true, false, 0, false},
		{"no zip64 field", other, false, false, 0, false},
		{"cut field", extra(1)[:6], false, false, 0, false},
	}
	for _, c := range cases {
		got, err := zip64Offset(c.extra, c.usize, c.csize)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%s: %d %v", c.name, got, err)
		}
	}
}
