package oci

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/qname"
)

// The cross-repository blob mount (R-OCI-012). A mount names its source, the host checks it, and nothing of the answer says whether
// a source exists that the caller may not read: whatever is wrong with the source, the answer is the one of a request without
// `from` - a session is opened and the client uploads the bytes (202), as the distribution specification allows.

// MountSource parses the `from` of a mount into the repository and the image name it names. On a bound host (bound is the repository
// the host's root is bound to) `from` is an image name of that repository; on the shared host it is
// "<projectKey>/<repoKey>/<name>". A repository key with the cache suffix names the cache view of a remote repository: the repository
// returned is its base repository and cache is true, as the dispatcher and the registry token's grant read it. ok is false for a
// `from` that names no image: the caller then treats the mount as one without `from`.
func MountSource(from string, bound qname.Repo) (repo qname.Repo, name string, cache, ok bool) {
	if !bound.IsZero() {
		if !ValidName(from) {
			return qname.Repo{}, "", false, false
		}
		return bound, from, false, true
	}
	parts := strings.SplitN(from, "/", 3)
	if len(parts) != 3 {
		return qname.Repo{}, "", false, false
	}
	key, cache := strings.CutSuffix(parts[1], qname.CacheSuffix)
	repo = qname.Repo{Project: parts[0], Key: key}
	if !repo.Valid() || !ValidName(parts[2]) || !ValidFullName(repo.Project, repo.Key, parts[2]) {
		return qname.Repo{}, "", false, false
	}
	return repo, parts[2], cache, true
}

// handleMount is POST …/blobs/uploads/?mount=<digest>&from=<source>. The blob is linked to the item `<name>/_blobs/<digest>` of the
// request's repository through the host's Link, which resolves the source by its name and decides, as the caller, that the caller may
// read it, that the item is a live file whose blob is served and that no finding blocks it (R-STOR-008, Q-OCI-27). The target
// needs `deploy`. 201 when the blob is linked; 202 and a new session for everything else the source answers.
func (f *Format) handleMount(ctx context.Context, rq *formats.Request, w http.ResponseWriter, name, digest, from string) error {
	if !ValidDigest(digest) {
		return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
	}
	// On a bound host `from` is an image name of the repository the host is bound to, as the match read it: for a virtual repository that is the
	// virtual one, not the deployment target the write was redirected to (rq.Repo), and it is the repository the enforcement point checked.
	repo, srcName, _, ok := MountSource(from, rq.Endpoint.BoundRepo)
	if !ok {
		return f.startSession(ctx, rq, w, name)
	}
	path := BlobPath(name, digest)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	if err := checkOwnership(ctx, rq, name, path); err != nil {
		return err
	}
	_, err := rq.Host.Ingest().Link(ctx, rq, formats.SourceRef{Repo: repo, Path: BlobPath(srcName, digest)},
		formats.Commit{Path: formats.Path{Path: path}, MediaType: blobContentType})
	if err == nil {
		return blobCreated(w, rq, name, digest)
	}
	if errs.KindOf(err) == errs.NotFound {
		// The source repository, the name or the blob does not exist, or the caller may not read it, or the blob is blocked: one answer.
		return f.startSession(ctx, rq, w, name)
	}
	return err
}
