package untrusted

import (
	"archive/tar"
	"bufio"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

const (
	// ratioSlack is added to the allowance of the ratio check: the inflater reads
	// ahead, so a small entry may have been paid for before its header.
	ratioSlack = 128 << 10
	// maxGzipHeaderBytes bounds the optional name, comment and extra fields.
	maxGzipHeaderBytes = 64 << 10
	// entryOverhead is what the inflated-bytes cap allows per entry beyond its
	// content: header blocks, padding and extended headers.
	entryOverhead = 2048
	// MaxNameBytes bounds one entry name.
	MaxNameBytes = 4096
	// maxNameTotal bounds the bookkeeping of the names a reader keeps to find
	// duplicates.
	maxNameTotal = 16 << 20
)

// Entry is an archive entry a reader hands out: a regular file or a directory.
type Entry struct {
	// Name is the name as the archive spells it.
	Name string
	// Size is the size the header declares.
	Size int64
	// Dir is true for a directory.
	Dir bool
}

// TarReader reads a gzip-compressed tar archive within the limits of R-ARCH-028.
// It refuses what two readers could resolve differently: a name used twice (a
// directory's trailing slash does not make a second name), a link, a device, a
// sparse file and data after the archive's end. It never writes anything: an
// entry name is a label, not a file-system path.
type TarReader struct {
	ctx context.Context
	l   Limits
	r   io.Reader

	st  *state
	cr  *countReader
	inf *inflatedReader
	br  *bufio.Reader
	tr  *tar.Reader

	started bool
	done    bool
	entries int
	regular int
	names   map[string]struct{}
	nameTot int64

	declared int64 // sum of the declared sizes of regular files
	read     int64 // bytes handed out by Read, all entries
	cur      *Entry
	curRead  int64
	curStart int64 // compressed bytes consumed when the current entry began
}

// NewTarGz reads the tar.gz archive in r. Nothing is read before the first Next.
// ctx carries the deadline (Limits.Context).
func NewTarGz(ctx context.Context, r io.Reader, l Limits) *TarReader {
	return newTarGz(ctx, r, effective(l))
}

func newTarGz(ctx context.Context, r io.Reader, l Limits) *TarReader {
	return &TarReader{ctx: ctx, l: l, r: r, st: &state{ctx: ctx}, names: map[string]struct{}{}}
}

// Next advances to the next entry. It returns io.EOF after the last one, once the
// bytes after the archive's end have been checked.
func (t *TarReader) Next() (Entry, error) {
	if t.done {
		return Entry{}, io.EOF
	}
	if !t.started {
		if err := t.start(); err != nil {
			return Entry{}, err
		}
	}
	hdr, err := t.tr.Next()
	if errors.Is(err, io.EOF) {
		if err := t.finish(); err != nil {
			return Entry{}, t.classify(err)
		}
		t.done = true
		return Entry{}, io.EOF
	}
	if err != nil {
		return Entry{}, t.classify(err)
	}
	e, err := t.admit(hdr)
	if err != nil {
		return Entry{}, err
	}
	t.cur, t.curRead, t.curStart = &e, 0, t.cr.n
	return e, nil
}

func (t *TarReader) start() error {
	t.started = true
	t.br = bufio.NewReaderSize(t.r, 32<<10)
	t.cr = &countReader{br: t.br, st: t.st, guard: maxGzipHeaderBytes}
	zr, err := gzip.NewReader(t.cr)
	if err != nil {
		return t.classify(err)
	}
	t.cr.guard = 0
	zr.Multistream(false)
	limit := t.l.StreamBytes + int64(t.l.Entries)*entryOverhead + 1<<20
	t.inf = &inflatedReader{r: zr, st: t.st, limit: limit}
	t.tr = tar.NewReader(t.inf)
	return nil
}

// admit applies the entry rules to a header.
func (t *TarReader) admit(h *tar.Header) (Entry, error) {
	t.entries++
	if t.entries > t.l.Entries {
		return Entry{}, TooLarge(fmt.Sprintf("the archive holds more than %d entries", t.l.Entries))
	}
	var dir bool
	switch h.Typeflag {
	case tar.TypeReg:
	case tar.TypeDir:
		dir = true
	case tar.TypeSymlink, tar.TypeLink:
		return Entry{}, Refused(fmt.Sprintf("entry %q is a link", shorten(h.Name)))
	default:
		return Entry{}, Refused(fmt.Sprintf("entry %q has an unsupported type (%q)", shorten(h.Name), string(h.Typeflag)))
	}
	for k := range h.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return Entry{}, Refused(fmt.Sprintf("entry %q is a sparse file", shorten(h.Name)))
		}
	}
	if h.Name == "" || len(h.Name) > MaxNameBytes {
		return Entry{}, Refused("an entry name is empty or longer than " + fmt.Sprint(MaxNameBytes) + " bytes")
	}
	key := path.Clean(strings.TrimSuffix(h.Name, "/"))
	if _, dup := t.names[key]; dup {
		return Entry{}, Refused(fmt.Sprintf("the name %q is used by two entries", shorten(h.Name)))
	}
	if t.nameTot += int64(len(key)); t.nameTot > maxNameTotal {
		return Entry{}, TooLarge("the entry names are larger than " + fmt.Sprint(maxNameTotal) + " bytes")
	}
	t.names[key] = struct{}{}
	if h.Size < 0 {
		return Entry{}, Refused(fmt.Sprintf("entry %q has a negative size", shorten(h.Name)))
	}
	if !dir {
		t.regular++
		if t.declared += h.Size; t.declared > t.l.StreamBytes || t.declared < 0 {
			return Entry{}, TooLarge(fmt.Sprintf("the archive unpacks to more than %d bytes", t.l.StreamBytes))
		}
	}
	return Entry{Name: h.Name, Size: h.Size, Dir: dir}, nil
}

// Read reads the content of the current entry. The bytes count against
// Limits.UncompressedBytes, and an entry that shows a compression ratio above
// Limits.Ratio fails with TooLarge.
func (t *TarReader) Read(p []byte) (int, error) {
	if t.cur == nil || t.cur.Dir {
		return 0, io.EOF
	}
	n, err := t.tr.Read(p)
	t.read += int64(n)
	t.curRead += int64(n)
	switch {
	case t.read > t.l.UncompressedBytes:
		return n, TooLarge(fmt.Sprintf("more than %d bytes were read into memory", t.l.UncompressedBytes))
	case t.curRead > int64(t.l.Ratio)*(t.cr.n-t.curStart)+ratioSlack:
		return n, TooLarge(fmt.Sprintf("entry %q compresses by more than %d:1", shorten(t.cur.Name), t.l.Ratio))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, t.classify(err)
	}
	return n, err
}

// ReadEntry reads the whole current entry into memory. max is the bound for this
// entry, Limits.ManifestEntryBytes when it is not positive; a declared size above
// it, or above what remains of Limits.UncompressedBytes, fails before a byte is
// read.
func (t *TarReader) ReadEntry(max int64) ([]byte, error) {
	if t.cur == nil || t.cur.Dir {
		return nil, Refused("the current entry has no content")
	}
	if max <= 0 {
		max = t.l.ManifestEntryBytes
	}
	if t.cur.Size > max {
		return nil, TooLarge(fmt.Sprintf("entry %q is larger than %d bytes", shorten(t.cur.Name), max))
	}
	if t.read+t.cur.Size > t.l.UncompressedBytes {
		return nil, TooLarge(fmt.Sprintf("more than %d bytes would be read into memory", t.l.UncompressedBytes))
	}
	data := make([]byte, t.cur.Size)
	if _, err := io.ReadFull(t, data); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, Refused("the archive ends inside an entry")
		}
		return nil, err
	}
	return data, nil
}

// Entries is the number of entries seen so far.
func (t *TarReader) Entries() int { return t.entries }

// Files is the number of regular files seen so far.
func (t *TarReader) Files() int { return t.regular }

// Declared is the sum of the declared sizes of the regular files seen so far.
func (t *TarReader) Declared() int64 { return t.declared }

// finish checks what follows the archive: zero padding only, then the end of the
// gzip stream (its checksum is verified at that point), and no second member.
func (t *TarReader) finish() error {
	buf := make([]byte, 32<<10)
	for {
		n, err := t.inf.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return Refused("the archive holds data after its end")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if _, err := t.br.Peek(1); err == nil {
		return Refused("the archive holds data after its gzip stream")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// classify turns what the layers returned into the error of the reader.
func (t *TarReader) classify(err error) error {
	if t.st.err != nil {
		return t.st.err
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	var corrupt flate.CorruptInputError
	switch {
	case errors.Is(err, gzip.ErrHeader), errors.Is(err, gzip.ErrChecksum), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, io.EOF), errors.Is(err, tar.ErrHeader), errors.Is(err, tar.ErrFieldTooLong),
		errors.As(err, &corrupt):
		return Refused("the content is not a valid gzip-compressed tar archive", errs.WithCause(err))
	}
	return fmt.Errorf("read archive: %w", err)
}

// CountTarGz is the streaming pass R-ARCH-028 allows a format: it counts the
// regular files of a tar.gz archive and sums their sizes without keeping any
// content, for example npm's dist.fileCount and dist.unpackedSize. The limits are
// the caller's own and finite: more than entries entries, or more than maxBytes
// bytes declared or inflated, fail with TooLarge. The pass shares the deadline of
// ctx.
func CountTarGz(ctx context.Context, r io.Reader, entries int, maxBytes int64) (files int, total int64, err error) {
	t := newTarGz(ctx, r, Limits{Entries: entries, StreamBytes: maxBytes, UncompressedBytes: 1, Ratio: DefaultLimits().Ratio})
	for {
		if _, err := t.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				return t.Files(), t.Declared(), nil
			}
			return 0, 0, err
		}
	}
}

// shorten keeps an attacker-chosen name from filling an error message.
func shorten(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// state carries the first error one of the stream layers raised, so it survives
// being wrapped or swallowed by the gzip and tar readers above it.
type state struct {
	ctx context.Context
	err error
}

func (s *state) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// countReader counts the compressed bytes the inflater consumed. It is an
// io.ByteReader, so the gzip reader adds no buffering of its own and the count
// stays exact. With a guard set it refuses to pass more than that many bytes.
type countReader struct {
	br    *bufio.Reader
	st    *state
	n     int64
	guard int64
}

func (c *countReader) Read(p []byte) (int, error) {
	if err := c.st.ctx.Err(); err != nil {
		return 0, c.st.fail(fmt.Errorf("read archive: %w", err))
	}
	n, err := c.br.Read(p)
	c.n += int64(n)
	if c.guard > 0 && c.n > c.guard {
		return 0, c.st.fail(Refused("the gzip header is too large"))
	}
	return n, err
}

func (c *countReader) ReadByte() (byte, error) {
	b, err := c.br.ReadByte()
	if err == nil {
		c.n++
		if c.guard > 0 && c.n > c.guard {
			return 0, c.st.fail(Refused("the gzip header is too large"))
		}
	}
	return b, err
}

// inflatedReader caps the inflated bytes: a stream that expands beyond the cap
// ends in 413 however it is built.
type inflatedReader struct {
	r     io.Reader
	st    *state
	n     int64
	limit int64
}

func (i *inflatedReader) Read(p []byte) (int, error) {
	if i.st.err != nil {
		return 0, i.st.err
	}
	if left := i.limit - i.n; left < int64(len(p)) {
		p = p[:max(left, 0)+1]
	}
	n, err := i.r.Read(p)
	i.n += int64(n)
	if i.n > i.limit {
		return 0, i.st.fail(TooLarge(fmt.Sprintf("the archive expands beyond %d bytes", i.limit)))
	}
	return n, err
}
