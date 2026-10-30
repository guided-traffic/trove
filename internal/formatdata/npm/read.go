package npm

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The reads of package documents, dist-tags and tarball state (R-NPM-010, R-NPM-014,
// R-NPM-016, R-NPM-017, R-NPM-047). A document is read by one statement, so the
// revision, the tags and the versions of what it returns are one snapshot, and a
// rendition keyed by that revision never holds the rows of another.

func parseTags(raw string) (map[string]string, error) {
	tags := map[string]string{}
	if raw == "" {
		return tags, nil
	}
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	return tags, nil
}

// LocalDocument reads the rows a package document renders from. found is false for a
// package without an npm row and for one that holds no version in its document.
func (s *Store) LocalDocument(ctx context.Context, repoID int64, name string) (rows.LocalPackage, bool, error) {
	var out rows.LocalPackage
	var found bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out, found = rows.LocalPackage{}, false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NPMLocalDocument(ctx, q.NPMLocalDocumentParams{RepoID: repoID, Name: name})
		if err != nil || len(rs) == 0 {
			return err
		}
		first := rs[0]
		tags, err := parseTags(first.DistTags)
		if err != nil {
			return err
		}
		out = rows.LocalPackage{
			PackageID: first.PackageID, RevSeq: first.RevSeq, CreatedAt: first.CreatedAt.Time, ModifiedAt: first.ModifiedAt.Time, Tags: tags,
		}
		for _, r := range rs {
			if r.Version == "" {
				continue
			}
			out.Versions = append(out.Versions, rows.LocalVersion{
				Version: r.Version, Manifest: []byte(r.Manifest), Deprecated: r.Deprecated, PublishedAt: r.PublishedAt.Time,
			})
			if r.Readme != "" || r.ReadmeFilename != "" {
				out.Readme, out.ReadmeFilename = r.Readme, r.ReadmeFilename
			}
		}
		found = len(out.Versions) > 0
		return nil
	})
	return out, found, err
}

// LocalVersion reads the single version a document holds under a number or a dist-tag.
// Exactly one of version and tag is set. The package it returns holds that version
// alone, with the tags and the revision of the same snapshot.
func (s *Store) LocalVersion(ctx context.Context, repoID int64, name, version, tag string) (rows.LocalPackage, bool, error) {
	var out rows.LocalPackage
	var found bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out, found = rows.LocalPackage{}, false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NPMLocalVersion(ctx, q.NPMLocalVersionParams{RepoID: repoID, Name: name, Version: version, Tag: tag})
		if err != nil || len(rs) == 0 {
			return err
		}
		r := rs[0]
		tags, err := parseTags(r.DistTags)
		if err != nil {
			return err
		}
		out = rows.LocalPackage{
			PackageID: r.PackageID, RevSeq: r.RevSeq, CreatedAt: r.CreatedAt.Time, ModifiedAt: r.ModifiedAt.Time, Tags: tags,
			Versions: []rows.LocalVersion{{Version: r.Version, Manifest: []byte(r.Manifest), Deprecated: r.Deprecated, PublishedAt: r.PublishedAt.Time}},
		}
		found = true
		return nil
	})
	return out, found, err
}

// DistTags reads the dist-tags of a package with the revision of the same snapshot;
// found is false for a package whose document holds no version.
func (s *Store) DistTags(ctx context.Context, repoID int64, name string) (rows.LocalTags, bool, error) {
	var out rows.LocalTags
	var found bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out, found = rows.LocalTags{}, false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NPMDistTags(ctx, q.NPMDistTagsParams{RepoID: repoID, Name: name})
		if err != nil || len(rs) == 0 || !rs[0].HasVersions {
			return err
		}
		tags, err := parseTags(rs[0].DistTags)
		if err != nil {
			return err
		}
		out, found = rows.LocalTags{RevSeq: rs[0].RevSeq, Tags: tags}, true
		return nil
	})
	return out, found, err
}

// LiveVersions lists the versions of a locked package that its document holds.
func (s *Store) LiveVersions(ctx context.Context, tx meta.Tx, packageID int64) ([]rows.LiveVersion, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	rs, err := qs.NPMLiveVersions(ctx, packageID)
	if err != nil {
		return nil, err
	}
	out := make([]rows.LiveVersion, len(rs))
	for i, r := range rs {
		out[i] = rows.LiveVersion{ID: r.ID, Version: r.Version}
	}
	return out, nil
}

// Tombstoned reports whether the number was unpublished, moved out, or withheld by a
// restore's replay (R-NPM-047): the serve-time check of a tarball.
func (s *Store) Tombstoned(ctx context.Context, repoID int64, name, version string) (bool, error) {
	var out bool
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		out, err = qs.NPMTombstoned(ctx, q.NPMTombstonedParams{RepoID: repoID, Name: name, Version: version})
		return err
	})
	return out, err
}

// ClearTombstone removes the tombstone of a version that is stored again with the
// integrity the tombstone holds; it reports whether one was removed.
func (s *Store) ClearTombstone(ctx context.Context, tx meta.Tx, repoID int64, name string, versionID int64) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.NPMDeleteTombstone(ctx, q.NPMDeleteTombstoneParams{VersionID: versionID, RepoID: repoID, Name: name})
	return n > 0, err
}
