package nupkg

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
)

const (
	sigLocal   = 0x04034b50
	sigCentral = 0x02014b50
	sigEOCD    = 0x06054b50
	sigEOCD64  = 0x06064b50
	sigLocator = 0x07064b50
	eocdLen    = 22
	locatorLen = 20
	eocd64Len  = 56
	centralLen = 46
	localLen   = 30
	maxComment = 1<<16 - 1
)

var le = binary.LittleEndian

// record is what the scan keeps of one central-directory entry.
type record struct {
	name          string
	flags, method uint16
	usize, csize  int64
	local         int64
}

// directory is the result of the scan: the records and where the central
// directory starts.
type directory struct {
	recs []record
	cdAt int64
}

// scan reads the central directory with its own bounded pass before the
// standard library parses the archive, so that an archive that declares
// millions of entries costs one bounded scan and not their memory. It refuses a
// split archive, an encrypted entry, entries outside the archive and
// overlapping entry data. Names are checked by the caller.
func scan(ctx context.Context, ra io.ReaderAt, size int64, lim Limits) (*directory, error) {
	total, cdSize, cdOff, err := endRecord(ra, size)
	if err != nil {
		return nil, err
	}
	switch {
	case total > int64(lim.MaxEntries):
		return nil, invalid(ErrLimit, fmt.Sprintf("the archive holds more than %d entries", lim.MaxEntries))
	case cdSize > maxCentralBytes:
		return nil, invalid(ErrLimit, fmt.Sprintf("the central directory is larger than %d bytes", int64(maxCentralBytes)))
	case cdOff > size || cdSize > size || cdOff > size-cdSize:
		return nil, invalid(ErrNotZip, "the central directory lies outside the archive")
	case cdSize < total*centralLen:
		return nil, invalid(ErrNotZip, "the central directory is smaller than its entries")
	}
	br := bufio.NewReaderSize(io.NewSectionReader(ra, cdOff, cdSize), 32<<10)
	cr := &centralReader{br: br, size: size}
	recs := make([]record, 0, total)
	var declared int64
	for range total {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := cr.next()
		if err != nil {
			return nil, err
		}
		if declared += r.usize; declared > lim.MaxUncompressedBytes || declared < 0 {
			return nil, invalid(ErrLimit, fmt.Sprintf("the archive unpacks to more than %d bytes", lim.MaxUncompressedBytes))
		}
		recs = append(recs, r)
	}
	if cr.consumed != cdSize {
		return nil, invalid(ErrNotZip, "the central directory holds data its entries do not account for")
	}
	if err := checkLocal(ctx, ra, size, cdOff, recs); err != nil {
		return nil, err
	}
	return &directory{recs: recs, cdAt: cdOff}, nil
}

// endRecord finds the end of central directory record, follows the zip64
// locator when the record is saturated and one is present, and returns the
// number of entries and the size and offset of the central directory.
func endRecord(ra io.ReaderAt, size int64) (total, cdSize, cdOff int64, err error) {
	notZip := func() (int64, int64, int64, error) {
		return 0, 0, 0, invalid(ErrNotZip, "the content is not a ZIP archive")
	}
	if size < eocdLen {
		return notZip()
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
		return notZip()
	}
	eocd := buf[at:]
	if le.Uint16(eocd[4:]) != 0 || le.Uint16(eocd[6:]) != 0 || le.Uint16(eocd[8:]) != le.Uint16(eocd[10:]) {
		return 0, 0, 0, invalid(ErrMultiDisk, "a ZIP archive in several parts is not accepted")
	}
	total, cdSize, cdOff = int64(le.Uint16(eocd[10:])), int64(le.Uint32(eocd[12:])), int64(le.Uint32(eocd[16:]))
	if total != 0xFFFF && cdSize != 0xFFFFFFFF && cdOff != 0xFFFFFFFF {
		return total, cdSize, cdOff, nil
	}
	// a saturated field announces zip64 unless no locator precedes the record
	eocdAt := size - tail + int64(at)
	if eocdAt < locatorLen {
		return total, cdSize, cdOff, nil
	}
	var loc [locatorLen]byte
	if _, err := ra.ReadAt(loc[:], eocdAt-locatorLen); err != nil {
		return 0, 0, 0, err
	}
	if le.Uint32(loc[:]) != sigLocator {
		return total, cdSize, cdOff, nil
	}
	if le.Uint32(loc[4:]) != 0 || le.Uint32(loc[16:]) != 1 {
		return 0, 0, 0, invalid(ErrMultiDisk, "a ZIP archive in several parts is not accepted")
	}
	off, ok := fit(le.Uint64(loc[8:]))
	if !ok || off > size-eocd64Len {
		return notZip()
	}
	var rec [eocd64Len]byte
	if _, err := ra.ReadAt(rec[:], off); err != nil || le.Uint32(rec[:]) != sigEOCD64 {
		return notZip()
	}
	if le.Uint32(rec[16:]) != 0 || le.Uint32(rec[20:]) != 0 || le.Uint64(rec[24:]) != le.Uint64(rec[32:]) {
		return 0, 0, 0, invalid(ErrMultiDisk, "a ZIP archive in several parts is not accepted")
	}
	var ok1, ok2, ok3 bool
	total, ok1 = fit(le.Uint64(rec[32:]))
	cdSize, ok2 = fit(le.Uint64(rec[40:]))
	cdOff, ok3 = fit(le.Uint64(rec[48:]))
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, invalid(ErrNotZip, "the central directory lies outside the archive")
	}
	return total, cdSize, cdOff, nil
}

// fit converts a 64-bit field to the int64 the readers use; a value above the
// range is no size or offset of any archive.
func fit(u uint64) (int64, bool) {
	if u > math.MaxInt64 {
		return 0, false
	}
	return int64(u), true
}

// centralReader reads the records of the central directory one by one.
type centralReader struct {
	br       *bufio.Reader
	size     int64
	nameTot  int64
	consumed int64
}

func (c *centralReader) next() (record, error) {
	damaged := invalid(ErrNotZip, "the central directory is damaged")
	var h [centralLen]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil || le.Uint32(h[:]) != sigCentral {
		return record{}, damaged
	}
	nameLen, extraLen, commentLen := int(le.Uint16(h[28:])), int(le.Uint16(h[30:])), int(le.Uint16(h[32:]))
	if nameLen == 0 || nameLen > MaxNameBytes {
		return record{}, invalidName(fmt.Sprintf("an entry name is empty or longer than %d bytes", MaxNameBytes))
	}
	name, extra := make([]byte, nameLen), make([]byte, extraLen)
	if _, err := io.ReadFull(c.br, name); err != nil {
		return record{}, damaged
	}
	if _, err := io.ReadFull(c.br, extra); err != nil {
		return record{}, damaged
	}
	if _, err := io.CopyN(io.Discard, c.br, int64(commentLen)); err != nil {
		return record{}, damaged
	}
	c.consumed += int64(centralLen + nameLen + extraLen + commentLen)
	if c.nameTot += int64(nameLen); c.nameTot > maxNameTotal {
		return record{}, invalid(ErrLimit, fmt.Sprintf("the entry names are larger than %d bytes", int64(maxNameTotal)))
	}
	r := record{name: string(name), flags: le.Uint16(h[8:]), method: le.Uint16(h[10:])}
	if r.flags&0x1 != 0 || r.flags&0x40 != 0 {
		return record{}, invalid(ErrEncrypted, fmt.Sprintf("entry %q is encrypted", shorten(r.name)))
	}
	if le.Uint16(h[34:]) != 0 {
		return record{}, invalid(ErrMultiDisk, "a ZIP archive in several parts is not accepted")
	}
	usize, csize, local := uint64(le.Uint32(h[24:])), uint64(le.Uint32(h[20:])), uint64(le.Uint32(h[42:]))
	needU, needC, needO := usize == 0xFFFFFFFF, csize == 0xFFFFFFFF, local == 0xFFFFFFFF
	if needU || needC || needO {
		var err error
		if usize, csize, local, err = zip64Fields(extra, needU, needC, needO, usize, csize, local); err != nil {
			return record{}, err
		}
	}
	var ok1, ok2, ok3 bool
	r.usize, ok1 = fit(usize)
	r.csize, ok2 = fit(csize)
	r.local, ok3 = fit(local)
	if !ok1 || !ok2 || !ok3 || r.local >= c.size {
		return record{}, invalid(ErrNotZip, fmt.Sprintf("entry %q declares sizes or an offset outside the archive", shorten(r.name)))
	}
	return r, nil
}

// zip64Fields reads the extended information extra field: the fields it holds
// are, in this order, those of the uncompressed size, the compressed size and
// the local header offset whose 32-bit field is all ones.
func zip64Fields(extra []byte, needU, needC, needO bool, usize, csize, local uint64) (u, c, o uint64, err error) {
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
		take := func(need bool, v uint64) (uint64, bool) {
			if !need {
				return v, true
			}
			if len(body) < 8 {
				return 0, false
			}
			x := le.Uint64(body)
			body = body[8:]
			return x, true
		}
		var ok1, ok2, ok3 bool
		u, ok1 = take(needU, usize)
		c, ok2 = take(needC, csize)
		o, ok3 = take(needO, local)
		if ok1 && ok2 && ok3 {
			return u, c, o, nil
		}
		break
	}
	return 0, 0, 0, invalid(ErrNotZip, "the central directory is damaged")
}

// checkLocal compares the name of every local header with the name of its
// central-directory record, so that a reader that walks the local headers and
// one that reads the central directory see the same archive, and refuses
// entries whose data overlaps another's or runs into the central directory.
func checkLocal(ctx context.Context, ra io.ReaderAt, size, cdAt int64, recs []record) error {
	type span struct{ from, to int64 }
	spans := make([]span, 0, len(recs))
	var h [localLen]byte
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.local+localLen > size {
			return invalid(ErrNotZip, "an entry starts outside the archive")
		}
		if _, err := ra.ReadAt(h[:], r.local); err != nil || le.Uint32(h[:]) != sigLocal {
			return invalid(ErrNotZip, "a local header is damaged")
		}
		n, extra := int(le.Uint16(h[26:])), int(le.Uint16(h[28:]))
		if n != len(r.name) || r.local+localLen+int64(n) > size {
			return invalid(ErrLocalMismatch, fmt.Sprintf("entry %q is named differently in its local header", shorten(r.name)))
		}
		name := make([]byte, n)
		if _, err := ra.ReadAt(name, r.local+localLen); err != nil {
			return invalid(ErrNotZip, "a local header is damaged")
		}
		if string(name) != r.name {
			return invalid(ErrLocalMismatch, fmt.Sprintf("entry %q is named differently in its local header", shorten(r.name)))
		}
		end := r.local + localLen + int64(n) + int64(extra) + r.csize
		if end > cdAt || end < r.local {
			return invalid(ErrNotZip, fmt.Sprintf("the data of entry %q runs beyond the archive", shorten(r.name)))
		}
		spans = append(spans, span{r.local, end})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	for i := 1; i < len(spans); i++ {
		if spans[i].from < spans[i-1].to {
			return invalid(ErrOverlap, "the data of two entries overlaps")
		}
	}
	return nil
}
