package oci

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// ServeCheck runs before the core serves a stored item of a local repository on any route, /files/ and the cache view included
// (R-OCI-033, Q-OCI-33). A manifest or a tag item is served only when the repository holds the row of its manifest and no tombstone
// withholds the digest: a manifest the journal replay withheld after a restore keeps its items and its bytes and is refused with the
// 404 of a missing manifest until an administrator reinstates it. A manifest without its row can only be a file that reached the
// repository around the format and fails closed. A blob is let through: the tombstone is a mark of manifests.
//
// The item lookup of the download read these rows with the item when the route asked for them ([Format.ServeFactsKey]), so the check
// costs no statement of its own on the hottest route (R-ARCH-027); it reads through the data port when the facts were not read for
// its key, and keeps no state of its own (R-ARCH-032).
func (f *Format) ServeCheck(ctx context.Context, h formats.Host, repo formats.RepoRef, it formats.ItemRef, _ formats.ServeMode) error {
	key := f.ServeFactsKey(repo, formats.Path{Path: it.Path})
	if key.Zero() {
		return nil
	}
	if it.Facts.For(key) {
		if it.Facts.Tombstoned || !it.Facts.OCIManifest {
			return errManifestUnknown()
		}
		return nil
	}
	st, ok := h.FormatData(ID).(Store)
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "The OCI data port is not bound.")
	}
	item, found, err := h.Items().Stat(ctx, repo, formats.Path{Path: it.Path})
	if err != nil {
		return err
	}
	if !found {
		return errManifestUnknown()
	}
	var digest [32]byte
	copy(digest[:], item.Digests.SHA256)
	if _, ok, err := st.Manifest(ctx, repo.ID, key.Package, digest); err != nil || !ok {
		if err != nil {
			return err
		}
		return errManifestUnknown()
	}
	if _, withheld, err := st.Tombstone(ctx, repo.ID, key.Package, digest); err != nil || withheld {
		if err != nil {
			return err
		}
		return errManifestUnknown()
	}
	return nil
}

// ServeFactsKey implements [formats.ServeFactsKeyer]: the rows ServeCheck decides on for a manifest or a tag item of a local
// repository, which the item lookup of the download reads with the item (R-ARCH-027). The zero key is a path ServeCheck lets
// through, and a class it does not check: a remote repository's cache is not withheld by a journal (its rows go with the entry).
func (f *Format) ServeFactsKey(repo formats.RepoRef, p formats.Path) formats.ServeKey {
	if repo.Class != formats.Local {
		return formats.ServeKey{}
	}
	name, kind, _, ok := splitLayout(p.Path)
	if !ok || (kind != dirManifests && kind != dirTags) || !ValidName(name) {
		return formats.ServeKey{}
	}
	return formats.ServeKey{Kind: formats.ServeOCILocal, Package: name}
}

var _ formats.ServeFactsKeyer = (*Format)(nil)

func errManifestUnknown() error {
	return errs.E(errs.NotFound, errs.CodeOCIManifestUnknown, "No manifest with this reference is stored in the repository.")
}
