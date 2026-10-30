package npm

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The statements of remote repositories (internal/meta/postgres/queries/npm_remote.sql;
// R-NPM-024 to R-NPM-026): the records of a stored upstream document, written in the
// transaction that stores the document, and the facts tarball serving reads.

// ApplyFill records the document a fill is about to store: the package's record, the
// facts of its versions and the removal marks. It runs after the engine locked the
// cache entry, in the transaction that upserts it, and takes the lock of the package's
// record, so that the stored document and the records describe one document whichever
// of two fills commits last (R-NPM-024).
//
// Both rules that keep the stored document are decided here, after the lock and against
// what is stored now, not against what the fill saw when it began: a first fill that a
// concurrent fill overtook has seen no entry. A stored entry that was fetched later than
// the document stays (R-REPO-026, RemoteApplied.Superseded), and a document older than the
// stored one is a stale copy (RemoteApplied.Stale); neither changes a row. A version that
// went from listed to unlisted writes npm.remote.version_removed in this transaction, so a
// duplicate fill finds nothing to report (R-NPM-026).
func (s *Store) ApplyFill(ctx context.Context, tx meta.Tx, f rows.RemoteFill) (rows.RemoteApplied, error) {
	qs, err := queries(tx)
	if err != nil {
		return rows.RemoteApplied{}, err
	}
	modified := ""
	if !f.DocModified.IsZero() {
		modified = stamp(f.DocModified)
	}
	n, err := qs.NPMRemoteInsertPackage(ctx, q.NPMRemoteInsertPackageParams{
		RepoID: f.RepoID, Name: f.Name, DocSha256: f.DocSHA256[:], DocModified: modified,
	})
	if err != nil {
		return rows.RemoteApplied{}, err
	}
	var out rows.RemoteApplied
	if n == 0 {
		// A record exists: lock it and decide on the document that is stored now.
		cur, err := qs.NPMRemoteLockPackage(ctx, q.NPMRemoteLockPackageParams{RepoID: f.RepoID, Name: f.Name})
		if err != nil {
			return rows.RemoteApplied{}, err
		}
		entry, err := qs.NPMRemoteStoredEntry(ctx, q.NPMRemoteStoredEntryParams{RepoID: f.RepoID, Name: f.Name})
		if err != nil {
			return rows.RemoteApplied{}, err
		}
		// A document that is gone is replaced by whatever the upstream serves now.
		if len(entry) > 0 && !entry[0].Gone {
			if !f.FetchedAt.IsZero() && time.UnixMicro(entry[0].FetchedUs).After(f.FetchedAt) {
				return rows.RemoteApplied{Superseded: true}, nil
			}
			if len(cur) > 0 && !f.DocModified.IsZero() && cur[0].DocModified.Valid && f.DocModified.Before(cur[0].DocModified.Time) {
				return rows.RemoteApplied{Stale: true}, nil
			}
		}
		if err := qs.NPMRemoteSetPackage(ctx, q.NPMRemoteSetPackageParams{
			RepoID: f.RepoID, Name: f.Name, DocSha256: f.DocSHA256[:], DocModified: modified,
		}); err != nil {
			return rows.RemoteApplied{}, err
		}
	}
	names := make([]string, len(f.Versions))
	integrities := make([]string, len(f.Versions))
	shasums := make([]string, len(f.Versions))
	published := make([]string, len(f.Versions))
	for i, v := range f.Versions {
		names[i], integrities[i], shasums[i] = v.Version, v.Integrity, v.Shasum
		if !v.PublishedAt.IsZero() {
			published[i] = stamp(v.PublishedAt)
		}
	}
	removed, err := qs.NPMRemoteUnlist(ctx, q.NPMRemoteUnlistParams{RepoID: f.RepoID, Name: f.Name, Versions: names})
	if err != nil {
		return rows.RemoteApplied{}, err
	}
	for _, r := range removed {
		out.Removed = append(out.Removed, rows.RemoteVersion{Version: r.Version, Integrity: r.Integrity})
	}
	if out.Relisted, err = qs.NPMRemoteRelist(ctx, q.NPMRemoteRelistParams{RepoID: f.RepoID, Name: f.Name, Versions: names}); err != nil {
		return rows.RemoteApplied{}, err
	}
	if len(names) > 0 {
		err = qs.NPMRemoteUpsertVersions(ctx, q.NPMRemoteUpsertVersionsParams{
			RepoID: f.RepoID, Name: f.Name, Versions: names, Integrities: integrities, Shasums: shasums, Published: published,
		})
		if err != nil {
			return rows.RemoteApplied{}, err
		}
	}
	for _, v := range out.Removed {
		details := map[string]any{"package": f.Name, "version": v.Version}
		if v.Integrity != "" {
			details["integrity"] = v.Integrity
		}
		if err := s.record(ctx, tx, audit.EventNPMVersionRemoved, f.Audit, f.Name, details, "", nil); err != nil {
			return rows.RemoteApplied{}, err
		}
	}
	return out, nil
}

// TarballFacts reads, in one statement, what the serving of a tarball of a remote
// repository decides on.
func (s *Store) TarballFacts(ctx context.Context, repoID int64, name, version string) (rows.TarballFacts, error) {
	var out rows.TarballFacts
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		out = rows.TarballFacts{}
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		rs, err := qs.NPMRemoteTarballFacts(ctx, q.NPMRemoteTarballFactsParams{RepoID: repoID, Name: name, Version: version})
		if err != nil || len(rs) == 0 {
			return err
		}
		r := rs[0]
		out = rows.TarballFacts{
			Known: r.Known, Listed: r.Listed, Integrity: r.Integrity, Shasum: r.Shasum,
			DocStored: r.DocStored, DocCurrent: r.DocCurrent, ServeRemoved: r.ServeRemoved,
		}
		if r.GoneUs != 0 {
			out.DocGone = time.UnixMicro(r.GoneUs).UTC()
		}
		return nil
	})
	return out, err
}
