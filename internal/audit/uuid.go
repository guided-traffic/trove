package audit

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// newID returns a UUIDv7 (RFC 9562) for the given instant: 48 bits of Unix
// milliseconds, then random bits, so event IDs sort by time.
func newID(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(t.UnixMilli())) //nolint:gosec // after 1970
	copy(b[:6], ms[2:])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}

// parseUUID reports whether s is a canonical 36-character UUID and returns its
// 16 bytes.
func parseUUID(s string) ([16]byte, bool) {
	var b [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return b, false
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if _, err := hex.Decode(b[:], []byte(h)); err != nil {
		return b, false
	}
	return b, true
}

// idTime returns the instant a UUIDv7 carries. It reports false for any other
// UUID version or a malformed string.
func idTime(id string) (time.Time, bool) {
	b, ok := parseUUID(id)
	if !ok || b[6]>>4 != 7 {
		return time.Time{}, false
	}
	var ms [8]byte
	copy(ms[2:], b[:6])
	return time.UnixMilli(int64(binary.BigEndian.Uint64(ms[:]))).UTC(), true //nolint:gosec // 48 bits
}
