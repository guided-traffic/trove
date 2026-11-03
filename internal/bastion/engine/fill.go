package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
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

// fillState is where a fill stands.
type fillState int

const (
	fillRunning fillState = iota
	// fillOK: the whole body arrived, its SHA-256 equals the content address and the commit was tried. The
	// withheld tail may be released.
	fillOK
	// fillFailed: the bytes cannot be trusted or could not be spooled; nothing is kept.
	fillFailed
)

// fillRun is the fill of an artifact (R-BAS-021, Q-BAS-123). It reads the main installation's body into the
// spool at the speed of the main installation, whatever the client does, and releases the budget slot when the
// body ends. The client of the request that started it, and nobody else, is served from the spool at its own
// speed ([engine.follow]): the bytes it may have are the ones written except for the withheld tail, which is
// released only after the digest equals the content address and the entry is written. The flight of the
// collapsed fetch ends at that point, so the waiters of other requests never wait for a client.
type fillRun struct {
	e    *engine
	fill *cache.Fill
	sha  string
	size int64
	// key and ent are the cache key and the entry written after a successful commit; key is empty when the
	// response must not be kept (it varies by something the Bastion cannot key on).
	key string
	ent cache.Entry

	mu      sync.Mutex
	written int64
	state   fillState
	notify  chan struct{}
	refs    int
}

// snapshot returns what a follower needs to decide, and the channel that is closed at the next change.
func (r *fillRun) snapshot() (written int64, st fillState, ch <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written, r.state, r.notify
}

func (r *fillRun) wake() {
	close(r.notify)
	r.notify = make(chan struct{})
}

func (r *fillRun) advance(n int64) {
	r.mu.Lock()
	r.written = n
	r.wake()
	r.mu.Unlock()
}

func (r *fillRun) finishWith(st fillState) {
	r.mu.Lock()
	if r.state == fillRunning {
		r.state = st
		r.wake()
	}
	r.mu.Unlock()
}

// release gives up a reference; the last one removes the spool file.
func (r *fillRun) release() {
	r.mu.Lock()
	r.refs--
	last := r.refs == 0
	r.mu.Unlock()
	if last {
		r.fill.Abort()
	}
}

// run reads the body to its end, verifies and commits. It runs once, in a goroutine of the engine (in the
// request's own while the engine drains). end cancels the request to the main installation; flightDone ends the
// flight of the collapsed fetch.
func (r *fillRun) run(body io.ReadCloser, end, flightDone func()) {
	e := r.e
	defer r.release()
	defer flightDone()
	defer end()
	stall := time.AfterFunc(e.fillStall, end)
	defer stall.Stop()
	defer body.Close()

	buf := make([]byte, 64<<10)
	var total int64
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			stall.Reset(e.fillStall)
			total += int64(n)
			if total > r.size {
				e.log.Warn("the main installation sent more bytes than it announced", "sha256", r.sha, "want", r.size)
				r.finishWith(fillFailed)
				return
			}
			if _, werr := r.fill.Write(buf[:n]); werr != nil {
				// The spool cannot take the bytes, so the transfer cannot be verified: it is cut, never
				// passed on unverified.
				e.log.Warn("the spool of a fill could not be written", "error", werr)
				r.finishWith(fillFailed)
				return
			}
			r.advance(total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			r.finishWith(fillFailed)
			return
		}
	}
	_ = body.Close() // the response is complete: the budget slot is free while the commit and the clients go on
	if total != r.size {
		e.log.Warn("the main installation sent another number of bytes than it announced", "sha256", r.sha, "got", total, "want", r.size)
		r.finishWith(fillFailed)
		return
	}
	ctx := context.Background()
	err := r.fill.Commit(ctx, r.sha)
	if errors.Is(err, cache.ErrMismatch) {
		e.log.Warn("the main installation sent bytes that do not match their content address", "sha256", r.sha)
		r.finishWith(fillFailed)
		return
	}
	if err == nil && r.key != "" {
		// A failure of the backend is counted by the cache and the fill is still served: the transfer is not
		// a failed request (R-BAS-030).
		_ = e.saveEntry(ctx, r.key, r.ent)
	}
	r.finishWith(fillOK)
}

// fillArtifact spools an artifact while it streams to the caller, with the withheld tail of R-REPO-041
// (R-BAS-021): the last 64 KiB reach the caller only after the SHA-256 equals the content address of the ETag
// and the fill is committed and its entry written. On a mismatch the transfer is aborted and nothing is kept. A
// request with Range or a conditional is answered from the fill after it was committed. The spooling runs
// without the client ([fillRun]); the verification, the commit under the content address and the signed entry
// are the cache's (package 5); what is the engine's is the tail towards the client.
func (e *engine) fillArtifact(q *rq, resp *mainclient.Response, sha string, size int64, sent time.Time) {
	fill, err := e.d.Cache.NewFill(cache.EntryArtifact)
	if err != nil {
		e.passThrough(q, resp, nil, metrics.ClassArtifact, metrics.ResultMiss)
		return
	}
	fill.Keep() // the committed bytes still answer the client, and a refused commit does not fail the request
	r := &fillRun{e: e, fill: fill, sha: sha, size: size, notify: make(chan struct{}), refs: 2}
	if key, keep := e.learnVary(q, resp.Header); keep {
		r.key, r.ent = key, e.artifactEntry(q, resp, sha, size, sent)
	}
	hdr := replayHeader(resp.Header)
	stream := !hasEntryHeaders(q.r)
	q.out.source = metrics.SourceMain
	if stream {
		q.finish(metrics.ClassArtifact, metrics.ResultMiss)
		copyHeader(q.w.Header(), hdr)
		q.w.WriteHeader(http.StatusOK)
	}
	// The fill takes over the response, the request to the main installation and the flight.
	body := resp.Body
	resp.Body = http.NoBody
	end := func() {}
	if q.hold != nil {
		end, _ = q.hold.take()
	}
	flightDone := q.flight.take()
	defer r.release()
	if !e.background(func() { r.run(body, end, flightDone) }) {
		r.run(body, end, flightDone) // the engine drains: the fill runs in the request, which releases its reference
	}
	if stream {
		e.follow(q, r)
		return
	}
	e.waitFill(q, r, hdr)
}

// artifactEntry is the signed entry of a committed artifact (R-BAS-021).
func (e *engine) artifactEntry(q *rq, resp *mainclient.Response, sha string, size int64, sent time.Time) cache.Entry {
	h := keepHeader(resp.Header)
	if h.Get("Etag") == "" {
		h.Set("Etag", `"`+sha+`"`)
	}
	repo := resp.Marking.Repository
	if repo == "" {
		repo = q.repo
	}
	return cache.Entry{
		Kind: cache.EntryArtifact, SHA256: sha, Size: size, Header: h, Repository: repo, Status: http.StatusOK, ValidatedAt: sent,
	}
}

// follow serves the client of the request from the spool as it grows: everything but the withheld tail while the
// fill runs, the tail once it is verified. The client's speed decides nothing but its own pace. A fill that
// failed cuts the transfer so the caller sees an incomplete response, never a complete one with wrong bytes.
func (e *engine) follow(q *rq, r *fillRun) {
	buf := make([]byte, 32<<10)
	var off int64
	for {
		written, st, ch := r.snapshot()
		if st == fillFailed {
			panic(http.ErrAbortHandler)
		}
		limit := written
		if st == fillRunning {
			limit = max(0, written-withheldTail)
		}
		for off < limit {
			n, err := r.fill.Reader().ReadAt(buf[:min(int64(len(buf)), limit-off)], off)
			if n > 0 {
				if _, werr := q.out.Write(buf[:n]); werr != nil {
					return
				}
				off += int64(n)
			}
			if err != nil && !errors.Is(err, io.EOF) {
				panic(http.ErrAbortHandler)
			}
		}
		if st == fillOK && off >= r.size {
			return
		}
		select {
		case <-ch:
		case <-q.ctx().Done():
			return
		}
	}
}

// waitFill answers a request with a Range or a conditional header from the committed fill.
func (e *engine) waitFill(q *rq, r *fillRun, hdr http.Header) {
	for {
		_, st, ch := r.snapshot()
		switch st {
		case fillFailed:
			e.fail(q, mainclient.ErrUnreachable)
			return
		case fillOK:
			q.finish(metrics.ClassArtifact, metrics.ResultMiss)
			e.serveFill(q, r, hdr)
			return
		}
		select {
		case <-ch:
		case <-q.ctx().Done():
			q.finish(metrics.ClassPass, metrics.ResultError)
			return
		}
	}
}

// serveFill answers a Range or conditional request from the committed fill.
func (e *engine) serveFill(q *rq, r *fillRun, hdr http.Header) {
	h := hdr.Clone()
	h.Del("Content-Length")
	src := &source{
		size: r.size, header: h, etag: h.Get("Etag"), label: metrics.SourceMain,
		open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(r.fill.Reader()), nil
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
