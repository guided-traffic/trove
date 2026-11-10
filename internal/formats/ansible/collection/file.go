package collection

import (
	"context"
	"errors"
	"io"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// ReadFile reads one member of a collection archive for a screen (the README of a version, R-ANS-026): the stream is read from its start
// until the member named name is found, which is then returned with at most max bytes; the rest of the archive is not read. A gzip stream
// cannot be read at an offset, so the cost is the inflation of what precedes the member, under the bounds of lim. found is false when the
// archive has no such member. The archive is not validated here: it was when it was published.
func ReadFile(ctx context.Context, r io.Reader, lim untrusted.Limits, name string, max int64) (data []byte, found bool, err error) {
	ctx, cancel := lim.Context(ctx)
	defer cancel()
	tr := untrusted.NewTarGz(ctx, r, lim)
	for {
		e, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) && !untrusted.IsRefused(err) {
				return nil, false, nil
			}
			return nil, false, fromStream(err)
		}
		if e.Dir {
			continue
		}
		if n, cerr := cleanName(e.Name); cerr != nil || n != name {
			continue
		}
		if e.Size > max {
			data, err = readPrefix(tr, max)
		} else {
			data, err = tr.ReadEntry(max)
		}
		if err != nil {
			return nil, false, fromStream(err)
		}
		return data, true, nil
	}
}

// readPrefix reads the first max bytes of the current entry.
func readPrefix(tr *untrusted.TarReader, max int64) ([]byte, error) {
	buf := make([]byte, max)
	n, err := io.ReadFull(tr, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}
