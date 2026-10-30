// Package crafted generates the hostile inputs of the conformance kit's check 6
// (R-ARCH-010, R-ARCH-028): a zip archive that expands to 10 GiB, a tar.gz with a
// 2 GiB manifest, a JSON document nested 100,000 deep, an XML document with a
// document type declaration. Every input is produced from a few bytes on demand,
// so a test holds none of the gigabytes it pretends to carry, and a reader that
// refuses early never pays for them.
//
// It imports the standard library only; a format's own tests and fuzz targets use
// the same generators as seeds.
package crafted

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
)

// Input is one crafted upload. A format's Inspect reads it through Open (a
// stream, once per call) or, for an archive that needs random access, through
// ReaderAt.
type Input struct {
	// Name says what the input is, for a test's failure message.
	Name string
	// Size is what a spool would report: exact for an input held in memory or
	// addressed by offset, an estimate of the compressed length for a stream.
	Size int64
	// Open returns a fresh stream of the input.
	Open func() io.ReadCloser
	// ReaderAt is non-nil for an input that supports random access.
	ReaderAt io.ReaderAt
}

// pull is a reader that produces its bytes in steps: step appends to buf and
// reports when it is done.
type pull struct {
	buf  bytes.Buffer
	step func() (done bool, err error)
	done bool
	err  error
}

func (p *pull) Read(b []byte) (int, error) {
	for p.buf.Len() == 0 {
		if p.err != nil {
			return 0, p.err
		}
		if p.done {
			return 0, io.EOF
		}
		p.done, p.err = p.step()
	}
	return p.buf.Read(b)
}

func (p *pull) Close() error {
	p.done, p.err = true, nil
	p.buf.Reset()
	return nil
}

// tarGz returns a stream of the gzip-compressed tar archive that fn writes: fn
// is called for one step of work at a time and reports whether the archive is
// complete.
func tarGz(fn func(tw *tar.Writer) (done bool, err error)) io.ReadCloser {
	p := &pull{}
	gz, _ := gzip.NewWriterLevel(&p.buf, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	p.step = func() (bool, error) {
		done, err := fn(tw)
		if err != nil || !done {
			return false, err
		}
		if err := tw.Close(); err != nil {
			return true, err
		}
		return true, gz.Close()
	}
	return p
}

// TarGzEntry is a tar.gz archive whose one regular file name declares and holds
// size bytes of zeros, for example crafted.TarGzEntry("package/package.json",
// 2<<30). A reader that trusts the header allocates the size; a reader that
// inflates pays for it. The stream is generated as it is read.
func TarGzEntry(name string, size int64) Input {
	zeros := make([]byte, 1<<20)
	return Input{Name: fmt.Sprintf("tar.gz with %q of %d bytes", name, size), Size: size/1000 + 4096, Open: func() io.ReadCloser {
		started, left := false, size
		return tarGz(func(tw *tar.Writer) (bool, error) {
			if !started {
				started = true
				return false, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg})
			}
			if left == 0 {
				return true, nil
			}
			n := min(left, int64(len(zeros)))
			left -= n
			_, err := tw.Write(zeros[:n])
			return false, err
		})
	}}
}

// TarGzEntries is a tar.gz archive of n empty regular files "f0" to "f<n-1>".
func TarGzEntries(n int) Input {
	return Input{Name: fmt.Sprintf("tar.gz with %d entries", n), Size: int64(n)*8 + 4096, Open: func() io.ReadCloser {
		i := 0
		return tarGz(func(tw *tar.Writer) (bool, error) {
			for range 1000 {
				if i == n {
					return true, nil
				}
				if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
					return false, err
				}
				i++
			}
			return false, nil
		})
	}}
}

// TarGzDuplicates is a tar.gz archive in which name is used by two regular files
// with different content: two readers could resolve it differently.
func TarGzDuplicates(name string) Input {
	return smallTarGz(fmt.Sprintf("tar.gz with %q twice", name), func(tw *tar.Writer) error {
		for _, body := range []string{"first", "second"} {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				return err
			}
		}
		return nil
	})
}

// TarGzSymlink is a tar.gz archive in which name is a symbolic link to target:
// a link where a reader expects a file.
func TarGzSymlink(name, target string) Input {
	return smallTarGz(fmt.Sprintf("tar.gz with %q linked to %q", name, target), func(tw *tar.Writer) error {
		return tw.WriteHeader(&tar.Header{Name: name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: target})
	})
}

func smallTarGz(name string, fn func(tw *tar.Writer) error) Input {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := fn(tw); err != nil {
		panic("crafted: " + err.Error())
	}
	if err := tw.Close(); err != nil {
		panic("crafted: " + err.Error())
	}
	if err := gz.Close(); err != nil {
		panic("crafted: " + err.Error())
	}
	b := buf.Bytes()
	return Input{Name: name, Size: int64(len(b)), Open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }, ReaderAt: bytes.NewReader(b)}
}

// ZipEntries is a zip archive of n empty stored files "f0" to "f<n-1>".
func ZipEntries(n int) Input {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range n {
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f%d", i), Method: zip.Store}); err != nil {
			panic("crafted: " + err.Error())
		}
	}
	if err := zw.Close(); err != nil {
		panic("crafted: " + err.Error())
	}
	b := buf.Bytes()
	return Input{
		Name: fmt.Sprintf("zip with %d entries", n), Size: int64(len(b)),
		Open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }, ReaderAt: bytes.NewReader(b),
	}
}

// ZipBomb is a zip archive with one deflated entry name that declares and
// inflates to size zeros: a few kilobytes of compressed data per gigabyte. The
// archive is addressed by offset and never held in memory, its central directory
// uses zip64 sizes and its CRC is correct, so a reader that inflates the entry
// reads exactly what the header promises. Size is the length of the whole archive.
func ZipBomb(name string, size int64) Input {
	b := newBomb(name, size)
	return Input{
		Name: fmt.Sprintf("zip with %q expanding to %d bytes", name, size), Size: b.size,
		Open:     func() io.ReadCloser { return io.NopCloser(io.NewSectionReader(b, 0, b.size)) },
		ReaderAt: b,
	}
}

const bombBlock = 1 << 20

// bomb is the zip archive of ZipBomb: local header, the deflate stream (the
// compressed block of bombBlock zeros repeated, the compressed remainder, an
// empty final block), the central directory and the end record.
type bomb struct {
	local, central, end []byte
	block, tail, final  []byte
	copies              int64
	dataLen             int64
	size                int64
}

func newBomb(name string, size int64) *bomb {
	b := &bomb{final: []byte{0x01, 0x00, 0x00, 0xff, 0xff}, copies: size / bombBlock}
	b.block = deflateZeros(bombBlock)
	if rest := size % bombBlock; rest > 0 {
		b.tail = deflateZeros(int(rest))
	}
	b.dataLen = b.copies*int64(len(b.block)) + int64(len(b.tail)) + int64(len(b.final))
	crc := crcZeros(size)
	le := binary.LittleEndian

	// zip64 sizes in the local header (both, as the format asks) and in the central
	// directory (the uncompressed size, the only field that does not fit)
	b.local = le.AppendUint32(nil, 0x04034b50)
	b.local = le.AppendUint16(b.local, 45)
	b.local = le.AppendUint16(b.local, 0)
	b.local = le.AppendUint16(b.local, 8)
	b.local = le.AppendUint16(b.local, 0)
	b.local = le.AppendUint16(b.local, 0x21)
	b.local = le.AppendUint32(b.local, crc)
	b.local = le.AppendUint32(b.local, 0xFFFFFFFF)
	b.local = le.AppendUint32(b.local, 0xFFFFFFFF)
	b.local = le.AppendUint16(b.local, u16(len(name)))
	b.local = le.AppendUint16(b.local, 20)
	b.local = append(b.local, name...)
	b.local = le.AppendUint16(b.local, 1)
	b.local = le.AppendUint16(b.local, 16)
	b.local = le.AppendUint64(b.local, u64(size))
	b.local = le.AppendUint64(b.local, u64(b.dataLen))

	b.central = le.AppendUint32(nil, 0x02014b50)
	b.central = le.AppendUint16(b.central, 45)
	b.central = le.AppendUint16(b.central, 45)
	b.central = le.AppendUint16(b.central, 0)
	b.central = le.AppendUint16(b.central, 8)
	b.central = le.AppendUint16(b.central, 0)
	b.central = le.AppendUint16(b.central, 0x21)
	b.central = le.AppendUint32(b.central, crc)
	b.central = le.AppendUint32(b.central, u32(b.dataLen))
	b.central = le.AppendUint32(b.central, 0xFFFFFFFF)
	b.central = le.AppendUint16(b.central, u16(len(name)))
	b.central = le.AppendUint16(b.central, 12)
	b.central = le.AppendUint16(b.central, 0)
	b.central = le.AppendUint16(b.central, 0)
	b.central = le.AppendUint16(b.central, 0)
	b.central = le.AppendUint32(b.central, 0)
	b.central = le.AppendUint32(b.central, 0)
	b.central = append(b.central, name...)
	b.central = le.AppendUint16(b.central, 1)
	b.central = le.AppendUint16(b.central, 8)
	b.central = le.AppendUint64(b.central, u64(size))

	cdOff := int64(len(b.local)) + b.dataLen
	b.end = le.AppendUint32(nil, 0x06054b50)
	b.end = le.AppendUint16(b.end, 0)
	b.end = le.AppendUint16(b.end, 0)
	b.end = le.AppendUint16(b.end, 1)
	b.end = le.AppendUint16(b.end, 1)
	b.end = le.AppendUint32(b.end, u32(int64(len(b.central))))
	b.end = le.AppendUint32(b.end, u32(cdOff))
	b.end = le.AppendUint16(b.end, 0)
	b.size = cdOff + int64(len(b.central)) + int64(len(b.end))
	return b
}

// u16, u32 and u64 convert a size that the generators build from their own
// arguments; a value outside the range of the zip field is a mistake of the
// caller of the generator.
func u16(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		panic("crafted: the name is too long for a zip entry")
	}
	return uint16(n)
}

func u32(n int64) uint32 {
	if n < 0 || n > math.MaxUint32 {
		panic("crafted: the archive is too large for a 32-bit zip field")
	}
	return uint32(n)
}

func u64(n int64) uint64 {
	if n < 0 {
		panic("crafted: a negative size")
	}
	return uint64(n)
}

// deflateZeros is n zero bytes as a deflate stream that is not final and ends on
// a byte boundary, so copies of it concatenate into one valid stream.
func deflateZeros(n int) []byte {
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.BestCompression)
	zeros := make([]byte, 64<<10)
	for n > 0 {
		k := min(n, len(zeros))
		_, _ = fw.Write(zeros[:k])
		n -= k
	}
	_ = fw.Flush()
	return buf.Bytes()
}

// ReadAt implements io.ReaderAt over the virtual archive.
func (b *bomb) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("crafted: negative offset")
	}
	n := 0
	for n < len(p) && off < b.size {
		var k int
		switch {
		case off < int64(len(b.local)):
			k = copy(p[n:], b.local[off:])
		case off < int64(len(b.local))+b.dataLen:
			k = b.copyData(p[n:], off-int64(len(b.local)))
		case off < int64(len(b.local))+b.dataLen+int64(len(b.central)):
			k = copy(p[n:], b.central[off-int64(len(b.local))-b.dataLen:])
		default:
			k = copy(p[n:], b.end[off-int64(len(b.local))-b.dataLen-int64(len(b.central)):])
		}
		n += k
		off += int64(k)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// copyData copies from the deflate stream at pos, at most up to the end of the
// piece pos is in.
func (b *bomb) copyData(p []byte, pos int64) int {
	blocks := b.copies * int64(len(b.block))
	switch {
	case pos < blocks:
		return copy(p, b.block[pos%int64(len(b.block)):])
	case pos < blocks+int64(len(b.tail)):
		return copy(p, b.tail[pos-blocks:])
	}
	return copy(p, b.final[pos-blocks-int64(len(b.tail)):])
}

// crcZeros is the CRC-32 (IEEE) of n zero bytes, computed by raising x to the
// power 8n modulo the polynomial instead of hashing n bytes.
func crcZeros(n int64) uint32 {
	if n < 1<<20 {
		return crc32.Update(0, crc32.IEEETable, make([]byte, n))
	}
	return mulmod(xpow8(n), 0xFFFFFFFF) ^ 0xFFFFFFFF
}

const crcPoly = 0xedb88320

// mulmod multiplies two polynomials in the reflected representation of CRC-32.
func mulmod(a, b uint32) uint32 {
	var p uint32
	for m := uint32(1) << 31; m != 0; m >>= 1 {
		if a&m != 0 {
			p ^= b
		}
		if b&1 != 0 {
			b = b>>1 ^ crcPoly
		} else {
			b >>= 1
		}
	}
	return p
}

// xpow8 returns x^(8n) modulo the CRC polynomial, by squaring.
func xpow8(n int64) uint32 {
	result := uint32(1) << 31 // x^0
	x := uint32(1) << 30      // x^1
	// x^8 first, then square for every bit of n
	for range 3 {
		x = mulmod(x, x)
	}
	for ; n > 0; n >>= 1 {
		if n&1 != 0 {
			result = mulmod(result, x)
		}
		x = mulmod(x, x)
	}
	return result
}

// segments is a reader over a list of (bytes, repeat) pieces. It serves each piece
// in chunks of whole repetitions, so a document of millions of tiny values costs a
// copy per chunk, not one per value.
type segments struct {
	parts []segment
	i     int
	chunk []byte // whole repetitions of parts[i].b
	reps  int64  // the number of repetitions in chunk
	off   int
}

type segment struct {
	b []byte
	n int64
}

const segmentChunk = 64 << 10

func (s *segments) Read(p []byte) (int, error) {
	for s.i < len(s.parts) {
		seg := &s.parts[s.i]
		if seg.n <= 0 {
			s.i++
			s.chunk = nil
			continue
		}
		if s.chunk == nil {
			s.reps = min(seg.n, int64(max(1, segmentChunk/len(seg.b))))
			s.chunk, s.off = bytes.Repeat(seg.b, int(s.reps)), 0
		}
		if s.off == len(s.chunk) {
			seg.n -= s.reps
			s.chunk = nil
			continue
		}
		n := copy(p, s.chunk[s.off:])
		s.off += n
		return n, nil
	}
	return 0, io.EOF
}

func (s *segments) Close() error { return nil }

func repeated(name string, size int64, parts ...segment) Input {
	return Input{Name: name, Size: size, Open: func() io.ReadCloser {
		return &segments{parts: append([]segment(nil), parts...)}
	}}
}

// DeepJSON is a JSON document of depth nested arrays: "[[[...]]]".
func DeepJSON(depth int) Input {
	return repeated(fmt.Sprintf("JSON nested %d deep", depth), int64(depth)*2,
		segment{[]byte("["), int64(depth)}, segment{[]byte("]"), int64(depth)})
}

// WideJSON is a JSON array of n zeros: few bytes per value, a node per value in a
// decoder's tree.
func WideJSON(n int) Input {
	return repeated(fmt.Sprintf("JSON array of %d values", n), int64(n)*2+1,
		segment{[]byte("["), 1}, segment{[]byte("0,"), int64(n - 1)}, segment{[]byte("0]"), 1})
}

// XMLDoctype is an XML document whose document type declaration defines entities
// that expand into each other ten levels deep: a decoder that expands entities
// reads a billion copies of a string.
func XMLDoctype() Input {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?>` + "\n" + `<!DOCTYPE package [` + "\n" + `<!ENTITY e0 "lol">` + "\n")
	for i := 1; i < 10; i++ {
		fmt.Fprintf(&b, `<!ENTITY e%d "`, i)
		for range 10 {
			fmt.Fprintf(&b, "&e%d;", i-1)
		}
		b.WriteString(`">` + "\n")
	}
	b.WriteString(`]>` + "\n" + `<package><metadata><id>&e9;</id></metadata></package>` + "\n")
	data := b.Bytes()
	return Input{
		Name: "XML with a document type declaration", Size: int64(len(data)),
		Open: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(data)) }, ReaderAt: bytes.NewReader(data),
	}
}

// DeepXML is an XML document of depth nested elements.
func DeepXML(depth int) Input {
	return repeated(fmt.Sprintf("XML nested %d deep", depth), int64(depth)*7,
		segment{[]byte("<a>"), int64(depth)}, segment{[]byte("</a>"), int64(depth)})
}
