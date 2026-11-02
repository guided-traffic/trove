package nupkg

import (
	"context"
	"fmt"
	"io"
)

// Head reads the first n bytes of one entry (fewer when the entry is shorter), for the
// check of a file signature that must not hold the whole entry in memory: a symbol file
// may be tens of megabytes (R-NUGET-020). The entry is held to the declaration of the
// archive like in [Archive.Read]: a declared compression ratio above Limits.MaxRatio is
// refused before a byte is inflated, and a stream that cannot be inflated within the bytes
// read is refused. The bytes read here are not counted against Limits.MaxReadBytes,
// because n is small and the caller bounds it. Not safe for concurrent use.
func (a *Archive) Head(ctx context.Context, e Entry, n int64) ([]byte, error) {
	if e.index < 0 || e.index >= len(a.entries) || a.entries[e.index].Name != e.Name {
		return nil, invalid(ErrEntry, "the entry is not one of this archive")
	}
	e = a.entries[e.index]
	if e.Dir {
		return nil, invalid(ErrEntry, "the entry has no content")
	}
	if n < 0 {
		n = 0
	}
	if e.Size > int64(a.lim.MaxRatio)*e.CompressedSize+ratioSlack {
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
	if n > e.Size {
		n = e.Size
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(&ctxReader{ctx: ctx, r: rc}, buf); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, invalid(ErrEntry, fmt.Sprintf("entry %q is damaged: %s", shorten(e.Name), shorten(err.Error())))
	}
	return buf, nil
}
