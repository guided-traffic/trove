package deb

import (
	"bytes"
	"context"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/index"
)

// The virtual class (R-DEB-001, R-DEB-025 to R-DEB-028): a virtual repository merges the package indexes of its local and remote members
// into one index per suite, which a job builds, signs with the virtual repository's own key and stores as generated items (R-DEB-026,
// Q-STOR-180). A client reads those items on the routes of the local class (handlers_dists.go) after the gate (virtual_gate.go), and
// downloads each package file from exactly the member whose paragraph it read, by the path members/<memberKey>/<path>
// (handlers_virtual.go). An upload goes to the deploy target (the dispatcher redirects it, R-REPO-021, R-DEB-028).
func init() { registerClass(formats.Virtual, func(f *Format) { f.virtual = virtualAdapter{} }) }

// virtualAdapter is the part of virtual resolution the format contributes. A Debian virtual repository's merged index is the job's, a stored
// item (Q-REPO-271), so no request merges per request and the core's merged-metadata cache holds none of it; Merge applies the job's rule to
// documents, and only the artifact tiers of a `pool/` request are the core's (handlers_virtual.go).
type virtualAdapter struct{}

var _ formats.VirtualAdapter = virtualAdapter{}

func (virtualAdapter) Strategy(formats.Path) formats.Strategy { return formats.FirstMatch }

// Merge merges Packages documents by the rule of the job (Q-DEB-38): the parts in the order of R-REPO-019 step 5, local before remote,
// and within a part its own order; a paragraph is kept when no earlier part kept the same package, version and architecture, and its
// Filename becomes members/<memberKey>/<Filename>. The job streams its members' indexes through the same function; this is the form
// the contract's per-request merge takes for a document that is small.
func (virtualAdapter) Merge(ctx context.Context, _ *formats.Request, _ formats.Path, parts []formats.MemberDocument) ([]byte, error) {
	sorted := slices.Clone(parts)
	slices.SortStableFunc(sorted, func(a, b formats.MemberDocument) int {
		if a.Tier != b.Tier {
			return int(a.Tier) - int(b.Tier)
		}
		return a.Position - b.Position
	})
	var out index.Plain
	seen := index.NewSeen(0)
	for _, p := range sorted {
		if _, err := index.MergeMember(ctx, &out, seen, p.Member.Name.Key, bytes.NewReader(p.Doc), func(string) bool { return true }); err != nil {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeInternal, "A member's index does not merge.", errs.WithCause(err))
		}
	}
	return out.Bytes(), nil
}

func (virtualAdapter) LocalDocument(context.Context, *formats.Request, formats.RepoRef, formats.Path, string) ([]byte, formats.RevSeq, bool, error) {
	return nil, 0, false, nil
}

// ArtifactPrefix is empty: the cached package files of a remote member are not below one prefix per package.
func (virtualAdapter) ArtifactPrefix(string) string { return "" }
