package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/merge"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
)

// The documents a remote member of a virtual repository hands to the merge (R-NUGET-017,
// Q-NUGET-82). The virtual engine asks the remote member for the document at the merged path
// and hands over what the remote stores there: the upstream's bytes as they came (R-NUGET-015),
// gzip-coded when the upstream's blob carries the coding, and for the registration the index
// alone. The merge needs every version of the member, so the adapter reads the member's
// registration the way the remote's own handler reads it: the stored index, inflated, and the
// pages the index does not inline, through the member's remote engine, converted with the
// remote's conversion. A version therefore shows the same entry through the remote repository and
// through the virtual repository, and an ID with 128 or more versions upstream is merged whole.

// remoteFlatDocument is a remote member's version list as the merge reads it: the stored bytes,
// inflated when the upstream coded them with gzip, within the bound of a document held in memory.
func remoteFlatDocument(doc []byte) ([]byte, error) {
	return remote.Decode(doc, MaxDocumentBytes)
}

// remoteMemberEntries reads the registration of the ID lowerID that the remote member d holds:
// the stored index d carries and the pages it lists by address, read through the member as the
// request derived for it (Request.Member), so the member's populate right decides whether a page
// the member has not stored may be fetched (R-AUTHZ-008). A member the caller may not fill
// contributes the leaves of the pages it has stored; the merge lists only its cached versions
// anyway. The budget is the merge's, shared by its remote members (Q-NUGET-83).
func (f *Format) remoteMemberEntries(ctx context.Context, rq *formats.Request, d formats.MemberDocument, lowerID string, b *remote.Budget) ([]merge.Entry, error) {
	idx, err := b.Index(d.Doc)
	if err != nil {
		return nil, err
	}
	bounds, err := boundsOf(idx)
	if err != nil {
		return nil, err
	}
	var pages []*formats.RemoteDocument
	if len(bounds) > 0 {
		member, err := memberRequest(rq, d.Member.Name)
		if err != nil {
			return nil, err
		}
		if pages, err = storedPages(ctx, member, lowerID, bounds, d.Restricted); err != nil {
			return nil, err
		}
	}
	vs, err := registrationVersions(ctx, lowerID, idx, pages, b)
	if err != nil {
		return nil, err
	}
	return merge.FromRemote(vs), nil
}

// memberRequest is the request derived for the member key of the request's virtual repository,
// found by key among its members. The merge of the registration and the search of a remote
// member both use it; a key that is no member is an internal error, never an omission.
func memberRequest(rq *formats.Request, key qname.Repo) (*formats.Request, error) {
	for _, m := range rq.Repo.Members() {
		if m.Name == key {
			return rq.Member(&m)
		}
	}
	return nil, errs.E(errs.Internal, errs.CodeInternal, "A member the virtual repository does not have was asked for.")
}
