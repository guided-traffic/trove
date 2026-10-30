package npm

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// noActor is the id a tombstone records for a removal that no principal asked for
// (the item store's job of a deletion queued without an actor): the column is a uuid
// and not null, and the nil uuid is the one value that names nobody.
const noActor = "00000000-0000-0000-0000-000000000000"

// removed groups the removed items of one package: the versions whose tarball went and
// those that lost their provenance bundle only.
type removed struct {
	versions []string
	bundles  []string
}

// OnItemsRemoved runs in the transaction that removed files of an npm repository: the
// unpublish of R-NPM-017, which removes the files of a version through the package
// writer, and a raw delete of a tarball through /files/ ("unpublishes that version the
// same way"). The removal of the tarball of a version is the unpublish of the version:
// its tombstone (R-NPM-008), the dist-tags without it (R-NPM-016), its version row,
// the revision and the audit event with the journal entry of Q-NPM-12. The removal of
// a bundle alone changes the package without removing a version.
//
// The ownership of the name was decided before, in the same transaction: by the
// unpublish route under the package row lock, and for a delete through /files/ or
// the items API by the generic layer, which asks the format seam
// (formathost.FilesSeam.CheckRemoval) for every package of the removed files under
// the same lock before the first row goes (Q-AUTHZ-161). The writer of this hook is
// bound to no request and decides none.
func (f *Format) OnItemsRemoved(ctx context.Context, tx formats.Tx, e formats.ItemsEvent) error {
	if e.Repo.Class != formats.Local {
		return nil
	}
	groups := f.removedFiles(e.Items)
	if len(groups) == 0 {
		return nil
	}
	st, err := storeOf(e.Data)
	if err != nil {
		return err
	}
	pkgs := make([]string, 0, len(groups))
	for p := range groups {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	rm := removal{
		source: "unpublish", purge: e.Reason != "trash", by: e.Actor,
		audit: rows.Audit{RepoKey: e.Repo.Key, Actor: rows.Actor{ID: e.Actor}},
	}
	if e.Reason == "move" {
		rm.source = "move"
	}
	if rm.by == "" {
		rm.by = noActor
	}
	for _, p := range pkgs {
		g := groups[p]
		pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, p, p)
		if err != nil {
			return err
		}
		gone, err := f.removeVersions(ctx, tx, st, e.Packages, e.Repo, pkg, g.versions, rm)
		if err != nil {
			return err
		}
		if len(gone) == 0 && len(g.bundles) > 0 {
			// A bundle of a version that stays: the package changed, its rows did not.
			if _, _, err := e.Packages.BumpRevision(ctx, pkg); err != nil {
				return err
			}
			if err := e.Packages.Notify(areaMetadataChanged, formats.PackageChanged(e.Repo.ID, p)); err != nil {
				return err
			}
		}
	}
	return nil
}

// removedFiles sorts the removed items of the event by package: a tarball names the
// version it removes, a provenance bundle the version it belongs to. Any other item
// is not the format's.
func (f *Format) removedFiles(items []formats.RemovedItem) map[string]*removed {
	out := map[string]*removed{}
	for _, it := range items {
		info := f.Classify(formats.Path{Path: it.Item.Path})
		if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
			continue
		}
		g := out[info.Package]
		if g == nil {
			g = &removed{}
			out[info.Package] = g
		}
		switch {
		case strings.HasSuffix(it.Item.Path, tarballSuffix):
			if !slices.Contains(g.versions, info.Version) {
				g.versions = append(g.versions, info.Version)
			}
		case strings.HasSuffix(it.Item.Path, sigstoreSuffix):
			g.bundles = append(g.bundles, info.Version)
		}
	}
	return out
}

// removal says how versions leave a package.
type removal struct {
	// source is the tombstone's source, "unpublish" or "move"; purge says the version
	// rows are deleted and not trashed.
	source string
	purge  bool
	// by is the principal id of the actor.
	by    string
	audit rows.Audit
}

// removeVersions takes versions out of a locked package: it computes the tags without
// them, tombstones those the document holds, removes the version rows and raises the
// revision once. A version that is not stored any more is skipped; one that is stored
// and not in the document (withheld by a restore's replay, R-NPM-047) loses its row and
// keeps the tombstone it has. It returns the versions it removed.
func (f *Format) removeVersions(ctx context.Context, tx formats.Tx, st Store, pw formats.PackageWriter, repo formats.RepoRef,
	pkg formats.PackageRef, versions []string, rm removal,
) ([]string, error) {
	if len(versions) == 0 {
		return nil, nil
	}
	stored, err := pw.Versions(ctx, pkg)
	if err != nil {
		return nil, err
	}
	ids := map[string]formats.VersionID{}
	for _, v := range stored {
		ids[v.Version] = v.ID
	}
	var gone []string
	for _, v := range versions {
		if _, ok := ids[v]; ok {
			gone = append(gone, v)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	doc, err := st.LiveVersions(ctx, tx, int64(pkg.ID))
	if err != nil {
		return nil, err
	}
	inDoc := map[string]bool{}
	var remaining []string
	for _, v := range doc {
		inDoc[v.Version] = true
		if !slices.Contains(gone, v.Version) {
			remaining = append(remaining, v.Version)
		}
	}
	tags, err := st.Tags(ctx, tx, int64(pkg.ID))
	if err != nil {
		return nil, err
	}
	for _, v := range gone {
		if inDoc[v] {
			tags = tagsAfterRemoval(tags, v, remaining)
		}
	}
	_, at, err := pw.BumpRevision(ctx, pkg)
	if err != nil {
		return nil, err
	}
	for _, v := range gone {
		if inDoc[v] {
			_, err := st.Tombstone(ctx, tx, rows.TombstoneInput{
				RepoID: repo.ID, PackageID: int64(pkg.ID), VersionID: int64(ids[v]), Name: pkg.Name, Version: v,
				Source: rm.source, By: rm.by, Tags: tags, At: at, Audit: rm.audit,
			})
			if err != nil {
				return nil, err
			}
		}
		if err := pw.TrashOrDeleteVersion(ctx, ids[v], rm.purge); err != nil {
			return nil, err
		}
	}
	return gone, pw.Notify(areaMetadataChanged, formats.PackageChanged(repo.ID, pkg.Name))
}

// OnItemsRestored runs in the transaction that brought removed files back (the trash
// can, P1; R-STOR-023). The restore of the tarball of a version brings back its own
// bytes, which is no reuse of the number (R-NPM-008): the tombstone goes, if it holds
// the integrity of the restored version, and the version reappears in documents. A
// package that has no latest tag gets one (R-NPM-016).
func (f *Format) OnItemsRestored(ctx context.Context, tx formats.Tx, e formats.ItemsEvent) error {
	if e.Repo.Class != formats.Local {
		return nil
	}
	groups := f.removedFiles(e.Items)
	if len(groups) == 0 {
		return nil
	}
	st, err := storeOf(e.Data)
	if err != nil {
		return err
	}
	ids := map[string]formats.VersionID{} // package/version -> the version row the event names
	for _, it := range e.Items {
		if info := f.Classify(formats.Path{Path: it.Item.Path}); it.Version != 0 && strings.HasSuffix(it.Item.Path, tarballSuffix) {
			ids[info.Package+"@"+info.Version] = it.Version
		}
	}
	pkgs := make([]string, 0, len(groups))
	for p := range groups {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	audit := rows.Audit{RepoKey: e.Repo.Key, Actor: rows.Actor{ID: e.Actor}}
	for _, p := range pkgs {
		pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, p, p)
		if err != nil {
			return err
		}
		cleared := false
		for _, v := range groups[p].versions {
			vid, ok := ids[p+"@"+v]
			if !ok {
				continue
			}
			ok, err := st.ClearTombstone(ctx, tx, e.Repo.ID, p, int64(vid))
			if err != nil {
				return err
			}
			cleared = cleared || ok
		}
		_, at, err := e.Packages.BumpRevision(ctx, pkg)
		if err != nil {
			return err
		}
		if cleared {
			if err := f.restoreLatest(ctx, tx, st, e, pkg, at, audit); err != nil {
				return err
			}
		}
		if err := e.Packages.Notify(areaMetadataChanged, formats.PackageChanged(e.Repo.ID, p)); err != nil {
			return err
		}
	}
	return nil
}

// restoreLatest gives a package that came back from nothing its latest tag.
func (f *Format) restoreLatest(ctx context.Context, tx formats.Tx, st Store, e formats.ItemsEvent, pkg formats.PackageRef, at time.Time, audit rows.Audit) error {
	tags, err := st.Tags(ctx, tx, int64(pkg.ID))
	if err != nil || tags["latest"] != "" {
		return err
	}
	live, err := st.LiveVersions(ctx, tx, int64(pkg.ID))
	if err != nil {
		return err
	}
	names := make([]string, len(live))
	for i, v := range live {
		names[i] = v.Version
	}
	best, ok := highest(names)
	if !ok {
		return nil
	}
	tags["latest"] = best
	return st.SetTag(ctx, tx, rows.SetTagInput{
		RepoID: e.Repo.ID, PackageID: int64(pkg.ID), Name: pkg.Name, Tag: "latest", Version: best, Tags: tags, At: at, Audit: audit,
	})
}
