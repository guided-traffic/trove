package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ByteSize is a size in bytes. It reads "10GiB", "256MiB", "1KiB", "512" (bytes)
// and the decimal forms "MB" and "GB"; it prints the largest binary unit that
// divides it exactly.
type ByteSize int64

// Byte size units.
const (
	KiB ByteSize = 1 << (10 * (iota + 1))
	MiB
	GiB
	TiB
)

type byteUnit struct {
	suffix string
	mult   ByteSize
}

// byteUnits is ordered so that a longer suffix is tried before its tail.
func byteUnits() []byteUnit {
	return []byteUnit{
		{"KiB", KiB},
		{"MiB", MiB},
		{"GiB", GiB},
		{"TiB", TiB},
		{"KB", 1000},
		{"MB", 1000 * 1000},
		{"GB", 1000 * 1000 * 1000},
		{"TB", 1000 * 1000 * 1000 * 1000},
		{"B", 1},
	}
}

// ParseByteSize parses the text form of a ByteSize.
func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := ByteSize(1)
	num := s
	for _, u := range byteUnits() {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			num = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q: want a non-negative integer with an optional unit B, KiB, MiB, GiB, TiB, KB, MB, GB or TB", s)
	}
	if mult != 1 && n > int64(^uint64(0)>>1)/int64(mult) {
		return 0, fmt.Errorf("size %q overflows", s)
	}
	return ByteSize(n) * mult, nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// String renders the size in the largest binary unit that divides it exactly.
func (b ByteSize) String() string {
	if b == 0 {
		return "0"
	}
	for _, u := range []byteUnit{{"TiB", TiB}, {"GiB", GiB}, {"MiB", MiB}, {"KiB", KiB}} {
		if b%u.mult == 0 {
			return strconv.FormatInt(int64(b/u.mult), 10) + u.suffix
		}
	}
	return strconv.FormatInt(int64(b), 10)
}
