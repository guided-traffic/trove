package virtual

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// Cache-Status values of a virtual response (R-REPO-047).
const (
	statusPartial = "trove; detail=partial"
	statusMiss    = "trove; fwd=miss"
)

// Metadata answers a metadata request of the virtual repository (R-REPO-019 step 6, R-REPO-020):
// the documents of the members the caller may use, merged by the format under the per-version
// source rule and cached per replica.
//
// A cached merge is used only when it is still valid for this caller: the key holds the members that
// took part and their populate rights (D-7), and every request that finds an entry compares the
// rev_seq of every eligible local member with the recorded ones in one query, so a local publish is
// visible on every replica as soon as its commit has returned, with or without a notification
// (R-ARCH-015). A merge that left a remote member out because it could not be asked is partial and
// never cached.
func (r *Resolver) Metadata(ctx context.Context, c Caller, p *Plan, m Merger) (*Merged, error) {
	repo, d := p.Virtual.Key, p.Descriptor
	if !p.Admitted {
		r.m.resolved(repo, sourceNone)
		return nil, notFound()
	}
	if m.Strategy(d) == FirstMatch {
		return r.firstMatch(ctx, c, p, m)
	}
	ttl := r.mergedTTL(p.Virtual)
	var key mergedKey
	// A path that belongs to no package has no rev_seq to validate an entry with.
	cacheable := r.merged != nil && d.Package != "" && ttl > 0
	if cacheable {
		key = newMergedKey(p, c.Subject.Endpoint)
		if e, ok := r.merged.Get(key); ok {
			valid, err := r.valid(ctx, e, p)
			if err != nil {
				return nil, err
			}
			if valid {
				r.m.resolved(repo, sourceCache)
				return e.result(r.o.Now(), true), nil
			}
			r.merged.Remove(key)
		}
	}
	build := func(ctx context.Context) (*mergedEntry, error) { return r.build(ctx, c, p, m, ttl) }
	var e *mergedEntry
	var err error
	if cacheable {
		e, err = r.merged.Load(ctx, key, build)
	} else {
		e, err = build(ctx)
	}
	if err != nil {
		r.m.resolved(repo, sourceNone)
		return nil, err
	}
	r.m.resolved(repo, e.source)
	return e.result(r.o.Now(), false), nil
}

// mergedTTL is mergedMetadataTtlSeconds of the virtual repository.
func (r *Resolver) mergedTTL(v repository.Repository) time.Duration {
	s, ok := v.Virtual()
	if !ok || s.MergedMetadataTTLSeconds == nil {
		return time.Duration(repository.DefaultMergedMetadataTTLSeconds) * time.Second
	}
	return time.Duration(*s.MergedMetadataTTLSeconds) * time.Second
}

// valid reports whether a cached entry may still be served: not expired, and the rev_seq of every
// eligible local member as recorded.
func (r *Resolver) valid(ctx context.Context, e *mergedEntry, p *Plan) (bool, error) {
	if !e.expires.After(r.o.Now()) {
		return false, nil
	}
	if len(e.locals) == 0 {
		return true, nil
	}
	ids := make([]int64, len(e.locals))
	for i, l := range e.locals {
		ids[i] = l.ID
	}
	revs, err := r.o.Locals.RevSeqs(ctx, ids, p.Descriptor.Package)
	if err != nil {
		return false, err
	}
	for _, l := range e.locals {
		if revs[l.ID] != l.Rev {
			return false, nil
		}
	}
	return true, nil
}

// mergedResult is what a response needs of an entry; the entry itself is shared and never changes.
func (e *mergedEntry) result(now time.Time, hit bool) *Merged {
	status := e.status
	if hit {
		status = fmt.Sprintf("trove; hit; ttl=%d", max(0, int64(e.expires.Sub(now)/time.Second)))
	}
	return &Merged{Bytes: e.bytes, MergeFingerprint: e.fingerprint, Partial: e.partial, CacheStatus: status}
}

// slot is the work of one member of a merge.
type slot struct {
	part    *Part
	local   *localRev
	expires time.Time
	fetched bool
	// miss: the member does not know the package; failure: it could not be asked.
	failure error
	fatal   error
}

// build collects the document of every member the caller may use and merges them.
func (r *Resolver) build(ctx context.Context, c Caller, p *Plan, m Merger, ttl time.Duration) (*mergedEntry, error) {
	d := p.Descriptor
	slots := make([]slot, len(p.Members))
	var wg sync.WaitGroup
	for i, mem := range p.Members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if mem.Local() {
				r.localSlot(ctx, m, mem, d, &slots[i])
			} else {
				r.remoteSlot(ctx, c, p, m, mem, &slots[i])
			}
		}()
	}
	wg.Wait()

	now := r.o.Now()
	e := &mergedEntry{expires: now.Add(ttl), source: sourceLocal}
	var parts []Part
	var failure error
	for _, s := range slots {
		switch {
		case s.fatal != nil:
			return nil, s.fatal
		case s.failure != nil:
			e.partial = true
			if failure == nil {
				failure = s.failure
			}
		}
		if s.local != nil {
			e.locals = append(e.locals, *s.local)
		}
		if s.part == nil {
			continue
		}
		parts = append(parts, *s.part)
		if s.part.Tier != TierLocal && e.source != sourceUpstream {
			e.source = sourceCache
		}
		if s.fetched {
			e.source = sourceUpstream
		}
		if !s.expires.IsZero() && s.expires.Before(e.expires) {
			e.expires = s.expires
		}
	}
	if len(parts) == 0 {
		// Step 7: 404 only when every consulted member said "not found".
		e.source = sourceNone
		if failure != nil {
			return nil, failure
		}
		return nil, notFound()
	}
	doc, err := m.Merge(ctx, d, parts)
	if err != nil {
		return nil, err
	}
	e.bytes = doc
	e.fingerprint = mergeFingerprint(p.Fingerprint, parts)
	e.keep = !e.partial && e.expires.After(now)
	switch {
	case e.partial:
		e.status = statusPartial
	default:
		e.status = statusMiss
	}
	return e, nil
}

func (r *Resolver) localSlot(ctx context.Context, m Merger, mem Member, d Descriptor, s *slot) {
	doc, rev, found, err := m.LocalDocument(ctx, mem, d)
	if err != nil {
		s.fatal = err
		return
	}
	// The revision is recorded whether or not the member holds a document: a package row
	// without a live version still has a rev_seq (R-ARCH-015).
	s.local = &localRev{ID: mem.ID, Rev: rev}
	if found {
		s.part = &Part{Member: mem, Tier: TierLocal, Revision: Revision{RevSeq: rev}, Doc: doc}
	}
}

func (r *Resolver) remoteSlot(ctx context.Context, c Caller, p *Plan, m Merger, mem Member, s *slot) {
	if r.o.Remotes == nil {
		s.failure = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The remote engine is not available on this replica.")
		return
	}
	d := p.Descriptor
	doc, err := r.o.Remotes.Metadata(ctx, r.call(c, p.Virtual, mem, d))
	switch {
	case err == nil:
	case isMiss(err):
		return
	case isTransient(err) && ctx.Err() == nil:
		s.failure = err
		return
	default:
		s.fatal = err
		return
	}
	bytes, err := doc.Bytes(ctx)
	if err != nil {
		s.fatal = err
		return
	}
	cached := map[string]bool{}
	paths, err := r.o.Remotes.CachedArtifacts(ctx, mem.ID, m.ArtifactPrefix(d))
	if err != nil {
		s.fatal = err
		return
	}
	for _, path := range paths {
		if v := m.CachedVersion(path); v != "" {
			cached[v] = true
		}
	}
	s.fetched, s.expires = doc.Fetched, doc.ExpiresAt
	s.part = &Part{
		Member: mem, Tier: TierRemote, Revision: Revision{Digest: doc.Digest}, Doc: bytes,
		CachedVersions: cached, Restricted: !mem.MayFill, Stale: doc.Stale,
	}
}

// call names a request to a remote member.
func (r *Resolver) call(c Caller, v repository.Repository, mem Member, d Descriptor) RemoteCall {
	return RemoteCall{
		Remote: mem.Repo, Via: v, Path: d.Path, Variant: d.Variant, Package: d.Package,
		Subject: c.Subject, Endpoint: c.Subject.Endpoint, MayFill: mem.MayFill,
	}
}

// mergeFingerprint is an input of the ETag: the plan fingerprint, then for every part the member,
// the tier, the revision it was built from and the versions the merge may take from its cache. It
// changes whenever the merged bytes could.
func mergeFingerprint(plan [32]byte, parts []Part) [32]byte {
	h := sha256.New()
	h.Write(plan[:])
	for _, p := range parts {
		writeInt(h, p.Member.ID)
		h.Write([]byte{byte(p.Tier), boolByte(p.Restricted), boolByte(p.Stale)})
		writeInt(h, p.Revision.RevSeq)
		h.Write(p.Revision.Digest[:])
		versions := make([]string, 0, len(p.CachedVersions))
		for v := range p.CachedVersions {
			versions = append(versions, v)
		}
		slices.Sort(versions)
		writeInt(h, int64(len(versions)))
		for _, v := range versions {
			writeInt(h, int64(len(v)))
			h.Write([]byte(v))
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// firstMatch serves the document of the first member that has it: the local members in order,
// then the remote members in order. Nothing is merged and nothing is cached here: a local document
// is cached by the rendition cache, a remote one by the remote engine.
func (r *Resolver) firstMatch(ctx context.Context, c Caller, p *Plan, m Merger) (*Merged, error) {
	repo, d := p.Virtual.Key, p.Descriptor
	var failure error
	for _, mem := range p.Local() {
		doc, rev, found, err := m.LocalDocument(ctx, mem, d)
		if err != nil {
			return nil, err
		}
		if found {
			r.m.resolved(repo, sourceLocal)
			return &Merged{Bytes: doc, MergeFingerprint: mergeFingerprint(p.Fingerprint, []Part{{Member: mem, Tier: TierLocal, Revision: Revision{RevSeq: rev}}})}, nil
		}
	}
	for _, mem := range p.Remote() {
		if r.o.Remotes == nil {
			if failure == nil {
				failure = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The remote engine is not available on this replica.")
			}
			continue
		}
		doc, err := r.o.Remotes.Metadata(ctx, r.call(c, p.Virtual, mem, d))
		switch {
		case err == nil:
		case isMiss(err):
			continue
		case isTransient(err) && ctx.Err() == nil:
			if failure == nil {
				failure = err
			}
			continue
		default:
			return nil, err
		}
		bytes, err := doc.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		source := sourceCache
		if doc.Fetched {
			source = sourceUpstream
		}
		r.m.resolved(repo, source)
		part := Part{Member: mem, Tier: TierRemote, Revision: Revision{Digest: doc.Digest}, Restricted: !mem.MayFill, Stale: doc.Stale}
		return &Merged{Bytes: bytes, MergeFingerprint: mergeFingerprint(p.Fingerprint, []Part{part})}, nil
	}
	r.m.resolved(repo, sourceNone)
	if failure != nil {
		return nil, failure
	}
	return nil, notFound()
}
