package edge

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
)

// ServeOptions configure [ServeItem].
type ServeOptions struct {
	// Buffer is the copy buffer of the body (storage.download.buffer_size); a value
	// below 4,096 uses 4,096.
	Buffer int
	// Served, when set, runs after a body that started at byte 0 was written
	// completely: 200 with the whole file, or 206 from byte 0 (R-STOR-015). HEAD,
	// 304 and a range that starts later are not served in that sense.
	Served func()
}

// CacheControl is `private, no-cache` for an authenticated request and `no-cache`
// for an anonymous one; clients revalidate with the ETag (R-STOR-012).
func CacheControl(r *http.Request) string {
	if httpserver.PrincipalFrom(r.Context()).Anonymous {
		return "no-cache"
	}
	return "private, no-cache"
}

// ServeItem answers a GET or HEAD of a stored file (R-STOR-012, R-ARCH-007): the
// conditional headers and the range are evaluated from the item row alone, the
// body comes from open (the verifying reader of R-STOR-013), and every response
// that carries the file goes through the content writer, which sets the content
// headers of R-ARCH-007 and checks them again at the status line. The headers of
// the file are collected first; they go to a response of a status that carries the
// file (200, 206, 304) and never to a problem.
//
// The status line is written only after open succeeded, so an object that cannot
// be opened is a problem the client can read. A body that fails in the middle
// aborts the response (panic(http.ErrAbortHandler)): the client sees a truncated
// transfer, never a complete corrupted file (R-STOR-013). Content-Length is
// always set, so a response is never chunked.
func ServeItem(w http.ResponseWriter, r *http.Request, it items.Item, open func(*items.Range) (io.ReadCloser, error), o ServeOptions) {
	etag, modified := it.ETag(), it.ModifiedAt.Truncate(time.Second)
	hd := http.Header{}
	hd.Set("ETag", etag)
	hd.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	hd.Set("Accept-Ranges", "bytes")
	hd.Set("Cache-Control", CacheControl(r))
	hd.Set("Repr-Digest", reprDigest(r.Header, it.Digest[:], it.SHA512))
	hd.Set("Content-Type", it.MediaTypeOrDefault())
	content := func() http.ResponseWriter {
		cw := httpserver.ContentWriterFor(w, r, httpserver.ContentFile, fileName(it.Path))
		for k, v := range hd {
			cw.Header()[k] = v
		}
		return cw
	}

	switch evaluate(r, etag, modified) {
	case notModified:
		content().WriteHeader(http.StatusNotModified)
		return
	case preconditionFailed:
		httpserver.Problem(w, r, errs.E(errs.PreconditionFailed, errs.CodePreconditionFailed, "A precondition of the request does not hold."))
		return
	}
	start, length, rr := byteRange(r, etag, modified, it.Size)
	status := http.StatusOK
	switch rr {
	case unsatisfiable:
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(it.Size, 10))
		httpserver.Problem(w, r, errs.E(errs.RangeNotSatisfiable, errs.CodeRangeNotSatisfiable, "The requested range is not satisfiable."))
		return
	case partialContent:
		status = http.StatusPartialContent
		hd.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(start+length-1, 10)+"/"+strconv.FormatInt(it.Size, 10))
	default:
		start, length = 0, it.Size
	}
	hd.Set("Content-Length", strconv.FormatInt(length, 10))
	if r.Method == http.MethodHead {
		content().WriteHeader(status)
		return
	}
	var rng *items.Range
	if rr == partialContent {
		rng = &items.Range{Offset: start, Length: length}
	}
	body, err := open(rng)
	if err != nil { // before the status line: a problem the client can read
		httpserver.Problem(w, r, err)
		return
	}
	defer body.Close()
	cw := content()
	cw.WriteHeader(status)
	if _, err := io.CopyBuffer(cw, readerOnly{body}, make([]byte, max(o.Buffer, 4096))); err != nil {
		if r.Context().Err() != nil { // the client went away
			return
		}
		// The held-back bytes were not released, or the object store failed in the
		// middle of the body.
		httpserver.Logger(r.Context()).Error("a download was aborted", "path", it.Path, "error", err)
		panic(http.ErrAbortHandler)
	}
	if start == 0 && o.Served != nil {
		o.Served()
	}
}

// readerOnly hides WriterTo and ReaderFrom so that CopyBuffer uses the buffer.
type readerOnly struct{ io.Reader }

// fileName is the last segment of a canonical path, the name for
// Content-Disposition.
func fileName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
