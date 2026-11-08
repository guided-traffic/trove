package oci

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// OnCommit runs in the transaction that wrote an item (R-OCI-007, R-OCI-013 step 8).
//
// The tag item of a push by tag is its lead: it creates the package row of the image name, claims the name, and creates the version
// of the tag (or leaves the version of a tag that moved where it is) and raises the revision. The manifest item, the companion of that
// push or the lead of a push by digest, records the manifest and what it names. Everything the push wrote commits together, so a tag
// item and a version row that disagree cannot be seen, and a manifest row exists exactly when its item does.
//
// A blob is a plain item: its commit (the finalize of an upload, a mount) writes no row of the format here. A manifest or a tag that
// reaches the repository any other way is refused: the only door is the protocol, and a raw deploy is refused earlier (R-OCI-001).
func (f *Format) OnCommit(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error {
	switch p := c.Payload.(type) {
	case *tagCommit:
		return f.commitTagItem(ctx, tx, c, p.pushed)
	case *manifestCommit:
		return f.commitManifestItem(ctx, tx, c, p.pushed)
	}
	if _, kind, _, ok := splitLayout(c.Path.Path); ok && kind == dirBlobs {
		return nil
	}
	switch {
	case c.Origin == formats.OriginFill && c.Repo.Class == formats.Remote:
		// The fill of a remote repository: its rows are written by the remote adapter.
		return nil
	case c.Repo.Class == formats.Local && admitSeeded(c.Payload):
		return nil
	}
	return errs.E(errs.Conflict, errs.CodeConflict, "A repository of the format oci takes manifests and tags only through the registry protocol under /v2/.")
}

// retryShadow is the answer to a push that turned out to be the first of its name after the shadowing check was skipped: the last
// version of the name went away between the check and the commit (R-REPO-048).
func retryShadow(name string) error {
	return errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
		name+" lost its last version while this push ran; try again.", errs.WithRetryAfter(time.Second))
}

func (f *Format) commitTagItem(ctx context.Context, tx formats.Tx, c formats.CommitEvent, p *pushed) error {
	// The package row lock first: the version, the claim and the reference check below read only after it, so a push racing a
	// deletion of the same tag sees the version or its absence, never neither.
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, p.name, p.name)
	if err != nil {
		return err
	}
	if p.linked {
		// The tag is linked to a manifest that was stored when the push began: it must still be, on a row held until this
		// transaction ends, or the tag would point at a manifest that is gone (the push then commits the manifest with the tag).
		// Manifest rows are removed only inside the transaction that deletes the manifest's item (the two callers of RemoveManifest, in
		// OnItemsRemoved), which is why holding the item row FOR SHARE keeps the row, and the references on it, in place as well.
		st, err := portOf(c.Data)
		if err != nil {
			return err
		}
		have, err := st.ExistingItems(ctx, tx, c.Repo.ID, []string{ManifestPath(p.name, p.m.Digest)})
		if err != nil {
			return err
		}
		if len(have) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, "The manifest the tag is linked to is not stored any more.")
		}
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	first := len(live) == 0
	if first && p.shadowSkipped {
		return retryShadow(p.name)
	}
	var existing *formats.VersionRow
	for i := range live {
		if live[i].Version == p.tag {
			existing = &live[i]
		}
	}
	if existing != nil && !c.Replaced && existing.LeadItem != c.Item.ID {
		// A version whose tag item is gone is replaced by the one of this push.
		if err := c.Packages.TrashOrDeleteVersion(ctx, existing.ID, true); err != nil {
			return err
		}
		existing = nil
	}
	// A tag that moved is a change by an owner or a writer and never a claim; a new tag publishes a version.
	if err := c.Packages.Claim(ctx, formats.Target{Package: p.name, Version: p.tag, Path: TagPath(p.name, p.tag), Actions: []formats.Action{formats.ActionDeploy}},
		formats.OwnershipWrite{Publish: existing == nil, FirstVersion: first}); err != nil {
		return err
	}
	if existing == nil {
		if first {
			if _, err := c.Packages.AutoClaim(ctx, p.name); err != nil {
				return err
			}
		}
		if _, err := c.Packages.InsertVersion(ctx, pkg, formats.NewVersion{
			Version: p.tag, DisplayVersion: p.tag, LeadItem: &c.Item, CreatedBy: c.Actor,
		}); err != nil {
			return err
		}
	}
	_, _, err = c.Packages.BumpRevision(ctx, pkg)
	return err
}

func (f *Format) commitManifestItem(ctx context.Context, tx formats.Tx, c formats.CommitEvent, p *pushed) error {
	st, err := portOf(c.Data)
	if err != nil {
		return err
	}
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, p.name, p.name)
	if err != nil {
		return err
	}
	if c.Lead.ID == 0 {
		// A push by digest: this item is the lead, so the name is claimed and the first version checked here.
		live, err := c.Packages.Versions(ctx, pkg)
		if err != nil {
			return err
		}
		first := len(live) == 0
		if first && p.shadowSkipped {
			return retryShadow(p.name)
		}
		if err := c.Packages.Claim(ctx, formats.Target{Package: p.name, Path: ManifestPath(p.name, p.m.Digest), Actions: []formats.Action{formats.ActionDeploy}},
			formats.OwnershipWrite{Publish: true, FirstVersion: first}); err != nil {
			return err
		}
		if first {
			if _, err := c.Packages.AutoClaim(ctx, p.name); err != nil {
				return err
			}
		}
	}
	// The references again, on rows held against a removal: a blob or a child cannot go between the check and the row.
	if err := checkReferences(ctx, st, tx, c.Repo.ID, p.name, p.m); err != nil {
		return err
	}
	if err := st.PutManifest(ctx, tx, manifestInput(c.Repo.ID, p.name, p.m)); err != nil {
		return err
	}
	_, _, err = c.Packages.BumpRevision(ctx, pkg)
	return err
}

func portOf(data any) (Store, error) {
	st, ok := data.(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The OCI data port is not bound.")
	}
	return st, nil
}

// manifestInput is the row of a parsed manifest and the references it names (R-OCI-007).
func manifestInput(repoID int64, name string, m *manifest.Manifest) rows.ManifestInput {
	annotations, cut := m.RowAnnotations(manifest.MaxAnnotationBytes)
	in := rows.ManifestInput{Manifest: rows.Manifest{
		RepoID: repoID, Name: name, Digest: m.Sum, MediaType: m.MediaType, ArtifactType: m.EffectiveArtifactType(),
		Size: m.Size, ContentSize: m.ContentSize(), Annotations: annotations, AnnotationsCut: cut,
	}}
	if m.Subject != nil {
		if s, ok := manifest.ParseDigest(m.Subject.Digest); ok {
			in.Subject = &s
		}
	}
	for _, r := range m.References() {
		target, ok := manifest.ParseDigest(r.Digest)
		if !ok {
			continue
		}
		ref := rows.Reference{Target: target, Kind: rows.KindBlob, Platform: r.Platform}
		if r.Kind == manifest.KindManifest {
			ref.Kind = rows.KindManifest
		}
		in.References = append(in.References, ref)
	}
	return in
}
