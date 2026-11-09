package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// mirrorStore is the group of the data port that lists what the parsed indexes offer a mirror pass (R-DEB-024, Mirror discovery). The
// index step that fills the rows is [Store.MirrorSync] (mirror_sync.go). The statements are queries/deb_mirror.sql.
type mirrorStore struct{ *group }

// scopeKeys lists the indexes of the scope as the keys "<dir>|<index path>" the statements compare: the Packages indexes of the
// components and architectures (and of "all") and, with sources, the Sources indexes of the components, of the suites whose stored
// release a key verifies (Q-DEB-32).
func (s mirrorStore) scopeKeys(ctx context.Context, sc rows.MirrorScope) ([]string, error) {
	dirs := make([]string, 0, len(sc.Suites))
	for _, suite := range sc.Suites {
		dirs = append(dirs, "dists/"+suite)
	}
	var keys []string
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		keys = keys[:0]
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		verified, err := qs.DebMirrorVerifiedDirs(ctx, q.DebMirrorVerifiedDirsParams{RepoID: sc.RepoID, Dirs: dirs})
		if err != nil || len(verified) == 0 {
			return err
		}
		idx, err := qs.DebRemoteIndexesOfDirs(ctx, q.DebRemoteIndexesOfDirsParams{RepoID: sc.RepoID, Dirs: verified})
		if err != nil {
			return err
		}
		for _, ix := range idx {
			if rows.IsSources(ix.Path) && !sc.Sources {
				continue
			}
			if rows.InScope(ix.Path, sc.Components, sc.Architectures) {
				keys = append(keys, ix.Dir+"|"+ix.Path)
			}
		}
		return nil
	})
	return keys, err
}

// MirrorNames lists the distinct package names that the parsed indexes of the scope list, in byte order after the cursor, at most
// limit. A name of a Sources index is a source package's.
func (s mirrorStore) MirrorNames(ctx context.Context, sc rows.MirrorScope, after string, limit int) ([]string, error) {
	keys, err := s.scopeKeys(ctx, sc)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	var out []string
	err = s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		out, err = qs.DebMirrorNames(ctx, q.DebMirrorNamesParams{RepoID: sc.RepoID, Keys: keys, After: after, MaxRows: int32(min(limit, 1<<30))}) //nolint:gosec // bounded
		return err
	})
	return out, err
}

// MirrorEntries lists the files the parsed indexes of the scope list for a package name: the plan of a pass.
func (s mirrorStore) MirrorEntries(ctx context.Context, sc rows.MirrorScope, name string) ([]rows.Entry, error) {
	keys, err := s.scopeKeys(ctx, sc)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	var list []q.DebMirrorEntriesRow
	err = s.g().read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.DebMirrorEntries(ctx, q.DebMirrorEntriesParams{RepoID: sc.RepoID, Keys: keys, Name: name})
		return err
	})
	out := make([]rows.Entry, 0, len(list))
	for _, r := range list {
		out = append(out, rows.Entry{
			Dir: r.Dir, IndexPath: r.IndexPath, Package: r.Package, Version: r.Version, Architecture: r.Architecture, Path: r.Path,
			SHA256: sum32(r.Sha256), Size: r.Size,
		})
	}
	return out, err
}
