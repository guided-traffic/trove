package oci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The statements of the manifests a push writes and of the checks and reads around them (internal/meta/postgres/queries/
// oci_manifests.sql). The rows carry no blob reference: the items hold the bytes (R-OCI-007).

var errForeignTx = errors.New("oci: the transaction is not one of internal/meta/postgres")

func txQueries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// read runs fn in the transaction when there is one and in a short one of its own otherwise.
func (s *Store) read(ctx context.Context, tx meta.Tx, fn func(qs *q.Queries) error) error {
	run := func(tx meta.Tx) error {
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	}
	if tx != nil {
		return run(tx)
	}
	return s.o.DB.InTx(ctx, run)
}

// PutManifest records a manifest row and the references it names, in the transaction that writes the manifest's item. A manifest
// recorded already changes nothing; a target named twice is one edge, the first occurrence's.
func (s *Store) PutManifest(ctx context.Context, tx meta.Tx, in rows.ManifestInput) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	var subject []byte
	if in.Subject != nil {
		subject = in.Subject[:]
	}
	annotations := in.Annotations
	if len(annotations) == 0 {
		annotations = nil
	}
	if err := qs.OCIPutManifest(ctx, q.OCIPutManifestParams{
		RepoID: in.RepoID, Name: in.Name, Digest: in.Digest[:], MediaType: in.MediaType, ArtifactType: in.ArtifactType,
		Subject: subject, Size: in.Size, ContentSize: in.ContentSize, Annotations: annotations, AnnotationsCut: in.AnnotationsCut,
	}); err != nil {
		return err
	}
	if len(in.References) == 0 {
		return nil
	}
	type edge struct {
		Target   string `json:"target"`
		Kind     string `json:"kind"`
		Platform string `json:"platform"`
	}
	seen := make(map[[32]byte]bool, len(in.References))
	edges := make([]edge, 0, len(in.References))
	for _, r := range in.References {
		if seen[r.Target] {
			continue
		}
		seen[r.Target] = true
		if r.Kind != rows.KindManifest && r.Kind != rows.KindBlob {
			return errs.E(errs.Internal, errs.CodeInternal, "oci: a reference of kind "+r.Kind)
		}
		edges = append(edges, edge{Target: hex.EncodeToString(r.Target[:]), Kind: r.Kind, Platform: r.Platform})
	}
	raw, err := json.Marshal(edges)
	if err != nil {
		return err
	}
	return qs.OCIPutReferences(ctx, q.OCIPutReferencesParams{RepoID: in.RepoID, Name: in.Name, Manifest: in.Digest[:], Refs: raw})
}

func manifestRow(m q.OciManifest) rows.Manifest {
	out := rows.Manifest{
		RepoID: m.RepoID, Name: m.Name, MediaType: m.MediaType, ArtifactType: m.ArtifactType.String, Size: m.Size,
		ContentSize: m.ContentSize, Annotations: m.Annotations, AnnotationsCut: m.AnnotationsCut, CreatedAt: m.CreatedAt.Time,
	}
	copy(out.Digest[:], m.Digest)
	if len(m.Subject) == 32 {
		var s [32]byte
		copy(s[:], m.Subject)
		out.Subject = &s
	}
	return out
}

// Manifest reads the row of a manifest.
func (s *Store) Manifest(ctx context.Context, repoID int64, name string, digest [32]byte) (rows.Manifest, bool, error) {
	var out rows.Manifest
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		out, found = rows.Manifest{}, false
		m, err := qs.OCIGetManifest(ctx, q.OCIGetManifestParams{RepoID: repoID, Name: name, Digest: digest[:]})
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = manifestRow(m), true
		return nil
	})
	return out, found, err
}

// References lists what a manifest names.
func (s *Store) References(ctx context.Context, repoID int64, name string, manifest [32]byte) ([]rows.Reference, error) {
	var out []rows.Reference
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		out = out[:0]
		rs, err := qs.OCIListReferences(ctx, q.OCIListReferencesParams{RepoID: repoID, Name: name, Manifest: manifest[:]})
		if err != nil {
			return err
		}
		for _, r := range rs {
			ref := rows.Reference{Kind: r.Kind, Platform: r.Platform}
			copy(ref.Target[:], r.Target)
			out = append(out, ref)
		}
		return nil
	})
	return out, err
}

// Tombstone reads the withheld mark of a digest under an image name.
func (s *Store) Tombstone(ctx context.Context, repoID int64, name string, digest [32]byte) (rows.Tombstone, bool, error) {
	var out rows.Tombstone
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		out, found = rows.Tombstone{}, false
		at, err := qs.OCIGetTombstone(ctx, q.OCIGetTombstoneParams{RepoID: repoID, Name: name, Digest: digest[:]})
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = rows.Tombstone{RepoID: repoID, Name: name, Digest: digest, WithheldAt: at.Time}, true
		return nil
	})
	return out, found, err
}

// ExistingItems returns the paths among the given ones that are live files of the repository. In a transaction the rows are held
// against a concurrent removal until it ends.
func (s *Store) ExistingItems(ctx context.Context, tx meta.Tx, repoID int64, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var out []string
	err := s.read(ctx, tx, func(qs *q.Queries) (err error) {
		if tx != nil {
			out, err = qs.OCILockExistingItems(ctx, q.OCILockExistingItemsParams{RepoID: repoID, Paths: paths})
		} else {
			out, err = qs.OCIExistingItems(ctx, q.OCIExistingItemsParams{RepoID: repoID, Paths: paths})
		}
		return err
	})
	return out, err
}

// WithheldAmong returns the digests among the given ones that carry a withheld mark under the image name.
func (s *Store) WithheldAmong(ctx context.Context, tx meta.Tx, repoID int64, name string, digests [][32]byte) ([][32]byte, error) {
	if len(digests) == 0 {
		return nil, nil
	}
	in := make([][]byte, len(digests))
	for i := range digests {
		in[i] = digests[i][:]
	}
	var out [][32]byte
	err := s.read(ctx, tx, func(qs *q.Queries) error {
		out = out[:0]
		rs, err := qs.OCIWithheldAmong(ctx, q.OCIWithheldAmongParams{RepoID: repoID, Name: name, Digests: in})
		if err != nil {
			return err
		}
		for _, r := range rs {
			var d [32]byte
			copy(d[:], r)
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// TagViews reads the tags with the given version ids with the facts of their manifests and, for an index, its children, in two
// statements however many packages the ids belong to.
func (s *Store) TagViews(ctx context.Context, versionIDs []int64) ([]rows.TagView, error) {
	if len(versionIDs) == 0 {
		return nil, nil
	}
	var out []rows.TagView
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		out = out[:0]
		rs, err := qs.OCITagViews(ctx, versionIDs)
		if err != nil {
			return err
		}
		at := make(map[int64]int, len(rs))
		for _, r := range rs {
			v := rows.TagView{
				VersionID: r.VersionID, Tag: r.Tag, MediaType: r.MediaType, ArtifactType: r.ArtifactType, Size: r.Size, ContentSize: r.ContentSize,
			}
			copy(v.Digest[:], r.Digest)
			at[v.VersionID] = len(out)
			out = append(out, v)
		}
		if len(out) == 0 {
			return nil
		}
		children, err := qs.OCIChildPlatforms(ctx, versionIDs)
		if err != nil {
			return err
		}
		for _, c := range children {
			i, ok := at[c.VersionID]
			if !ok {
				continue
			}
			child := rows.Child{Platform: c.Platform, MediaType: c.MediaType, Size: c.Size, ContentSize: c.ContentSize}
			copy(child.Digest[:], c.Digest)
			out[i].Children = append(out[i].Children, child)
		}
		return nil
	})
	return out, err
}

// StoredTags lists the tags a local repository serves of an image name, newest push first.
func (s *Store) StoredTags(ctx context.Context, repoID int64, name string, limit int) ([]string, error) {
	var out []string
	err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		out, err = qs.OCIStoredTags(ctx, q.OCIStoredTagsParams{RepoID: repoID, Name: name, MaxRows: int32(min(max(limit, 0), math.MaxInt32))})
		return err
	})
	return out, err
}

// WithheldVersions returns the live tags of the repositories that point at a withheld manifest.
func (s *Store) WithheldVersions(ctx context.Context, repos []string) ([]int64, error) {
	var out []int64
	err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		out, err = qs.OCIWithheldVersions(ctx, repos)
		return err
	})
	return out, err
}
