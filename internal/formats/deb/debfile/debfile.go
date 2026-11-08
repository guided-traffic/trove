// Package debfile reads a Debian binary package (deb(5), R-DEB-007) and returns its normalized control paragraph. It reads the ar
// container through untrusted.NewAr, the control archive through the bounded decompressors of untrusted and the control file through
// the strict deb822 parser of package control, within the bounds of R-DEB-034. The data archive is never decompressed. It is a leaf.
package debfile

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// MaxControlArchiveBytes bounds the compressed control archive (R-DEB-034).
const MaxControlArchiveBytes = 16 << 20

// ratioSlack is what the decompressor may have read ahead of the bytes it has consumed.
const ratioSlack = 128 << 10

// Computed are the fields an index computes itself and an uploaded paragraph must not carry into it (R-DEB-007).
var Computed = [...]string{"Filename", "Size", "MD5sum", "SHA1", "SHA256", "SHA512", "Description-md5"}

// Required are the fields every package declares (R-DEB-007).
var Required = [...]string{"Package", "Version", "Architecture", "Maintainer", "Description"}

// Package is what the reading of a package file yields.
type Package struct {
	// Name, Version and Architecture are the declared identity; the grammars of R-DEB-002 are the format's to apply.
	Name, Version, Architecture string
	// Source is the name of the source package: the Source field without a version in parentheses, else Name.
	Source string
	// Control is the normalized paragraph: the fields in their order without the computed ones.
	Control control.Paragraph
}

var binaryRE = regexp.MustCompile(`^2\.[0-9]{1,4}\n$`)

func invalid(detail string) error {
	return errs.E(errs.Unprocessable, errs.CodeDebInvalidArchive, "The upload is not a valid Debian package: "+detail+".")
}

// Read reads the package file ra of the given size. lim holds the bounds of the control archive: Entries, UncompressedBytes, Ratio and
// ManifestEntryBytes (the control file). A violation is 422 deb_invalid_archive, deb_unsupported_compression or deb_invalid_control, a
// crossed bound 413.
func Read(ra io.ReaderAt, size int64, lim untrusted.Limits) (*Package, error) {
	lim = untrusted.DefaultLimits().Tighten(lim)
	ar, err := untrusted.NewAr(ra, size, lim)
	if err != nil {
		return nil, err
	}
	m, err := ar.Next()
	if err != nil || m.Name != "debian-binary" {
		return nil, invalid("the first member is not debian-binary")
	}
	body, err := ar.Open()
	if err != nil {
		return nil, err
	}
	if m.Size > 16 {
		return nil, invalid("debian-binary is too long")
	}
	ver, err := io.ReadAll(body)
	if err != nil || !binaryRE.Match(ver) {
		return nil, invalid("debian-binary does not name a format version 2.x")
	}

	m, err = ar.Next()
	if err != nil {
		return nil, invalid("there is no control archive")
	}
	ext, ok := strings.CutPrefix(m.Name, "control.tar")
	if !ok {
		return nil, invalid("the second member is not the control archive")
	}
	if !knownCompression(ext) {
		return nil, errs.E(errs.Unprocessable, errs.CodeDebUnsupportedCompression,
			"The control archive is compressed with a method trove does not read; use none, gzip, xz or zstd.")
	}
	if m.Size > MaxControlArchiveBytes {
		return nil, errs.E(errs.TooLarge, errs.CodeTooLarge, "The control archive is larger than 16 MiB.")
	}
	cbody, err := ar.Open()
	if err != nil {
		return nil, err
	}
	file, err := controlFile(cbody, ext, lim)
	if err != nil {
		return nil, err
	}

	m, err = ar.Next()
	if err != nil {
		return nil, invalid("there is no data archive")
	}
	if rest, ok := strings.CutPrefix(m.Name, "data.tar"); !ok || !validDataSuffix(rest) {
		return nil, invalid("the third member is not the data archive")
	}
	for {
		m, err = ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if !strings.HasPrefix(m.Name, "_") {
			return nil, invalid("the member " + m.Name + " is not one a package has")
		}
	}
	return fromControl(file)
}

func knownCompression(ext string) bool {
	switch ext {
	case "", ".gz", ".xz", ".zst":
		return true
	}
	return false
}

// validDataSuffix admits the compressions dpkg writes for the data archive, which trove never decompresses.
func validDataSuffix(ext string) bool {
	switch ext {
	case "", ".gz", ".xz", ".zst", ".bz2", ".lzma":
		return true
	}
	return false
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ratioReader fails a stream that decompresses by more than ratio:1.
type ratioReader struct {
	out   io.Reader
	in    *countReader
	ratio int64
	read  int64
}

func (r *ratioReader) Read(p []byte) (int, error) {
	n, err := r.out.Read(p)
	r.read += int64(n)
	if r.read > r.ratio*r.in.n+ratioSlack {
		return n, untrusted.TooLarge(fmt.Sprintf("the control archive compresses by more than %d:1", r.ratio))
	}
	return n, err
}

func decompress(r io.Reader, ext string, lim untrusted.Limits) (io.Reader, error) {
	switch ext {
	case "":
		return r, nil
	case ".gz":
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, invalid("the control archive is not valid gzip")
		}
		return zr, nil
	case ".xz":
		xr, err := untrusted.NewXZReader(r, lim)
		if err != nil {
			return nil, invalid("the control archive is not valid xz")
		}
		return xr, nil
	default:
		zr, err := untrusted.NewZstdReader(r, lim)
		if err != nil {
			return nil, invalid("the control archive is not valid Zstandard")
		}
		return zr, nil
	}
}

// controlFile reads the member ./control of the control archive. Every other entry is skipped, and counted: the archive holds at most
// lim.Entries, decompresses to at most lim.UncompressedBytes and by at most lim.Ratio:1.
func controlFile(member io.Reader, ext string, lim untrusted.Limits) ([]byte, error) {
	in := &countReader{r: member}
	dec, err := decompress(in, ext, lim)
	if err != nil {
		return nil, err
	}
	limited := untrusted.Limit(&ratioReader{out: dec, in: in, ratio: int64(lim.Ratio)}, lim.UncompressedBytes)
	tr := tar.NewReader(limited)
	var file []byte
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, tarError(err)
		}
		if entries++; entries > lim.Entries {
			return nil, untrusted.TooLarge(fmt.Sprintf("the control archive holds more than %d entries", lim.Entries))
		}
		name := strings.TrimPrefix(path.Clean(h.Name), "./")
		if name != "control" {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, invalid("control is not a regular file")
		}
		if file != nil {
			return nil, invalid("the control archive holds control twice")
		}
		if h.Size > lim.ManifestEntryBytes {
			return nil, untrusted.TooLarge("the control file is larger than 1 MiB")
		}
		file, err = io.ReadAll(io.LimitReader(tr, lim.ManifestEntryBytes+1))
		if err != nil {
			return nil, tarError(err)
		}
		if file == nil {
			file = []byte{}
		}
	}
	if file == nil {
		return nil, invalid("the control archive has no control file")
	}
	return file, nil
}

// tarError keeps the 413 of a crossed bound and turns every other failure of the archive into 422.
func tarError(err error) error {
	if untrusted.IsTooLarge(err) {
		return err
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	return invalid("the control archive cannot be read")
}

func fromControl(file []byte) (*Package, error) {
	p, err := control.Parse(file)
	if err != nil {
		return nil, control.Refusal(err)
	}
	for _, name := range Required {
		if v, ok := p.Get(name); !ok || strings.TrimSpace(v) == "" {
			return nil, control.Refusal(&control.Error{Field: name, Reason: "the field is required"})
		}
	}
	norm := p.Without(Computed[:]...)
	// The normalized paragraph is what the index lists: at most 1 MiB (R-DEB-007).
	out, err := norm.Bytes()
	if err != nil {
		return nil, control.Refusal(err)
	}
	if len(out) > control.MaxFieldBytes {
		return nil, errs.E(errs.TooLarge, errs.CodeTooLarge, "The normalized control data is larger than 1 MiB.")
	}
	pkg := &Package{Name: p.Value("Package"), Version: p.Value("Version"), Architecture: p.Value("Architecture"), Control: norm}
	pkg.Source = pkg.Name
	if s, ok := p.Get("Source"); ok {
		name, _, _ := strings.Cut(strings.TrimSpace(s), " ")
		if name == "" {
			return nil, control.Refusal(&control.Error{Field: "Source", Reason: "the field is empty"})
		}
		pkg.Source = name
	}
	return pkg, nil
}
