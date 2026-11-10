package ansible

import (
	"context"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/merge"
	"github.com/guided-traffic/trove/internal/qname"
)

// The documents a member of a virtual repository hands to the merge (R-ANS-022, Q-ANS-12). A local member's document is the list of its
// rows. A remote member's is the first stored page of the upstream's list as the remote class stores it; the merge needs every version
// of the member, so the adapter reads the further pages through the member (Request.Member), the way the remote's own handler reads
// them, and a version shows the same entry through the remote repository and through the virtual one.

// maxMergePages bounds the pages one merge reads beyond the first page of each member (R-ANS-017: 512 pages of 100 versions, the bound of
// one request). One budget serves all remote members of a merge.
const maxMergePages = 512

type pageBudget struct {
	mu   sync.Mutex
	left int
}

func newPageBudget() *pageBudget { return &pageBudget{left: maxMergePages} }

func (b *pageBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left == 0 {
		return false
	}
	b.left--
	return true
}

// memberRequest is the request derived for the member key of the request's virtual repository, found by key among its members. A key
// that is no member is an internal error, never an omission.
func memberRequest(rq *formats.Request, key qname.Repo) (*formats.Request, error) {
	for _, m := range rq.Repo.Members() {
		if m.Name == key {
			return rq.Member(&m)
		}
	}
	return nil, errs.E(errs.Internal, errs.CodeInternal, "A member the virtual repository does not have was asked for.")
}

// memberParts converts the document of one member to the parts of the merge. A local member is one part. A remote member the caller may
// fill is two: the versions whose archive is cached (the second tier of R-REPO-019) and the others; one the caller may not fill supplies
// its cached versions only (R-AUTHZ-008), because every version a document lists must be downloadable.
func (f *Format) memberParts(ctx context.Context, rq *formats.Request, d formats.MemberDocument, ns, name string, b *pageBudget) ([]merge.Part, error) {
	key := d.Member.Name.Key
	if d.Tier == formats.TierLocal {
		l, err := decodeList(d.Doc)
		if err != nil {
			return nil, err
		}
		p := merge.Part{Member: key, Tier: int(d.Tier), Position: d.Position, SHA256: map[string][32]byte{}}
		for _, e := range l.Entries {
			p.Entries = append(p.Entries, e.galaxy())
			if sum, err := hex.DecodeString(e.SHA256); err == nil && len(sum) == 32 {
				p.SHA256[e.Version] = [32]byte(sum)
			}
		}
		return []merge.Part{p}, nil
	}
	if len(d.Doc) == 0 {
		return nil, nil
	}
	entries, err := f.memberEntries(ctx, rq, d, ns, name, b)
	if err != nil {
		return nil, err
	}
	sums, err := f.announcedSums(ctx, rq, d.Member, ns+"."+name)
	if err != nil {
		return nil, err
	}
	cached := merge.Part{Member: key, Tier: int(formats.TierRemoteCached), Position: d.Position, SHA256: sums}
	rest := merge.Part{Member: key, Tier: int(formats.TierRemote), Position: d.Position, SHA256: sums}
	for _, e := range entries {
		switch {
		case d.CachedVersions[e.Version]:
			cached.Entries = append(cached.Entries, e)
		case !d.Restricted:
			rest.Entries = append(rest.Entries, e)
		}
	}
	return []merge.Part{cached, rest}, nil
}

// announcedSums reads the digests the remote member's version documents announced, the digests of the archives a client would get
// from it (R-ANS-018). A version whose document was never read has none, and cannot be a conflict.
func (f *Format) announcedSums(ctx context.Context, rq *formats.Request, member formats.RepoRef, name string) (map[string][32]byte, error) {
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return nil, err
	}
	announced, err := st.AnnouncedNames(ctx, member.ID, name)
	if err != nil {
		return nil, err
	}
	sums := make(map[string][32]byte, len(announced))
	for _, a := range announced {
		sums[a.Version] = a.SHA256
	}
	return sums, nil
}

// memberEntries reads every version of the remote member d holds: the first page d carries and the pages after it, read through the
// member. A page the caller may not bring into the cache is left out for a member the caller may not fill: the merge lists only its
// cached versions anyway.
func (f *Format) memberEntries(ctx context.Context, rq *formats.Request, d formats.MemberDocument, ns, name string, b *pageBudget) ([]galaxydoc.VersionEntry, error) {
	entries, count, _, err := galaxydoc.ParseUpstreamList(d.Doc)
	if err != nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's version list is not a document the registry reads.", errs.WithCause(err))
	}
	if count <= pageSize {
		return entries, nil
	}
	mrq, err := memberRequest(rq, d.Member.Name)
	if err != nil {
		return nil, err
	}
	for off := pageSize; off < count; off += pageSize {
		if !b.take() {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
				"The version lists of this collection are longer than a merge reads ("+strconv.Itoa(maxMergePages*pageSize)+" versions).")
		}
		page, err := f.memberPage(ctx, mrq, ns, name, off, d.Restricted)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
	}
	return entries, nil
}

// memberPage reads one stored page of a remote member's list. unfillable says that the caller may not bring a page into the cache: a
// page that is not cached is then no error but no entries.
func (f *Format) memberPage(ctx context.Context, mrq *formats.Request, ns, name string, off int, unfillable bool) ([]galaxydoc.VersionEntry, error) {
	doc, err := mrq.Host.Remote().Metadata(ctx, mrq, formats.Path{Path: VersionsDocumentPath(ns, name)}, "offset="+strconv.Itoa(off))
	if err != nil {
		if e, ok := errs.As(err); ok && unfillable && e.Kind == errs.Forbidden && e.Code == errs.CodePopulateNotPermitted {
			return nil, nil
		}
		if errs.KindOf(err) == errs.NotFound {
			// The first page said the list is longer: a page the upstream does not have is a broken list, not a missing collection.
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's version list ends before the count it announced.", errs.WithCause(err))
		}
		return nil, err
	}
	if doc.Status.Negative {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's version list ends before the count it announced.")
	}
	raw, err := doc.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	page, _, _, err := galaxydoc.ParseUpstreamList(raw)
	if err != nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's version list is not a document the registry reads.", errs.WithCause(err))
	}
	return page, nil
}

// recordCollisions learns the digests of the versions that a local member and a remote member both hold, which is what a conflict is
// (R-ANS-022): the digest of a remote version is announced by its version document, and a document that was never read has recorded
// none, so the merge reads it through the member (the engine records what it announces, R-ANS-018). Only collisions are read, so the
// cost is the number of versions a local member shares with an upstream, and every read is paid from the budget of the merge. A
// document that cannot be read leaves the version without a digest: it is then no conflict, and the merge does not fail for it.
func (f *Format) recordCollisions(ctx context.Context, rq *formats.Request, ns, name string, parts []formats.MemberDocument, in []merge.Part, b *pageBudget) {
	local := map[string]bool{}
	for _, p := range in {
		if p.Tier == int(formats.TierLocal) {
			for _, e := range p.Entries {
				local[merge.Key(e.Version)] = true
			}
		}
	}
	if len(local) == 0 {
		return
	}
	for _, p := range in {
		if p.Tier == int(formats.TierLocal) {
			continue
		}
		var member *formats.Request
		for _, e := range p.Entries {
			if _, known := p.SHA256[e.Version]; known || !local[merge.Key(e.Version)] || !b.take() {
				continue
			}
			if member == nil {
				for _, d := range parts {
					if d.Member.Name.Key == p.Member && !d.Restricted {
						member, _ = memberRequest(rq, d.Member.Name)
					}
				}
				if member == nil {
					break
				}
			}
			if sum, ok := f.announcedDigest(ctx, member, ns, name, e.Version); ok {
				p.SHA256[e.Version] = sum
			}
		}
	}
}

// announcedDigest reads the version document of a remote member and returns the archive digest it announces.
func (f *Format) announcedDigest(ctx context.Context, mrq *formats.Request, ns, name, ver string) ([32]byte, bool) {
	doc, err := mrq.Host.Remote().Metadata(ctx, mrq, formats.Path{Path: VersionDocumentPath(ns, name, ver)}, documentVariant)
	if err != nil || doc.Status.Negative {
		return [32]byte{}, false
	}
	raw, err := doc.Bytes(ctx)
	if err != nil {
		return [32]byte{}, false
	}
	v, _, err := galaxydoc.ParseUpstreamVersion(raw)
	if err != nil {
		return [32]byte{}, false
	}
	return v.SHA256, true
}
