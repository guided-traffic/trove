package oci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The listings of the data port (R-OCI-015 to R-OCI-017) and the reads and the record of the removal hook (R-OCI-018). The layout of
// R-OCI-007 is written here as the directory names of the item paths, because this domain package may not import the format.
const (
	dirTags      = "_tags"
	dirManifests = "_manifests"
)

func tagsDir(name string) string { return name + "/" + dirTags }

func digests(raw [][]byte) [][32]byte {
	out := make([][32]byte, 0, len(raw))
	for _, b := range raw {
		if d, ok := digestOf(b); ok {
			out = append(out, d)
		}
	}
	return out
}

// Tags lists the tags of an image name after the cursor in byte order, at most limit, and reports the last tag of a page that is
// followed by more (R-OCI-015). A tag whose manifest the journal replay withheld is not listed (R-OCI-033).
func (s *Store) Tags(ctx context.Context, repoID int64, name, after string, limit int) (rows.TagPage, error) {
	var page rows.TagPage
	if limit <= 0 {
		return page, nil
	}
	dir := tagsDir(name)
	afterPath := ""
	if after != "" {
		afterPath = dir + "/" + after
	}
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		paths, err := qs.OCITagPage(ctx, q.OCITagPageParams{
			RepoID: repoID, Parent: dir, AfterPath: afterPath, Name: name, MaxRows: int32(limit + 1), //nolint:gosec // limit is bounded by the caller (1,000)
		})
		if err != nil {
			return err
		}
		page = rows.TagPage{Tags: make([]string, 0, min(len(paths), limit))}
		for i, p := range paths {
			if i == limit {
				page.Next = page.Tags[limit-1]
				break
			}
			page.Tags = append(page.Tags, strings.TrimPrefix(p, dir+"/"))
		}
		return nil
	})
	return page, err
}

// Referrers lists the manifests whose subject is the digest, in digest order after the cursor, at most limit (R-OCI-016); a
// non-empty artifactType keeps only the manifests of that type. A manifest the journal replay withheld is not listed.
func (s *Store) Referrers(ctx context.Context, repoID int64, name string, subject [32]byte, artifactType string, after *[32]byte, limit int) ([]rows.Manifest, error) {
	if limit <= 0 {
		return nil, nil
	}
	var cursor []byte
	if after != nil {
		cursor = after[:]
	}
	var out []rows.Manifest
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		rs, err := qs.OCIListReferrers(ctx, q.OCIListReferrersParams{
			RepoID: repoID, Name: name, Subject: subject[:], ArtifactType: artifactType, After: cursor, MaxRows: int32(limit), //nolint:gosec // bounded by the caller
		})
		if err != nil {
			return err
		}
		out = make([]rows.Manifest, 0, len(rs))
		for _, r := range rs {
			out = append(out, manifestRow(r))
		}
		return nil
	})
	return out, err
}

// Holds reports whether the repository holds any live item below the image name.
func (s *Store) Holds(ctx context.Context, repoID int64, name string) (bool, error) {
	var held bool
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		held, err = qs.OCIHoldsName(ctx, q.OCIHoldsNameParams{RepoID: repoID, Name: name})
		return err
	})
	return held, err
}

// CatalogNames lists the image names of the repository that hold a manifest, after the cursor name, at most limit.
func (s *Store) CatalogNames(ctx context.Context, repoID int64, after string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []string
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		out, err = qs.OCICatalogNames(ctx, q.OCICatalogNamesParams{RepoID: repoID, After: after, MaxRows: int32(limit)}) //nolint:gosec // bounded by the caller
		return err
	})
	return out, err
}

// TagsPointingAt lists the tags of an image name whose item holds the manifest with the digest, in the transaction of the hook.
func (s *Store) TagsPointingAt(ctx context.Context, tx meta.Tx, repoID int64, name string, digest [32]byte) ([]string, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return nil, err
	}
	dir := tagsDir(name)
	paths, err := qs.OCITagsPointingAt(ctx, q.OCITagsPointingAtParams{RepoID: repoID, Parent: dir, Digest: digest[:]})
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(paths))
	for _, p := range paths {
		tags = append(tags, strings.TrimPrefix(p, dir+"/"))
	}
	return tags, nil
}

// SubjectManifests lists the manifests of the name whose subject is the digest, in the transaction of the hook.
func (s *Store) SubjectManifests(ctx context.Context, tx meta.Tx, repoID int64, name string, subject [32]byte) ([][32]byte, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return nil, err
	}
	raw, err := qs.OCISubjectManifests(ctx, q.OCISubjectManifestsParams{RepoID: repoID, Name: name, Subject: subject[:]})
	if err != nil {
		return nil, err
	}
	return digests(raw), nil
}

// Children lists the manifests that a manifest names, in the transaction of the hook.
func (s *Store) Children(ctx context.Context, tx meta.Tx, repoID int64, name string, manifest [32]byte) ([][32]byte, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return nil, err
	}
	refs, err := qs.OCIListReferences(ctx, q.OCIListReferencesParams{RepoID: repoID, Name: name, Manifest: manifest[:]})
	if err != nil {
		return nil, err
	}
	var out [][32]byte
	for _, r := range refs {
		if r.Kind != rows.KindManifest {
			continue
		}
		if d, ok := digestOf(r.Target); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// journalEntry is the entry of oci.manifest.deleted (R-OCI-033): the repository, the image name, the digest and the principal of
// the deletion, and no secret. The replay treats it as a claim to be checked against the restored database.
func journalEntry(in rows.ManifestDeleted) ([]byte, error) {
	return json.Marshal(map[string]any{
		"repository": in.RepoID, "name": in.Name, "digest": "sha256:" + hex.EncodeToString(in.Digest[:]), "actorId": in.Actor,
	})
}

// RecordManifestDeleted writes oci.manifest.deleted with its journal entry in the transaction of the removal.
func (s *Store) RecordManifestDeleted(ctx context.Context, tx meta.Tx, in rows.ManifestDeleted) error {
	digest := "sha256:" + hex.EncodeToString(in.Digest[:])
	details, err := json.Marshal(map[string]any{"name": in.Name, "digest": digest, "deleted": in.Deleted})
	if err != nil {
		return err
	}
	entry, err := journalEntry(in)
	if err != nil {
		return err
	}
	return s.o.Recorder.RecordTx(ctx, tx, s.event(ctx, audit.EventOCIManifestDeleted, in.RepoKey, in.Name, in.Actor, details, entry))
}

// event completes an audit event of the OCI data port: the repository, the image name as the subject, the actor of the request when
// it is the principal with the id, and the source.
func (s *Store) event(ctx context.Context, typ, repoKey, name, actorID string, details, journal []byte) audit.Event {
	ev := audit.Event{
		Type: typ, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "package", Name: repoKey + "/" + name},
		Repository: repoKey, Path: name, Details: details, Journal: journal,
	}
	if s.o.Source != nil {
		ev.Source = s.o.Source(ctx)
	}
	ev.Actor = s.actorOf(ctx, actorID)
	return ev
}

// actorOf describes the actor of an event the hook could name only by the principal's id: the actor of the request when it is the
// principal with that id, else a user with that id, else the system actor "removal".
func (s *Store) actorOf(ctx context.Context, id string) audit.Actor {
	if id == "" {
		// A removal that no principal asked for (a retention run, the purge of the trash, a job) is the system's (Q-OCI-24); no
		// principal can delete without being one.
		return audit.Actor{Kind: "system", Name: "removal"}
	}
	if s.o.Actor != nil {
		if a := s.o.Actor(ctx); a.Kind != "" && a.ID == id {
			return a
		}
	}
	return audit.Actor{Kind: "user", ID: id}
}
