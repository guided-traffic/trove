package deb

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// What a client sees of a virtual repository besides its generated index (handlers_dists.go): the package files, by the member paths
// its merged index names and by their own path (R-DEB-027).
func init() {
	registerHandler(routeMemberGet, func(f *Format) formats.HandlerFunc { return f.handleMember })
}

// resolvedFromHeader names the member that served a package file (R-REPO-019); an endpoint with exposure rules sends none (R-EP-018).
const resolvedFromHeader = "Trove-Resolved-From"

// handleVirtualPool is GET pool/<path> on a virtual repository: a client with an index from elsewhere asks for a package file by its
// own path, and the artifact tiers of R-REPO-019 step 6 resolve it among the members: the local ones in order, the caches of the remote
// ones, then their upstreams for a caller who may fill them (R-DEB-027).
func (f *Format) handleVirtualPool(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	info := f.Classify(rq.Path)
	target := formats.Target{Package: info.Package, Version: info.Version, Path: rq.Path.Path, Actions: []formats.Action{formats.ActionRead}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	return rq.Host.Resolver().Artifact(ctx, rq, rq.Path, info, immutableWriter{ResponseWriter: w, mediaType: MediaType})
}

// handleMember is GET members/<memberKey>/<path>: a package file of one member, the one whose paragraph the client read (R-DEB-027). The
// member must be a member of the virtual repository (404 otherwise) and take part for the package: the virtual repository's patterns, pin
// rules and claims, the caller's read on the member for the coordinate and, for a remote member, the cache-fill check decide as for any
// request through the repository (R-REPO-019); a file the caller may neither read from a cache nor fill is a 404.
func (f *Format) handleMember(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	key, p := rq.Params.Get("member"), rq.Params.Get("path")
	if rq.Repo.Class != formats.Virtual || !strings.HasPrefix(p, dirPool+"/") {
		return errNoSuchFile()
	}
	members := rq.Repo.Members()
	at := -1
	for i := range members {
		if members[i].Name.Key == key && memberActive(members[i].Disabled, string(members[i].Lifecycle)) {
			at = i
			break
		}
	}
	if at < 0 {
		return errNoSuchFile()
	}
	member := &members[at]
	mp := formats.Path{Path: p}
	info := f.Classify(mp)
	target := formats.Target{Package: info.Package, Version: info.Version, Path: p, Actions: []formats.Action{formats.ActionRead}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	plan, err := rq.Host.Resolver().Plan(ctx, rq, mp, info)
	if err != nil {
		return err
	}
	var pm *formats.PlannedMember
	if plan.Admitted {
		for i := range plan.Members {
			if plan.Members[i].Member.Name.Key == key {
				pm = &plan.Members[i]
				break
			}
		}
	}
	if pm == nil {
		return errNoSuchFile()
	}
	mrq, err := rq.Member(member)
	if err != nil {
		return err
	}
	if !rq.Endpoint.Rules {
		w.Header().Set(resolvedFromHeader, key)
	}
	out := immutableWriter{ResponseWriter: w, mediaType: MediaType}
	if pm.Tier == formats.TierLocal {
		it, found, err := rq.Host.Items().Stat(ctx, pm.Member, mp)
		if err != nil {
			return err
		}
		if !found || it.Folder {
			w.Header().Del(resolvedFromHeader)
			return errNoSuchFile()
		}
		return rq.Host.Items().Serve(ctx, mrq, out, it, formats.ServeStored)
	}
	if pm.MayFill {
		return rq.Host.Remote().Artifact(ctx, mrq, mp, formats.Digests{}, out)
	}
	it, ok, err := rq.Host.Remote().Cached(ctx, mrq, mp)
	if err != nil {
		return err
	}
	if !ok {
		w.Header().Del(resolvedFromHeader)
		return errNoSuchFile()
	}
	if err := rq.Host.Remote().Gate(ctx, mrq, mp); err != nil {
		return err
	}
	return rq.Host.Items().Serve(ctx, mrq, out, it, formats.ServeStored)
}
