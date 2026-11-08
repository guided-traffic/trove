package oci

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
)

// Blob uploads (R-OCI-011). The handlers hold the protocol and nothing of a session: every call passes the image name of the path as
// the session's binding, and the host answers 404 BLOB_UPLOAD_UNKNOWN for a session of another principal, repository or name, a
// finished or expired one, and a `deploy` that is gone (internal/formathost/uploads.go). A session is never read by its id alone.

// uploadGone is the answer for a session id that names no session of this request.
func uploadGone() error {
	return errs.E(errs.NotFound, errs.CodeOCIBlobUploadUnknown, "The upload does not exist.")
}

func uploadInvalid(detail string) error {
	return errs.E(errs.Invalid, errs.CodeOCIBlobUploadInvalid, detail)
}

// sessionRefOf is the session of the path under the image name of the path. An id that is no UUID names no session.
func sessionRefOf(rq *formats.Request) (formats.SessionRef, error) {
	id, err := uuid.Parse(rq.Params.Get("session"))
	if err != nil {
		return formats.SessionRef{}, uploadGone()
	}
	return formats.SessionRef{ID: id, Binding: rq.Params.Get("name")}, nil
}

// rangeOf is the Range header of a session that holds received bytes: "0-<received-1>", and "0-0" for none, the form the
// distribution specification uses for an empty session.
func rangeOf(received int64) string {
	if received <= 0 {
		return "0-0"
	}
	return "0-" + strconv.FormatInt(received-1, 10)
}

// sessionLocation is the URL a client continues the session at.
func sessionLocation(rq *formats.Request, name string, id uuid.UUID) string {
	return rq.Host.Links(rq).Path(name + "/blobs/uploads/" + id.String())
}

// blobLocation is the URL of a stored blob.
func blobLocation(rq *formats.Request, name, digest string) string {
	return rq.Host.Links(rq).Path(name + "/blobs/" + digest)
}

// setSession sets the headers every answer about a session carries.
func setSession(w http.ResponseWriter, rq *formats.Request, name string, id uuid.UUID, received int64) {
	h := w.Header()
	h.Set("Location", sessionLocation(rq, name, id))
	h.Set("Range", rangeOf(received))
	h.Set("Docker-Upload-UUID", id.String())
}

// checkOwnership is the ownership rule of R-AUTHZ-018 asked before an upload: a blob is a write to the image name, so a caller who may
// not write a name that has an owner is refused at the start of the session, the monolithic upload, the mount and the finish of a
// session, before a body is read (Q-OCI-115). It is no publish: a blob claims nothing, the manifest push does. It is the first version
// of the name while the repository holds no live version of it, as the manifest push computes it: the rule then treats the name as
// unclaimed, which a deployer with the right to create names may write and a caller without it may not.
func checkOwnership(ctx context.Context, rq *formats.Request, name, path string) error {
	live, _, err := rq.Host.Packages().Versions(ctx, rq.RepoRef(), name)
	if err != nil {
		return err
	}
	return rq.Host.CheckOwnership(ctx, rq,
		formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionDeploy}}, formats.OwnershipWrite{FirstVersion: len(live) == 0})
}

// handleUploadStart is POST …/blobs/uploads/: a mount (`?mount=`), a monolithic upload (`?digest=`), or a new session.
func (f *Format) handleUploadStart(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, q := rq.Params.Get("name"), rq.URL.Query()
	switch {
	case q.Has("mount"):
		return f.handleMount(ctx, rq, w, name, q.Get("mount"), q.Get("from"))
	case q.Has("digest"):
		return f.handleMonolithic(ctx, rq, w, name, q.Get("digest"))
	}
	return f.startSession(ctx, rq, w, name)
}

// startSession opens a session of the image name on the repository of the request and answers 202 (R-OCI-011). The upload routes serve
// the classes local and virtual; through a virtual repository (R-OCI-028) the decisions act on its deployment target (rq), while the
// session records the repository the client addressed (addr) as well as the target, and its Location names the addressed one (H-663).
func (f *Format) startSession(ctx context.Context, addr *formats.Request, w http.ResponseWriter, name string) error {
	rq, err := writeRequest(ctx, addr)
	if err != nil {
		return err
	}
	// A session has no blob yet: `deploy` and the ownership of the name are decided on the name's document path (Q-OCI-116), `deploy`
	// first, so that a caller a path grant refuses never learns that the name has an owner.
	doc := TaglistPath(name)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: doc, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	if err := checkOwnership(ctx, rq, name, doc); err != nil {
		return err
	}
	sess, err := addr.Host.Uploads().Start(ctx, addr, rq.RepoRef(), name)
	if err != nil {
		return err
	}
	setSession(w, addr, name, sess.ID, 0)
	h := w.Header()
	h.Set("OCI-Chunk-Min-Length", strconv.FormatInt(sess.PartSize, 10))
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusAccepted)
	return nil
}

// handleMonolithic is POST …/blobs/uploads/?digest=<d> with the whole blob as the body: one request through the upload pipeline
// of R-STOR-004, no session. The digest is compared with the computed one after the body is spooled; a difference is 400
// DIGEST_INVALID and nothing is stored.
func (f *Format) handleMonolithic(ctx context.Context, addr *formats.Request, w http.ResponseWriter, name, digest string) error {
	sum, err := blobDigest(digest)
	if err != nil {
		return err
	}
	rq, err := writeRequest(ctx, addr)
	if err != nil {
		return err
	}
	path := BlobPath(name, digest)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	if err := checkOwnership(ctx, rq, name, path); err != nil {
		return err
	}
	sp, err := rq.Host.Ingest().Spool(ctx, rq, rq.Body, formats.IngestOptions{MediaType: blobContentType, Length: rq.ContentLength})
	if err != nil {
		return ioError(err)
	}
	if string(sp.Digests().SHA256) != string(sum[:]) {
		return errDigestInvalid("The digest of the content differs from the digest the request names.")
	}
	if _, err := rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{Path: formats.Path{Path: path}, MediaType: blobContentType}); err != nil {
		return err
	}
	return blobCreated(w, addr, name, digest)
}

// blobCreated answers 201 for a stored blob.
func blobCreated(w http.ResponseWriter, rq *formats.Request, name, digest string) error {
	h := w.Header()
	h.Set("Location", blobLocation(rq, name, digest))
	h.Set("Docker-Content-Digest", digest)
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
	return nil
}

// blobDigest parses the digest of a blob upload. Only sha256 is offered (R-OCI-002); the error says so.
func blobDigest(digest string) ([32]byte, error) {
	if digest == "" {
		return [32]byte{}, errDigestInvalid("The request names no digest: a blob is finished with ?digest=sha256:<hex>.")
	}
	sum, ok := manifest.ParseDigest(digest)
	if !ok {
		return [32]byte{}, errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
	}
	return sum, nil
}

// chunkStart reads the Content-Range of a chunk, "<start>-<end>" as the distribution specification writes it. It returns the start,
// or -1 for a request without the header (the chunk goes to the end of what the session holds). A header that is not that form,
// ends before it starts, or does not match the Content-Length is 400 BLOB_UPLOAD_INVALID.
func chunkStart(rq *formats.Request) (int64, error) {
	v := rq.Header.Get("Content-Range")
	if v == "" {
		return -1, nil
	}
	bad := func() error {
		return uploadInvalid("Content-Range is <start>-<end> in decimal, and covers as many bytes as the body holds.")
	}
	a, b, ok := strings.Cut(v, "-")
	if !ok {
		return 0, bad()
	}
	start, err1 := strconv.ParseInt(a, 10, 64)
	end, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start || a != strconv.FormatInt(start, 10) || b != strconv.FormatInt(end, 10) {
		return 0, bad()
	}
	if n := rq.ContentLength; n >= 0 && end-start+1 != n {
		return 0, bad()
	}
	return start, nil
}

// outOfOrder renders the 416 of a chunk the session cannot take with the headers of the specification: the bytes received as Range,
// the session's Location and Docker-Upload-UUID. Any other error passes unchanged.
func outOfOrder(err error, rq *formats.Request, name string, id uuid.UUID, received int64) error {
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.RangeNotSatisfiable {
		return err
	}
	return errs.E(e.Kind, e.Code, e.Detail, errs.WithCause(e),
		errs.WithHeader("Range", rangeOf(received)),
		errs.WithHeader("Location", sessionLocation(rq, name, id)),
		errs.WithHeader("Docker-Upload-UUID", id.String()))
}

// handleUploadPatch is PATCH <location>: a chunk, at the start its Content-Range names or at the end of what the session holds.
func (f *Format) handleUploadPatch(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if _, err := writeRequest(ctx, rq); err != nil {
		return err
	}
	ref, err := sessionRefOf(rq)
	if err != nil {
		return err
	}
	offset, err := chunkStart(rq)
	if err != nil {
		return err
	}
	received, err := rq.Host.Uploads().Append(ctx, rq, ref, offset, rq.Body)
	if err != nil {
		return outOfOrder(err, rq, ref.Binding, ref.ID, received)
	}
	setSession(w, rq, ref.Binding, ref.ID, received)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusAccepted)
	return nil
}

// handleUploadPut is PUT <location>?digest=<d>, optionally with the last bytes: it finishes the session and commits the blob as
// the item `<name>/_blobs/<digest>`. A digest other than the computed one is 400 DIGEST_INVALID and the session stays open.
func (f *Format) handleUploadPut(ctx context.Context, addr *formats.Request, w http.ResponseWriter) error {
	rq, err := writeRequest(ctx, addr)
	if err != nil {
		return err
	}
	ref, err := sessionRefOf(rq)
	if err != nil {
		return err
	}
	digest := rq.URL.Query().Get("digest")
	sum, err := blobDigest(digest)
	if err != nil {
		return err
	}
	start, err := chunkStart(rq)
	if err != nil {
		return err
	}
	// The finish of a blob asks the same as its start did, with the body still unread (Q-OCI-115): the window in which a name can gain
	// an owner under a running upload is this one request.
	// A refusal here is the answer for a session the caller may not continue (the host answers the same for a `PATCH` whose `deploy` is gone):
	// 404 BLOB_UPLOAD_UNKNOWN, so a session id tells a caller nothing about the name's owners or its grants.
	path := BlobPath(ref.Binding, digest)
	gone := func(err error) error {
		if k := errs.KindOf(err); k == errs.Forbidden || k == errs.NotFound || k == errs.Unauthenticated {
			return uploadGone()
		}
		return err
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: ref.Binding, Path: path, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return gone(err)
	}
	if err := checkOwnership(ctx, rq, ref.Binding, path); err != nil {
		return gone(err)
	}
	// The start of the last bytes is checked by the session under its claim, as for a chunk. A request without bytes names no offset.
	if rq.ContentLength == 0 {
		start = -1
	}
	// The host decides `deploy` on the blob's path as well before it commits (Uploads().Finalize).
	done, err := addr.Host.Uploads().Finalize(ctx, addr, ref, start, sum, rq.Body, formats.Commit{
		Path: formats.Path{Path: path}, MediaType: blobContentType,
	})
	if err != nil {
		// The 416 carries the range the session holds, which the port returns with the refusal (Committed.Received).
		return outOfOrder(err, addr, ref.Binding, ref.ID, done.Received)
	}
	return blobCreated(w, addr, ref.Binding, digest)
}

// handleUploadStatus is GET <location>: 204 with the bytes received.
func (f *Format) handleUploadStatus(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if _, err := writeRequest(ctx, rq); err != nil {
		return err
	}
	ref, err := sessionRefOf(rq)
	if err != nil {
		return err
	}
	sess, err := rq.Host.Uploads().Get(ctx, rq, ref)
	if err != nil {
		return err
	}
	setSession(w, rq, ref.Binding, ref.ID, sess.Received)
	w.Header().Set("OCI-Chunk-Min-Length", strconv.FormatInt(sess.PartSize, 10))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// handleUploadCancel is DELETE <location>: the session is aborted and what it staged removed (204). Cancelling is not a deletion
// of a blob (R-OCI-011).
func (f *Format) handleUploadCancel(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if _, err := writeRequest(ctx, rq); err != nil {
		return err
	}
	ref, err := sessionRefOf(rq)
	if err != nil {
		return err
	}
	if err := rq.Host.Uploads().Abort(ctx, rq, ref); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
