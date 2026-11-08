package blob

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 payload checksum for stores without additional checksums (R-STOR-006)
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// IngestSegment reads at most n bytes into the spool for one part of a multi-request upload (R-STOR-011 step 3): first the bytes of
// prefix, which are in the hash state of the session already, then bytes of body, each of which is passed to tee before it is
// kept, so that the session's hash functions see every new byte exactly once. Segments below the memory threshold stay in memory;
// larger ones are a spool file whose budget is reserved as the bytes arrive, so a segment occupies at most its own size, whatever
// the size of the chunk the client sends.
//
// The result carries Size and the SHA-256 and MD5 of the segment (the payload checksums of R-STOR-006); its other digests are
// not computed. A segment shorter than n means both sources ended. The caller closes the result on every path; on an error
// nothing is left behind, and an error of tee or of a read is returned as it is.
func (s *Spool) IngestSegment(ctx context.Context, prefix, body io.Reader, n int64, tee func(p []byte) error) (out *Spooled, err error) {
	out = &Spooled{spool: s}
	work := out
	defer func() {
		if err != nil {
			_ = work.Close()
		}
	}()
	h256, hmd5 := sha256.New(), md5.New() //nolint:gosec // see imports
	var mem bytes.Buffer
	var written int64

	put := func(p []byte) error {
		if out.file == nil && int64(mem.Len()+len(p)) > s.memThreshold {
			f, cerr := s.create()
			if cerr != nil {
				return fmt.Errorf("create spool file: %w", cerr)
			}
			out.file = f
			if err := s.grow(out, int64(mem.Len()+len(p)), n); err != nil {
				return err
			}
			if _, werr := f.Write(mem.Bytes()); werr != nil {
				return fmt.Errorf("write spool file: %w", werr)
			}
			mem = bytes.Buffer{}
		}
		if out.file == nil {
			mem.Write(p)
		} else {
			if err := s.grow(out, written+int64(len(p)), n); err != nil {
				return err
			}
			if _, werr := out.file.Write(p); werr != nil {
				return fmt.Errorf("write spool file: %w", werr)
			}
		}
		h256.Write(p)
		hmd5.Write(p)
		written += int64(len(p))
		return nil
	}

	bp := copyBuffers.Get().(*[]byte)
	defer copyBuffers.Put(bp)
	buf := *bp
	for _, src := range []struct {
		r     io.Reader
		fresh bool
	}{{prefix, false}, {body, true}} {
		if src.r == nil {
			continue
		}
		for written < n {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			want := buf
			if left := n - written; int64(len(want)) > left {
				want = want[:left]
			}
			m, rerr := src.r.Read(want)
			if m > 0 {
				if src.fresh && tee != nil {
					if err := tee(want[:m]); err != nil {
						return nil, err
					}
				}
				if err := put(want[:m]); err != nil {
					return nil, err
				}
			}
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				return nil, rerr
			}
		}
	}
	if out.file == nil {
		out.data = mem.Bytes()
	}
	out.Size = written
	h256.Sum(out.SHA256[:0])
	hmd5.Sum(out.MD5[:0])
	return out, nil
}

// grow makes the reservation of a spooled segment cover need bytes, in steps, never beyond the n bytes the segment may take.
func (s *Spool) grow(out *Spooled, need, n int64) error {
	if need <= out.reserved {
		return nil
	}
	step := max(int64(growStep), need-out.reserved)
	if out.reserved+step > n {
		step = max(n-out.reserved, need-out.reserved)
	}
	if err := s.reserve(step); err != nil {
		step = need - out.reserved
		if err := s.reserve(step); err != nil {
			return err
		}
	}
	out.reserved += step
	return nil
}
