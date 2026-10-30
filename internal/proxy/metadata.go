package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Metadata is the request path of R-REPO-025 for a metadata document, steps 4 to 10.
// Steps 1 to 3 (the repository is enabled, the patterns and claims admit the
// coordinate, the caller may read) belong to the dispatcher and the format's
// Authorize, which run before; the engine repeats only the cheap check of step 1.
//
// A nil error and a Document whose Status.Negative is set is the 404 of a negative
// entry; the format answers it. Every other refusal is an error.
func (e *Engine) Metadata(ctx context.Context, rq Request) (*Document, error) {
	return e.metadata(ctx, rq, time.Time{})
}

// EnsureCurrent serves the document like Metadata but counts a stored document as
// current only while it is fresh and was validated after restoredAt (Q-NPM-13); a
// stale one is revalidated under the steps of R-REPO-025 first. The currency says
// whether it was: Current (fresh), Revalidated, or Unknown, when the upstream could
// not be asked (offline, circuit, no right to fill, failure) and the stored copy is
// what there is.
func (e *Engine) EnsureCurrent(ctx context.Context, rq Request, restoredAt time.Time) (*Document, Currency, error) {
	doc, err := e.metadata(ctx, rq, restoredAt)
	if err != nil || doc == nil {
		return doc, Unknown, err
	}
	switch {
	case doc.Status.Stale:
		return doc, Unknown, nil
	case doc.Status.Forwarded != "":
		return doc, Revalidated, nil
	}
	return doc, Current, nil
}

func (e *Engine) metadata(ctx context.Context, rq Request, restoredAt time.Time) (*Document, error) {
	if err := e.validate(rq, Metadata); err != nil {
		return nil, err
	}
	rem := rq.Remote
	if blocked, err := e.goneGate(ctx, rq); err != nil || blocked != nil {
		return nil, firstError(err, blocked)
	}
	en, found, err := e.getEntry(ctx, rem.id, rq.Path, rq.Variant)
	if err != nil {
		return nil, err
	}
	if found && en.Gone() {
		found = false // counts as absent in every later step (R-REPO-025 step 4)
	}
	if found && e.fresh(rem, rq.Path, en, restoredAt) {
		e.m.lookup(rem.Repo.Key, Metadata, resultHit)
		return e.document(en, hitStatus(en.TTL)), nil
	}
	st := &reqState{role: roleLeader, restoredAt: restoredAt}
	if found {
		st.firstValidatedUs = en.ValidatedAt.UnixMicro()
	}

	// Step 5: the cache view and only-if-cached answer from storage.
	switch rq.Mode {
	case CacheView, OnlyIfCached:
		if found {
			e.m.lookup(rem.Repo.Key, Metadata, resultStale)
			return e.document(en, e.storedStatus(en)), nil
		}
		e.m.lookup(rem.Repo.Key, Metadata, resultMiss)
		return nil, notStored(rq.Mode)
	}
	// Step 6: offline.
	if e.offline(rem) {
		if found {
			e.m.lookup(rem.Repo.Key, Metadata, resultStale)
			return e.document(en, servedStale()), nil
		}
		return nil, offlineMiss(rem)
	}
	// Step 7: the circuit.
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return nil, err
	}
	if open {
		if found && e.withinMaxStale(rem, en) {
			e.m.lookup(rem.Repo.Key, Metadata, resultStale)
			return e.document(en, servedStale()), nil
		}
		return nil, withStatus(assumedOffline(retry), CacheStatus{})
	}
	// Step 8: the negative entry, the replica's L1 first and then Valkey.
	if _, hit := e.neg.lookup(ctx, rem, rq.Path, rq.Variant); hit {
		e.m.lookup(rem.Repo.Key, Metadata, resultNegative)
		return negativeDocument(false), nil
	}
	// Step 9: the cache-fill check.
	if !e.mayFill(ctx, rq) {
		if found {
			e.m.lookup(rem.Repo.Key, Metadata, resultStale)
			return e.document(en, servedStale()), nil
		}
		return nil, e.denyFill(ctx, rq)
	}
	// Step 10: the collapsing protocol and the fetch.
	e.m.lookup(rem.Repo.Key, Metadata, resultMiss)
	if found {
		st.refresh = time.Duration(rem.Settings.Cache.MetadataRefreshTimeoutSeconds) * time.Second
	}
	res, end := e.collapse(ctx, rq, st)
	return e.metadataAnswer(ctx, rq, st, en, found, res, end)
}

func firstError(err error, blocked *errs.Error) error {
	if err != nil {
		return err
	}
	return blocked
}

// metadataAnswer turns the flight's result into the request's answer: the document
// the flight stored, a negative entry, or the stored copy when the refresh failed or
// ran past the refresh timeout.
func (e *Engine) metadataAnswer(ctx context.Context, rq Request, st *reqState, en entry, found bool, res flightResult, end waitEnd) (*Document, error) {
	rem := rq.Remote
	switch end {
	case endGone:
		return nil, ctx.Err()
	case endTimeout:
		// The refresh runs on, detached; the client gets what is stored (R-REPO-027).
		e.m.lookup(rem.Repo.Key, Metadata, resultStale)
		return e.document(en, staleAfterFailure(0)), nil
	}
	if st.collapsed {
		e.m.collapsed.WithLabelValues(rem.Repo.Key).Inc()
	}
	switch {
	case res.err != nil:
		if found && e.withinMaxStale(rem, en) {
			e.m.lookup(rem.Repo.Key, Metadata, resultStale)
			return e.document(en, staleAfterFailure(res.status)), nil
		}
		return nil, withStatus(res.err, CacheStatus{Forwarded: "uri-miss", FwdStatus: res.status})
	case res.negative:
		return negativeDocument(st.collapsed), nil
	case res.doc != nil:
		d := *res.doc
		d.Status.Collapsed = st.collapsed
		return &d, nil
	}
	return nil, errs.E(errs.Unavailable, errs.CodeUnavailable, "The refresh left no document to serve; retry.", errs.WithRetryAfter(time.Second))
}

func negativeDocument(collapsed bool) *Document {
	return &Document{Status: CacheStatus{Hit: true, Negative: true, Detail: detailNegative, Collapsed: collapsed}}
}

// fresh applies the freshness rules (R-REPO-027): the period has not run out on the
// database clock, no invalidation covers the entry (R-REPO-045), and, for
// EnsureCurrent, the entry was validated after the restore.
func (e *Engine) fresh(rem Remote, path string, en entry, restoredAt time.Time) bool {
	if en.TTL <= 0 {
		return false
	}
	if e.o.Invalid != nil && !e.o.Invalid.MetadataValid(rem.id, path, en.ValidatedAt) {
		return false
	}
	return restoredAt.IsZero() || en.ValidatedAt.After(restoredAt)
}

// withinMaxStale applies cache.maxStaleSeconds (R-REPO-029): stale serving ends that
// long after the period ran out. 0 is unbounded.
func (e *Engine) withinMaxStale(rem Remote, en entry) bool {
	max := time.Duration(rem.Settings.Cache.MaxStaleSeconds) * time.Second
	return max <= 0 || -en.TTL <= max
}

// storedStatus is the status of a stored entry served from storage alone: a hit,
// and stale when its period has run out.
func (e *Engine) storedStatus(en entry) CacheStatus {
	if en.TTL > 0 {
		return hitStatus(en.TTL)
	}
	return servedStale()
}

// document builds the Document of a stored entry.
func (e *Engine) document(en entry, cs CacheStatus) *Document {
	d := &Document{
		Digest: en.Digest, Size: en.Size, FetchedAt: en.FetchedAt, ValidatedAt: en.ValidatedAt,
		ExpiresAt: en.ExpiresAt, GoneAt: en.GoneAt, Stale: cs.Stale, Status: cs,
	}
	digest, size := en.Digest, en.Size
	d.Bytes = func(ctx context.Context) ([]byte, error) { return e.bytesOf(ctx, digest, size) }
	return d
}

// bytesOf reads a stored document: the replica's cache by digest first, then the
// object store. The digest is the content address, so the cache can never make one
// replica answer differently from another (R-REPO-025).
func (e *Engine) bytesOf(ctx context.Context, d [32]byte, size int64) ([]byte, error) {
	if e.mdBytes != nil {
		if b, ok := e.mdBytes.Get(d); ok {
			return b, nil
		}
	}
	rc, err := e.o.Blobs.Open(ctx, blob.Digest(d), size, blob.OpenOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, size+1))
	if err != nil {
		return nil, err
	}
	e.keepBytes(d, b)
	return b, nil
}

// keepBytes puts a document into the bytes cache; one above an eighth of the budget
// is not kept.
func (e *Engine) keepBytes(d [32]byte, b []byte) {
	if e.mdBytes == nil || int64(len(b)) > int64(e.o.Config.MetadataCacheBytes)/8 {
		return
	}
	e.mdBytes.Set(d, b)
}

// notModified handles a 304: only the freshness moves, no body is transferred
// (R-REPO-027).
func (f *fetcher) notModified(ctx context.Context, resp *http.Response) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	e.circuitSuccess(ctx, rem)
	ttl := int64(rem.Settings.Cache.MetadataTTLSeconds)
	var renewed bool
	err := e.o.Runner.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rows, err := qs.ProxyMetadataRenew(ctx, q.ProxyMetadataRenewParams{
			RepoID: rem.id, Path: rq.Path, Variant: rq.Variant, TtlSeconds: ttl, UpstreamEtag: resp.Header.Get("ETag"),
		})
		renewed = len(rows) > 0
		return err
	})
	if err != nil {
		return flightResult{err: err}
	}
	if !renewed {
		return flightResult{err: errs.E(errs.Unavailable, errs.CodeUnavailable, "The stored document disappeared during its revalidation; retry.", errs.WithRetryAfter(time.Second))}
	}
	en, found, err := e.getEntry(ctx, rem.id, rq.Path, rq.Variant)
	if err != nil || !found {
		return flightResult{err: firstNonNil(err, errs.E(errs.Unavailable, errs.CodeUnavailable, "The stored document disappeared during its revalidation; retry."))}
	}
	cs := CacheStatus{Forwarded: "stale", FwdStatus: http.StatusNotModified}
	f.box.set(cs.Header())
	return flightResult{doc: e.document(en, cs), status: http.StatusNotModified}
}

func firstNonNil(a error, b error) error {
	if a != nil {
		return a
	}
	return b
}

// fillOutcome is what the fill transaction learned.
type fillOutcome struct {
	hadEntry  bool
	keptNewer bool
}

// metadata handles a 2xx for a document: the checks before the first byte, the
// transfer to the spool, the format's validation outside any transaction, the blob
// commit and the cache-fill transaction (R-REPO-025 step 10, R-ARCH-013).
func (f *fetcher) metadata(ctx context.Context, resp *http.Response, idle *atomic.Bool) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	if res, ok := f.admitBody(ctx, resp); !ok {
		return res
	}
	fetchedUs, err := e.dbNow(ctx)
	if err != nil {
		return flightResult{err: err}
	}
	// The upstream log record completes with the body, before this function returns.
	fwd := "uri-miss"
	if f.haveStored {
		fwd = "stale"
	}
	f.box.set(CacheStatus{Forwarded: fwd, FwdStatus: resp.StatusCode, Stored: true}.Header())
	sp, err := e.o.Items.Spool(ctx, f.bodyReader(resp, nil), items.IngestOptions{ContentLength: resp.ContentLength})
	if err != nil {
		return f.bodyFailure(ctx, err, idle)
	}
	defer func() { _ = sp.Close() }()
	e.circuitSuccess(ctx, rem)

	size := sp.Size
	ra, ok := sp.Section(0, size).(io.ReaderAt)
	if !ok {
		return flightResult{err: errs.E(errs.Internal, errs.CodeInternal, "The spooled document cannot be read at an offset.")}
	}
	prepared, err := rq.Adapter.PrepareFill(ctx, rq.Path, rq.Variant, ra, size)
	if err != nil {
		return flightResult{err: asUpstreamError(err), status: resp.StatusCode}
	}
	var out fillOutcome
	_, err = e.o.Blobs.Commit(ctx, sp, f.fillLink(resp, sp, prepared, fetchedUs, &out))
	if err != nil {
		if ee, ok := errs.As(err); ok && ee.Code == errs.CodeObjectStoreUnavail {
			return f.unstoredDocument(sp, resp)
		}
		return flightResult{err: err}
	}
	en, found, err := e.getEntry(ctx, rem.id, rq.Path, rq.Variant)
	if err != nil || !found {
		return flightResult{err: firstNonNil(err, errs.E(errs.Unavailable, errs.CodeUnavailable, "The stored document disappeared after its fill; retry."))}
	}
	if b, rerr := io.ReadAll(sp.Reader()); rerr == nil && en.Digest == [32]byte(sp.SHA256) {
		e.keepBytes(en.Digest, b)
	}
	cs := CacheStatus{Forwarded: "uri-miss", FwdStatus: resp.StatusCode, Stored: true}
	if out.hadEntry {
		cs.Forwarded = "stale"
	}
	f.box.set(cs.Header())
	return flightResult{doc: e.document(en, cs), status: resp.StatusCode}
}

// unstoredDocument serves a refreshed document that could not be stored because the
// object store is unavailable: it is served, the entries are not updated
// (R-REPO-025, R-STOR-036).
func (f *fetcher) unstoredDocument(sp *items.Spooled, resp *http.Response) flightResult {
	b, err := io.ReadAll(sp.Reader())
	if err != nil {
		return flightResult{err: err}
	}
	cs := CacheStatus{Forwarded: "uri-miss", FwdStatus: resp.StatusCode}
	f.e.log.Warn("the object store is unavailable: a refreshed document is served and not stored", "repo", f.rq.Remote.Repo.Key)
	d := &Document{Digest: sp.SHA256, Size: sp.Size, Status: cs}
	d.Bytes = func(context.Context) ([]byte, error) { return b, nil }
	return flightResult{doc: d, status: resp.StatusCode}
}

// bodyFailure maps an error of the transfer to the fetch's result.
func (f *fetcher) bodyFailure(ctx context.Context, err error, idle *atomic.Bool) flightResult {
	rem := f.rq.Remote
	switch {
	case errors.Is(err, errTooLargeBody):
		return flightResult{err: tooLarge(rem)}
	case idle.Load():
		return f.transportFailure(ctx, err, true)
	}
	if _, ok := errs.As(err); ok {
		return flightResult{err: err} // the spool's own refusal (R-STOR-004)
	}
	return f.transportFailure(ctx, err, false)
}

// fillLink is the link function of the blob commit: it runs in the cache-fill
// transaction (R-REPO-026 step 5). It locks the entry, lets the format write its
// rows, upserts the entry keeping the newer document, records the URL map, signals
// the change and writes the populate event, so that no fill exists without its
// event. It may run again after a restart of the transaction.
func (f *fetcher) fillLink(resp *http.Response, sp *items.Spooled, prepared Prepared, fetchedUs int64, out *fillOutcome) blob.LinkFunc {
	e, rq := f.e, f.rq
	rem := rq.Remote
	return func(ctx context.Context, tx meta.Tx) error {
		*out = fillOutcome{}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		locked, err := qs.ProxyEntryLock(ctx, q.ProxyEntryLockParams{RepoID: rem.id, Path: rq.Path, Variant: rq.Variant})
		if err != nil {
			return err
		}
		ev := FillEvent{
			Path: rq.Path, Variant: rq.Variant, Digest: sp.SHA256, Prepared: prepared, FetchedAt: micro(fetchedUs),
			ETag: resp.Header.Get("ETag"), LastModified: lastModified(resp.Header),
		}
		wasGone := false
		if len(locked) > 0 {
			l := locked[0]
			wasGone = l.GoneUs != 0
			out.hadEntry = !wasGone
			ev.Replaces = !wasGone
			ev.StoredAt = micro(l.FetchedUs)
			copy(ev.StoredDigest[:], l.BlobSha256)
		}
		keep, err := rq.Adapter.OnFill(ctx, tx, ev)
		if err != nil {
			return err
		}
		ttl := int64(rem.Settings.Cache.MetadataTTLSeconds)
		// keep renews the entry that is stored now, which a concurrent fill may have created
		// after this one read none; an entry that is there to renew decides, not Replaces.
		renewed := false
		if keep {
			rows, err := qs.ProxyMetadataRenew(ctx, q.ProxyMetadataRenewParams{
				RepoID: rem.id, Path: rq.Path, Variant: rq.Variant, TtlSeconds: ttl, UpstreamEtag: ev.ETag,
			})
			if err != nil {
				return err
			}
			renewed = len(rows) > 0
		}
		if !renewed {
			rows, err := qs.ProxyMetadataUpsert(ctx, q.ProxyMetadataUpsertParams{
				RepoID: rem.id, Path: rq.Path, Variant: rq.Variant, BlobSha256: sp.SHA256[:], UpstreamUrl: stripUser(f.finalURL(resp)),
				UpstreamEtag: ev.ETag, LastModifiedUs: unixMicro(ev.LastModified), FetchedUs: fetchedUs, TtlSeconds: ttl,
			})
			if err != nil {
				return err
			}
			out.keptNewer = len(rows) == 0 // a newer document is stored: keep it (R-REPO-026)
		}
		if wasGone {
			if err := e.notifyState(tx, rem.id); err != nil {
				return err
			}
		}
		// The map follows the document that is stored: a fill that renewed the stored one, or lost
		// to a newer row, leaves the URLs of that document alone (R-NPM-024, R-REPO-043). The
		// early eviction of merged documents is the format's: OnFill signals the package it
		// stored, and a signal of the engine would carry no package (R-REPO-020).
		if !renewed && !out.keptNewer {
			if err := f.recordMappings(ctx, qs, prepared); err != nil {
				return err
			}
		}
		if rq.Populate != nil {
			return rq.Populate.Record(ctx, tx)
		}
		return nil
	}
}

// finalURL is the URL the response came from, after redirects.
func (f *fetcher) finalURL(resp *http.Response) string {
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return f.target0().String()
}

func stripUser(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

func lastModified(h http.Header) time.Time {
	t, err := http.ParseTime(h.Get("Last-Modified"))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// recordMappings writes the URL-map rows of a document (R-REPO-043): only for paths
// that belong to the package the document was fetched for, and none when the URL is
// the one the format derives from url and the path anyway.
func (f *fetcher) recordMappings(ctx context.Context, qs *q.Queries, prepared Prepared) error {
	rq := f.rq
	rem := rq.Remote
	for _, m := range prepared.Mappings {
		if !rq.Adapter.OwnPath(rq.Package, m.Path) {
			f.e.log.Debug("a document names the artifact of another package; the entry is ignored", "repo", rem.Repo.Key, "package", rq.Package)
			continue
		}
		u, err := url.Parse(m.Upstream)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		u.User, u.Fragment = nil, ""
		if def, err := rq.Adapter.UpstreamRequest(ctx, UpstreamBase{URL: f.cl.Origin()}, m.Path, ""); err == nil && def.URL.String() == u.String() {
			if err := qs.ProxyUrlMapDelete(ctx, q.ProxyUrlMapDeleteParams{RepoID: rem.id, Path: m.Path}); err != nil {
				return err
			}
			continue
		}
		if err := qs.ProxyUrlMapPut(ctx, q.ProxyUrlMapPutParams{RepoID: rem.id, Path: m.Path, UpstreamUrl: u.String()}); err != nil {
			return err
		}
	}
	return nil
}
