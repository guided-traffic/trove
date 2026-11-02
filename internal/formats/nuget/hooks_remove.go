package nuget

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// noActor is the id a tombstone records for a removal that no principal asked for (the
// item store's job of a deletion queued without an actor): the column is a uuid and not
// null, and the nil UUID is the one value that names nobody.
const noActor = "00000000-0000-0000-0000-000000000000"

// removedFiles groups the removed items of one package by version.
type removedFiles struct {
	// nupkg holds the SHA-256 of the removed package of each version whose .nupkg went;
	// derived holds the versions that lost a generated file only.
	nupkg   map[string]removedNupkg
	derived map[string]bool
}

type removedNupkg struct {
	version formats.VersionID
	sha256  []byte
}

// OnItemsRemoved runs in the transaction that removed files of a NuGet repository: the
// hard delete of R-NUGET-007, which removes the files of a version through the package
// writer, a raw delete of a package through /files/, and a move out of the repository.
// The removal of the .nupkg of a version is the removal of the version: its tombstone
// (R-NUGET-006), its version row with the NuGet row, the revision and package.deleted with
// the journal entry of R-NUGET-033. The removal of a generated file of a version that
// stays is refused, which rolls the removal back (Q-NUGET-13).
//
// The ownership of the ID was decided before, in the same transaction: by the delete route
// under the package row lock, and for a delete through /files/ or the items API by the
// generic layer, which asks the format seam for every package of the removed files. The
// writer of this hook is bound to no request and decides none.
func (f *Format) OnItemsRemoved(ctx context.Context, tx formats.Tx, e formats.ItemsEvent) error {
	if e.Repo.Class == formats.Remote {
		return f.onCacheRemoved(ctx, e)
	}
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
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	by := e.Actor
	if by == "" {
		by = noActor
	}
	audit := rows.Audit{RepoKey: e.Repo.Key, Actor: rows.Actor{ID: e.Actor}}
	for _, lowerID := range ids {
		g := groups[lowerID]
		pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, lowerID, lowerID)
		if err != nil {
			return err
		}
		stored, err := e.Packages.Versions(ctx, pkg)
		if err != nil {
			return err
		}
		byVersion := map[string]formats.VersionRow{}
		for _, v := range stored {
			byVersion[v.Version] = v
		}
		// A generated file of a version whose package stays is not removed on its own.
		for v := range g.derived {
			if _, gone := g.nupkg[v]; gone {
				continue
			}
			if row, ok := byVersion[v]; ok && row.LeadItem != 0 {
				return errs.E(errs.Conflict, errs.CodeConflict,
					"A file of "+lowerID+" "+v+" that trove generated cannot be deleted on its own; delete the package version.")
			}
		}
		versions := make([]string, 0, len(g.nupkg))
		for v := range g.nupkg {
			versions = append(versions, v)
		}
		slices.Sort(versions)
		var at time.Time
		for _, v := range versions {
			row, ok := byVersion[v]
			if !ok {
				continue // the version row is gone already
			}
			if at.IsZero() {
				// Once per package: the revision rises once per transaction, and the clock is
				// PostgreSQL's, read after the row lock.
				if _, at, err = e.Packages.BumpRevision(ctx, pkg); err != nil {
					return err
				}
			}
			if _, err := st.InsertTombstone(ctx, tx, rows.TombstoneInput{
				RepoID: e.Repo.ID, LowerID: lowerID, LowerVersion: v, SHA256: g.nupkg[v].sha256, At: at, By: by, Audit: audit,
			}); err != nil {
				return err
			}
			if err := e.Packages.TrashOrDeleteVersion(ctx, row.ID, e.Reason != "trash"); err != nil {
				return err
			}
		}
	}
	return nil
}

// removedFiles sorts the removed items of the event by package: a .nupkg names the
// version it removes, a generated file the version it belongs to. Any other item is not
// the format's.
func (f *Format) removedFiles(items []formats.RemovedItem) map[string]*removedFiles {
	out := map[string]*removedFiles{}
	for _, it := range items {
		info := f.Classify(formats.Path{Path: it.Item.Path})
		if info.Kind != formats.Content || info.Package == "" || info.Version == "" {
			continue
		}
		g := out[info.Package]
		if g == nil {
			g = &removedFiles{nupkg: map[string]removedNupkg{}, derived: map[string]bool{}}
			out[info.Package] = g
		}
		switch {
		case it.Item.Path == NupkgPath(info.Package, info.Version):
			g.nupkg[info.Version] = removedNupkg{version: it.Version, sha256: it.SHA256}
		case it.Item.Path == NuspecPath(info.Package, info.Version), strings.Contains(it.Item.Path, "/"+canonEmbedded+"/"):
			g.derived[info.Version] = true
		}
	}
	return out
}

// OnItemsRestored runs in the transaction that brought removed files back (the trash
// can, P1; R-STOR-023). The restore of the package of a version brings back its own bytes,
// which is no reuse of the number (R-NUGET-006): the tombstone goes, if it holds the
// SHA-512 of the restored package, and the version reappears in the documents.
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
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, lowerID := range ids {
		pkg, _, err := e.Packages.EnsureLocked(ctx, e.Repo, lowerID, lowerID)
		if err != nil {
			return err
		}
		for _, n := range groups[lowerID].nupkg {
			if n.version == 0 {
				continue
			}
			if _, err := st.DeleteTombstone(ctx, tx, int64(n.version)); err != nil {
				return err
			}
		}
		if _, _, err := e.Packages.BumpRevision(ctx, pkg); err != nil {
			return err
		}
	}
	return nil
}
