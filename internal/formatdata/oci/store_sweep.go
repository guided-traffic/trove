package oci

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The statements of the sweep of unreferenced blobs (R-OCI-019) and of the withheld marks (R-OCI-033).

// UnreferencedBlobs lists up to limit blob items of the repository that no manifest of their image name references and that are
// older than before, in path order (R-OCI-019). A blob that an unfinished upload session of the name holds the digest of is not
// listed.
func (s *Store) UnreferencedBlobs(ctx context.Context, repoID int64, before time.Time, limit int) ([]rows.Blob, error) {
	return s.UnreferencedBlobsAfter(ctx, repoID, before, "", limit)
}

// UnreferencedBlobsAfter is [Store.UnreferencedBlobs] after a cursor item path: the sweep pages with it, so that a candidate it
// leaves in place (a blob a manifest took in the meantime) is not read again.
func (s *Store) UnreferencedBlobsAfter(ctx context.Context, repoID int64, before time.Time, afterPath string, limit int) ([]rows.Blob, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []rows.Blob
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		rs, err := qs.OCIUnreferencedBlobs(ctx, q.OCIUnreferencedBlobsParams{
			RepoID: repoID, AfterPath: afterPath, Before: stamp(before), MaxRows: int32(limit), //nolint:gosec // bounded by the caller
		})
		if err != nil {
			return err
		}
		out = make([]rows.Blob, 0, len(rs))
		for _, r := range rs {
			d, ok := digestOf(r.Digest)
			if !ok {
				continue
			}
			out = append(out, rows.Blob{RepoID: repoID, Name: r.Name, Digest: d, Path: r.Path, Since: r.CreatedAt.Time})
		}
		return nil
	})
	return out, err
}

// StillUnreferenced is the sweep's re-check in the transaction that removes a blob item, after it locked the image name's package:
// no manifest of the name references the digest, the item is the one the scan saw and an unfinished upload session does not hold it.
func (s *Store) StillUnreferenced(ctx context.Context, tx meta.Tx, b rows.Blob, before time.Time) (bool, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return false, err
	}
	return qs.OCIBlobStillUnreferenced(ctx, q.OCIBlobStillUnreferencedParams{
		RepoID: b.RepoID, Name: b.Name, Digest: b.Digest[:], Path: b.Path, Before: stamp(before),
	})
}

// PutTombstone withholds a manifest: the journal replay writes the mark in its transaction and deletes nothing (R-OCI-033). A mark
// that is there stays.
func (s *Store) PutTombstone(ctx context.Context, tx meta.Tx, in rows.Tombstone) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	at := in.WithheldAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err = qs.OCIPutTombstone(ctx, q.OCIPutTombstoneParams{
		RepoID: in.RepoID, Name: in.Name, Digest: in.Digest[:], WithheldAt: stamp(at), DeletedBy: in.DeletedBy,
	})
	return err
}

// RemoveTombstone reinstates a withheld manifest: it removes the mark and reports whether one was there.
func (s *Store) RemoveTombstone(ctx context.Context, tx meta.Tx, repoID int64, name string, digest [32]byte) (bool, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.OCIDeleteTombstone(ctx, q.OCIDeleteTombstoneParams{RepoID: repoID, Name: name, Digest: digest[:]})
	return n > 0, err
}
