package oci

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The removal side of the manifest rows (R-OCI-018): the row goes with the manifest's item, and the deletion of a manifest or a blob asks
// which manifests still name its digest.

// RemoveManifest removes a manifest row and, with it, the references it names.
func (*Store) RemoveManifest(ctx context.Context, tx meta.Tx, repoID int64, name string, digest [32]byte) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	_, err = qs.OCIDeleteManifest(ctx, q.OCIDeleteManifestParams{RepoID: repoID, Name: name, Digest: digest[:]})
	return err
}

// ReferencedBy lists the manifests of the name that name target, reading in the transaction when there is one.
func (s *Store) ReferencedBy(ctx context.Context, tx meta.Tx, repoID int64, name string, target [32]byte) ([][32]byte, error) {
	var out [][32]byte
	err := s.read(ctx, tx, func(qs *q.Queries) error {
		rs, err := qs.OCIReferencedBy(ctx, q.OCIReferencedByParams{RepoID: repoID, Name: name, Target: target[:]})
		if err != nil {
			return err
		}
		out = digests(rs)
		return nil
	})
	return out, err
}
