package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

// DefaultHoldBack is the length of the tail a verifying reader withholds until the digest matched (R-BAS-021,
// R-REPO-041): the last 64 KiB.
const DefaultHoldBack = 64 << 10

// verifyBufferSize is the buffer of a verifying reader, at least twice the hold-back.
const verifyBufferSize = 256 << 10

// verifyReader serves the bytes of a cached object while it hashes them, and withholds the last hold bytes
// until the body has ended and its SHA-256 and length equal the content address (R-BAS-032, R-STOR-013's
// form). On a mismatch it returns [ErrCorrupt] instead of the held bytes, so the caller aborts the transfer and
// the client never receives a complete corrupted file; onCorrupt then deletes the object. buf[off:ready] is
// cleared and not yet handed out, buf[ready:k] is the withheld tail, which the next refill moves to the front.
type verifyReader struct {
	src       io.ReadCloser
	hash      hash.Hash
	want      string
	size      int64
	hold      int
	onCorrupt func(error)

	buf           []byte
	off, ready, k int
	total         int64
	done          bool  // the source ended and the digest matched
	err           error // sticky
}

func newVerifyReader(src io.ReadCloser, sha string, size int64, hold int, onCorrupt func(error)) *verifyReader {
	hold = max(hold, 0)
	capacity := max(verifyBufferSize, 2*hold, 4096)
	if size < int64(capacity) {
		capacity = int(size) + 1 // the object and the byte that would show it is longer
	}
	capacity = max(capacity, hold+1)
	return &verifyReader{src: src, hash: sha256.New(), want: sha, size: size, hold: hold, onCorrupt: onCorrupt, buf: make([]byte, capacity)}
}

// Read implements io.Reader.
func (v *verifyReader) Read(p []byte) (int, error) {
	for {
		if v.off < v.ready {
			n := copy(p, v.buf[v.off:v.ready])
			v.off += n
			return n, nil
		}
		if v.err != nil {
			return 0, v.err
		}
		if v.done {
			return 0, io.EOF
		}
		v.fill()
	}
}

// WriteTo writes the cleared bytes straight from the buffer.
func (v *verifyReader) WriteTo(w io.Writer) (int64, error) {
	var written int64
	for {
		if v.off < v.ready {
			n, err := w.Write(v.buf[v.off:v.ready])
			v.off += n
			written += int64(n)
			if err != nil {
				return written, err
			}
			continue
		}
		if v.err != nil {
			return written, v.err
		}
		if v.done {
			return written, nil
		}
		v.fill()
	}
}

// fill moves the withheld tail to the front and reads until the buffer is full or the source ended. Then
// everything but the last hold bytes is cleared, or everything once the digest matched. A body longer than the
// object is a mismatch at once: nothing more is cleared and nothing more is read.
func (v *verifyReader) fill() {
	v.k = copy(v.buf, v.buf[v.ready:v.k])
	v.off, v.ready = 0, 0
	for v.k < len(v.buf) {
		n, err := v.src.Read(v.buf[v.k:])
		if n > 0 {
			v.hash.Write(v.buf[v.k : v.k+n])
			v.total += int64(n)
			v.k += n
		}
		if v.total > v.size {
			v.k = 0
			v.err = v.finish()
			return
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			if v.err = v.finish(); v.err == nil {
				v.ready, v.done = v.k, true
			}
			return
		default:
			v.err = err // a transport error: abort, the object is not judged
			return
		}
	}
	v.ready = max(0, v.k-v.hold)
}

// finish compares the body with the content address.
func (v *verifyReader) finish() error {
	got := hex.EncodeToString(v.hash.Sum(nil))
	if v.total == v.size && got == v.want {
		return nil
	}
	err := fmt.Errorf("%w: read %d bytes with SHA-256 %s, expected %d bytes with %s", ErrCorrupt, v.total, got, v.size, v.want)
	if v.onCorrupt != nil {
		v.onCorrupt(err)
		v.onCorrupt = nil
	}
	return err
}

// Close closes the source.
func (v *verifyReader) Close() error {
	v.off, v.ready, v.k = 0, 0, 0
	if v.err == nil {
		v.err = errors.New("cache: read after close")
	}
	return v.src.Close()
}
