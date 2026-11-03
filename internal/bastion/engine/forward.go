package engine

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// maxDocumentBytes is the largest document the Bastion keeps (R-BAS-021).
const maxDocumentBytes = 64 << 20

// baseline is what a conditional request validated: the cache entry, or the caller's private copy.
type baseline struct {
	ent  *cache.Entry
	priv *privDoc
}

// forward sends the caller's request to the main installation with the caller's credential and handles the
// response (R-BAS-021). validator is the Bastion's own If-None-Match of a conditional request.
func (e *engine) forward(q *rq, kind mainclient.Kind, validator string, bl *baseline) {
	if !e.d.Link.Reachable() {
		e.unreachable(q)
		return
	}
	if v := e.d.Guard.Miss(q.ctx(), q.addr, guard.Credentialed(!q.cred.anonymous)); !v.Allowed {
		e.refuseVerdict(q, v)
		return
	}
	sent := e.now()
	// The request to the main installation does not end with the client: an artifact fill reads on at the
	// main installation's speed when the client is slow or gone (Q-BAS-123). Everything else ends with the client.
	hold := newHold(q.ctx())
	q.hold = hold
	resp, err := e.d.Main.Do(hold.ctx, mainclient.Forward{
		Request: e.forwardRequest(q), Class: e.budgetClass(q), Kind: kind, ClientAddress: q.clientText, IfNoneMatch: validator,
	})
	if err != nil {
		hold.release()
		e.mainError(q, err)
		return
	}
	defer func() {
		hold.release()
		_ = resp.Body.Close() // the body of a fill that took the response over is replaced by an empty one
	}()
	e.process(q, resp, sent, bl)
}

// mainHold is the context of a request to the main installation: it ends with the client's request unless a
// fill takes it over.
type mainHold struct {
	ctx    context.Context
	cancel context.CancelFunc
	stop   func() bool
	mu     sync.Mutex
	taken  bool
}

func newHold(client context.Context) *mainHold {
	ctx, cancel := context.WithCancel(context.WithoutCancel(client))
	return &mainHold{ctx: ctx, cancel: cancel, stop: context.AfterFunc(client, cancel)}
}

// release ends the request to the main installation with the client's, unless a fill took it over.
func (h *mainHold) release() {
	h.mu.Lock()
	taken := h.taken
	h.mu.Unlock()
	if !taken {
		h.stop()
		h.cancel()
	}
}

// take gives the request to the main installation to a fill: the client going away no longer cancels it. The
// returned function ends it. ok is false when the client went away before: the request is cancelled already.
func (h *mainHold) take() (end func(), ok bool) {
	h.mu.Lock()
	h.taken = true
	h.mu.Unlock()
	return h.cancel, h.stop()
}

// stripRequest are the request headers that are never forwarded (R-BAS-021): the cookie, the forwarded-for
// families the Bastion writes itself, the interface's own headers, the caller's conditionals and range (the
// Bastion's conditionals are its own), the hop-by-hop headers, and the NuGet API key, which is no read
// credential (R-BAS-019).
func stripRequest(h http.Header) {
	drop := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			drop[http.CanonicalHeaderKey(strings.TrimSpace(tok))] = true
		}
	}
	for k := range h {
		ck := http.CanonicalHeaderKey(k)
		switch {
		case ck == "Cookie", ck == "Forwarded", ck == "Range", ck == "X-Nuget-Apikey", ck == "Expect",
			strings.HasPrefix(ck, "X-Forwarded-"), strings.HasPrefix(ck, "Trove-"), strings.HasPrefix(ck, "If-"),
			hopByHop(ck), drop[ck]:
			delete(h, k)
		}
	}
}

// forwardRequest is the caller's request without the headers above and without a body.
func (e *engine) forwardRequest(q *rq) *http.Request {
	fr := q.r.Clone(q.ctx())
	fr.Header = q.r.Header.Clone()
	stripRequest(fr.Header)
	fr.Body, fr.ContentLength = nil, 0
	return fr
}

// process handles the main installation's response by its marking (R-BAS-021).
func (e *engine) process(q *rq, resp *mainclient.Response, sent time.Time, bl *baseline) {
	q.mainRequestID = resp.Header.Get("X-Request-Id")
	if !resp.Marked {
		// The answer of a proxy in front of the main installation.
		if resp.Status >= 500 {
			e.fail(q, mainclient.ErrUnreachable)
			return
		}
		e.passThrough(q, resp, nil, metrics.ClassPass, metrics.ResultMiss)
		return
	}
	m := resp.Marking
	e.recordAnswers(q, resp, sent)
	if m.Class == iface.ClassArtifact || m.Class == iface.ClassShared {
		// The Vary of the response decides the cache key everything below is kept under.
		e.learnVary(q, resp.Header)
	}
	if bl != nil && bl.priv != nil && m.Class != iface.ClassPrivate {
		// The main installation no longer filters a document for this caller: the copy is stale.
		e.priv.remove(privKey(q.cred.key, q.cacheKey))
	}
	switch m.Class {
	case iface.ClassArtifact:
		e.onArtifact(q, resp, sent, bl)
	case iface.ClassShared:
		e.onShared(q, resp, sent, bl)
	case iface.ClassPrivate:
		e.onPrivate(q, resp, sent, bl)
	case iface.ClassAbsent:
		e.onAbsent(q, resp, bl)
	default:
		e.onPass(q, resp, sent)
	}
}

// recordAnswers keeps the credential and shape answers a marked response carries (R-BAS-020) and counts the
// failed authentication of a credential the main installation called invalid (R-BAS-036).
func (e *engine) recordAnswers(q *rq, resp *mainclient.Response, sent time.Time) {
	m, ctx := resp.Marking, q.ctx()
	sub := m.Subject
	switch sub.Kind {
	case iface.SubjectToken:
		q.subject = sub
		if !q.cred.anonymous {
			e.answers.put(ctx, answers.Credential, q.cred.key, &answerVal{Answer: answerBase(answers.Credential, sent, sub, ""), Subject: sub})
		}
	case iface.SubjectInvalid:
		e.keepInvalid(q, sent)
	}
	if m.Read.Valid() && m.Repository == q.repo && (sub.Kind == iface.SubjectToken || sub.Kind == iface.SubjectAnonymous) {
		status := 0
		if m.Read == iface.ReadNone {
			status = resp.Status
		}
		e.answers.put(ctx, answers.Shape, shapeKey(q.cred.key, q.repo), &answerVal{Answer: answerBase(answers.Shape, sent, sub, q.repo), Read: m.Read, Status: status})
	}
}

// putURL keeps the URL answer of this credential and cache key: allowed, or the refusal.
func (e *engine) putURL(q *rq, sent time.Time, allowed bool, ref *refusal) {
	sub := q.subject
	if q.cred.anonymous {
		sub = iface.Subject{Kind: iface.SubjectAnonymous}
	}
	e.answers.put(q.ctx(), answers.URL, urlKey(q.cred.key, q.cacheKey), &answerVal{Answer: answerBase(answers.URL, sent, sub, q.repo), Allowed: allowed, Refusal: ref})
}

// passThrough replays the main installation's response to the caller: its status, headers and body, without
// the headers of the interface (R-BAS-021). pre holds bytes already read from the body.
func (e *engine) passThrough(q *rq, resp *mainclient.Response, pre []byte, class, result string) {
	q.finish(class, result)
	copyHeader(q.w.Header(), replayHeader(resp.Header))
	q.out.source = metrics.SourceMain
	q.w.WriteHeader(resp.Status)
	if q.r.Method == http.MethodHead || resp.Status == http.StatusNoContent || resp.Status == http.StatusNotModified {
		return
	}
	if len(pre) > 0 {
		if _, err := q.out.Write(pre); err != nil {
			return
		}
	}
	buf := make([]byte, 32<<10)
	began, sent := time.Now(), int64(len(pre))
	rc := http.NewResponseController(q.out)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(e.paceChunk)) // a chunk that the client does not take in time ends the transfer
			if _, werr := q.out.Write(buf[:n]); werr != nil {
				return
			}
			sent += int64(n)
			if e.tooSlow(began, sent) {
				// The budget slot is held until the body ends: a client that takes the body slower than the
				// floor does not keep it (R-BAS-025, Q-BAS-123).
				e.log.Warn("a pass-through transfer is cut: the client takes it slower than the floor", "bytes", sent, "after", time.Since(began).Round(time.Second).String())
				panic(http.ErrAbortHandler)
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// tooSlow reports whether a pass-through transfer has taken longer than the grace period and moved fewer bytes
// per second, on average since it began, than the floor.
func (e *engine) tooSlow(began time.Time, sent int64) bool {
	el := time.Since(began)
	return el > e.paceGrace && float64(sent) < el.Seconds()*float64(e.paceMinRate)
}

// onPass handles every other answer: nothing is kept but a refusal of the read (R-BAS-020). A 403 is kept. A
// 404 that is not marked absent is never kept, whether it answers a plain fetch or a conditional request: it may
// be a missing name or one the caller may not see, and replaying it only for names the Bastion holds would show a
// narrowed reader which hidden names are cached (R-BAS-024, Q-BAS-42, Q-BAS-124).
func (e *engine) onPass(q *rq, resp *mainclient.Response, sent time.Time) {
	if resp.Status >= http.StatusBadRequest {
		e.markVolatileFor(q) // a refusal is this caller's: the main installation answers another caller otherwise
	} else {
		e.markVolatile(q.cacheKey) // a search or another answer that is nobody's to share
	}
	keepable := resp.Status == http.StatusForbidden
	if q.r.Method == http.MethodGet && keepable {
		body, rest, whole := readBounded(resp.Body, maxRefusalBody)
		if whole {
			h := replayHeader(resp.Header)
			h.Del("Content-Length")
			e.putURL(q, sent, false, &refusal{Status: resp.Status, Header: h, Body: body})
		}
		e.passThroughRest(q, resp, body, rest, metrics.ClassPass)
		return
	}
	e.passThrough(q, resp, nil, metrics.ClassPass, metrics.ResultMiss)
}

// passThroughRest continues a pass-through after a prefix of the body was read.
func (e *engine) passThroughRest(q *rq, resp *mainclient.Response, pre []byte, rest io.Reader, class string) {
	resp.Body = readCloser{Reader: rest, Closer: resp.Body}
	e.passThrough(q, resp, pre, class, metrics.ResultMiss)
}

type readCloser struct {
	io.Reader
	io.Closer
}

// readBounded reads up to limit bytes of r. whole is true when the end of r was reached within the limit;
// otherwise rest continues after the bytes returned.
func readBounded(r io.Reader, limit int64) (body []byte, rest io.Reader, whole bool) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(body)) > limit {
		return body, r, false
	}
	return body, strings.NewReader(""), true
}

// onAbsent handles a 404 marked absent: kept per cache key for the negative period and replayed to full readers
// (R-BAS-024). It removes a cache entry the main installation says no longer exists (R-BAS-022).
func (e *engine) onAbsent(q *rq, resp *mainclient.Response, bl *baseline) {
	if bl != nil && bl.ent != nil {
		e.dropEntry(q.ctx(), q.cacheKey)
	}
	if q.r.Method != http.MethodGet || resp.Status != http.StatusNotFound {
		e.markVolatile(q.cacheKey)
		e.passThrough(q, resp, nil, metrics.ClassAbsent, metrics.ResultMiss)
		return
	}
	body, rest, whole := readBounded(resp.Body, maxRefusalBody)
	if whole {
		h := replayHeader(resp.Header)
		h.Del("Content-Length")
		e.negPut(q.ctx(), q.cacheKey, &negEntry{Repository: resp.Marking.Repository, StoredAt: e.now(), Refusal: refusal{Status: resp.Status, Header: h, Body: body}})
	} else {
		e.markVolatile(q.cacheKey)
	}
	e.passThroughRest(q, resp, body, rest, metrics.ClassAbsent)
}

// onPrivate handles a document filtered for the caller (R-BAS-023): the caller's last copy is kept in memory,
// validated with a conditional request on every use.
func (e *engine) onPrivate(q *rq, resp *mainclient.Response, sent time.Time, bl *baseline) {
	e.markVolatileFor(q) // the document is filtered for this caller; a full reader's fetch is shareable
	sub := q.subject
	if resp.Status == http.StatusNotModified && bl != nil && bl.priv != nil {
		_ = resp.Body.Close()
		d := *bl.priv
		d.ObtainedAt = sent
		e.privPut(q.cred.key, q.cacheKey, &d)
		e.servePrivate(q, &d, metrics.ResultChecked)
		return
	}
	if resp.Status == http.StatusNotModified && bl != nil && bl.ent != nil {
		// The Bastion's own condition named the shared entry's validator and the main installation answered 304 for
		// this caller: the caller's rendition is the entry's bytes (R-NPM-013's validators are derived from what the
		// caller may read). The entry is served as this caller's 200, without renewing it: it is not this caller's
		// to renew (R-BAS-023). Passing the 304 on would answer a request that carried no condition.
		_ = resp.Body.Close()
		if !e.serveEntry(q, *bl.ent, metrics.ResultChecked) {
			e.forward(q, mainclient.KindForward, "", nil)
		}
		return
	}
	limit := min(maxDocumentBytes, int64(e.cfg.Cache.PrivateDocumentsMaxBytes))
	if q.r.Method != http.MethodGet || resp.Status != http.StatusOK || limit <= 0 {
		e.passThrough(q, resp, nil, metrics.ClassPrivate, metrics.ResultMiss)
		return
	}
	body, rest, whole := readBounded(resp.Body, limit)
	if !whole {
		e.passThroughRest(q, resp, body, rest, metrics.ClassPrivate)
		return
	}
	if etag := resp.Header.Get("Etag"); etag != "" {
		d := &privDoc{Answer: answerBase(answers.URL, sent, sub, q.repo), Status: resp.Status, Header: replayHeader(resp.Header), Body: body, ETag: etag}
		e.privPut(q.cred.key, q.cacheKey, d)
	}
	e.serveBuffered(q, resp, body, metrics.ClassPrivate)
}

// serveBuffered answers from a document held in memory, with the conditionals of the caller: the document came
// from the main installation, so its bytes count as served from there.
func (e *engine) serveBuffered(q *rq, resp *mainclient.Response, body []byte, class string) {
	h := replayHeader(resp.Header)
	h.Del("Content-Length")
	src := bytesSource(h, body)
	src.label = metrics.SourceMain
	q.finish(class, metrics.ResultMiss)
	e.serveSource(q, src)
}

// onShared handles a document the same for every full reader (R-BAS-023): kept under its cache key.
func (e *engine) onShared(q *rq, resp *mainclient.Response, sent time.Time, bl *baseline) {
	if resp.Status == http.StatusNotModified && bl != nil && bl.ent != nil {
		_ = resp.Body.Close() // the slot is free while the cached bytes are served
		ent := *bl.ent
		ent.ValidatedAt = sent
		_ = e.saveEntry(q.ctx(), q.cacheKey, ent)
		if !e.serveEntry(q, ent, metrics.ResultChecked) {
			e.forward(q, mainclient.KindForward, "", nil)
		}
		return
	}
	if resp.Status == http.StatusNotModified && bl != nil && bl.ent == nil && bl.priv != nil {
		// The Bastion's own condition named the validator of the caller's private copy and the main installation
		// no longer filters the document for the caller: the copy is current, and it answers the caller, who sent
		// no condition, as a 200. The copy itself is gone (process); the shared entry is not this caller's to
		// renew (R-BAS-023, Q-BAS-107).
		_ = resp.Body.Close()
		src := bytesSource(bl.priv.Header, bl.priv.Body)
		if e.serveSource(q, src) {
			q.finish(metrics.ClassShared, metrics.ResultChecked)
			return
		}
	}
	if q.r.Method != http.MethodGet || resp.Status != http.StatusOK {
		e.passThrough(q, resp, nil, metrics.ClassShared, metrics.ResultMiss)
		return
	}
	limit := min(maxDocumentBytes, int64(e.cfg.Cache.DocumentsMaxBytes))
	body, rest, whole := readBounded(resp.Body, limit)
	if !whole {
		e.markVolatile(q.cacheKey)
		e.passThroughRest(q, resp, body, rest, metrics.ClassShared)
		return
	}
	key, keep := e.learnVary(q, resp.Header)
	if keep {
		e.storeDocument(q, key, resp, body, sent)
	} else {
		e.markVolatile(q.cacheKey)
	}
	e.serveBuffered(q, resp, body, metrics.ClassShared)
}

// storeDocument writes a shared document through a cache fill and its entry; a failure of the backend, counted
// by the cache, only means it is not kept (R-BAS-030).
func (e *engine) storeDocument(q *rq, key string, resp *mainclient.Response, body []byte, sent time.Time) {
	ctx := context.WithoutCancel(q.ctx())
	sha, err := e.keepBytes(ctx, cache.EntryDocument, body)
	if err != nil {
		return
	}
	h := keepHeader(resp.Header)
	_ = e.saveEntry(ctx, key, cache.Entry{
		Kind: cache.EntryDocument, SHA256: sha, Size: int64(len(body)), Header: h, Repository: resp.Marking.Repository,
		Status: resp.Status, ValidatedAt: sent,
	})
}

// contentAddress reads the SHA-256 of an artifact's strong ETag (R-STOR-012: ETag "<sha256 hex>").
func contentAddress(etag string) string {
	etag = strings.Trim(etag, `"`)
	if len(etag) != 64 {
		return ""
	}
	for i := 0; i < len(etag); i++ {
		c := etag[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return etag
}

// contentLength reads a response's Content-Length, -1 when absent or malformed.
func contentLength(h http.Header) int64 {
	n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}
