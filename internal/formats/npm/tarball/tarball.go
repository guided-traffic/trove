// Package tarball validates an npm package tarball in one streaming pass
// (R-NPM-007 step 3, R-ARCH-028): a gzip-compressed tar whose entries all share
// one first path component, with exactly one regular package/package.json.
//
// Nothing is unpacked and no entry name is used as a file-system path. The
// compressed stream is never held in memory; package.json and the README are
// the only entry content read, each within its own limit, and the digests of
// the compressed bytes are computed while they stream. It is a leaf: standard
// library, internal/errs and its sibling internal/formats/npm/manifest.
package tarball

import (
	"archive/tar"
	"bufio"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // npm's dist.shasum is a SHA-1 and must be reproduced
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
)

// Limits bound what a tarball may cost. A zero field takes its default
// (Q-NPM-17); a format can only tighten them (R-ARCH-028).
type Limits struct {
	// MaxCompressedBytes caps the compressed stream; the publish body cap
	// (npm.publish.max_tarball_size) is the caller's, this is the backstop.
	MaxCompressedBytes int64
	// MaxUnpackedBytes caps the sum of the sizes of all entries, declared and
	// actually inflated (npm.publish.max_unpacked_size).
	MaxUnpackedBytes int64
	// MaxEntries caps the number of tar entries (npm.publish.max_tar_entries).
	MaxEntries int
	// MaxManifestBytes caps package.json; declared sizes above it are refused
	// before a byte is read.
	MaxManifestBytes int64
	// MaxManifestDepth caps the JSON nesting depth of package.json.
	MaxManifestDepth int
	// MaxRatio is the compression ratio an entry read into memory may show.
	MaxRatio int
	// MaxTotalRatio is the ratio of all inflated bytes to the compressed bytes the
	// inflater consumed that the whole archive may show, beyond TotalRatioSlack: a
	// publish that costs the server gigabytes of inflation pays for them in upload
	// (S-4).
	MaxTotalRatio int
	// MaxNameBytes caps the length of one entry name and one link target.
	MaxNameBytes int
	// MaxPathBytes caps the bookkeeping of entry names the pass keeps.
	MaxPathBytes int64
	// MaxReadmeBytes is how much of the README is kept; the rest is skipped.
	MaxReadmeBytes int
}

// Defaults of the limits (Q-NPM-17).
const (
	DefaultMaxCompressedBytes = 256 << 20
	DefaultMaxUnpackedBytes   = 2 << 30
	DefaultMaxEntries         = 100000
	DefaultMaxRatio           = 100
	DefaultMaxTotalRatio      = 100
	DefaultMaxNameBytes       = 1024
	DefaultMaxPathBytes       = 32 << 20
	DefaultMaxReadmeBytes     = 64 << 10

	// ratioSlack is added to the allowance of the ratio check: the inflater
	// reads ahead, so a small entry may have been paid for before its header.
	ratioSlack = 128 << 10
	// totalRatioSlack is added to the allowance of the overall ratio check, so that a
	// small archive of repetitive files is never judged by its ratio.
	totalRatioSlack = 8 << 20
	// maxGzipHeaderBytes bounds the optional name, comment and extra fields.
	maxGzipHeaderBytes = 64 << 10
	// entryOverhead is what the decompressed-bytes cap allows per entry beyond
	// its content: header blocks, padding and extended headers.
	entryOverhead = 2048
)

// DefaultLimits returns the defaults of R-NPM-007 and R-ARCH-028.
func DefaultLimits() Limits { return Limits{}.WithDefaults() }

// WithDefaults replaces zero fields by the defaults.
func (l Limits) WithDefaults() Limits {
	if l.MaxCompressedBytes <= 0 {
		l.MaxCompressedBytes = DefaultMaxCompressedBytes
	}
	if l.MaxUnpackedBytes <= 0 {
		l.MaxUnpackedBytes = DefaultMaxUnpackedBytes
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultMaxEntries
	}
	if l.MaxManifestBytes <= 0 {
		l.MaxManifestBytes = manifest.DefaultMaxBytes
	}
	if l.MaxManifestDepth <= 0 {
		l.MaxManifestDepth = manifest.DefaultMaxDepth
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = DefaultMaxRatio
	}
	if l.MaxTotalRatio <= 0 {
		l.MaxTotalRatio = DefaultMaxTotalRatio
	}
	if l.MaxNameBytes <= 0 {
		l.MaxNameBytes = DefaultMaxNameBytes
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = DefaultMaxPathBytes
	}
	if l.MaxReadmeBytes <= 0 {
		l.MaxReadmeBytes = DefaultMaxReadmeBytes
	}
	return l
}

// Result is what one pass learned about a valid tarball.
type Result struct {
	// Manifest is the parsed package/package.json.
	Manifest manifest.Manifest
	// Facts are the install paths of the regular files, for the normalization
	// steps of R-NPM-007.
	Facts manifest.Facts
	// SHA1 and SHA512 are the digests of the compressed bytes.
	SHA1   [sha1.Size]byte
	SHA512 [sha512.Size]byte
	// Size is the number of compressed bytes.
	Size int64
	// FileCount and UnpackedSize are dist.fileCount and dist.unpackedSize:
	// regular files and the sum of their sizes.
	FileCount    int64
	UnpackedSize int64
	// HasShrinkwrap is true for a top-level npm-shrinkwrap.json.
	HasShrinkwrap bool
	// Readme is the first top-level README file, cut at MaxReadmeBytes, with its
	// name; empty when there is none.
	Readme         string
	ReadmeFilename string
}

// SHA1Hex is dist.shasum.
func (r *Result) SHA1Hex() string { return hex.EncodeToString(r.SHA1[:]) }

// Integrity is dist.integrity, the SRI string of the SHA-512 digest.
func (r *Result) Integrity() string {
	return "sha512-" + base64.StdEncoding.EncodeToString(r.SHA512[:])
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

// sourceReader checks the context and the compressed cap, and counts bytes.
type sourceReader struct {
	r   io.Reader
	st  *state
	n   int64
	max int64
}

func (s *sourceReader) Read(p []byte) (int, error) {
	if s.st.err != nil {
		return 0, s.st.err
	}
	if err := s.st.ctx.Err(); err != nil {
		return 0, s.st.fail(fmt.Errorf("read tarball: %w", err))
	}
	if left := s.max - s.n; left < int64(len(p)) {
		p = p[:max(left, 0)+1] // one byte over is how an excess is noticed
	}
	n, err := s.r.Read(p)
	s.n += int64(n)
	if s.n > s.max {
		return 0, s.st.fail(manifest.TooLarge(fmt.Sprintf("the tarball is larger than %d bytes", s.max)))
	}
	return n, err
}

// countReader counts the compressed bytes the inflater consumed. It is a
// io.ByteReader, so the gzip reader adds no buffering of its own and the count
// stays exact. With a guard set it refuses to pass more than that many bytes.
type countReader struct {
	br    *bufio.Reader
	st    *state
	n     int64
	guard int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.br.Read(p)
	c.n += int64(n)
	if c.guard > 0 && c.n > c.guard {
		return 0, c.st.fail(manifest.BadPublish("the gzip header is too large"))
	}
	return n, err
}

func (c *countReader) ReadByte() (byte, error) {
	b, err := c.br.ReadByte()
	if err == nil {
		c.n++
		if c.guard > 0 && c.n > c.guard {
			return 0, c.st.fail(manifest.BadPublish("the gzip header is too large"))
		}
	}
	return b, err
}

// inflatedReader caps the decompressed bytes: a stream that expands beyond the
// cap ends in 413 however it is built (R-ARCH-028). With a ratio set it also
// caps the expansion against the compressed bytes the inflater consumed so far.
type inflatedReader struct {
	r     io.Reader
	st    *state
	n     int64
	limit int64

	cr    *countReader
	ratio int64
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
		return 0, i.st.fail(manifest.TooLarge(fmt.Sprintf("the tarball expands beyond %d bytes", i.limit)))
	}
	// The inflater has consumed at least n/1032 compressed bytes for n output bytes, so the
	// check holds for every byte it reports, whatever the stream does.
	if i.cr != nil && i.n > i.ratio*i.cr.n+totalRatioSlack {
		return 0, i.st.fail(manifest.TooLarge(fmt.Sprintf("the tarball expands by more than %d:1", i.ratio)))
	}
	return n, err
}

// linkEntry is a link of the archive: a symbolic link at install, or a hard link
// (hard) to the install path target, which names no folder of its own.
type linkEntry struct {
	install, target string
	hard            bool
}

// kind is what an install path is known to be.
type kind uint8

const (
	kindImplicit kind = iota + 1 // only the parent of another entry
	kindDir
	kindFile
	kindLink
)

// Validate reads a gzip-compressed npm tarball from r and checks it against
// R-NPM-007 and the limits. Every violation is an *errs.Error: 400
// npm_bad_publish for a malformed or ambiguous archive, 413 npm_too_large for a
// limit. An error from r or the context is returned wrapped, not classified.
func Validate(ctx context.Context, r io.Reader, lim Limits) (*Result, error) {
	lim = lim.WithDefaults()
	st := &state{ctx: ctx}
	src := &sourceReader{r: r, st: st, max: lim.MaxCompressedBytes}
	h1, h512 := sha1.New(), sha512.New() //nolint:gosec // see the import
	br := bufio.NewReaderSize(io.TeeReader(src, io.MultiWriter(h1, h512)), 32<<10)
	cr := &countReader{br: br, st: st, guard: maxGzipHeaderBytes}

	zr, err := gzip.NewReader(cr)
	if err != nil {
		return nil, classify(st, err)
	}
	cr.guard = 0
	zr.Multistream(false)
	inflateCap := lim.MaxUnpackedBytes + int64(lim.MaxEntries)*entryOverhead + 1<<20
	inflated := &inflatedReader{r: zr, st: st, limit: inflateCap, cr: cr, ratio: int64(lim.MaxTotalRatio)}
	tr := tar.NewReader(inflated)

	w := &walker{lim: lim, tr: tr, cr: cr, kinds: map[string]kind{}}
	if err := w.walk(); err != nil {
		return nil, classify(st, err)
	}
	// Anything after the end-of-archive blocks must be padding, and the gzip
	// stream must end cleanly: its CRC is checked at that EOF.
	if err := drainZeros(inflated); err != nil {
		return nil, classify(st, err)
	}
	if _, err := br.Peek(1); err == nil {
		return nil, manifest.BadPublish("the tarball holds data after its gzip stream")
	} else if !errors.Is(err, io.EOF) {
		return nil, classify(st, err)
	}

	res, err := w.finish()
	if err != nil {
		return nil, err
	}
	res.Size = src.n
	copy(res.SHA1[:], h1.Sum(nil))
	copy(res.SHA512[:], h512.Sum(nil))
	return res, nil
}

// classify turns what the layers returned into the error of Validate.
func classify(st *state, err error) error {
	if st.err != nil {
		return st.err
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	var corrupt flate.CorruptInputError
	switch {
	case errors.Is(err, gzip.ErrHeader), errors.Is(err, gzip.ErrChecksum), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, io.EOF), errors.Is(err, tar.ErrHeader), errors.Is(err, tar.ErrFieldTooLong),
		errors.As(err, &corrupt):
		return manifest.BadPublish("the file is not a valid gzip-compressed tar archive", errs.WithCause(err))
	default:
		return fmt.Errorf("read tarball: %w", err)
	}
}

// drainZeros reads r to its end and requires every byte to be zero.
func drainZeros(r io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return manifest.BadPublish("the tarball holds data after the end of its tar archive")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// walker is the state of one pass over the tar entries.
type walker struct {
	lim Limits
	tr  *tar.Reader
	cr  *countReader

	first     string
	kinds     map[string]kind
	rootSeen  bool
	pathBytes int64
	entries   int
	declared  int64

	// links are the link entries, whose targets are checked against every link of the
	// archive once the walk has seen them all (S-6).
	links       []linkEntry
	files       []string
	fileCount   int64
	unpacked    int64
	shrinkwrap  bool
	pkgJSON     []byte
	pkgJSONSeen bool
	readme      string
	readmeName  string
	readmeSeen  bool
}

func (w *walker) walk() error {
	for {
		hdr, err := w.tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		w.entries++
		if w.entries > w.lim.MaxEntries {
			return manifest.TooLarge(fmt.Sprintf("the tarball holds more than %d entries", w.lim.MaxEntries))
		}
		if err := w.entry(hdr); err != nil {
			return err
		}
	}
}

func bad(format string, a ...any) error { return manifest.BadPublish(fmt.Sprintf(format, a...)) }

func (w *walker) entry(hdr *tar.Header) error {
	var k kind
	switch hdr.Typeflag {
	case tar.TypeReg:
		k = kindFile
	case tar.TypeDir:
		k = kindDir
	case tar.TypeSymlink, tar.TypeLink:
		k = kindLink
	default:
		return bad("entry %q has a type npm packages never use (%q)", shorten(hdr.Name), string(hdr.Typeflag))
	}
	for key := range hdr.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return bad("entry %q is a sparse file", shorten(hdr.Name))
		}
	}

	first, install, err := splitName(hdr.Name, k == kindDir, w.lim.MaxNameBytes)
	if err != nil {
		return err
	}
	if w.first == "" {
		w.first = first
	} else if first != w.first {
		return bad("entries do not share one first path component: %q and %q", shorten(w.first), shorten(first))
	}

	if k == kindFile {
		w.declared += hdr.Size
		if hdr.Size < 0 || w.declared > w.lim.MaxUnpackedBytes {
			return manifest.TooLarge(fmt.Sprintf("the tarball unpacks to more than %d bytes", w.lim.MaxUnpackedBytes))
		}
	}
	if k == kindLink {
		if err := w.checkLink(hdr, install); err != nil {
			return err
		}
	}

	top := !strings.Contains(install, "/")
	if top && install != "package.json" && strings.EqualFold(install, "package.json") {
		return bad("entry %q differs from package.json only in letter case", shorten(install))
	}
	if install == "package.json" && k != kindFile {
		return bad("package.json is not a regular file")
	}
	if err := w.place(install, k); err != nil {
		return err
	}

	if k != kindFile {
		return nil
	}
	w.fileCount++
	w.unpacked += hdr.Size
	w.files = append(w.files, install)
	switch {
	case install == "package.json":
		return w.readManifest(hdr)
	case top && install == "npm-shrinkwrap.json":
		w.shrinkwrap = true
	case top && !w.readmeSeen && isReadme(install):
		w.readmeSeen = true
		return w.readReadme(hdr, install)
	}
	return nil
}

// splitName checks an entry name against the path rules of R-NPM-007 and splits
// it into its first component and its install path.
func splitName(name string, isDir bool, maxBytes int) (first, install string, err error) {
	switch {
	case name == "":
		return "", "", bad("an entry has no name")
	case len(name) > maxBytes:
		return "", "", bad("an entry name is longer than %d bytes", maxBytes)
	case !utf8.ValidString(name):
		return "", "", bad("entry %q is not valid UTF-8", shorten(name))
	case strings.HasPrefix(name, "/"):
		return "", "", bad("entry %q is an absolute path", shorten(name))
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "", "", bad("entry %q holds a backslash or a control character", shorten(name))
		}
	}
	trimmed := name
	if isDir {
		trimmed = strings.TrimSuffix(name, "/")
	}
	for _, comp := range strings.Split(trimmed, "/") {
		if comp == "" || comp == "." || comp == ".." {
			return "", "", bad("entry %q is not a clean path", shorten(name))
		}
	}
	first, install, _ = strings.Cut(trimmed, "/")
	return first, install, nil
}

// checkLink refuses a link that leaves the package.
func (w *walker) checkLink(hdr *tar.Header, install string) error {
	if install == "" {
		return bad("the package folder %q is a link", shorten(hdr.Name))
	}
	target := hdr.Linkname
	if target == "" || len(target) > w.lim.MaxNameBytes || !utf8.ValidString(target) || strings.ContainsAny(target, "\\\x00") {
		return bad("link %q has no usable target", shorten(hdr.Name))
	}
	if hdr.Typeflag == tar.TypeLink {
		// A hard link names another entry of the archive, first component included.
		first, rest, err := splitName(target, false, w.lim.MaxNameBytes)
		if err != nil || first != w.first || rest == "" {
			return bad("hard link %q points outside the package", shorten(hdr.Name))
		}
		return w.keepLink(linkEntry{install: install, target: rest, hard: true})
	}
	if strings.HasPrefix(target, "/") {
		return bad("symbolic link %q has an absolute target", shorten(hdr.Name))
	}
	// A lexical check on the archive's own names: nothing here touches a file system.
	resolved := path.Join(path.Dir(install), target) //nolint:gosec // G305 matches the join, not an extraction
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return bad("symbolic link %q points outside the package", shorten(hdr.Name))
	}
	return w.keepLink(linkEntry{install: install, target: target})
}

// keepLink remembers a link for the check of linkChains, within the bookkeeping cap of the
// entry names.
func (w *walker) keepLink(l linkEntry) error {
	w.pathBytes += int64(len(l.target)) + 48
	if w.pathBytes > w.lim.MaxPathBytes {
		return manifest.TooLarge("the entry names of the tarball take too much memory")
	}
	w.links = append(w.links, l)
	return nil
}

// linkChains refuses a link whose target passes through another link of the archive. The lexical
// check of checkLink cleans a target such as sub/l2/../.. as if l2 were a folder; a file system
// follows l2 first, and a chain of links can lead out of the package that way. A target may
// name a link as its last component, which stops there (S-6). It runs after the walk because a
// link may come before the entries it passes through.
func (w *walker) linkChains() error {
	for _, l := range w.links {
		cur := path.Dir(l.install)
		if l.hard {
			cur = "."
		}
		comps := strings.FieldsFunc(l.target, func(r rune) bool { return r == '/' })
		for i, c := range comps {
			switch c {
			case ".":
			case "..":
				cur = path.Dir(cur)
			default:
				cur = path.Join(cur, c)
				if i < len(comps)-1 && w.kinds[cur] == kindLink {
					return bad("link %q passes through another link", shorten(l.install))
				}
			}
		}
	}
	return nil
}

// place records an entry under its install path and refuses a duplicate, an
// entry below a file or a link, and a file where a folder is needed.
func (w *walker) place(install string, k kind) error {
	if install == "" {
		if k != kindDir {
			return bad("the package folder is not a directory")
		}
		if w.rootSeen {
			return bad("the package folder appears twice")
		}
		w.rootSeen = true
		return nil
	}
	var implicit []string
	for i := 0; i < len(install); i++ {
		if install[i] != '/' {
			continue
		}
		anc := install[:i]
		switch w.kinds[anc] {
		case kindFile, kindLink:
			return bad("entry %q lies below a file or a link", shorten(install))
		case 0:
			implicit = append(implicit, anc)
		}
	}
	if cur := w.kinds[install]; cur != 0 {
		if cur == kindImplicit && k == kindDir {
			w.kinds[install] = kindDir
			return nil
		}
		return bad("the install path %q appears twice", shorten(install))
	}
	for _, anc := range append(implicit, install) {
		w.pathBytes += int64(len(anc)) + 48
	}
	if w.pathBytes > w.lim.MaxPathBytes {
		return manifest.TooLarge("the entry names of the tarball take too much memory")
	}
	for _, anc := range implicit {
		w.kinds[anc] = kindImplicit
	}
	w.kinds[install] = k
	return nil
}

// readManifest reads package.json into memory, within the limits of R-ARCH-028.
func (w *walker) readManifest(hdr *tar.Header) error {
	if hdr.Size > w.lim.MaxManifestBytes {
		return manifest.TooLarge(fmt.Sprintf("package.json is larger than %d bytes", w.lim.MaxManifestBytes))
	}
	before := w.cr.n
	data := make([]byte, hdr.Size)
	if _, err := io.ReadFull(w.tr, data); err != nil {
		return err
	}
	used := w.cr.n - before
	if hdr.Size > int64(w.lim.MaxRatio)*used+ratioSlack {
		return manifest.TooLarge(fmt.Sprintf("package.json compresses by more than %d:1", w.lim.MaxRatio))
	}
	w.pkgJSON, w.pkgJSONSeen = data, true
	return nil
}

func isReadme(name string) bool {
	l := strings.ToLower(name)
	return l == "readme" || strings.HasPrefix(l, "readme.")
}

// readReadme keeps the first MaxReadmeBytes of the README.
func (w *walker) readReadme(hdr *tar.Header, name string) error {
	n := min(hdr.Size, int64(w.lim.MaxReadmeBytes))
	data := make([]byte, n)
	if _, err := io.ReadFull(w.tr, data); err != nil {
		return err
	}
	if n < hdr.Size {
		// the cut may fall inside a character: drop the partial one
		for i := len(data) - 1; i >= 0 && i >= len(data)-utf8.UTFMax; i-- {
			if utf8.RuneStart(data[i]) {
				if !utf8.FullRune(data[i:]) {
					data = data[:i]
				}
				break
			}
		}
	}
	w.readme = strings.ToValidUTF8(string(data), "�")
	w.readmeName = name
	return nil
}

func (w *walker) finish() (*Result, error) {
	if w.first == "" {
		return nil, bad("the tarball holds no entries")
	}
	if !w.pkgJSONSeen {
		return nil, bad("the tarball holds no %s/package.json", shorten(w.first))
	}
	if err := w.linkChains(); err != nil {
		return nil, err
	}
	m, err := manifest.Parse(w.pkgJSON, manifest.Limits{MaxBytes: w.lim.MaxManifestBytes, MaxDepth: w.lim.MaxManifestDepth})
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.Invalid {
			return nil, manifest.BadPublish("package.json is not valid JSON or not an object", errs.WithCause(err))
		}
		return nil, err
	}
	return &Result{
		Manifest:       m,
		Facts:          manifest.Facts{Files: w.files},
		FileCount:      w.fileCount,
		UnpackedSize:   w.unpacked,
		HasShrinkwrap:  w.shrinkwrap,
		Readme:         w.readme,
		ReadmeFilename: w.readmeName,
	}, nil
}

// shorten keeps an attacker-chosen name from filling an error message.
func shorten(s string) string {
	const max = 80
	if len(s) <= max {
		return strings.ToValidUTF8(s, "?")
	}
	return strings.ToValidUTF8(s[:max], "?") + "..."
}
