package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// onArtifact handles a 200 or 304 of an artifact (R-BAS-022): a 304 serves the cached bytes and renews the
// entry and the caller's URL answer; a 200 is the new content, kept and served.
func (e *engine) onArtifact(q *rq, resp *mainclient.Response, sent time.Time, bl *baseline) {
	switch resp.Status {
	case http.StatusNotModified:
		_ = resp.Body.Close() // the slot is free while the cached bytes are served
		e.putURL(q, sent, true, nil)
		if bl != nil && bl.ent != nil {
			ent := *bl.ent
			ent.ValidatedAt = sent
			_ = e.saveEntry(q.ctx(), q.cacheKey, ent)
			if e.serveEntry(q, ent, metrics.ResultChecked) {
				return
			}
			// The bytes are gone: fetch them.
			e.forward(q, mainclient.KindForward, "", nil)
			return
		}
		e.passThrough(q, resp, nil, metrics.ClassArtifact, metrics.ResultMiss)
	case http.StatusOK:
		e.putURL(q, sent, true, nil)
		e.onArtifactBody(q, resp, sent)
	default:
		e.passThrough(q, resp, nil, metrics.ClassArtifact, metrics.ResultMiss)
	}
}

// onArtifactBody keeps the bytes of an artifact when it can be verified and is within the limits, and passes
// everything else through (R-BAS-021): a HEAD, an answer without Content-Length or above max_object_bytes, an
// ETag that is no content address.
func (e *engine) onArtifactBody(q *rq, resp *mainclient.Response, sent time.Time) {
	size := contentLength(resp.Header)
	sha := contentAddress(resp.Header.Get("Etag"))
	_, keepVary := varyNames(resp.Header)
	if q.r.Method != http.MethodGet || size < 0 || size > int64(e.cfg.Cache.MaxObjectBytes) || sha == "" || !keepVary {
		e.passThrough(q, resp, nil, metrics.ClassArtifact, metrics.ResultMiss)
		return
	}
	e.fillArtifact(q, resp, sha, size, sent)
}

// hasEntryHeaders reports whether the caller sent a header whose answer needs the whole representation first:
// Range, If-Range or If-None-Match (RFC 9110). Such a request is answered from the committed fill.
func hasEntryHeaders(r *http.Request) bool {
	return r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "" || r.Header.Get("If-None-Match") != ""
}

// fillArtifact streams an artifact into a cache fill while it streams to the caller, with the withheld tail of
// R-REPO-041 (R-BAS-021): the last 64 KiB reach the caller only after the SHA-256 equals the content address of
// the ETag and the fill is committed and its entry written. On a mismatch the transfer is aborted and nothing
// is kept. A request with Range or a conditional is answered from the fill after it was committed. The
// verification, the commit under the content address and the signed entry are the cache's (package 5); what is
// the engine's is the tail towards the client.
func (e *engine) fillArtifact(q *rq, resp *mainclient.Response, sha string, size int64, sent time.Time) {
	fill, err := e.d.Cache.NewFill(cache.EntryArtifact)
	if err != nil {
		e.passThrough(q, resp, nil, metrics.ClassArtifact, metrics.ResultMiss)
		return
	}
	defer fill.Abort()

	stream := !hasEntryHeaders(q.r)
	if !stream {
		fill.Keep() // the committed bytes answer the Range or conditional request, and a refused commit does not fail it
	}
	q.out.source = metrics.SourceMain
	if stream {
		q.finish(metrics.ClassArtifact, metrics.ResultMiss)
		copyHeader(q.w.Header(), replayHeader(resp.Header))
		q.w.WriteHeader(http.StatusOK)
	}
	var (
		pending  []byte
		total    int64
		spoolOK  = true
		clientOK = stream
	)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > size {
				e.abortFill(q, stream)
				return
			}
			if spoolOK {
				if _, werr := fill.Write(buf[:n]); werr != nil {
					spoolOK = false
				}
			}
			if clientOK {
				pending = append(pending, buf[:n]...)
				if len(pending) > withheldTail {
					cut := len(pending) - withheldTail
					if _, werr := q.out.Write(pending[:cut]); werr != nil {
						clientOK = false
					}
					pending = append(pending[:0], pending[cut:]...)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			e.abortFill(q, stream)
			return
		}
	}
	if total != size {
		e.log.Warn("the main installation sent another number of bytes than it announced", "sha256", sha, "got", total, "want", size)
		e.abortFill(q, stream)
		return
	}
	committed := false
	if spoolOK {
		err := fill.Commit(context.WithoutCancel(q.ctx()), sha)
		if errors.Is(err, cache.ErrMismatch) {
			e.log.Warn("the main installation sent bytes that do not match their content address", "sha256", sha)
			e.abortFill(q, stream)
			return
		}
		committed = err == nil
	}
	if committed {
		e.commitEntry(q, resp, sha, size, sent)
	}
	switch {
	case stream:
		if clientOK {
			_, _ = q.out.Write(pending)
		}
	case spoolOK:
		q.finish(metrics.ClassArtifact, metrics.ResultMiss)
		e.serveFill(q, fill, resp, size)
	default:
		e.fail(q, mainclient.ErrUnreachable)
	}
}

// abortFill ends a fill whose bytes cannot be trusted: nothing is kept. With the headers sent the connection
// is cut so the caller sees an incomplete transfer; before them it is an error answer.
func (e *engine) abortFill(q *rq, streamed bool) {
	if streamed {
		panic(http.ErrAbortHandler)
	}
	e.fail(q, mainclient.ErrUnreachable)
}

// commitEntry writes the signed entry of a committed artifact (R-BAS-021). A failure of the backend is counted
// by the cache and the fill is still served: the transfer is not a failed request (R-BAS-030).
func (e *engine) commitEntry(q *rq, resp *mainclient.Response, sha string, size int64, sent time.Time) {
	ctx := context.WithoutCancel(q.ctx())
	key, keep := e.learnVary(q, resp.Header)
	if !keep {
		return
	}
	h := keepHeader(resp.Header)
	if h.Get("Etag") == "" {
		h.Set("Etag", `"`+sha+`"`)
	}
	repo := resp.Marking.Repository
	if repo == "" {
		repo = q.repo
	}
	_ = e.saveEntry(ctx, key, cache.Entry{
		Kind: cache.EntryArtifact, SHA256: sha, Size: size, Header: h, Repository: repo, Status: http.StatusOK, ValidatedAt: sent,
	})
}

// serveFill answers a Range or conditional request from the committed fill.
func (e *engine) serveFill(q *rq, fill *cache.Fill, resp *mainclient.Response, size int64) {
	h := replayHeader(resp.Header)
	h.Del("Content-Length")
	src := &source{
		size: size, header: h, etag: h.Get("Etag"), label: metrics.SourceMain,
		open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(fill.Reader()), nil
		},
	}
	e.serveSource(q, src)
}

// keepBytes stores the bytes of a shared document through a cache fill and returns their content address.
func (e *engine) keepBytes(ctx context.Context, kind cache.EntryKind, body []byte) (string, error) {
	fill, err := e.d.Cache.NewFill(kind)
	if err != nil {
		return "", err
	}
	defer fill.Abort()
	if _, err := fill.Write(body); err != nil {
		return "", err
	}
	sum := fill.SHA256()
	return sum, fill.Commit(ctx, sum)
}
