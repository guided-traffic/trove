package audit

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// counterBits is the width of the sub-millisecond counter in the rand_a field of a UUIDv7 (RFC 9562, method 1).
	counterBits = 12
	// counterStart bounds the random value a new millisecond starts its counter at, so a millisecond has at least
	// 2048 IDs of headroom before the counter overflows.
	counterStart = 1 << (counterBits - 1)
	// streamSkew is how far behind the newest ID's instant a requested instant may lie and still join the ID
	// stream; an older one (an event whose producer learned of it later) takes a plain random ID.
	streamSkew = time.Second
)

// idStream hands out the IDs of one process in creation order. Two events of one operation share the
// transaction's occurred_at, so the ID is what orders them in the listing and the export (R-OPS-017): the
// counter makes the ID of the event created second sort after the first within one millisecond.
var idStream struct {
	mu      sync.Mutex
	ms      int64
	counter uint16
}

// nextStamp returns the millisecond and counter of the next ID for the requested millisecond.
func nextStamp(ms int64, random uint16) (int64, uint16) {
	idStream.mu.Lock()
	defer idStream.mu.Unlock()
	switch {
	case ms > idStream.ms:
		idStream.ms, idStream.counter = ms, random%counterStart
	case ms+streamSkew.Milliseconds() <= idStream.ms:
		return ms, random & (1<<counterBits - 1)
	case idStream.counter == 1<<counterBits-1:
		// The counter is spent: the stream moves on to the next millisecond. The ID then carries a time at most a
		// few milliseconds past the instant asked for, far inside the window Get reads around it.
		idStream.ms++
		idStream.counter = random % counterStart
	default:
		idStream.counter++
	}
	return idStream.ms, idStream.counter
}

// newID returns a UUIDv7 (RFC 9562) for the given instant: 48 bits of Unix
// milliseconds, a counter, then random bits, so event IDs sort by time and the
// IDs one process creates within a millisecond sort in creation order.
func newID(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	msec, counter := nextStamp(t.UnixMilli(), binary.BigEndian.Uint16(b[6:8]))
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(msec)) //nolint:gosec // after 1970
	copy(b[:6], ms[2:])
	b[6] = byte(counter>>8) | 0x70
	b[7] = byte(counter & 0xff)
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
