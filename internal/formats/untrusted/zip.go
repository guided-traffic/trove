package untrusted

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

const (
	sigLocal    = 0x04034b50
	sigCentral  = 0x02014b50
	sigEOCD     = 0x06054b50
	sigEOCD64   = 0x06064b50
	sigLocator  = 0x07064b50
	eocdLen     = 22
	locatorLen  = 20
	eocd64Len   = 56
	centralLen  = 46
	localLen    = 30
	maxComment  = 1<<16 - 1
	methodStore = 0
	methodFlate = 8
)

// ZipEntry is an entry of a zip archive as its central directory declares it.
type ZipEntry struct {
	// Name is the name as the archive spells it.
	Name string
	// Size and CompressedSize are the declared sizes.
	Size, CompressedSize int64
	// Dir is true for a directory.
	Dir bool

	index int
}

// ZipReader reads a zip archive within the limits of R-ARCH-028. NewZip has
// already refused what two readers could resolve differently: a central directory
// whose entry count or size is out of bounds, names that the central directory
// and the local headers spell differently, a name used twice, links, encrypted
// entries and methods other than store and deflate. Nothing is extracted: an entry
// name is a label, not a file-system path.
type ZipReader struct {
	ctx     context.Context
	l       Limits
	zr      *zip.Reader
	entries []ZipEntry
	read    int64
}

// NewZip opens the archive in ra, size bytes long. The central directory is read
// and checked before the standard library parses it, so an archive with millions
// of entries costs one bounded scan, not their memory.
func NewZip(ctx context.Context, ra io.ReaderAt, size int64, l Limits) (*ZipReader, error) {
	l = effective(l)
	recs, err := scanCentral(ctx, ra, size, l)
	if err != nil {
		return nil, err
	}
	if err := checkLocal(ctx, ra, size, recs); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, Refused("the content is not a valid zip archive", errs.WithCause(err))
	}
	if len(zr.File) != len(recs) {
		return nil, Refused("the central directory is read differently by two readers")
	}
	z := &ZipReader{ctx: ctx, l: l, zr: zr}
	var declared int64
	for i, f := range zr.File {
		if f.Name != recs[i].name {
			return nil, Refused("the central directory is read differently by two readers")
		}
		switch {
		case f.Flags&0x1 != 0:
			return nil, Refused(fmt.Sprintf("entry %q is encrypted", shorten(f.Name)))
		case f.Method != methodStore && f.Method != methodFlate:
			return nil, Refused(fmt.Sprintf("entry %q uses a compression method other than store and deflate", shorten(f.Name)))
		case f.Mode()&os.ModeSymlink != 0:
			return nil, Refused(fmt.Sprintf("entry %q is a link", shorten(f.Name)))
		case f.UncompressedSize64 > uint64(1)<<62 || f.CompressedSize64 > uint64(1)<<62:
			return nil, Refused(fmt.Sprintf("entry %q declares an impossible size", shorten(f.Name)))
		}
		e := ZipEntry{
			Name: f.Name, Size: int64(f.UncompressedSize64), CompressedSize: int64(f.CompressedSize64),
			Dir: strings.HasSuffix(f.Name, "/"), index: i,
		}
		if !e.Dir {
			if declared += e.Size; declared > l.StreamBytes {
				return nil, TooLarge(fmt.Sprintf("the archive unpacks to more than %d bytes", l.StreamBytes))
			}
		}
		z.entries = append(z.entries, e)
	}
	return z, nil
}

// Entries lists the entries in the order of the central directory.
func (z *ZipReader) Entries() []ZipEntry { return z.entries }

// ReadEntry reads one entry into memory. max is the bound for this entry,
// Limits.ManifestEntryBytes when it is not positive. A declared size above it, or
// above what remains of Limits.UncompressedBytes, or a declared compression ratio
// above Limits.Ratio, fails with TooLarge before a byte is inflated; the inflater
// is cut at the declared size, and a stream that disagrees with its declaration is
// Refused.
func (z *ZipReader) ReadEntry(e ZipEntry, max int64) ([]byte, error) {
	if e.index < 0 || e.index >= len(z.zr.File) || z.entries[e.index].Name != e.Name {
		return nil, Refused("the entry is not one of this archive")
	}
	e = z.entries[e.index] // the declaration of the archive, not the caller's copy
	if e.Dir {
		return nil, Refused("the entry has no content")
	}
	if max <= 0 {
		max = z.l.ManifestEntryBytes
	}
	switch {
	case e.Size > max:
		return nil, TooLarge(fmt.Sprintf("entry %q is larger than %d bytes", shorten(e.Name), max))
	case z.read+e.Size > z.l.UncompressedBytes:
		return nil, TooLarge(fmt.Sprintf("more than %d bytes would be read into memory", z.l.UncompressedBytes))
	case e.Size > int64(z.l.Ratio)*e.CompressedSize+ratioSlack:
		return nil, TooLarge(fmt.Sprintf("entry %q compresses by more than %d:1", shorten(e.Name), z.l.Ratio))
	}
	if err := z.ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := z.zr.File[e.index].Open()
	if err != nil {
		return nil, Refused("the entry cannot be opened", errs.WithCause(err))
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(WithContext(z.ctx, rc), e.Size+1))
	if err != nil {
		if cerr := z.ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, Refused(fmt.Sprintf("entry %q is damaged", shorten(e.Name)), errs.WithCause(err))
	}
	if int64(len(data)) != e.Size {
		return nil, Refused(fmt.Sprintf("entry %q is not as large as its header says", shorten(e.Name)))
	}
	z.read += e.Size
	return data, nil
}

// cdRecord is what the scan of the central directory keeps of one entry.
type cdRecord struct {
	name  string
	local int64
}

var le = binary.LittleEndian

// scanCentral reads the central directory: at most Limits.Entries records, each
// within its bounds, with nothing left over.
func scanCentral(ctx context.Context, ra io.ReaderAt, size int64, l Limits) ([]cdRecord, error) {
	total, cdSize, cdOff, err := endRecord(ra, size)
	if err != nil {
		return nil, err
	}
	switch {
	case total > int64(l.Entries):
		return nil, TooLarge(fmt.Sprintf("the archive holds more than %d entries", l.Entries))
	case cdOff > size || cdSize > size || cdOff > size-cdSize:
		return nil, Refused("the central directory lies outside the archive")
	case cdSize < total*centralLen:
		return nil, Refused("the central directory is smaller than its entries")
	}
	br := bufio.NewReaderSize(io.NewSectionReader(ra, cdOff, cdSize), 32<<10)
	cr := &centralReader{br: br, size: size, names: make(map[string]struct{}, total)}
	recs := make([]cdRecord, 0, total)
	for range total {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec, err := cr.next()
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	if cr.consumed != cdSize {
		return nil, Refused("the central directory holds data its entries do not account for")
	}
	return recs, nil
}

// endRecord finds the end of central directory record, follows the zip64 locator
// when the record says to, and returns the number of entries and the size and the
// offset of the central directory.
func endRecord(ra io.ReaderAt, size int64) (total, cdSize, cdOff int64, err error) {
	if size < eocdLen {
		return 0, 0, 0, Refused("the content is not a zip archive")
	}
	tail := min(size, maxComment+eocdLen)
	buf := make([]byte, tail)
	if _, err := ra.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return 0, 0, 0, err
	}
	at := -1
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if le.Uint32(buf[i:]) == sigEOCD && i+eocdLen+int(le.Uint16(buf[i+20:])) == len(buf) {
			at = i
			break
		}
	}
	if at < 0 {
		return 0, 0, 0, Refused("the content is not a zip archive")
	}
	eocd := buf[at:]
	if le.Uint16(eocd[4:]) != 0 || le.Uint16(eocd[6:]) != 0 {
		return 0, 0, 0, Refused("a zip archive in several parts is not accepted")
	}
	total, cdSize, cdOff = int64(le.Uint16(eocd[10:])), int64(le.Uint32(eocd[12:])), int64(le.Uint32(eocd[16:]))
	if total != 0xFFFF && cdSize != 0xFFFFFFFF && cdOff != 0xFFFFFFFF {
		return total, cdSize, cdOff, nil
	}
	eocdAt := size - tail + int64(at)
	if eocdAt < locatorLen {
		return 0, 0, 0, Refused("the content is not a zip archive")
	}
	var loc [locatorLen]byte
	if _, err := ra.ReadAt(loc[:], eocdAt-locatorLen); err != nil || le.Uint32(loc[:]) != sigLocator {
		return 0, 0, 0, Refused("the content is not a zip archive")
	}
	off, ok := fit(le.Uint64(loc[8:]))
	if !ok || off > size-eocd64Len {
		return 0, 0, 0, Refused("the content is not a zip archive")
	}
	var rec [eocd64Len]byte
	if _, err := ra.ReadAt(rec[:], off); err != nil || le.Uint32(rec[:]) != sigEOCD64 {
		return 0, 0, 0, Refused("the content is not a zip archive")
	}
	var ok1, ok2, ok3 bool
	total, ok1 = fit(le.Uint64(rec[32:]))
	cdSize, ok2 = fit(le.Uint64(rec[40:]))
	cdOff, ok3 = fit(le.Uint64(rec[48:]))
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, Refused("the central directory lies outside the archive")
	}
	return total, cdSize, cdOff, nil
}

// centralReader reads the records of the central directory one by one.
type centralReader struct {
	br       *bufio.Reader
	size     int64
	names    map[string]struct{}
	nameTot  int64
	consumed int64
}

func (c *centralReader) next() (cdRecord, error) {
	damaged := Refused("the central directory is damaged")
	var h [centralLen]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil || le.Uint32(h[:]) != sigCentral {
		return cdRecord{}, damaged
	}
	nameLen, extraLen, commentLen := int(le.Uint16(h[28:])), int(le.Uint16(h[30:])), int(le.Uint16(h[32:]))
	if nameLen == 0 || nameLen > MaxNameBytes {
		return cdRecord{}, Refused("an entry name is empty or longer than " + fmt.Sprint(MaxNameBytes) + " bytes")
	}
	name, extra := make([]byte, nameLen), make([]byte, extraLen)
	if _, err := io.ReadFull(c.br, name); err != nil {
		return cdRecord{}, damaged
	}
	if _, err := io.ReadFull(c.br, extra); err != nil {
		return cdRecord{}, damaged
	}
	if _, err := io.CopyN(io.Discard, c.br, int64(commentLen)); err != nil {
		return cdRecord{}, damaged
	}
	c.consumed += int64(centralLen + nameLen + extraLen + commentLen)
	local := int64(le.Uint32(h[42:]))
	if local == 0xFFFFFFFF {
		v, err := zip64Offset(extra, le.Uint32(h[24:]) == 0xFFFFFFFF, le.Uint32(h[20:]) == 0xFFFFFFFF)
		if err != nil {
			return cdRecord{}, err
		}
		var ok bool
		if local, ok = fit(v); !ok {
			return cdRecord{}, Refused("an entry starts outside the archive")
		}
	}
	if local >= c.size {
		return cdRecord{}, Refused("an entry starts outside the archive")
	}
	key := path.Clean(strings.TrimSuffix(string(name), "/"))
	if _, dup := c.names[key]; dup {
		return cdRecord{}, Refused(fmt.Sprintf("the name %q is used by two entries", shorten(string(name))))
	}
	if c.nameTot += int64(len(key)); c.nameTot > maxNameTotal {
		return cdRecord{}, TooLarge("the entry names are larger than " + fmt.Sprint(maxNameTotal) + " bytes")
	}
	c.names[key] = struct{}{}
	return cdRecord{name: string(name), local: local}, nil
}

// fit converts a 64-bit field to the int64 offsets and sizes the readers use; a
// value above the range is no offset of any archive.
func fit(u uint64) (int64, bool) {
	if u > math.MaxInt64 {
		return 0, false
	}
	return int64(u), true
}

// zip64Offset reads the local header offset out of a zip64 extended information
// extra field. The fields are present, in this order, for the 32-bit fields that
// are all ones: uncompressed size, compressed size, offset.
func zip64Offset(extra []byte, usize, csize bool) (uint64, error) {
	for len(extra) >= 4 {
		id, n := le.Uint16(extra), int(le.Uint16(extra[2:]))
		if 4+n > len(extra) {
			break
		}
		body := extra[4 : 4+n]
		extra = extra[4+n:]
		if id != 0x0001 {
			continue
		}
		skip := 0
		if usize {
			skip += 8
		}
		if csize {
			skip += 8
		}
		if len(body) < skip+8 {
			break
		}
		return le.Uint64(body[skip:]), nil
	}
	return 0, Refused("the central directory is damaged")
}

// checkLocal compares the name of every local header with the name of its central
// directory record: a reader that walks the local headers and one that reads the
// central directory must see the same archive.
func checkLocal(ctx context.Context, ra io.ReaderAt, size int64, recs []cdRecord) error {
	var h [localLen]byte
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.local+localLen > size {
			return Refused("an entry starts outside the archive")
		}
		if _, err := ra.ReadAt(h[:], r.local); err != nil || le.Uint32(h[:]) != sigLocal {
			return Refused("a local header is damaged")
		}
		n := int(le.Uint16(h[26:]))
		if n != len(r.name) || r.local+localLen+int64(n) > size {
			return Refused(fmt.Sprintf("entry %q is named differently in its local header", shorten(r.name)))
		}
		name := make([]byte, n)
		if _, err := ra.ReadAt(name, r.local+localLen); err != nil {
			return Refused("a local header is damaged")
		}
		if string(name) != r.name {
			return Refused(fmt.Sprintf("entry %q is named differently in its local header", shorten(r.name)))
		}
	}
	return nil
}
