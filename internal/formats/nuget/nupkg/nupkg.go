package nupkg

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
)

// Entry is an entry of the archive as its central directory declares it, with
// the name rules of R-NUGET-005 step 2 applied.
type Entry struct {
	// Name is the name with backslashes read as slashes.
	Name string
	// Path is Name with percent escapes decoded once: the spelling a NuGet
	// client resolves a package part by. Names are unique by Path, compared
	// case-insensitively.
	Path string
	// Size and CompressedSize are the declared sizes.
	Size, CompressedSize int64
	// Dir is true for a directory entry.
	Dir bool

	index int
}

// Archive is an opened .nupkg: its structure has been checked and its entry
// names are unique and safe. It holds no file; reading an entry goes back to the
// io.ReaderAt it was opened on, which has to stay valid.
type Archive struct {
	zr      *zip.Reader
	lim     Limits
	entries []Entry
	byPath  map[string]int
	nuspec  int
	read    int64
}

// Open checks the archive in ra, size bytes long, within lim (R-NUGET-005
// steps 1 and 2): the central directory is scanned within its bounds, every
// local header name is compared with the central-directory name, the names are
// normalized and checked, and exactly one .nuspec at the root is located. No
// entry is inflated. Open is bounded by lim.Deadline as well as by ctx.
func Open(ctx context.Context, ra io.ReaderAt, size int64, lim Limits) (*Archive, error) {
	lim = lim.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, lim.Deadline)
	defer cancel()

	dir, err := scan(ctx, ra, size, lim)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, invalid(ErrNotZip, "the content is not a valid ZIP archive: "+shorten(err.Error()))
	}
	if err := dir.readBy(zr); err != nil {
		return nil, err
	}
	a := &Archive{zr: zr, lim: lim, byPath: make(map[string]int, len(dir.recs)), nuspec: -1}
	for i := range zr.File {
		r := dir.recs[i]
		name, path, isDir, err := normalize(r.name)
		if err != nil {
			return nil, err
		}
		key := foldKey(path)
		if _, dup := a.byPath[key]; dup {
			return nil, invalid(ErrDuplicateName, fmt.Sprintf("the name %q is used by two entries", shorten(r.name)))
		}
		a.byPath[key] = i
		a.entries = append(a.entries, Entry{Name: name, Path: path, Size: r.usize, CompressedSize: r.csize, Dir: isDir, index: i})
		if !isDir && !strings.Contains(path, "/") && strings.HasSuffix(strings.ToLower(path), ".nuspec") {
			if a.nuspec >= 0 {
				return nil, invalid(ErrNuspecCount, "the archive holds more than one .nuspec at its root")
			}
			a.nuspec = i
		}
	}
	if a.nuspec < 0 {
		return nil, invalid(ErrNuspecCount, "the archive holds no .nuspec at its root")
	}
	return a, nil
}

// readBy holds the standard library's reading of the archive, which opens the entries, to
// the scan's: two readers must see one archive, so every entry the standard library will
// serve has the scan's name, flags, method and sizes and starts its data at the offset the
// scan computed from the local header it checked (F-26, Q-NUGET-89). The offset is read
// through (*zip.File).DataOffset, one local header per entry.
func (d *directory) readBy(zr *zip.Reader) error {
	differ := invalid(ErrLocalMismatch, "the central directory is read differently by two readers")
	if len(zr.File) != len(d.recs) {
		return differ
	}
	for i, f := range zr.File {
		r := d.recs[i]
		usize, ok1 := fit(f.UncompressedSize64)
		csize, ok2 := fit(f.CompressedSize64)
		if f.Name != r.name || f.Flags != r.flags || f.Method != r.method || !ok1 || !ok2 || usize != r.usize || csize != r.csize {
			return differ
		}
		if off, err := f.DataOffset(); err != nil || off != r.data {
			return differ
		}
	}
	return nil
}

// Entries lists the entries in the order of the central directory.
func (a *Archive) Entries() []Entry { return a.entries }

// Lookup finds the entry a package part path names. The path is normalized and
// decoded like entry names and compared case-insensitively, which is
// unambiguous because Open refused names that differ only in case.
func (a *Archive) Lookup(p string) (Entry, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	if d, ok := unescape(p); ok {
		p = d
	}
	i, ok := a.byPath[foldKey(p)]
	if !ok {
		return Entry{}, false
	}
	return a.entries[i], true
}

// NuspecEntry is the one .nuspec at the archive root.
func (a *Archive) NuspecEntry() Entry { return a.entries[a.nuspec] }

// Read reads one entry into memory. max is the bound for this entry, the .nuspec
// bound when not positive. A declared size above it, a declared compression
// ratio above Limits.MaxRatio, or a total above Limits.MaxReadBytes is refused
// before a byte is inflated; the inflater is cut at the declared size, and a
// stream that disagrees with its declaration or fails its checksum is refused.
// Not safe for concurrent use.
func (a *Archive) Read(ctx context.Context, e Entry, max int64) ([]byte, error) {
	if e.index < 0 || e.index >= len(a.entries) || a.entries[e.index].Name != e.Name {
		return nil, invalid(ErrEntry, "the entry is not one of this archive")
	}
	e = a.entries[e.index] // the declaration of the archive, not the caller's copy
	if e.Dir {
		return nil, invalid(ErrEntry, "the entry has no content")
	}
	if max <= 0 {
		max = a.lim.MaxNuspecBytes
	}
	switch {
	case e.Size > max:
		return nil, invalid(ErrLimit, fmt.Sprintf("entry %q is larger than %d bytes", shorten(e.Name), max))
	case a.read+e.Size > a.lim.MaxReadBytes:
		return nil, invalid(ErrLimit, fmt.Sprintf("more than %d bytes would be read into memory", a.lim.MaxReadBytes))
	case e.Size > int64(a.lim.MaxRatio)*e.CompressedSize+ratioSlack:
		return nil, invalid(ErrLimit, fmt.Sprintf("entry %q compresses by more than %d:1", shorten(e.Name), a.lim.MaxRatio))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := a.zr.File[e.index].Open()
	if err != nil {
		return nil, invalid(ErrEntry, "the entry cannot be opened: "+shorten(err.Error()))
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(&ctxReader{ctx: ctx, r: rc}, e.Size+1))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, invalid(ErrEntry, fmt.Sprintf("entry %q is damaged: %s", shorten(e.Name), shorten(err.Error())))
	}
	if int64(len(data)) != e.Size {
		return nil, invalid(ErrEntry, fmt.Sprintf("entry %q is not as large as its header says", shorten(e.Name)))
	}
	a.read += e.Size
	return data, nil
}

// ctxReader fails a read after its context ended.
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

// Result is what Inspect returns.
type Result struct {
	// Archive is the opened archive, for further reads (embedded files).
	Archive *Archive
	// Metadata is the parsed .nuspec.
	Metadata *nuspec.Metadata
	// NuspecEntry is the entry the .nuspec was read from; its Name keeps the
	// spelling of the archive.
	NuspecEntry Entry
	// Nuspec is the .nuspec exactly as the archive holds it, stored byte for
	// byte as the generated item of R-NUGET-005.
	Nuspec []byte
}

// Inspect runs steps 1 to 3 of R-NUGET-005: it opens the archive (see Open),
// reads the .nuspec within Limits.MaxNuspecBytes and parses it. The rules for
// the id, the version and the dependency ranges (step 4) are the caller's: the
// metadata carries them as the document spells them. Inspect is bounded by
// lim.Deadline as well as by ctx.
func Inspect(ctx context.Context, ra io.ReaderAt, size int64, lim Limits) (*Result, error) {
	lim = lim.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, lim.Deadline)
	defer cancel()
	a, err := Open(ctx, ra, size, lim)
	if err != nil {
		return nil, err
	}
	e := a.NuspecEntry()
	if e.Size > lim.MaxNuspecBytes {
		return nil, invalid(nuspec.ErrTooLarge, fmt.Sprintf("the .nuspec is larger than %d bytes", lim.MaxNuspecBytes))
	}
	data, err := a.Read(ctx, e, lim.MaxNuspecBytes)
	if err != nil {
		return nil, err
	}
	md, err := nuspec.Parse(data, nuspec.Limits{MaxBytes: lim.MaxNuspecBytes})
	if err != nil {
		return nil, err
	}
	return &Result{Archive: a, Metadata: md, NuspecEntry: e, Nuspec: data}, nil
}

// IsRefusal reports whether err is a refusal of the package content, a 400 of
// this package or of nuspec.
func IsRefusal(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Kind == errs.Invalid && e.Code == nuspec.CodeInvalidPackage
}
