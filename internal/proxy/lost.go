package proxy

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
)

// A cached item whose object is missing from the object store, or failed verification, is a cache miss of its remote
// repository (storage.md R-STOR-027, ha-and-helm.md R-HA-024, Q-REPO-202): the item and its entry stay, the request fills it
// again from the upstream under steps 5 to 10 of R-REPO-025, and the commit of the same content writes the object again
// (R-STOR-005). The first read that finds the object gone marks its blob missing (blob.Store.Open), so every replica sees it
// in PostgreSQL from then on. A client's request that cannot fill the item again is answered 503 content_unavailable, never
// the 404 of an uncached item, a 500 or the upstream's 502 or 504: the item exists, and its content may come back.

// lostRetry is the Retry-After of a lost item that has no better one: the 30 s of any missing object (items.ContentError).
const lostRetry = 30 * time.Second

// changedRetry is the Retry-After of a document whose refill brought another digest than the request had read: the new
// document is stored already, and the next request reads it.
const changedRetry = time.Second

// refillKey marks the context of a refill of a document by the digest the request read: the bytes of that digest are not
// filled a second time under it, and the documents the flight reads besides (a discovery document) still are.
type refillKey struct{}

// lostContent is the answer for a client's request of a cached item whose object is missing or damaged and that the request
// could not fill again: 503 content_unavailable. why completes "The stored object of the item is missing and …"; the
// Retry-After is that of cause when it has one (an open circuit), else lostRetry. cause stays the error's cause for the log.
func lostContent(why string, cause error) *errs.Error {
	retry := lostRetry
	if ee, ok := errs.As(cause); ok && ee.RetryAfter > 0 {
		retry = ee.RetryAfter
	}
	opts := []errs.Option{errs.WithRetryAfter(retry)}
	if cause != nil {
		opts = append(opts, errs.WithCause(cause))
	}
	return errs.E(errs.Unavailable, errs.CodeContentUnavailable, "The stored object of the item is missing and "+why+"; retry later.", opts...)
}

// lostOr is the answer of a request that found nothing to serve and may not, or cannot, fetch: miss, the answer of an
// uncached item, unless the stored item is lost and the request is a client's. A mirror's pass keeps miss, whose outcomes
// it reads (an open circuit is a wait, R-REPO-054).
func lostOr(rq Request, lost bool, why string, miss error) error {
	if !lost || rq.background() {
		return miss
	}
	return lostContent(why, miss)
}

// storedObjectError maps an error of reading a stored document's object: a missing object is 503 content_unavailable,
// one that failed verification 500 content_unavailable, as for a file (items.Store.Open). Any other error stays.
func storedObjectError(err error) error {
	switch {
	case errors.Is(err, blob.ErrNotFound):
		return errs.E(errs.Unavailable, errs.CodeContentUnavailable, "The stored object of the document is missing; retry later.",
			errs.WithRetryAfter(lostRetry), errs.WithCause(err))
	case errors.Is(err, blob.ErrCorrupt):
		return errs.E(errs.Internal, errs.CodeContentUnavailable, "The stored object of the document failed verification.", errs.WithCause(err))
	}
	return err
}

// noteDamaged writes the log line of a cached item whose object is lost, at most once a minute per item and replica: the
// repository, the path and the digest (blob.Store logs the detection itself, with the counter
// trove_storage_blobs_damaged_total{status} that the alert TroveBlobMissing reads, R-HA-045).
func (e *Engine) noteDamaged(rq Request, digest [32]byte, status string) {
	key := rq.Remote.Repo.Key + "\x00" + rq.Path + "\x00" + rq.Variant + "\x00lost"
	if !e.throttle(key, time.Minute) {
		return
	}
	e.log.Warn("the stored object of a cached item is not usable; the item is a cache miss and is filled again from the upstream",
		"repo", rq.Remote.Repo.Key, "path", rq.Path, "sha256", hex.EncodeToString(digest[:]), "status", status)
}

// refillArtifact answers a client's request for a cached artifact whose object is missing or damaged (Q-REPO-202): a cache
// miss under steps 5 to 10 of R-REPO-025, fetched once per cluster and verified like any fill, whose commit writes the object
// of the recorded digest again. The commit keeps the recorded digest (R-REPO-041): an upstream that now serves other bytes
// repairs nothing, raises proxy.upstream_digest_changed, and the answer is the 503. Nothing streams to the leader's client
// while the fetch runs, so that every outcome is a clean answer; the leader is served from the stored item like the others.
func (e *Engine) refillArtifact(ctx context.Context, rq Request, w http.ResponseWriter, r *http.Request, it items.Item) error {
	rem := rq.Remote
	e.noteDamaged(rq, it.Digest, it.ContentStatus)
	e.m.lookup(rem.Repo.Key, Artifact, resultMiss)
	switch rq.Mode {
	case CacheView, OnlyIfCached:
		return lostContent("this request is answered from the cache alone", nil)
	}
	if e.offline(rem) {
		return lostContent(rem.Repo.Key+" is offline", nil)
	}
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return err
	}
	if open {
		return lostContent("the upstream is assumed to be offline", assumedOffline(retry))
	}
	if _, hit := e.neg.lookup(ctx, rem, rq.Path, rq.Variant); hit {
		return lostContent("the upstream does not have it", nil)
	}
	if !e.mayFill(ctx, rq) {
		return lostContent("the caller may not fill the cache of "+rem.Repo.Key, nil)
	}
	if err := e.o.Enforce.GateMiss(ctx, enforce.Miss{
		Repo: rem.Repo, Addressed: rq.Addressed, Package: rq.Package, Version: rq.Version, Path: rq.Path,
		Actor: auditActor(rq.Caller), Source: rq.Caller.Source, Endpoint: rq.Caller.Endpoint,
	}); err != nil {
		return err // the block of the vulnerability scanning answers as for any miss (R-VULN-015)
	}
	if r.Method == http.MethodHead {
		if err := e.head(ctx, rq, w); err != nil {
			return lostContent("the upstream could not serve it again", err)
		}
		return nil
	}
	st := &reqState{role: roleLeader}
	res, end := e.collapse(ctx, rq, st)
	if end == endGone {
		return ctx.Err()
	}
	if st.collapsed {
		e.m.collapsed.WithLabelValues(rem.Repo.Key).Inc()
	}
	fwd := CacheStatus{Forwarded: "uri-miss", FwdStatus: res.status}
	switch {
	case res.err != nil:
		if ee, ok := errs.As(res.err); ok && ee.Code == errs.CodeObjectStoreUnavail {
			return withStatus(res.err, fwd) // the store that lost the object cannot take it either: its own 503 (R-STOR-036)
		}
		return withStatus(lostContent("the upstream could not serve it again", res.err), fwd)
	case res.negative:
		return withStatus(lostContent("the upstream does not have it", nil), fwd)
	case !res.stored:
		return withStatus(errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail,
			"The artifact could not be stored again; retry.", errs.WithRetryAfter(5*time.Second)), fwd)
	}
	cs := CacheStatus{Forwarded: "uri-miss", FwdStatus: http.StatusOK, Stored: true}
	if st.collapsed {
		cs = CacheStatus{Hit: true, Collapsed: true, TTL: -1}
	}
	again, found, err := e.o.Items.Live(ctx, e.liveRef(rem.Repo, rq.Facts), rq.Path)
	switch {
	case err != nil:
		return err
	case !found:
		return errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist.")
	case items.ContentError(again) != nil:
		// The fill kept the recorded digest: the upstream serves other bytes now (R-REPO-041).
		return withStatus(lostContent("the upstream now serves other content for it", nil), cs)
	}
	return e.serveItem(ctx, rq, w, cs, again, false)
}

// refillBytes reads a stored document's bytes after its object turned out to be missing or damaged (Q-REPO-203): the read
// marked the blob, so the request path of R-REPO-025 counts the entry as a cache miss and fills it again, once. The bytes are
// served when the upstream's copy has the digest the request read (the document and its validators stay one document); a
// copy with another digest is stored, and the request is answered 503 with a Retry-After of a second, after which the next
// request reads the new document. A request that cannot fill it gets the 503 of [lostContent].
func (e *Engine) refillBytes(ctx context.Context, rq Request, restoredAt time.Time, want [32]byte) ([]byte, error) {
	ctx = context.WithValue(ctx, refillKey{}, want)
	doc, err := e.metadata(ctx, rq, restoredAt)
	switch {
	case err != nil:
		return nil, err
	case doc == nil || doc.Status.Negative || doc.Bytes == nil || !doc.GoneAt.IsZero():
		return nil, lostContent("the upstream no longer has it", nil)
	case doc.Digest != want:
		return nil, errs.E(errs.Unavailable, errs.CodeContentUnavailable,
			"The stored object of the document was missing; the upstream's copy, which is stored now, differs from it; retry.",
			errs.WithRetryAfter(changedRetry))
	}
	return doc.Bytes(ctx)
}

// refilling reports whether ctx is that of a refill of the document of digest d.
func refilling(ctx context.Context, d [32]byte) bool {
	v, ok := ctx.Value(refillKey{}).([32]byte)
	return ok && v == d
}
