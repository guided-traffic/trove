package nuget

import (
	"context"
	"log/slog"
	"slices"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/internal/observability"
)

// The packages a remote repository caches (R-NUGET-015). NuGet v3 announces no content digest
// (H-35), so the declared identity of the package is the one check that the upstream served the
// package that was asked for: the .nuspec inside the archive must name the ID and the version
// of the path. The check runs before anything is stored, and no byte of the package reaches a
// client before it ran (Q-ARCH-267); a package that fails it answers 502 to every request of
// the fetch and leaves no blob, no item and no row.

// InspectsFill declares the hook for the .nupkg of a version. Nothing else a remote repository
// caches is read: a .nuspec has no identity check of its own (Q-NUGET-54).
func (remoteAdapter) InspectsFill(p formats.Path) bool {
	t, err := parseTarget(p)
	return err == nil && t.kind == kindPackage
}

// InspectFill reads the spooled package like an upload (R-NUGET-005 steps 1 to 3, with the
// upstream rule for the ID of R-NUGET-003 and without the refusal of a symbol package) and holds
// its declared identity to the path's:
//
//   - the ID differs from the path's under Unicode simple case folding, or the version's URL
//     form differs from the path's: 502 upstream_package_mismatch, nothing is stored;
//   - the archive cannot be read, or the .nuspec is not one: the package is served and cached as
//     the upstream sent it, logged and counted, and gets no NuGet rows (a nil inspection). The
//     bytes are never altered;
//   - otherwise the inspection reaches OnCommit, which records the version (Q-NUGET-53).
//
// The archive limits are the defaults of nuget.archive: the hook meets the package without its
// replica, as Inspect does (H-336).
func (remoteAdapter) InspectFill(ctx context.Context, _ formats.RemoteContext, p formats.Path, sp formats.Spooled) (*formats.Inspection, error) {
	t, err := parseTarget(p)
	if err != nil || t.kind != kindPackage {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "NuGet inspects the packages of a remote repository only.")
	}
	got, err := readCached(ctx, sp)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if e, ok := errs.As(err); ok && e.Kind == errs.Invalid {
			slog.Warn("a cached NuGet package cannot be read; it is served as it came and gets no NuGet rows",
				"package", t.id, "version", t.version, "cause", e.Detail)
			return nil, nil //nolint:nilnil // no inspection is the answer for an unreadable package, see above
		}
		return nil, err
	}
	if !sameID(got.declaredID, t.id) || got.lowerVersion != t.version {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamPackageMismatch,
			"The upstream answered the request for "+t.id+" "+t.version+" with a package that declares "+got.declaredID+" "+got.lowerVersion+"; it is not stored.")
	}
	return &formats.Inspection{
		Package: t.id, Version: t.version,
		Properties: map[string]string{"nuget.id": t.id, "nuget.version": t.version},
		Payload:    got,
	}, nil
}

// readCached runs the reading steps of an upload on a package a remote cached.
func readCached(ctx context.Context, sp formats.Spooled) (*incoming, error) {
	ra, err := sp.ReaderAt()
	if err != nil {
		return nil, err
	}
	res, err := nupkg.Inspect(ctx, ra, sp.Size(), defaultReplica().archiveLimits())
	if err != nil {
		return nil, err
	}
	m := res.Metadata
	if err := id.ValidateUpstream(m.ID); err != nil {
		return nil, err
	}
	v, err := version.Parse(m.Version)
	if err != nil {
		return nil, err
	}
	return &incoming{
		result: res, meta: m, declaredID: m.ID, lowerID: id.Lower(m.ID), version: v, lowerVersion: v.URLForm(),
	}, nil
}

// commitFill records a package a remote repository cached (R-NUGET-015): the package row, the
// version with the .nupkg as its lead item, and the NuGet row with origin cache, in the
// transaction that stored the item. No claim, no tombstone and no event: no request published
// anything, and a remote has neither owners nor deletes of its own. A version that is recorded
// already (the item was evicted and fetched again) keeps its row and takes the item back.
func (f *Format) commitFill(ctx context.Context, tx formats.Tx, c formats.CommitEvent, in *incoming) error {
	st, err := storeOf(c.Data)
	if err != nil {
		return err
	}
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, in.lowerID, in.declaredID)
	if err != nil {
		return err
	}
	props := map[string]string{"nuget.id": in.lowerID, "nuget.version": in.lowerVersion}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version == in.lowerVersion {
			if err := c.Packages.LinkItem(ctx, c.Item, v.ID, true); err != nil {
				return err
			}
			return c.Packages.SetProperties(ctx, c.Item, props)
		}
	}
	vid, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{
		Version: in.lowerVersion, DisplayVersion: in.version.FullNormalized(), LeadItem: &c.Item,
	})
	if err != nil {
		return err
	}
	_, at, err := c.Packages.BumpRevision(ctx, pkg)
	if err != nil {
		return err
	}
	if err := c.Packages.SetProperties(ctx, c.Item, props); err != nil {
		return err
	}
	vi, err := in.versionInput(c.Repo, int64(vid), &pushCommit{origin: rows.OriginCache, digests: c.Digests, size: c.Size})
	if err != nil {
		return err
	}
	vi.PublishedAt = at
	return st.InsertVersion(ctx, tx, vi)
}

// uncounted notes a cached package that got no rows: it is logged where it is found
// (InspectFill) and counted where the replica is known (OnCommit).
func (f *Format) uncounted(c formats.CommitEvent) {
	if m := f.replicaOfPort(c.Data).remote; m != nil {
		m.unparseable(c.Repo.Key)
	}
}

// remoteMetrics are the families of R-NUGET-028 that the remote class counts.
type remoteMetrics struct {
	repos *observability.CappedLabels
	// unreadable counts cached packages that were served without NuGet rows.
	unreadable *prometheus.CounterVec
}

func newRemoteMetrics(m *observability.Metrics) *remoteMetrics {
	return &remoteMetrics{
		repos: m.Repos(),
		unreadable: m.NewCounterVec("trove_nuget_remote_unparseable_packages_total",
			"Packages cached by a remote repository that could not be read as a NuGet package and were served without NuGet rows (R-NUGET-015).", "repo"),
	}
}

func (m *remoteMetrics) unparseable(repoKey string) {
	m.unreadable.WithLabelValues(m.repos.Value("trove_nuget_remote_unparseable_packages_total", repoKey)).Inc()
}

// fillPayload returns the package the inspection of a fill accepted.
func fillPayload(c formats.CommitEvent) (*incoming, bool) {
	if c.Inspection == nil {
		return nil, false
	}
	in, ok := c.Inspection.Payload.(*incoming)
	return in, ok
}

// onCacheRemoved runs when items of a remote repository's cache are removed (an eviction, a
// delete through the items API): the version a removed .nupkg belonged to leaves the packages
// and the NuGet rows with it, because the cache holds no version without its package. No
// tombstone is written: a remote repository can fetch the version again (R-NUGET-015).
func (f *Format) onCacheRemoved(ctx context.Context, e formats.ItemsEvent) error {
	byPackage := map[string][]formats.RemovedItem{}
	for _, it := range e.Items {
		info := f.Classify(formats.Path{Path: it.Item.Path})
		if info.Kind != formats.Content || info.Package == "" || info.Version == "" || it.Version == 0 {
			continue
		}
		if it.Item.Path == NupkgPath(info.Package, info.Version) {
			byPackage[info.Package] = append(byPackage[info.Package], it)
		}
	}
	names := make([]string, 0, len(byPackage))
	for n := range byPackage {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, name, name)
		if err != nil {
			return err
		}
		for _, it := range byPackage[name] {
			if err := e.Packages.TrashOrDeleteVersion(ctx, it.Version, e.Reason != "trash"); err != nil {
				return err
			}
		}
		if _, _, err := e.Packages.BumpRevision(ctx, pkg); err != nil {
			return err
		}
	}
	return nil
}
