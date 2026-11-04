package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/servefacts"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
)

// errDigestChanged is returned by the fill transaction of an artifact whose entry
// already holds other content: the upsert refuses a different digest (R-REPO-041).
var errDigestChanged = errors.New("proxy: the entry holds a different digest")

// Artifact is the request path of R-REPO-025 for an artifact: a stored artifact is
// served, a missing one is fetched once per cluster under the collapsing protocol,
// verified, stored as an item of the repository and then served. The response is
// written to w; Range and conditional requests are served from the stored item
// (rq.Serve). An error is returned only before the status line; once the leader has
// started streaming, a failure aborts the connection with http.ErrAbortHandler so
// that no client completes an unverified download (R-REPO-041).
func (e *Engine) Artifact(ctx context.Context, rq Request, w http.ResponseWriter, r *http.Request) error {
	if err := e.validate(rq, Artifact); err != nil {
		return err
	}
	if rq.Serve == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "proxy: an artifact request needs a Serve function")
	}
	rem := rq.Remote
	if blocked, err := e.goneGate(ctx, rq); err != nil || blocked != nil {
		return firstError(err, blocked)
	}
	it, state, err := e.lookupHit(ctx, rq, true)
	if err != nil {
		return err
	}
	switch state {
	case hitStored: // step 4: an artifact entry is served
		e.m.lookup(rem.Repo.Key, Artifact, resultHit)
		return e.serveItem(ctx, rq, w, CacheStatus{Hit: true, TTL: -1}, it, true)
	case hitEntryOnly:
		e.m.lookup(rem.Repo.Key, Artifact, resultHit)
		return errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist.")
	}
	// Step 5.
	switch rq.Mode {
	case CacheView, OnlyIfCached:
		e.m.lookup(rem.Repo.Key, Artifact, resultMiss)
		return notStored(rq.Mode)
	}
	// Step 6.
	if e.offline(rem) {
		return offlineMiss(rem)
	}
	// Step 7.
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return err
	}
	if open {
		return assumedOffline(retry)
	}
	// Step 8.
	if _, hit := e.neg.lookup(ctx, rem, rq.Path, rq.Variant); hit {
		e.m.lookup(rem.Repo.Key, Artifact, resultNegative)
		return withStatus(errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist upstream.", errs.WithAbsent()),
			CacheStatus{Hit: true, Negative: true, Detail: detailNegative})
	}
	// Step 9.
	if !e.mayFill(ctx, rq) {
		return e.denyFill(ctx, rq)
	}
	// The remote miss of the vulnerability scanning (R-VULN-015): the version the path maps to is evaluated before anything
	// is fetched; a blocked version is not fetched, not stored and not served, and no upstream is contacted for it.
	if err := e.o.Enforce.GateMiss(ctx, enforce.Miss{
		Repo: rem.Repo, Addressed: rq.Addressed, Package: rq.Package, Version: rq.Version, Path: rq.Path,
		Actor: auditActor(rq.Caller), Source: rq.Caller.Source, Endpoint: rq.Caller.Endpoint,
	}); err != nil {
		return err
	}
	e.m.lookup(rem.Repo.Key, Artifact, resultMiss)
	if r.Method == http.MethodHead {
		return e.head(ctx, rq, w)
	}
	// Step 10.
	st := &reqState{role: roleLeader}
	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		st.stream = newStream(w)
	}
	res, end := e.collapse(ctx, rq, st)
	if st.stream != nil {
		st.stream.detach() // the handler is about to return: nothing writes to w afterwards
	}
	return e.artifactAnswer(ctx, rq, st, w, res, end)
}

// artifactAnswer ends the request: the leader's own client has its response already
// (and an incomplete one is aborted), the others are served from the stored entry.
func (e *Engine) artifactAnswer(ctx context.Context, rq Request, st *reqState, w http.ResponseWriter, res flightResult, end waitEnd) error {
	rem := rq.Remote
	started, done := false, false
	if st.stream != nil {
		started, done = st.stream.state()
	}
	if end == endGone {
		return ctx.Err()
	}
	if done {
		return nil
	}
	if started {
		// The status line is out and the entry is not: the fetch failed, was refused
		// or lost to a duplicate flight. Nothing may complete.
		panic(http.ErrAbortHandler)
	}
	if st.collapsed {
		e.m.collapsed.WithLabelValues(rem.Repo.Key).Inc()
	}
	cs := CacheStatus{Hit: true, Collapsed: st.collapsed, TTL: -1}
	switch {
	case res.err != nil:
		return withStatus(res.err, CacheStatus{Forwarded: "uri-miss", FwdStatus: res.status})
	case res.negative:
		return withStatus(errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist upstream.", errs.WithAbsent()),
			CacheStatus{Hit: true, Negative: true, Detail: detailNegative, Collapsed: st.collapsed})
	case res.stored:
		if !st.collapsed {
			cs = CacheStatus{Forwarded: "uri-miss", FwdStatus: http.StatusOK, Stored: true}
		}
		return e.serveStored(ctx, rq, w, cs)
	}
	return errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail,
		"The artifact could not be stored and a ranged request cannot be answered from the transfer; retry.", errs.WithRetryAfter(5*time.Second))
}

// serveStored serves the item a fill stored, with the status header.
func (e *Engine) serveStored(ctx context.Context, rq Request, w http.ResponseWriter, cs CacheStatus) error {
	it, found, err := e.o.Items.Live(ctx, e.liveRef(rq.Remote.Repo, rq.Facts), rq.Path)
	if err != nil {
		return err
	}
	if !found {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist.")
	}
	return e.serveItem(ctx, rq, w, cs, it, false)
}

// serveItem serves the item of a stored artifact with the status header. cached says the request read the item from the cache:
// the announced digests are then compared with the stored ones (R-REPO-041).
func (e *Engine) serveItem(ctx context.Context, rq Request, w http.ResponseWriter, cs CacheStatus, it items.Item, cached bool) error {
	if cached {
		e.checkAnnounced(ctx, rq, it)
	}
	w.Header().Set("Cache-Status", cs.Header())
	return rq.Serve(ctx, w, it)
}

// hitState is what the lookup of a cached artifact found.
type hitState uint8

const (
	// hitNone: no cache entry, or no item for it to serve with: the request misses.
	hitNone hitState = iota
	// hitStored: the cache entry and its item.
	hitStored
	// hitEntryOnly: the entry exists and the item it names does not.
	hitEntryOnly
)

// lookupHit reads the cached artifact of a request in one statement (R-ARCH-027): the item, whether its cache entry exists, the
// verdict of the vulnerability scanning and the rows the format's serve check decides on all come with the item lookup. Only a
// path that holds no item asks for the entry itself, to tell a miss from an entry whose item is gone (an answer only the request
// that serves needs: asEntry). A caller that read the artifact already hands it in as rq.Hit.
func (e *Engine) lookupHit(ctx context.Context, rq Request, asEntry bool) (items.Item, hitState, error) {
	if rq.Hit != nil {
		return *rq.Hit, hitStored, nil
	}
	ref := e.liveRef(rq.Remote.Repo, rq.Facts)
	ref.Entry = &items.EntryProbe{Variant: rq.Variant}
	it, found, err := e.o.Items.Live(ctx, ref, rq.Path)
	if err != nil {
		return items.Item{}, hitNone, err
	}
	if found {
		if it.EntryStored {
			return it, hitStored, nil
		}
		return items.Item{}, hitNone, nil
	}
	if !asEntry {
		return items.Item{}, hitNone, nil
	}
	en, entryFound, err := e.getEntry(ctx, rq.Remote.id, rq.Path, rq.Variant)
	if err != nil || !entryFound || en.Gone() {
		return items.Item{}, hitNone, err
	}
	return items.Item{}, hitEntryOnly, nil
}

// Cached reads the cache tier of an artifact and never contacts the upstream. The
// item is returned unless the entry is marked gone or the package is gone.
func (e *Engine) Cached(ctx context.Context, rq Request) (items.Item, bool, error) {
	if err := e.validate(rq, Artifact); err != nil {
		return items.Item{}, false, err
	}
	if gone, err := e.packageGone(ctx, rq); err != nil || gone {
		return items.Item{}, false, err
	}
	it, state, err := e.lookupHit(ctx, rq, false)
	return it, state == hitStored, err
}

// liveRef is the reference a stored artifact is read with: the repository and its download policy, so that the lookup reads
// the blocking finding of the artifact's version in the same statement (R-VULN-015), and the key of the rows the format's
// serve check decides on (R-ARCH-027). The verdict is applied where the item is served (the host's Serve), for the remote hit,
// the cache view and the tiers of a virtual repository alike.
func (e *Engine) liveRef(r repository.Repository, facts servefacts.Key) items.RepoRef {
	ref := items.RepoOf(r)
	ref.Vuln = e.o.Enforce.PolicyOf(r)
	ref.Facts = facts
	return ref
}

// checkAnnounced is the "immutable stays immutable" rule (R-REPO-041): the upstream's
// metadata now announces another digest for an artifact that is cached. The cached
// artifact is kept and served, and the security event names both.
func (e *Engine) checkAnnounced(ctx context.Context, rq Request, it items.Item) {
	if rq.Expected.Empty() || !binding(rq.Remote.Settings.Integrity.ChecksumPolicy, rq.AlwaysVerify) {
		return
	}
	have := blob.Digests{SHA256: it.Digest}
	copy(have.SHA512[:], it.SHA512)
	copy(have.SHA1[:], it.SHA1)
	if storedMatches(rq.Expected, have) {
		return
	}
	e.m.integrity.WithLabelValues(rq.Remote.Repo.Key, "digest_changed").Inc()
	key := rq.Remote.Repo.Key + "\x00" + rq.Path + "\x00" + hex.EncodeToString(rq.Expected.SHA512) + hex.EncodeToString(rq.Expected.SHA1)
	if !e.throttle(key, time.Hour) {
		return
	}
	e.o.Audit.Record(ctx, e.event(rq, audit.EventProxyUpstreamDigestChanged, map[string]any{
		"path": rq.Path, "cachedSha256": hex.EncodeToString(it.Digest[:]), "announced": announcedJSON(rq.Expected),
	}))
}

// head answers a HEAD request for an uncached artifact from the upstream's HEAD
// answer under steps 1 to 10, and stores nothing (R-REPO-025).
func (e *Engine) head(ctx context.Context, rq Request, w http.ResponseWriter) error {
	rem := rq.Remote
	f := &fetcher{e: e, rq: rq, st: &reqState{role: roleLeader}, fl: &flight{done: make(chan struct{})}}
	if err := f.discover(ctx); err != nil {
		return err
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return err
	}
	f.cl = cl
	release, wait, err := e.slots.acquire(ctx, rem.id, rem.Settings.Limits.MaxConcurrentFetches, e.o.Config.QueueTimeout())
	e.m.queueWait.WithLabelValues(rem.Repo.Key).Observe(wait.Seconds())
	if err != nil {
		return err
	}
	defer release()
	adm, err := e.circuitAdmit(ctx, rem)
	if err != nil {
		return err
	}
	if adm.open {
		return assumedOffline(adm.retryAfter)
	}
	defer adm.release()
	req, err := f.request(ctx)
	if err != nil {
		return err
	}
	req.Method = http.MethodHead
	resp, rcancel, _, err := f.send(ctx, req)
	if err != nil {
		res := f.transportFailure(ctx, err, false)
		return withStatus(res.err, CacheStatus{Forwarded: "uri-miss"})
	}
	defer rcancel()
	defer func() { _ = resp.Body.Close() }()
	switch s := resp.StatusCode; {
	case s >= 200 && s < 300:
		e.circuitSuccess(ctx, rem)
		h := w.Header()
		for _, k := range []string{"Content-Type", "Content-Length", "ETag", "Last-Modified", "Accept-Ranges"} {
			if v := resp.Header.Get(k); v != "" {
				h.Set(k, v)
			}
		}
		h.Set("Cache-Status", CacheStatus{Forwarded: "uri-miss", FwdStatus: s}.Header())
		w.WriteHeader(s)
		return nil
	case s == http.StatusNotFound || s == http.StatusGone:
		return withStatus(errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist upstream.", errs.WithAbsent()), CacheStatus{Forwarded: "uri-miss", FwdStatus: s})
	}
	res := f.failed(ctx, classifyStatus(rem, resp))
	return withStatus(res.err, CacheStatus{Forwarded: "uri-miss", FwdStatus: resp.StatusCode})
}

// artifact handles a 2xx for an artifact: the checks before the first byte, the
// transfer to the spool (and to the leader's client, except for the withheld tail),
// the verification, and the commit of the item and its entry (R-REPO-025 step 10).
func (f *fetcher) artifact(ctx context.Context, resp *http.Response, idle *atomic.Bool) flightResult {
	e, rq := f.e, f.rq
	rem := rq.Remote
	if res, ok := f.admitBody(ctx, resp); !ok {
		return res
	}
	// The leader's own client gets the body while it arrives only when the upstream
	// announced its length: a body of unknown length can cross the size limit after
	// the status line is out, and R-REPO-040 owes the client a 502, not a cut transfer.
	//
	// An artifact that the format inspects before it is stored (the fill hook) reaches no
	// client before the hook ran: the leader is served from the stored entry like every
	// request that waited, so a refusal answers 502 to all of them (WP3.0a).
	//
	// Nor does a body that fits in the withheld tail: no byte of it would leave before the
	// commit, so streaming it gains nothing, and the stored entry answers it with every
	// header of a stored answer, the validators included (Q-NUGET-86).
	held := rq.Adapter.InspectsFill(rq.Path) || resp.ContentLength <= withheldTail
	var tee *stream
	if f.st.stream != nil && resp.ContentLength >= 0 && !held {
		tee = f.begin(resp)
	}
	// The upstream log record completes with the body, before this function returns:
	// it reads the status of the response the client is going to get.
	f.box.set(CacheStatus{Forwarded: "uri-miss", FwdStatus: resp.StatusCode, Stored: true}.Header())
	sink := teeWriter(tee)
	sp, err := e.o.Items.Spool(ctx, f.bodyReader(resp, sink), items.IngestOptions{ContentLength: resp.ContentLength})
	if err != nil {
		return f.bodyFailure(ctx, err, idle)
	}
	defer func() { _ = sp.Close() }()
	e.circuitSuccess(ctx, rem)

	if verify(rq, sp.Digests) != verdictOK {
		return f.integrityMismatch(ctx, sp)
	}
	var payload any
	if rq.Adapter.InspectsFill(rq.Path) {
		if payload, err = rq.Adapter.InspectFill(ctx, rq.Path, sp); err != nil {
			return f.fillRefused(err, resp.StatusCode)
		}
	}
	var changed [32]byte
	declared := items.Expected{SHA256: rq.Expected.SHA256, SHA512: rq.Expected.SHA512, SHA1: rq.Expected.SHA1}
	final := stripUser(f.finalURL(resp))
	_, err = e.o.Items.Commit(ctx, items.CommitSpec{
		Repo: rem.Repo, Path: repository.Cleaned{Path: rq.Path}, Spooled: sp, MediaType: resp.Header.Get("Content-Type"),
		Declared: declared, Origin: items.OriginFill, UpstreamURL: final, MayOverwrite: true, Payload: payload,
		Actor: auditActor(rq.Caller), Source: rq.Caller.Source, Endpoint: rq.Caller.Endpoint,
		Extra: f.artifactEntry(sp, resp, final, &changed),
	})
	switch {
	case errors.Is(err, errDigestChanged):
		return f.digestChanged(ctx, sp, changed)
	case err != nil:
		if ee, ok := errs.As(err); ok && ee.Code == errs.CodeObjectStoreUnavail {
			if held && f.st.stream != nil && resp.ContentLength >= 0 {
				tee = f.replay(sp, resp) // verified, and inspected where the format inspects, so the leader's client may have it now
			}
			return f.passThrough(tee)
		}
		return flightResult{err: err}
	}
	if tee != nil {
		tee.finish()
	}
	f.box.set(CacheStatus{Forwarded: "uri-miss", FwdStatus: resp.StatusCode, Stored: true}.Header())
	return flightResult{stored: true, status: resp.StatusCode}
}

// fillRefused ends a fetch whose fill hook refused the artifact: nothing is stored, no
// negative entry is written, the circuit does not count it (the upstream answered; it
// answered with something else than was asked) and every request of the flight gets the
// refusal, 502 unless the hook named another answer (WP3.0a).
func (f *fetcher) fillRefused(err error, status int) flightResult {
	f.e.log.Warn("the format refused a fetched artifact", "repo", f.rq.Remote.Repo.Key, "path", f.rq.Path, "error", err)
	return flightResult{err: asUpstreamError(err), status: status}
}

// replay hands the spooled body to the leader's client: the artifact could not be stored
// because the object store is unavailable and the client was held back, for the fill hook,
// which has accepted the body, or because the body fits in the withheld tail.
func (f *fetcher) replay(sp *items.Spooled, resp *http.Response) *stream {
	tee := f.begin(resp)
	_, _ = io.Copy(tee, sp.Reader()) // the stream drops a client that went away and never fails
	return tee
}

// begin fixes the headers of the answer streamed to the leader's own client: the upstream's
// media type and length, the Cache-Status, and the headers of a stored answer the host names
// that do not depend on the body (Request.Headers, Q-NUGET-86).
func (f *fetcher) begin(resp *http.Response) *stream {
	tee := f.st.stream
	tee.begin(resp.Header.Get("Content-Type"), resp.ContentLength, CacheStatus{Forwarded: "uri-miss", FwdStatus: resp.StatusCode, Stored: true}.Header(), f.rq.Headers)
	return tee
}

// teeWriter returns the stream as an io.Writer, nil when there is none (a nil
// *stream in an interface would not be nil).
func teeWriter(s *stream) io.Writer {
	if s == nil {
		return nil
	}
	return s
}

// passThrough ends a fetch whose artifact could not be stored because the object
// store is unavailable: the verified body goes to the leader's client and nothing is
// stored; the requests that waited fetch for themselves (R-REPO-025).
func (f *fetcher) passThrough(tee *stream) flightResult {
	f.e.log.Warn("the object store is unavailable: an artifact is streamed and not stored", "repo", f.rq.Remote.Repo.Key)
	if tee != nil {
		tee.finish()
	}
	return flightResult{}
}

// integrityMismatch ends a fetch whose body does not match the announced digest:
// nothing is stored, nothing is negatively cached, the security event is written and
// the client that streamed has its connection aborted (R-REPO-041).
func (f *fetcher) integrityMismatch(ctx context.Context, sp *items.Spooled) flightResult {
	e, rq := f.e, f.rq
	e.m.integrity.WithLabelValues(rq.Remote.Repo.Key, "mismatch").Inc()
	e.o.Audit.Record(ctx, e.event(rq, audit.EventProxyIntegrityMismatch, map[string]any{
		"path": rq.Path, "announced": announcedJSON(rq.Expected), "computedSha256": hex.EncodeToString(sp.SHA256[:]),
	}))
	return flightResult{err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamIntegrityMismatch,
		"The artifact does not match the digest the upstream announced; it was not stored.")}
}

// digestChanged ends a fetch that lost to a duplicate flight: another flight
// committed different content under the same path, the entry upsert refuses this one
// and everybody is served the committed entry (R-REPO-026).
func (f *fetcher) digestChanged(ctx context.Context, sp *items.Spooled, cached [32]byte) flightResult {
	e, rq := f.e, f.rq
	e.m.integrity.WithLabelValues(rq.Remote.Repo.Key, "digest_changed").Inc()
	e.o.Audit.Record(ctx, e.event(rq, audit.EventProxyUpstreamDigestChanged, map[string]any{
		"path": rq.Path, "cachedSha256": hex.EncodeToString(cached[:]), "fetchedSha256": hex.EncodeToString(sp.SHA256[:]),
		"announced": announcedJSON(rq.Expected),
	}))
	return flightResult{stored: true}
}

// artifactEntry is the Extra of the item commit: in the transaction that wrote the
// item row it locks the artifact's entry, refuses an entry that holds other content,
// inserts the entry, writes the populate event and the artifact.cached event.
func (f *fetcher) artifactEntry(sp *items.Spooled, resp *http.Response, final string, changed *[32]byte) func(context.Context, meta.Tx, items.Ref) error {
	e, rq := f.e, f.rq
	rem := rq.Remote
	return func(ctx context.Context, tx meta.Tx, ref items.Ref) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		locked, err := qs.ProxyEntryLock(ctx, q.ProxyEntryLockParams{RepoID: rem.id, Path: rq.Path, Variant: rq.Variant})
		if err != nil {
			return err
		}
		if len(locked) > 0 {
			if !bytes.Equal(locked[0].BlobSha256, sp.SHA256[:]) {
				copy(changed[:], locked[0].BlobSha256)
				return errDigestChanged
			}
		} else if _, err := qs.ProxyArtifactInsert(ctx, q.ProxyArtifactInsertParams{
			RepoID: rem.id, Path: rq.Path, Variant: rq.Variant, ItemID: ref.ID, BlobSha256: sp.SHA256[:],
			UpstreamUrl: final, UpstreamEtag: resp.Header.Get("ETag"), LastModifiedUs: unixMicro(lastModified(resp.Header)),
		}); err != nil {
			return err
		}
		if rq.Populate != nil {
			if err := rq.Populate.Record(ctx, tx); err != nil {
				return err
			}
		}
		return e.o.Audit.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventArtifactCached, Actor: auditActor(rq.Caller), Outcome: audit.Success, Source: rq.Caller.Source,
			Subject:    audit.Subject{Kind: "repository", ID: rem.Repo.ID, Name: rem.Repo.Key},
			Repository: rem.Repo.Key, Path: rq.Path, Endpoint: rq.Caller.Endpoint,
			Details: mustJSON(map[string]any{
				"path": rq.Path, "name": rq.Package, "sha256": hex.EncodeToString(sp.SHA256[:]), "size": sp.Size, "upstreamUrl": final,
			}),
		})
	}
}
