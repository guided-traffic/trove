package oci

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

func errBlobUnknown() error {
	return errs.E(errs.NotFound, errs.CodeOCIBlobUnknown, "No blob with this digest is stored under this image name in the repository.")
}

// blobContentType is the type of every blob response (R-OCI-008).
const blobContentType = "application/octet-stream"

// handleBlobGet is GET and HEAD of a blob of a local repository (R-OCI-010). A blob is readable through a name only when an item of
// that name holds it, never because another name or repository holds the same bytes: the item path carries the name. Ranges,
// conditional requests and the verifying reader are the item store's (R-STOR-012, R-STOR-013).
func (f *Format) handleBlobGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	switch rq.Repo.Class {
	case formats.Remote:
		return f.remoteBlobGet(ctx, rq, w)
	case formats.Local:
	default:
		return f.virtualBlobGet(ctx, rq, w)
	}
	name, digest := rq.Params.Get("name"), rq.Params.Get("digest")
	if !ValidDigest(digest) {
		return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
	}
	path := BlobPath(name, digest)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	return serveStored(ctx, rq, w, path, blobContentType, errBlobUnknown)
}
