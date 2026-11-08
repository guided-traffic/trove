package uploads

import (
	"crypto/md5"  //nolint:gosec // MD5 is part of the digest set for client compatibility (R-STOR-002)
	"crypto/sha1" //nolint:gosec // SHA-1 is part of the digest set for client compatibility (R-STOR-002)
	"crypto/sha256"
	"crypto/sha512"
	"encoding"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"

	"github.com/guided-traffic/trove/internal/blob"
)

// hashes are the four hash functions of the digest set (R-STOR-002), advanced over the whole content of a session. Go's
// implementations marshal and restore their internal state, so a request on any replica continues where the previous request
// of the session stopped, and no byte is hashed twice (R-STOR-011 State).
type hashes struct {
	sha256, sha1, md5, sha512 hash.Hash
	n                         int64
}

func newHashes() *hashes {
	return &hashes{sha256: sha256.New(), sha1: sha1.New(), md5: md5.New(), sha512: sha512.New()} //nolint:gosec // see imports
}

func (h *hashes) all() [4]hash.Hash { return [4]hash.Hash{h.sha256, h.sha1, h.md5, h.sha512} }

// write feeds bytes to every function.
func (h *hashes) write(p []byte) {
	for _, f := range h.all() {
		f.Write(p)
	}
	h.n += int64(len(p))
}

// marshal is the state of the four functions and the byte count they have seen: the byte count as 8 bytes, then each state
// preceded by its length as 4 bytes.
func (h *hashes) marshal() ([]byte, error) {
	out := binary.BigEndian.AppendUint64(nil, uint64(h.n)) //nolint:gosec // a count of bytes is never negative
	for _, f := range h.all() {
		m, ok := f.(encoding.BinaryMarshaler)
		if !ok {
			return nil, errors.New("a hash function of the digest set cannot save its state")
		}
		b, err := m.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("save a hash state: %w", err)
		}
		out = binary.BigEndian.AppendUint32(out, uint32(len(b))) //nolint:gosec // a hash state is a few hundred bytes
		out = append(out, b...)
	}
	return out, nil
}

// restoreHashes reads a state [hashes.marshal] wrote.
func restoreHashes(b []byte) (*hashes, error) {
	if len(b) < 8 {
		return nil, errors.New("the saved hash state is too short")
	}
	h := newHashes()
	h.n = int64(binary.BigEndian.Uint64(b)) //nolint:gosec // written from a non-negative count
	b = b[8:]
	for _, f := range h.all() {
		if len(b) < 4 {
			return nil, errors.New("the saved hash state is truncated")
		}
		n := int(binary.BigEndian.Uint32(b))
		b = b[4:]
		if n > len(b) {
			return nil, errors.New("the saved hash state is truncated")
		}
		u, ok := f.(encoding.BinaryUnmarshaler)
		if !ok {
			return nil, errors.New("a hash function of the digest set cannot restore its state")
		}
		if err := u.UnmarshalBinary(b[:n]); err != nil {
			return nil, fmt.Errorf("restore a hash state: %w", err)
		}
		b = b[n:]
	}
	if len(b) != 0 {
		return nil, errors.New("the saved hash state has trailing bytes")
	}
	return h, nil
}

// digests is the digest set of the content hashed so far.
func (h *hashes) digests() blob.Digests {
	var d blob.Digests
	h.sha256.Sum(d.SHA256[:0])
	h.sha1.Sum(d.SHA1[:0])
	h.md5.Sum(d.MD5[:0])
	h.sha512.Sum(d.SHA512[:0])
	d.Size = h.n
	return d
}
