package engine

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	resp, err := e.d.Main.Do(q.ctx(), mainclient.Forward{
		Request: e.forwardRequest(q), Class: e.budgetClass(q), Kind: kind, ClientAddress: q.clientText, IfNoneMatch: validator,
	})
	if err != nil {
		e.mainError(q, err)
		return
	}
	defer resp.Body.Close()
	e.process(q, resp, sent, bl)
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
		e.onPass(q, resp, sent, bl)
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
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := q.out.Write(buf[:n]); werr != nil {
				return
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

// onPass handles every other answer: nothing is kept but a refusal of the read (R-BAS-020). A 403 is kept; a
// 404 only when it answers a conditional request for something the Bastion holds, because a 404 that answers a
// plain fetch may be a missing name, and a partial reader asking for one twice sends two requests (R-BAS-024,
// Q-BAS-80).
func (e *engine) onPass(q *rq, resp *mainclient.Response, sent time.Time, bl *baseline) {
	e.markVolatile(q.cacheKey)
	keepable := resp.Status == http.StatusForbidden || (resp.Status == http.StatusNotFound && bl != nil && (bl.ent != nil || bl.priv != nil))
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
	e.markVolatile(q.cacheKey)
	sub := q.subject
	if resp.Status == http.StatusNotModified && bl != nil && bl.priv != nil {
		_ = resp.Body.Close()
		d := *bl.priv
		d.ObtainedAt = sent
		e.privPut(q.cred.key, q.cacheKey, &d)
		e.servePrivate(q, &d, metrics.ResultChecked)
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
