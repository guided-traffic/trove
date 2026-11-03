package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// withheldTail is R-REPO-041's withheld tail towards the client: the last 64 KiB of what the caller receives
// are released only after the whole object was read and verified (by the cache's verifying reader for the
// bytes it holds, by the fill's digest for the bytes of a miss), so a corrupt object never reaches the caller
// as a complete transfer (R-BAS-021, R-BAS-032). A range is verified by reading the object to its end, so the
// range's tail is withheld the same way.
const withheldTail = 64 << 10

// source is a representation the engine serves with the semantics of RFC 9110: a cache entry's bytes, a
// private copy, or a fill that was committed.
type source struct {
	size   int64
	header http.Header
	etag   string
	// open returns the whole representation from its start.
	open func(ctx context.Context) (io.ReadCloser, error)
	// download is set for an artifact: a 200 or 206 served from the cache is a download event (R-BAS-016).
	download func(q *rq)
	// label is the source of the bytes counted in trove_bastion_bytes_served_total: cache (the default) or main.
	label string
}

// bytesSource is a representation held in memory.
func bytesSource(header http.Header, body []byte) *source {
	return &source{
		size: int64(len(body)), header: header, etag: header.Get("Etag"),
		open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil },
	}
}

// serveSource answers the request from a source: If-None-Match, Range with If-Range, HEAD (RFC 9110). It
// returns false, with nothing written, when the bytes cannot be opened, so the caller treats the entry as gone.
func (e *engine) serveSource(q *rq, src *source) bool {
	hdr := http.Header{}
	for k, vs := range src.header {
		hdr[k] = append([]string(nil), vs...)
	}
	hdr.Set("Accept-Ranges", "bytes")
	if inm := q.r.Header.Get("If-None-Match"); inm != "" && src.etag != "" && etagMatches(inm, src.etag) {
		e.notModified(q, hdr)
		return true
	}
	start, end, partial, unsat := parseRange(q.r.Header.Get("Range"), q.r.Header.Get("If-Range"), src.etag, src.size)
	if unsat {
		q.w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(src.size, 10))
		q.w.Header().Set("Cache-Control", "no-store")
		httpserver.Problem(q.w, q.r, errs.E(errs.RangeNotSatisfiable, errs.CodeRangeNotSatisfiable, "The range lies beyond the end of the representation."))
		return true
	}
	status, length := http.StatusOK, src.size
	if partial {
		status, length = http.StatusPartialContent, end-start+1
		hdr.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(src.size, 10))
	}
	hdr.Set("Content-Length", strconv.FormatInt(length, 10))
	if q.r.Method == http.MethodHead {
		copyHeader(q.w.Header(), hdr)
		q.w.WriteHeader(status)
		return true
	}
	rc, err := src.open(q.ctx())
	if err != nil {
		return false
	}
	defer rc.Close()
	copyHeader(q.w.Header(), hdr)
	q.w.WriteHeader(status)
	q.out.source = metrics.SourceCache
	if src.label != "" {
		q.out.source = src.label
	}
	if e.streamVerified(q, rc, src, start, end) && src.download != nil {
		src.download(q)
	}
	return true
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
}

// notModified answers 304 with the headers RFC 9110 section 15.4.5 asks for.
func (e *engine) notModified(q *rq, hdr http.Header) {
	h := q.w.Header()
	for _, k := range []string{"Etag", "Cache-Control", "Expires", "Vary"} {
		if vs := hdr.Values(k); len(vs) > 0 {
			h[k] = append([]string(nil), vs...)
		}
	}
	q.w.WriteHeader(http.StatusNotModified)
}

// etagMatches is the weak comparison of RFC 9110 section 13.1.2 for If-None-Match.
func etagMatches(inm, etag string) bool {
	etag = strings.TrimPrefix(etag, "W/")
	for _, c := range strings.Split(inm, ",") {
		c = strings.TrimSpace(c)
		if c == "*" || strings.TrimPrefix(c, "W/") == etag {
			return true
		}
	}
	return false
}

// parseRange reads a single byte range of a Range header (RFC 9110 section 14). A Range that is not honoured
// (another unit, several ranges, a failed If-Range, a malformed value) is ignored and the whole representation
// served; unsat reports a range no byte of which exists.
func parseRange(rng, ifRange, etag string, size int64) (start, end int64, partial, unsat bool) {
	if rng == "" || !strings.HasPrefix(rng, "bytes=") || strings.Contains(rng, ",") {
		return 0, size - 1, false, false
	}
	if ifRange != "" && (strings.HasPrefix(ifRange, "W/") || ifRange != etag) {
		return 0, size - 1, false, false
	}
	first, last, ok := strings.Cut(strings.TrimPrefix(rng, "bytes="), "-")
	if !ok {
		return 0, size - 1, false, false
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	switch first {
	case "":
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, size - 1, false, err == nil
		}
		if n > size {
			n = size
		}
		if size == 0 {
			return 0, 0, false, true
		}
		return size - n, size - 1, true, false
	default:
		s, err := strconv.ParseInt(first, 10, 64)
		if err != nil || s < 0 {
			return 0, size - 1, false, false
		}
		en := size - 1
		if last != "" {
			v, err := strconv.ParseInt(last, 10, 64)
			if err != nil || v < s {
				return 0, size - 1, false, false
			}
			en = min(v, size-1)
		}
		if s >= size {
			return 0, 0, false, true
		}
		return s, en, true, false
	}
}

// streamVerified copies the object to the caller. r is the cache's verifying reader (or a document held in
// memory), which fails with cache.ErrCorrupt instead of releasing its own tail when the digest or the length
// do not match; the bytes of the range [start, end] are written as they arrive except for the last
// withheldTail of them, which are released only after the whole object was read. A failed read removes nothing
// here (the cache did) and aborts the transfer: the caller sees an incomplete response, never a complete one
// with wrong bytes. It reports whether the whole range was delivered.
func (e *engine) streamVerified(q *rq, r io.Reader, src *source, start, end int64) bool {
	buf := make([]byte, 32<<10)
	var pending []byte
	var pos int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if lo, hi := max(pos, start), min(pos+int64(n), end+1); lo < hi {
				pending = append(pending, chunk[lo-pos:hi-pos]...)
				if len(pending) > withheldTail {
					cut := len(pending) - withheldTail
					if _, werr := q.out.Write(pending[:cut]); werr != nil {
						return false
					}
					pending = append(pending[:0], pending[cut:]...)
				}
			}
			pos += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			e.log.Warn("cached bytes could not be served", "error", err, "corrupt", errors.Is(err, cache.ErrCorrupt))
			panic(http.ErrAbortHandler)
		}
	}
	if pos != src.size {
		e.d.Metrics.CacheCorrupt.Inc()
		e.log.Warn("cached bytes have another length than their entry", "got", pos, "want", src.size)
		panic(http.ErrAbortHandler)
	}
	_, err := q.out.Write(pending)
	return err == nil
}

// downloadEvent adds the event of an artifact served from the cache to the download buffer (R-BAS-016): a hit,
// or a check answered 304.
func (e *engine) downloadEvent(q *rq, repo, sha string) {
	if q.r.Method != http.MethodGet {
		return
	}
	e.d.Downloads.Add(iface.DownloadEvent{
		At: e.now(), TokenID: q.subject.TokenID, PrincipalID: q.subject.PrincipalID, ClientAddress: q.clientText,
		Repository: repo, Path: q.r.URL.Path, SHA256: sha, UserAgent: q.r.UserAgent(), Replica: e.d.Replica,
	})
}
