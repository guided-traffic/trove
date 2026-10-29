package blob

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // MD5 is part of the digest set for client compatibility (R-STOR-002)
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is part of the digest set for client compatibility (R-STOR-002)
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

const (
	// copyBuffer is the read size of the hashing pass.
	copyBuffer = 256 << 10
	// growStep is how much budget a body of unknown length reserves at a time.
	growStep = 4 << 20
)

// Spool is the replica-local scratch space of uploads (R-STOR-004): one
// directory on an emptyDir or generic ephemeral volume, a byte budget shared by
// all concurrent uploads of the replica and the single hashing pass. It holds
// the data of running requests only; nothing in it outlives its request.
type Spool struct {
	dir          string
	max          int64
	minFree      int64
	memThreshold int64
	warnPercent  int
	free         func(dir string) (uint64, error)
	log          *slog.Logger
	metrics      *Metrics

	mu       sync.Mutex
	reserved int64
	warnedAt time.Time
}

// SpoolOption configures a Spool.
type SpoolOption func(*Spool)

// WithSpoolLogger sets the logger of the budget warning.
func WithSpoolLogger(l *slog.Logger) SpoolOption { return func(s *Spool) { s.log = l } }

// WithSpoolMetrics sets the metrics.
func WithSpoolMetrics(m *Metrics) SpoolOption { return func(s *Spool) { s.metrics = m } }

// WithFreeBytes replaces the free-space probe of the spool volume; tests use it
// to simulate a full volume.
func WithFreeBytes(f func(dir string) (uint64, error)) SpoolOption {
	return func(s *Spool) { s.free = f }
}

// NewSpool prepares the spool directory and deletes every file in it: no spool
// file survives its request, so whatever a crashed process left is garbage
// (R-STOR-004 step 6).
func NewSpool(cfg config.StorageSpool, opts ...SpoolOption) (*Spool, error) {
	s := &Spool{
		dir: cfg.Dir, max: int64(cfg.MaxBytes), minFree: int64(cfg.MinFreeBytes),
		memThreshold: int64(cfg.MemoryThreshold), warnPercent: cfg.WarnPercent,
		free: freeBytes, log: slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	if s.dir == "" {
		return nil, errors.New("storage.spool.dir is empty")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool directory: %w", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("spool directory: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.dir, e.Name())); err != nil {
			return nil, fmt.Errorf("clean spool directory: %w", err)
		}
	}
	return s, nil
}

// Dir is the spool directory.
func (s *Spool) Dir() string { return s.dir }

// Reserved is the budget held by running uploads.
func (s *Spool) Reserved() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

// reserve takes n bytes of the budget, or fails with ErrSpoolFull when the
// budget or the free space of the volume cannot cover it.
func (s *Spool) reserve(n int64) error {
	free, ferr := s.free(s.dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserved+n > s.max {
		s.metrics.reject("spool_budget")
		return spoolFull("this replica has no spool budget left for the upload")
	}
	if ferr == nil {
		if free < uint64(s.minFree) || free-uint64(s.minFree) < uint64(n) { //nolint:gosec // both are non-negative
			s.metrics.reject("spool_volume")
			return spoolFull("the spool volume of this replica is nearly full")
		}
	}
	s.reserved += n
	s.metrics.setSpool(s.reserved, free)
	if s.max > 0 && s.reserved*100/s.max >= int64(s.warnPercent) && time.Since(s.warnedAt) > time.Minute {
		s.warnedAt = time.Now()
		s.log.Warn("spool budget is above its warning level", "reserved_bytes", s.reserved, "max_bytes", s.max, "warn_percent", s.warnPercent)
	}
	return nil
}

func (s *Spool) release(n int64) {
	if n == 0 {
		return
	}
	s.mu.Lock()
	s.reserved -= n
	r := s.reserved
	s.mu.Unlock()
	s.metrics.setSpoolReserved(r)
}

// create opens a new spool file: mode 0600, named by a random UUID.
func (s *Spool) create() (*os.File, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	name := h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	return os.OpenFile(filepath.Join(s.dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the name is generated here
}

// Expected are the digests a protocol declared; empty ones are not checked.
type Expected struct {
	SHA256 []byte
	SHA512 []byte
	SHA1   []byte
	MD5    []byte
}

// IngestOptions describe one body.
type IngestOptions struct {
	// ContentLength is the declared length, -1 when unknown (chunked).
	ContentLength int64
	// MaxSize is the effective maximum upload size: the smaller of
	// storage.upload.max_size and the repository's limit. Zero means no limit
	// beyond the spool budget.
	MaxSize int64
	// Expect are digests to verify after the body ended.
	Expect Expected
}

// Spooled is a body that was spooled and hashed. The caller owns it and must
// Close it on every path; Close removes the file and returns the budget.
type Spooled struct {
	Digests

	data     []byte // set for a body kept in memory
	file     *os.File
	spool    *Spool
	reserved int64
	closed   bool
}

// InMemory reports whether the body was small enough to stay in memory.
func (s *Spooled) InMemory() bool { return s.file == nil }

// Path is the spool file, empty for a body in memory.
func (s *Spooled) Path() string {
	if s.file == nil {
		return ""
	}
	return s.file.Name()
}

// Reader returns a new reader over the whole content. Readers are independent
// of each other and read the file at their own offset.
func (s *Spooled) Reader() io.ReadSeeker { return s.Section(0, s.Size) }

// Section returns a reader over [off, off+n) of the content.
func (s *Spooled) Section(off, n int64) io.ReadSeeker {
	if s.file == nil {
		return bytes.NewReader(s.data[off : off+n])
	}
	return io.NewSectionReader(s.file, off, n)
}

// Close removes the spool file and returns the reserved budget. It is safe to
// call more than once.
func (s *Spooled) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.file != nil {
		name := s.file.Name()
		err = s.file.Close()
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
		s.file = nil
	}
	s.data = nil
	s.spool.release(s.reserved)
	s.reserved = 0
	return err
}

// hashers is the fan-out of the single pass.
type hashers struct {
	sha256 hash.Hash
	sha1   hash.Hash
	md5    hash.Hash
	sha512 hash.Hash
	n      int64
}

func newHashers() *hashers {
	return &hashers{sha256: sha256.New(), sha1: sha1.New(), md5: md5.New(), sha512: sha512.New()} //nolint:gosec // see imports
}

func (h *hashers) write(p []byte) {
	h.sha256.Write(p)
	h.sha1.Write(p)
	h.md5.Write(p)
	h.sha512.Write(p)
	h.n += int64(len(p))
}

func (h *hashers) sum() Digests {
	var d Digests
	h.sha256.Sum(d.SHA256[:0])
	h.sha1.Sum(d.SHA1[:0])
	h.md5.Sum(d.MD5[:0])
	h.sha512.Sum(d.SHA512[:0])
	d.Size = h.n
	return d
}

var copyBuffers = sync.Pool{New: func() any { b := make([]byte, copyBuffer); return &b }}

// Ingest runs admission, spools the body and hashes it in one pass (R-STOR-004
// steps 1 to 4). Every error leaves nothing behind: the file is removed and the
// budget is returned. The errors are *errs.Error values: 413 for ErrTooLarge,
// 503 with Retry-After for ErrSpoolFull, 400 for ErrLengthMismatch, 409 for
// ErrDigestMismatch.
func (s *Spool) Ingest(ctx context.Context, body io.Reader, o IngestOptions) (out *Spooled, err error) {
	maxSize := o.MaxSize
	if maxSize <= 0 {
		maxSize = int64(^uint64(0) >> 1)
	}
	known := o.ContentLength >= 0
	// Admission, before the body is read.
	if known && o.ContentLength > maxSize {
		s.metrics.reject("too_large")
		return nil, tooLarge(maxSize)
	}
	out = &Spooled{spool: s}
	work := out // the return statements clear out before the deferred function runs
	defer func() {
		if err != nil {
			_ = work.Close()
		}
	}()

	h := newHashers()
	switch {
	case known && o.ContentLength <= s.memThreshold:
		buf := make([]byte, 0, o.ContentLength)
		b := bytes.NewBuffer(buf)
		// One byte more than declared tells a body that is too long.
		n, cerr := io.Copy(b, io.LimitReader(&ctxReader{ctx, body}, o.ContentLength+1))
		if cerr != nil {
			return nil, cerr
		}
		if n != o.ContentLength {
			return nil, lengthMismatch(fmt.Sprintf("the body differs from the declared Content-Length of %d", o.ContentLength))
		}
		h.write(b.Bytes())
		out.data = b.Bytes()
	default:
		if known {
			if err := s.reserve(o.ContentLength); err != nil {
				return nil, err
			}
			out.reserved = o.ContentLength
		}
		f, cerr := s.create()
		if cerr != nil {
			return nil, fmt.Errorf("create spool file: %w", cerr)
		}
		out.file = f
		if err := s.copyHashed(ctx, body, f, h, out, o, known, maxSize); err != nil {
			return nil, err
		}
	}

	out.Digests = h.sum()
	if known && out.Size != o.ContentLength {
		return nil, lengthMismatch(fmt.Sprintf("the body has %d bytes, Content-Length declared %d", out.Size, o.ContentLength))
	}
	if err := o.Expect.check(out.Digests); err != nil {
		s.metrics.reject("digest_mismatch")
		return nil, err
	}
	return out, nil
}

func (s *Spool) copyHashed(ctx context.Context, body io.Reader, f *os.File, h *hashers, out *Spooled, o IngestOptions, known bool, maxSize int64) error {
	bp := copyBuffers.Get().(*[]byte)
	defer copyBuffers.Put(bp)
	buf := *bp
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			next := h.n + int64(n)
			switch {
			case next > maxSize:
				s.metrics.reject("too_large")
				return tooLarge(maxSize)
			case known && next > o.ContentLength:
				return lengthMismatch(fmt.Sprintf("the body is longer than the declared Content-Length of %d", o.ContentLength))
			case !known && next > out.reserved:
				step := int64(growStep)
				if next+step > maxSize {
					step = maxSize - out.reserved
				}
				need := next - out.reserved
				step = max(step, need)
				if err := s.reserve(step); err != nil {
					// The increment does not fit; try just what the body needs now.
					step = need
					if err := s.reserve(step); err != nil {
						return err
					}
				}
				out.reserved += step
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write spool file: %w", werr)
			}
			h.write(buf[:n])
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// ctxReader fails reads once the context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func (e Expected) check(d Digests) error {
	for _, c := range []struct {
		name      string
		want, got []byte
	}{
		{"sha-256", e.SHA256, d.SHA256[:]},
		{"sha-512", e.SHA512, d.SHA512[:]},
		{"sha-1", e.SHA1, d.SHA1[:]},
		{"md5", e.MD5, d.MD5[:]},
	} {
		if len(c.want) > 0 && !bytes.Equal(c.want, c.got) {
			return digestMismatch(c.name)
		}
	}
	return nil
}
