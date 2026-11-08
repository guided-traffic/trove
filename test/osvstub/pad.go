package osvstub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PadKind is the content of the padding of [Stub.Pad].
type PadKind int

const (
	// PadHex pads with the hex text of a SHA-256 chain: it deflates to about half its size, so a test that pads a record
	// above a size bound meets the size and not the compression ratio.
	PadHex PadKind = iota
	// PadRepeat pads with one repeated character: the entry compresses by far more than 100:1, so a test meets the ratio bound
	// of one entry with a record well below any size bound.
	PadRepeat
)

// padSteps bounds the corrections of the padding length: the serialization of the record is computed again after each try, and
// the length of the padding is the only thing that changes, so one correction suffices.
const padSteps = 4

// Pad replaces the record id of ecosystem with a copy whose body is exactly size bytes, with a later modified (so it is a row
// of the change file and the archive is rebuilt). The padding is a member database_specific.padding; with PadHex it is hex text
// of a SHA-256 chain, which deflates to about half, so the size and not the ratio is what a test meets; with PadRepeat it is one
// repeated character, so the entry compresses by far more than 100:1. It fails when the record is unknown or size is smaller than
// the record with an empty padding.
func (s *Stub) Pad(ecosystem, id string, size int, kind PadKind) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return time.Time{}, err
	}
	e, ok := st.recs[id]
	if !ok {
		return time.Time{}, fmt.Errorf("%w record %q", ErrUnknown, id)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(e.body, &obj); err != nil {
		return time.Time{}, err
	}
	var ds map[string]json.RawMessage
	if json.Unmarshal(obj["database_specific"], &ds) != nil || ds == nil {
		ds = map[string]json.RawMessage{}
	}
	modified := s.next()
	obj["modified"], _ = json.Marshal(stamp(modified))
	// The serialization is computed before anything is stored, so that a size that cannot be met leaves the record as it was.
	n := 0
	for range padSteps {
		ds["padding"], _ = json.Marshal(padding(id, n, kind))
		obj["database_specific"], _ = json.Marshal(ds)
		body, _ := json.Marshal(obj)
		have := len(body)
		if have == size {
			s.putLocked(st, id, body, modified)
			return modified, nil
		}
		if n += size - have; n < 0 {
			return time.Time{}, fmt.Errorf("osvstub: %s is larger than %d bytes without padding", id, size)
		}
	}
	return time.Time{}, fmt.Errorf("osvstub: %s could not be padded to %d bytes", id, size)
}

// padding is n bytes of text of the kind; the chain starts from the id, so the same call gives the same bytes.
func padding(id string, n int, kind PadKind) string {
	if kind == PadRepeat {
		return strings.Repeat("a", n)
	}
	var b strings.Builder
	b.Grow(n + sha256.Size*2)
	h := sha256.Sum256([]byte(id))
	for b.Len() < n {
		b.WriteString(hex.EncodeToString(h[:]))
		h = sha256.Sum256(h[:])
	}
	return b.String()[:n]
}
