package formathost

import (
	"context"
	"strconv"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
)

// liveRef is the reference the item store reads an item of repo with: the repository and, when the vulnerability scanning is
// wired, the policy of that repository from the replica's snapshot (no query), so that the lookup reads the blocking finding
// of the item's version in the same statement (R-VULN-015). A virtual repository's member is looked up by its own key: the
// member that stores the version decides with its own policy.
func (h *Host) liveRef(ctx context.Context, repo formats.RepoRef) (items.RepoRef, error) {
	ref := itemsRepo(repo)
	if h.o.Enforce == nil || h.o.Repos == nil {
		return ref, nil
	}
	r, err := h.o.Repos.ByKey(ctx, repo.Key)
	if err != nil {
		return items.RepoRef{}, err
	}
	ref.Vuln = h.o.Enforce.PolicyOf(r)
	return ref, nil
}

// refuseBlocked applies the verdict of the vulnerability scanning to a stored item about to be served (R-VULN-015): it counts
// and records a block and returns the refusal, 403 with the code and text of R-VULN-016, for GET, HEAD, conditional and Range
// requests alike. The error ends the request and, in a virtual repository, the resolution: the next member is not asked.
func (h *Host) refuseBlocked(ctx context.Context, rq *formats.Request, repo formats.RepoRef, row items.Item) error {
	if h.o.Enforce == nil || row.Kind != items.KindFile {
		return nil
	}
	return h.o.Enforce.Refuse(ctx, enforce.Download{
		Addressed: rq.Addressed(), Store: repo.Key, RepoID: strconv.FormatInt(repo.ID, 10), Format: string(repo.Format),
		Path: row.Path, VersionID: row.VersionID, Stored: true, Ecosystem: row.Policy.Ecosystem,
		Block: h.o.Enforce.Decide(row.Policy, row.Block, row.VersionID),
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx), Endpoint: rq.Subject.Endpoint,
	})
}
