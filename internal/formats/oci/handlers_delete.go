package oci

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The content-management category of the distribution specification (R-OCI-018, Q-OCI-11). The routes serve the classes local and
// virtual: a DELETE on a remote repository is answered 405 by the dispatcher before a handler runs, and one on a virtual repository by the
// handler (R-OCI-028), because the dispatcher would carry a write of a virtual repository to its deployment target. The removal itself is the item store's,
// and OnItemsRemoved (hooks_remove.go) holds the cascade and the refusals, so a deletion through /files/, the items API or a retention
// run behaves exactly as these two routes do.

// handleDeleteManifest is DELETE …/manifests/<reference>:
//
//   - a tag removes the tag item and its version; the manifest stays (202);
//   - a digest removes the manifest, every tag of the name that points at it, its referrers and, for an index, the children no other
//     manifest and no tag of the name references (202); a manifest that an index of the name lists is refused (409 DENIED).
//
// A reference the repository holds nothing under answers 404 MANIFEST_UNKNOWN.
func (f *Format) handleDeleteManifest(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class == formats.Virtual {
		return virtualDeleteRefused(http.MethodGet, http.MethodHead, http.MethodPut)
	}
	name, ref := rq.Params.Get("name"), rq.Params.Get("reference")
	target := formats.Target{Package: name, Actions: []formats.Action{formats.ActionDelete}}
	var path string
	switch {
	case ValidDigest(ref):
		path = ManifestPath(name, ref)
	case ValidTag(ref):
		target.Version, path = ref, TagPath(name, ref)
	default:
		if strings.Contains(ref, ":") {
			return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The reference is not a sha256 digest.")
		}
		return errs.E(errs.Invalid, errs.CodeOCITagInvalid, "The reference is not a tag.")
	}
	// The coordinate is the item the request removes: the path decides the grants and the patterns that are scoped by path (R-AUTHZ-005),
	// as it does for the same removal through /files/.
	target.Path = path
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	if err := f.deleteItem(ctx, rq, target, path, errManifestUnknown()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}

// handleDeleteBlob is DELETE …/blobs/<digest>: the blob item of the name goes (202) unless a manifest of the name references it
// (409 DENIED). A blob the name holds no item of answers 404 BLOB_UNKNOWN. The blob object is collected once no item references it
// (R-STOR-024).
func (f *Format) handleDeleteBlob(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class == formats.Virtual {
		return virtualDeleteRefused(http.MethodGet, http.MethodHead)
	}
	name, digest := rq.Params.Get("name"), rq.Params.Get("digest")
	if !ValidDigest(digest) {
		return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The digest is not a sha256 digest.")
	}
	target := formats.Target{Package: name, Path: BlobPath(name, digest), Actions: []formats.Action{formats.ActionDelete}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	unknown := errs.E(errs.NotFound, errs.CodeOCIBlobUnknown, "The image name holds no blob with this digest.")
	if err := f.deleteItem(ctx, rq, target, BlobPath(name, digest), unknown); err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}

// deleteItem removes the item at path in one transaction, under the lock of the image name's package and its ownership check
// (R-AUTHZ-018). The hook holds the cascade and the refusals. An item that is not there answers missing; one that went between the
// look and the transaction is gone already and the delete is repeatable.
func (f *Format) deleteItem(ctx context.Context, rq *formats.Request, target formats.Target, path string, missing error) error {
	repo := rq.RepoRef()
	if _, found, err := rq.Host.Items().Stat(ctx, repo, formats.Path{Path: path}); err != nil {
		return err
	} else if !found {
		return missing
	}
	// A name that holds only blobs has no package: nothing to lock and nobody to own it, and none is made for the delete.
	_, exists, err := rq.Host.Packages().Package(ctx, repo, target.Package)
	if err != nil {
		return err
	}
	return rq.Host.Write(ctx, rq, func(_ formats.Tx, pw formats.PackageWriter) error {
		if exists {
			if _, _, err := pw.EnsureLocked(ctx, repo, target.Package, target.Package); err != nil {
				return err
			}
			if err := pw.Claim(ctx, target, formats.OwnershipWrite{}); err != nil {
				return err
			}
		}
		return pw.DeleteItems(ctx, repo, path)
	})
}
