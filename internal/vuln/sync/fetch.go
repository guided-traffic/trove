package sync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // the bucket announces the MD5 of its objects; it is compared, not trusted for security
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
)

// osvURL is the object name below the base URL of the OSV source (R-VULN-003): <base>/<ecosystem>/<name>. The sync requests
// all.zip, modified_id.csv and <id>.json of the ecosystems it serves, and nothing else below the archive root.
func (s *Service) osvURL(ecosystem, name string) string {
	base := strings.TrimRight(s.o.Config.Sources.OSV.BaseURL, "/")
	return base + "/" + url.PathEscape(ecosystem) + "/" + url.PathEscape(name)
}

// get sends one GET with the headers and returns the response; a transport error is mapped to the run's problem codes
// (R-VULN-004 "Transport", R-ARCH-023).
func (s *Service) get(ctx context.Context, rawURL string, hdr ...string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, problemf(errs.CodeUpstreamError, "the source URL is not valid", redacted(err))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return nil, s.transportError(ctx, err)
	}
	return resp, nil
}

// transportError maps the error of a request or of reading a body to a problem code.
func (s *Service) transportError(ctx context.Context, err error) error {
	switch {
	case released(ctx):
		return context.Cause(ctx)
	case errors.Is(err, outbound.ErrEgressDenied):
		return problemf(errs.CodeEgressDenied, "the address policy refuses the source's address", redacted(err))
	case errors.Is(err, context.DeadlineExceeded):
		return problemf(errs.CodeUpstreamTimeout, "the source did not answer in time", redacted(err))
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return problemf(errs.CodeUpstreamUnreachable, "the source could not be reached or the transfer broke off", redacted(err))
}

// redactedError is a transport error without the URL it carries: the URL of a request may hold userinfo, a token in its path or a
// query, and the error ends in the error of a job, which readers of the jobs API see. Only scheme and host remain.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redacted returns err with the URL of a *url.Error reduced to scheme and host; an error that carries no URL is returned as it
// is. The text of the error inside is kept, and errors.Is and errors.As still reach it.
func redacted(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) {
		return err
	}
	where := "<invalid URL>"
	if u, perr := url.Parse(ue.URL); perr == nil && u.Host != "" {
		where = u.Scheme + "://" + u.Host
	}
	return &redactedError{msg: fmt.Sprintf("%s %q: %v", ue.Op, where, ue.Err), err: err}
}

// unexpected is the problem of a status the run does not expect.
func unexpected(what string, status int) error {
	return problemf(errs.CodeUpstreamUnexpectedStatus, fmt.Sprintf("%s answered %d", what, status), nil)
}

// googMD5 returns the MD5 the bucket announces in the x-goog-hash header lines. The bucket sends two lines, one per digest
// (crc32c and md5), so every line is read (R-VULN-004, the facts table). found is false when no line carries md5; a value that
// does not decode is found with a nil digest, which no content equals.
func googMD5(h http.Header) (sum []byte, found bool) {
	for _, line := range h.Values("X-Goog-Hash") {
		for _, part := range strings.Split(line, ",") {
			v, ok := strings.CutPrefix(strings.TrimSpace(part), "md5=")
			if !ok {
				continue
			}
			found = true
			if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == md5.Size {
				sum = b
			}
		}
	}
	return sum, found
}

// archive is a downloaded or mirrored OSV archive in the worker's spool.
type archive struct {
	sp   *blob.Spooled
	ra   io.ReaderAt
	size int64
	// ident identifies the bytes for the resumption rule: the ETag of the download, the hex SHA-256 of a mirrored item.
	ident string
	// etag is the ETag to store ("" for a mirrored item); sha256 is the digest of the received bytes.
	etag   string
	sha256 [sha256.Size]byte
}

func (a *archive) Close() {
	if a != nil && a.sp != nil {
		_ = a.sp.Close()
	}
}

// archiveTooLarge is the problem of an archive above max_archive_size; size is the length the source or the mirror declared, not
// positive when none is known (Q-VULN-121).
func archiveTooLarge(limit, size int64, cause error) error {
	detail := fmt.Sprintf("the archive is larger than vulnerabilities.sources.osv.max_archive_size (%d bytes)", limit)
	if size > 0 {
		detail += fmt.Sprintf(": %d bytes", size)
	}
	return problemf(errs.CodeArchiveTooLarge, detail, cause)
}

// spooled admits a body to the spool under the archive size limit and wraps it.
func (s *Service) spooled(ctx context.Context, body io.Reader, length int64) (*archive, error) {
	sp, err := s.o.Spool.Ingest(ctx, body, blob.IngestOptions{ContentLength: length, MaxSize: int64(s.o.Config.Sources.OSV.MaxArchiveSize)})
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.TooLarge {
			return nil, archiveTooLarge(int64(s.o.Config.Sources.OSV.MaxArchiveSize), length, err)
		}
		if e, ok := errs.As(err); ok {
			return nil, problemf(e.Code, "the archive could not be admitted to the spool: "+e.Detail, err)
		}
		return nil, s.transportError(ctx, err)
	}
	ra, ok := sp.Reader().(io.ReaderAt)
	if !ok {
		_ = sp.Close()
		return nil, problemf(errs.CodeInternal, "the spooled archive is not randomly readable", nil)
	}
	return &archive{sp: sp, ra: ra, size: sp.Size, sha256: sp.SHA256}, nil
}

// downloadArchive fetches <base>/<ecosystem>/all.zip with the conditional header of the stored ETag (R-VULN-004 full sync steps
// 1 and 2): a 304 is notModified; the body goes through the spool admission at most max_archive_size, and the MD5 of the
// received bytes must equal the one the bucket announces.
func (s *Service) downloadArchive(ctx context.Context, ecosystem, ifNoneMatch string) (a *archive, notModified bool, err error) {
	hdr := []string{"Accept", "application/zip"}
	if ifNoneMatch != "" {
		hdr = append(hdr, "If-None-Match", ifNoneMatch)
	}
	resp, err := s.get(ctx, s.osvURL(ecosystem, "all.zip"), hdr...)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, true, nil
	case http.StatusOK:
	default:
		return nil, false, unexpected("the archive", resp.StatusCode)
	}
	a, err = s.spooled(ctx, resp.Body, resp.ContentLength)
	if err != nil {
		return nil, false, err
	}
	if want, found := googMD5(resp.Header); found {
		if got := a.sp.MD5; want == nil || !bytes.Equal(got[:], want) {
			a.Close()
			return nil, false, problemf(errs.CodeArchiveDigestMismatch, "the MD5 of the received archive differs from the x-goog-hash header", nil)
		}
	}
	a.etag = resp.Header.Get("ETag")
	a.ident = a.etag
	if a.ident == "" {
		a.ident = fmt.Sprintf("sha256:%x", a.sha256)
	}
	return a, false, nil
}

// changeRow is one row of modified_id.csv.
type changeRow struct {
	Modified time.Time
	ID       string
}

// parseChangeRow reads "<RFC 3339 modified>,<id>"; ok is false for a row that does not parse.
func parseChangeRow(line []byte) (changeRow, bool) {
	line = bytes.TrimSpace(line)
	ts, id, found := bytes.Cut(line, []byte{','})
	if !found {
		return changeRow{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, string(ts))
	if err != nil || len(id) == 0 {
		return changeRow{}, false
	}
	return changeRow{Modified: t.UTC(), ID: string(id)}, true
}

// rowSink receives the rows of the change file, newest first, and says when to stop.
type rowSink struct {
	watermark time.Time
	rows      []changeRow
	bad       int
	// badLines are the first rows that did not parse, cut, so that the rejection log can name them (Q-VULN-119).
	badLines []string
	// limit is the number of rows after which the run turns full; 0 is none.
	limit int
	over  bool
	done  bool
}

// add takes one line; it sets done at the first row that is not newer than the watermark.
func (k *rowSink) add(line []byte) {
	if k.done || len(bytes.TrimSpace(line)) == 0 {
		return
	}
	r, ok := parseChangeRow(line)
	if !ok {
		k.bad++
		if len(k.badLines) < maxRejectLines {
			k.badLines = append(k.badLines, cut(string(bytes.TrimSpace(line))))
		}
		return
	}
	if !r.Modified.After(k.watermark) {
		k.done = true
		return
	}
	k.rows = append(k.rows, r)
	if k.limit > 0 && len(k.rows) > k.limit {
		k.over, k.done = true, true
	}
}

// changeRows reads the change file of an ecosystem from the start, in ranges of 64 KiB, until a row at or before the
// watermark (R-VULN-004 incremental step 1). A 200 where a range was asked for is read the same way: from its start, and the
// rest of the body is discarded once a row at or before the watermark is reached.
func (s *Service) changeRows(ctx context.Context, ecosystem string, watermark time.Time, limit int) (*rowSink, error) {
	sink := &rowSink{watermark: watermark, limit: limit}
	var carry []byte
	for offset := int64(0); !sink.done; {
		pctx, cancel := context.WithTimeout(ctx, PartTimeout)
		data, total, full, err := s.changePart(pctx, ecosystem, offset)
		cancel()
		if err != nil {
			return nil, err
		}
		if full {
			// A server that ignores Range sent the whole file: its rows replace what was read so far.
			sink.rows, sink.bad, sink.badLines, carry = nil, 0, nil, nil
			if err := scanLines(data, &carry, sink, true); err != nil {
				return nil, err
			}
			break
		}
		buf := append(carry, data...)
		carry = nil
		if err := scanLines(buf, &carry, sink, false); err != nil {
			return nil, err
		}
		offset += int64(len(data))
		if int64(len(data)) < RangeSize || (total > 0 && offset >= total) || len(data) == 0 {
			if !sink.done && len(carry) > 0 {
				sink.add(carry) // the last row has no newline
			}
			break
		}
	}
	return sink, nil
}

// scanLines feeds the complete lines of buf to the sink and leaves the incomplete last one in carry. The data of a full body
// is read through to its end.
func scanLines(buf []byte, carry *[]byte, sink *rowSink, last bool) error {
	for !sink.done {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		sink.add(buf[:i])
		buf = buf[i+1:]
	}
	if sink.done {
		return nil
	}
	if last {
		sink.add(buf)
		return nil
	}
	*carry = append([]byte(nil), buf...)
	return nil
}

// changePart requests bytes offset to offset+RangeSize-1 of the change file. It returns the data, the file's total size when
// the answer says it, and full when the answer is a 200 whose body was read from the start (bounded, up to the row the sink
// stops at).
func (s *Service) changePart(ctx context.Context, ecosystem string, offset int64) (data []byte, total int64, full bool, err error) {
	rng := fmt.Sprintf("bytes=%d-%d", offset, offset+RangeSize-1)
	resp, err := s.get(ctx, s.osvURL(ecosystem, "modified_id.csv"), "Range", rng, "Accept", "text/csv")
	if err != nil {
		return nil, 0, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		return nil, offset, false, nil
	case http.StatusPartialContent:
		start, _, tot, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			return nil, 0, false, problemf(errs.CodeUpstreamUnexpectedStatus, "the change file's Content-Range is not the range that was asked for", nil)
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, RangeSize+1))
		if rerr != nil {
			return nil, 0, false, s.transportError(ctx, rerr)
		}
		if len(b) > RangeSize {
			return nil, 0, false, problemf(errs.CodeUpstreamUnexpectedStatus, "the change file's part is larger than the range asked for", nil)
		}
		return b, tot, false, nil
	case http.StatusOK:
		// Whole body, bounded; the caller stops at the watermark row and the rest is never used.
		br := bufio.NewReaderSize(io.LimitReader(resp.Body, maxChangeFile), 64<<10)
		b, rerr := io.ReadAll(br)
		if rerr != nil {
			return nil, 0, false, s.transportError(ctx, rerr)
		}
		return b, int64(len(b)), true, nil
	}
	return nil, 0, false, unexpected("the change file", resp.StatusCode)
}

// parseContentRange reads "bytes a-b/total"; total is 0 for "*".
func parseContentRange(v string) (start, end, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, size, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, 0, false
	}
	a, b, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if start, err = strconv.ParseInt(a, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if end, err = strconv.ParseInt(b, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if size != "*" {
		if total, err = strconv.ParseInt(size, 10, 64); err != nil {
			return 0, 0, 0, false
		}
	}
	return start, end, total, true
}

// fetchRecord reads <base>/<ecosystem>/<id>.json. found is false for a 404 (left to the next full sync); tooLarge is true for a
// record above max_record_size, and size is then the length the response declared, 0 when it declared none. The body is counted
// in gate while it waits for the decoder: before it is read when the response declares its length, after it when it does not.
// The caller releases f.held once it has decoded the body.
func (s *Service) fetchRecord(ctx context.Context, ecosystem, id string, gate *byteGate) (f fetched) {
	f.id = id
	// The timeout of a record covers its request and the read of its body, not the wait at the gate: that wait is for the decoder,
	// which is the run's own, and it ends with the run's context.
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	begin := time.Now()
	var timedOut atomic.Bool
	timer := time.AfterFunc(s.partTimeout(), func() {
		timedOut.Store(true)
		cancel()
	})
	defer timer.Stop()
	fail := func(err error) fetched {
		if timedOut.Load() && ctx.Err() == nil {
			f.err = problemf(errs.CodeUpstreamTimeout, "the source did not answer in time", nil)
		} else {
			f.err = s.transportError(pctx, err)
		}
		return f
	}
	resp, err := s.get(pctx, s.osvURL(ecosystem, id+".json"), "Accept", "application/json")
	if err != nil {
		if timedOut.Load() && ctx.Err() == nil {
			return fail(err)
		}
		f.err = err
		return f
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return f
	default:
		f.err = unexpected("the record", resp.StatusCode)
		return f
	}
	limit := int64(s.o.Config.Sources.OSV.MaxRecordSize)
	if declared := min(max(resp.ContentLength, 0), limit+1); declared > 0 {
		timer.Stop()
		spent := time.Since(begin)
		if err := gate.acquire(ctx, declared); err != nil {
			f.err = s.transportError(ctx, err)
			return f
		}
		f.held = declared
		// The body read gets what is left of the timeout, so that the request and the read together take one timeout.
		timer.Reset(max(s.partTimeout()-spent, time.Millisecond))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		gate.release(f.held)
		f.held = 0
		return fail(err)
	}
	if extra := int64(len(b)) - f.held; extra > 0 {
		gate.add(extra)
		f.held += extra
	}
	f.found = true
	if int64(len(b)) > limit {
		f.tooLarge, f.size = true, max(resp.ContentLength, 0)
		return f
	}
	f.body = b
	return f
}

// partTimeout is the timeout of one record or one part of the change file.
func (s *Service) partTimeout() time.Duration {
	if s.partTimeoutOverride > 0 {
		return s.partTimeoutOverride
	}
	return PartTimeout
}

// fetchDocument reads a document with the conditional header and a size bound; notModified is true for a 304.
func (s *Service) fetchDocument(ctx context.Context, rawURL, ifNoneMatch string, max int64) (body []byte, etag string, notModified bool, err error) {
	hdr := []string{"Accept", "application/json"}
	if ifNoneMatch != "" {
		hdr = append(hdr, "If-None-Match", ifNoneMatch)
	}
	resp, err := s.get(ctx, rawURL, hdr...)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, "", true, nil
	case http.StatusOK:
	default:
		return nil, "", false, unexpected("the catalog", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, "", false, s.transportError(ctx, err)
	}
	if int64(len(b)) > max {
		return nil, "", false, problemf(errs.CodeArchiveTooLarge, fmt.Sprintf("the catalog is larger than %d bytes", max), nil)
	}
	return b, resp.Header.Get("ETag"), false, nil
}
