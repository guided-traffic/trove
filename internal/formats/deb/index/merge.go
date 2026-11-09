package index

import (
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // the MD5Sum field of a Release is a compatibility field (R-DEB-011)
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/ulikunitz/xz"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
)

// MaxMergedParagraphs bounds one merged index (R-DEB-034): the set of the packages kept so far holds at most this many 128-bit hashes.
const MaxMergedParagraphs = 1_000_000

// ErrTooMany is the failure of a merge that would keep more paragraphs than the bound.
var ErrTooMany = errors.New("index: the merged index would have more than 1,000,000 paragraphs")

// PackagesWriter writes one merged Packages file as a stream (R-DEB-025, Q-DEB-38): each paragraph goes through the hashes of the
// uncompressed content and into the compressed variants, and nothing of the plain index is kept, so the memory of a merge is the
// compressed output and the set of hashes of the paragraphs kept. Equal paragraphs in equal order give equal bytes.
type PackagesWriter struct {
	plain, md5 hash.Hash
	size       int64
	gz         bytes.Buffer
	zw         *gzip.Writer
	xz         *bytes.Buffer
	xw         *xz.Writer
	n          int
}

// NewPackagesWriter starts a Packages file; withXZ adds the Packages.xz variant (deb.compression lists xz).
func NewPackagesWriter(withXZ bool) (*PackagesWriter, error) {
	w := &PackagesWriter{plain: sha256.New(), md5: md5.New()} //nolint:gosec // see the import
	w.zw, _ = gzip.NewWriterLevel(&w.gz, gzip.BestCompression)
	if withXZ {
		w.xz = &bytes.Buffer{}
		xw, err := xz.NewWriter(w.xz)
		if err != nil {
			return nil, err
		}
		w.xw = xw
	}
	return w, nil
}

// Paragraph appends one rendered paragraph (it ends in a newline); a blank line separates it from the one before.
func (w *PackagesWriter) Paragraph(p []byte) error {
	if w.n > 0 {
		if err := w.write([]byte{'\n'}); err != nil {
			return err
		}
	}
	w.n++
	return w.write(p)
}

func (w *PackagesWriter) write(b []byte) error {
	w.plain.Write(b)
	w.md5.Write(b)
	w.size += int64(len(b))
	if _, err := w.zw.Write(b); err != nil {
		return err
	}
	if w.xw != nil {
		if _, err := w.xw.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// Paragraphs is the number of paragraphs written.
func (w *PackagesWriter) Paragraphs() int { return w.n }

// Close ends the file. base is the path of the uncompressed file relative to dists/<suite>/ ("main/binary-amd64/Packages"); listed is
// its entry in the hash lists of the Release, files its compressed variants.
func (w *PackagesWriter) Close(base string) (listed File, files []File, err error) {
	if err := w.zw.Close(); err != nil {
		return File{}, nil, err
	}
	listed = File{Path: base, Size: w.size, SHA256: hex.EncodeToString(w.plain.Sum(nil)), MD5: hex.EncodeToString(w.md5.Sum(nil))}
	files = append(files, newFile(base+".gz", w.gz.Bytes()))
	if w.xw != nil {
		if err := w.xw.Close(); err != nil {
			return File{}, nil, err
		}
		files = append(files, newFile(base+".xz", w.xz.Bytes()))
	}
	return listed, files, nil
}

// Seen is the bounded set of the packages a merge kept, as 128-bit hashes of the package, version and architecture (Q-DEB-38).
type Seen struct {
	m   map[[16]byte]struct{}
	max int
}

// NewSeen makes a set that holds at most max hashes; 0 is MaxMergedParagraphs.
func NewSeen(max int) *Seen {
	if max <= 0 {
		max = MaxMergedParagraphs
	}
	return &Seen{m: map[[16]byte]struct{}{}, max: max}
}

// Add records a package, version and architecture; added is false when an earlier member kept it already. It fails with ErrTooMany
// when the set is full.
func (s *Seen) Add(pkg, version, arch string) (added bool, err error) {
	h := sha256.New()
	for _, part := range []string{pkg, version, arch} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	var key [16]byte
	copy(key[:], h.Sum(nil))
	if _, dup := s.m[key]; dup {
		return false, nil
	}
	if len(s.m) >= s.max {
		return false, ErrTooMany
	}
	s.m[key] = struct{}{}
	return true, nil
}

// Len is the number of hashes held.
func (s *Seen) Len() int { return len(s.m) }

var (
	packageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9.+~:-]*$`)
	archRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// MemberFilename is the Filename of a merged paragraph: members/<memberKey>/<the member's Filename> (R-DEB-025). ok is false for a
// Filename that is empty, absolute, holds a backslash or a dot segment, so a member never points outside its own paths (R-DEB-034).
func MemberFilename(member, filename string) (string, bool) {
	if filename == "" || strings.HasPrefix(filename, "/") || strings.Contains(filename, `\`) {
		return "", false
	}
	for _, seg := range strings.Split(filename, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return "members/" + member + "/" + path.Clean(filename), true
}

// ParagraphSink takes the paragraphs of a merge in order: a PackagesWriter streams them into the compressed variants, Plain keeps them.
type ParagraphSink interface {
	Paragraph(p []byte) error
}

// Plain is a ParagraphSink that keeps the plain index in memory, for a merge of documents that are small (the contract's Merge).
type Plain struct {
	buf bytes.Buffer
	n   int
}

// Paragraph appends a paragraph, a blank line after the one before.
func (p *Plain) Paragraph(b []byte) error {
	if p.n > 0 {
		p.buf.WriteByte('\n')
	}
	p.n++
	p.buf.Write(b)
	return nil
}

// Bytes is the merged index.
func (p *Plain) Bytes() []byte { return p.buf.Bytes() }

// MergeResult counts what a member's index gave a merge.
type MergeResult struct {
	Kept int
	// Skipped are the paragraphs left out because they are not valid (a name outside the grammar, a Filename that points elsewhere).
	Skipped int
	// Others are the paragraphs the member's patterns, the claims or an earlier member left out.
	Others int
}

// MergeMember streams the paragraphs of one member's index, plain, into w in the member's own order: a paragraph is kept when keep
// admits its package name (the member's patterns, the pin rules and claims, R-DEB-025) and no earlier member kept the same package,
// version and architecture. The Filename of a kept paragraph becomes the member path; every other field stays as the member serves it. A
// paragraph that does not validate is left out and counted, as the parse of a remote does (R-DEB-034).
func MergeMember(w ParagraphSink, seen *Seen, memberKey string, r io.Reader, keep func(name string) bool) (MergeResult, error) {
	var res MergeResult
	rd := control.NewReader(r, false)
	for {
		p, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("index: the index of member %s does not read: %w", memberKey, err)
		}
		name, version, arch := p.Value("Package"), p.Value("Version"), p.Value("Architecture")
		filename, ok := MemberFilename(memberKey, p.Value("Filename"))
		if !ok || !packageRE.MatchString(name) || !versionRE.MatchString(version) || !archRE.MatchString(arch) {
			res.Skipped++
			continue
		}
		if !keep(name) {
			res.Others++
			continue
		}
		added, err := seen.Add(name, version, arch)
		if err != nil {
			return res, err
		}
		if !added {
			res.Others++
			continue
		}
		b, err := p.With("Filename", filename).Bytes()
		if err != nil {
			res.Skipped++
			continue
		}
		if err := w.Paragraph(b); err != nil {
			return res, err
		}
		res.Kept++
	}
}
