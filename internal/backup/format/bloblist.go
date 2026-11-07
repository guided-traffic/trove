package format

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// BlobEntry is one line of a blob list: a digest and the blob's size in bytes (R-BAK-002).
type BlobEntry struct {
	Digest Digest
	Size   int64
	// Status is the blobs.content_status of the row the entry was read from in the snapshot, "missing" or "corrupt" for a
	// damaged blob; empty means ok. It is not part of a blob list line (R-BAK-006 "Source damage").
	Status string
}

// BlobIterator is a sorted stream of blob entries, ascending by digest without duplicates. Next returns io.EOF after the
// last entry. The streams of the lane (the referenced set from the snapshot, a blob list from the target) never fit in
// memory, so everything that merges them works on this interface (R-BAK-006).
type BlobIterator interface {
	Next() (BlobEntry, error)
}

// SumWriter hashes and counts what passes through it. A table file and the blob list stream through one, so the manifest's
// sha256, bytes and compressedBytes are computed while the file is uploaded (R-BAK-003).
type SumWriter struct {
	w io.Writer
	h hash.Hash
	n int64
}

// NewSumWriter wraps w.
func NewSumWriter(w io.Writer) *SumWriter { return &SumWriter{w: w, h: sha256.New()} }

// Write implements io.Writer.
func (s *SumWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	s.h.Write(p[:n])
	s.n += int64(n)
	return n, err
}

// SHA256 is the lowercase hex digest of what was written so far.
func (s *SumWriter) SHA256() string { return hex.EncodeToString(s.h.Sum(nil)) }

// Bytes is the number of bytes written so far.
func (s *SumWriter) Bytes() int64 { return s.n }

// BlobListWriter writes a blob list: zstd-compressed UTF-8 text, one line `<64 lowercase hex> <size>\n` per blob, ascending
// by digest, without duplicates. It refuses an entry that breaks the order, so a bug in a producer cannot leave a list
// that the sweep and the diff would misread.
type BlobListWriter struct {
	zw   *zstd.Encoder
	last Digest
	have bool
	n    int64
	size int64
	line [64 + 1 + 20 + 1]byte
}

// blobListWindow is the zstd window of a blob list. A decoder allocates its history from the frame's window, and the sweep and
// verify --all hold one decoder per retained backup, so the writer keeps the window small instead of the default of its level
// (8 MiB at level 3, which cost about 10 MiB per open reader). Sorted hex lines gain nothing from a longer window.
const blobListWindow = 1 << 20

// NewBlobListWriter compresses into w at the given zstd level (backup.compression_level, 1 to 19).
func NewBlobListWriter(w io.Writer, level int) (*BlobListWriter, error) {
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)), zstd.WithWindowSize(blobListWindow))
	if err != nil {
		return nil, err
	}
	return &BlobListWriter{zw: zw}, nil
}

// Add appends an entry.
func (b *BlobListWriter) Add(e BlobEntry) error {
	if e.Size < 0 {
		return fmt.Errorf("blob list: negative size for %s", e.Digest.Hex())
	}
	if b.have && !b.last.Less(e.Digest) {
		return fmt.Errorf("blob list: %s does not follow %s: entries must ascend without duplicates", e.Digest.Hex(), b.last.Hex())
	}
	buf := append(b.line[:0], e.Digest.Hex()...)
	buf = append(buf, ' ')
	buf = strconv.AppendInt(buf, e.Size, 10)
	buf = append(buf, '\n')
	if _, err := b.zw.Write(buf); err != nil {
		return err
	}
	b.last, b.have = e.Digest, true
	b.n++
	b.size += e.Size
	return nil
}

// Count is the number of entries and their total size so far.
func (b *BlobListWriter) Count() (entries, bytes int64) { return b.n, b.size }

// Close flushes the compressor. It does not close the underlying writer.
func (b *BlobListWriter) Close() error { return b.zw.Close() }

// BlobListReader reads a blob list and verifies its order while it reads.
type BlobListReader struct {
	zr   *zstd.Decoder
	sc   *bufio.Scanner
	last Digest
	have bool
}

// NewBlobListReader decompresses r.
func NewBlobListReader(r io.Reader) (*BlobListReader, error) {
	zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxWindow(64<<20))
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 256), 1024)
	return &BlobListReader{zr: zr, sc: sc}, nil
}

// Next implements BlobIterator.
func (b *BlobListReader) Next() (BlobEntry, error) {
	if !b.sc.Scan() {
		if err := b.sc.Err(); err != nil {
			return BlobEntry{}, err
		}
		return BlobEntry{}, io.EOF
	}
	digest, size, ok := strings.Cut(b.sc.Text(), " ")
	if !ok {
		return BlobEntry{}, fmt.Errorf("blob list: line %q has no size", b.sc.Text())
	}
	d, err := ParseDigest(digest)
	if err != nil {
		return BlobEntry{}, fmt.Errorf("blob list: %w", err)
	}
	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil || n < 0 {
		return BlobEntry{}, fmt.Errorf("blob list: size %q of %s is not a size", size, digest)
	}
	if b.have && !b.last.Less(d) {
		return BlobEntry{}, fmt.Errorf("blob list: %s does not follow %s: entries must ascend without duplicates", digest, b.last.Hex())
	}
	b.last, b.have = d, true
	return BlobEntry{Digest: d, Size: n}, nil
}

// Close releases the decompressor.
func (b *BlobListReader) Close() { b.zr.Close() }

// EntryList is an in-memory BlobIterator, for tests and for small sets.
type EntryList struct {
	Entries []BlobEntry
	i       int
}

// Next implements BlobIterator.
func (l *EntryList) Next() (BlobEntry, error) {
	if l.i >= len(l.Entries) {
		return BlobEntry{}, io.EOF
	}
	e := l.Entries[l.i]
	l.i++
	return e, nil
}

// Without wraps it so that the digests in skip (lowercase hex) are left out: the previous list minus its blobs.damaged,
// which is what is known to be in the pool (R-BAK-006 step 1, R-BAK-017).
func Without(it BlobIterator, skip map[string]bool) BlobIterator { return &without{it: it, skip: skip} }

type without struct {
	it   BlobIterator
	skip map[string]bool
}

func (w *without) Next() (BlobEntry, error) {
	for {
		e, err := w.it.Next()
		if err != nil || !w.skip[e.Digest.Hex()] {
			return e, err
		}
	}
}

// Diff is the one-pass merge of R-BAK-006 step 2. referenced is the sorted referenced set from the snapshot; known is the
// sorted set already in the pool (the previous list minus its damaged blobs). Every referenced entry goes to list, the
// new blob list; the ones that known lacks also go to toCopy. Both streams are read once, in bounded memory, and an input
// that is out of order is an error. Either callback may be nil.
func Diff(referenced, known BlobIterator, list, toCopy func(BlobEntry) error) error {
	var k BlobEntry
	kok := false
	advance := func() error {
		e, err := known.Next()
		switch {
		case errors.Is(err, io.EOF):
			kok = false
			return nil
		case err != nil:
			return err
		}
		k, kok = e, true
		return nil
	}
	if err := advance(); err != nil {
		return err
	}
	var prev Digest
	havePrev := false
	for {
		r, err := referenced.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if havePrev && !prev.Less(r.Digest) {
			return fmt.Errorf("referenced set: %s does not follow %s: entries must ascend without duplicates", r.Digest.Hex(), prev.Hex())
		}
		prev, havePrev = r.Digest, true
		for kok && k.Digest.Less(r.Digest) {
			if err := advance(); err != nil {
				return err
			}
		}
		if list != nil {
			if err := list(r); err != nil {
				return err
			}
		}
		if kok && k.Digest == r.Digest {
			continue
		}
		if toCopy != nil {
			if err := toCopy(r); err != nil {
				return err
			}
		}
	}
}

// Union merges sorted streams into one sorted stream without duplicates: the keep set of the pool sweep, the blob lists of
// the remaining complete backups (R-BAK-017). The number of streams is the number of retained backups, so the merge scans
// their heads instead of keeping a heap.
func Union(its ...BlobIterator) BlobIterator {
	u := &union{its: its, head: make([]BlobEntry, len(its)), ok: make([]bool, len(its))}
	u.primed = false
	return u
}

type union struct {
	its    []BlobIterator
	head   []BlobEntry
	ok     []bool
	primed bool
	last   Digest
	have   bool
}

func (u *union) fill(i int) error {
	e, err := u.its[i].Next()
	switch {
	case errors.Is(err, io.EOF):
		u.ok[i] = false
		return nil
	case err != nil:
		return err
	}
	u.head[i], u.ok[i] = e, true
	return nil
}

func (u *union) Next() (BlobEntry, error) {
	if !u.primed {
		for i := range u.its {
			if err := u.fill(i); err != nil {
				return BlobEntry{}, err
			}
		}
		u.primed = true
	}
	for {
		best := -1
		for i := range u.its {
			if u.ok[i] && (best < 0 || u.head[i].Digest.Less(u.head[best].Digest)) {
				best = i
			}
		}
		if best < 0 {
			return BlobEntry{}, io.EOF
		}
		e := u.head[best]
		if err := u.fill(best); err != nil {
			return BlobEntry{}, err
		}
		if u.have && u.last == e.Digest {
			continue
		}
		u.last, u.have = e.Digest, true
		return e, nil
	}
}

// ReadAll collects an iterator, for tests and small lists.
func ReadAll(it BlobIterator) ([]BlobEntry, error) {
	var out []BlobEntry
	for {
		e, err := it.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
}

// EncodeBlobList is the whole-list form of the writer, for tests and small lists.
func EncodeBlobList(entries []BlobEntry, level int) ([]byte, error) {
	var buf bytes.Buffer
	w, err := NewBlobListWriter(&buf, level)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := w.Add(e); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
