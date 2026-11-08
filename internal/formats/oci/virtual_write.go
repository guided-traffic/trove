package oci

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
)

// What a client does to a virtual repository when it writes (R-OCI-028). The write routes serve the classes local and virtual: the
// dispatcher keeps the repository the client addressed (a redirect to the deployment target would replace it, and the session and the
// Location of an upload would then name the target, H-663), and the handler resolves the target itself. Everything that is decided or
// committed acts on a request made for the target ([formats.Request.WriteTarget]); the upload sessions are called with the request for
// the addressed repository, whose session records both (R-OCI-011), and every Location is built for the addressed repository.

// writeRequest returns the request a write is decided and committed on: rq itself for a local repository, a request made for the
// deployment target for a virtual one (405 without one, naming the missing target).
func writeRequest(ctx context.Context, rq *formats.Request) (*formats.Request, error) {
	if rq.Repo.Class != formats.Virtual {
		return rq, nil
	}
	target, err := rq.Host.Resolver().DeployTarget(ctx, rq)
	if err != nil {
		return nil, err
	}
	if target.Class != formats.Local {
		return nil, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "The deployment target of the virtual repository is not a local repository.", errs.WithAllow(http.MethodGet, http.MethodHead))
	}
	return rq.WriteTarget(target), nil
}

// virtualDeleteRefused is the answer to a delete through a virtual repository (R-OCI-028): the content is deleted in the repository that holds it.
func virtualDeleteRefused(allow ...string) error {
	return errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed,
		"A virtual repository does not delete content; delete it in the repository that holds it.", errs.WithAllow(allow...))
}

// memWriter collects an answer in memory, to read a manifest of a member without answering the client with it.
type memWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (w *memWriter) Header() http.Header {
	if w.h == nil {
		w.h = http.Header{}
	}
	return w.h
}

func (w *memWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}

var errMemOverflow = errors.New("the answer is larger than a manifest may be")

func (w *memWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if w.buf.Len()+len(b) > MaxManifestBytes {
		return 0, errMemOverflow
	}
	return w.buf.Write(b)
}

// readRequest is the request of a read the push makes on its own account: a GET of the manifest of the digest, without the client's
// conditions, so that the members answer it as they answer a pull.
func readRequest(rq *formats.Request, name, digest string) *formats.Request {
	r := rq.Clone(rq.Context())
	r.Method = http.MethodGet
	for _, k := range []string{"If-None-Match", "If-Match", "If-Range", "If-Modified-Since", "If-Unmodified-Since", "Range"} {
		r.Header.Del(k)
	}
	c := *rq
	c.Request = r
	c.Params = formats.Params{"name": name, "reference": digest}
	return &c
}

// memberManifest reads the manifest of a digest from the members, as a pull would but without a request to an upstream (the first two
// tiers of R-REPO-019 step 6): the link step brings in what a member holds, it never fetches. ok is false when no member holds it.
func (f *Format) memberManifest(ctx context.Context, rq *formats.Request, name, digest string) (data []byte, mediaType string, ok bool, err error) {
	grq := readRequest(rq, name, digest)
	steps, err := f.digestSteps(ctx, grq, name, digest, f.manifestLookup(), false)
	if err != nil {
		return nil, "", false, err
	}
	mw := &memWriter{}
	if err := walk(ctx, grq, mw, steps, errManifestUnknown); err != nil {
		if isMemberMiss(err) || errors.Is(err, errMemOverflow) || isMemberFailure(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	if mw.code != http.StatusOK {
		return nil, "", false, nil
	}
	return mw.buf.Bytes(), mw.Header().Get("Content-Type"), true, nil
}

// linkFromMembers is the link step of a manifest push through a virtual repository (R-OCI-028): every blob or manifest the manifest
// names that the deployment target lacks is linked into it from the member that holds it (a local member, or the cache of a remote
// member), when the caller may read it there, before the reference check of R-OCI-013 step 4. The source is the virtual repository by
// name: the host resolves it as a read of the virtual repository is resolved (the routing of the name, the per-member read check, the
// claims, the finding block), so a link can never reach what a pull could not. What cannot be linked is left to the reference check,
// which answers MANIFEST_BLOB_UNKNOWN. live is the number of live versions of the name in the target.
func (f *Format) linkFromMembers(ctx context.Context, orq, trq *formats.Request, st Store, name string, live int, m *manifest.Manifest, depth int) error {
	if depth >= maxLinkDepth {
		return nil
	}
	var missing []manifest.Reference
	var paths []string
	for _, r := range m.References() {
		if !r.Required {
			continue
		}
		missing = append(missing, r)
		if r.Kind == manifest.KindManifest {
			paths = append(paths, ManifestPath(name, r.Digest))
		} else {
			paths = append(paths, BlobPath(name, r.Digest))
		}
	}
	have, err := st.ExistingItems(ctx, nil, trq.RepoRef().ID, paths)
	if err != nil {
		return err
	}
	stored := make(map[string]struct{}, len(have))
	for _, p := range have {
		stored[p] = struct{}{}
	}
	for i, r := range missing {
		if _, ok := stored[paths[i]]; ok {
			continue
		}
		if r.Kind == manifest.KindManifest {
			err = f.linkManifest(ctx, orq, trq, st, name, live, r.Digest, depth)
		} else {
			err = f.linkItem(ctx, orq, trq, name, BlobPath(name, r.Digest), blobContentType, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// linkManifest links a manifest the target lacks, after the blobs and manifests it names: it is read from the members, parsed and checked
// against its digest, and committed with the payload of a push by digest, so that the hook records its row and its references.
func (f *Format) linkManifest(ctx context.Context, orq, trq *formats.Request, st Store, name string, live int, digest string, depth int) error {
	data, mt, ok, err := f.memberManifest(ctx, orq, name, digest)
	if err != nil || !ok {
		return err
	}
	child, ok := f.parseMember(data, mt, digest)
	if !ok {
		return nil // not a manifest the registry reads, or not the bytes of its digest: the reference check answers
	}
	if err := f.linkFromMembers(ctx, orq, trq, st, name, live, child, depth+1); err != nil {
		return err
	}
	skipped, err := f.shadowCheck(ctx, trq, name, live)
	if err != nil {
		return err
	}
	return f.linkItem(ctx, orq, trq, name, ManifestPath(name, digest), child.MediaType,
		&manifestCommit{&pushed{name: name, m: child, shadowSkipped: skipped}})
}

// parseMember reads the manifest a member answered with, as a push is read, and checks it against the digest it was asked by.
func (f *Format) parseMember(data []byte, contentType, digest string) (*manifest.Manifest, bool) {
	mediaType, err := manifest.MediaTypeOf(contentType)
	if err != nil {
		return nil, false
	}
	m, err := manifest.Parse(data, mediaType, f.parseLimits())
	if err != nil || m.Digest != digest {
		return nil, false
	}
	return m, true
}

// linkItem links one item of the virtual repository into the target at the same path. The caller needs deploy on the target's path; an
// item the source does not hold, or that the caller may not read there, is not linked and is no error here.
func (f *Format) linkItem(ctx context.Context, orq, trq *formats.Request, name, path, mediaType string, payload any) error {
	if err := trq.Host.Authorize(ctx, trq, formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	_, err := trq.Host.Ingest().Link(ctx, trq, formats.SourceRef{Repo: orq.Repo.Name, Path: path},
		formats.Commit{Path: formats.Path{Path: path}, MediaType: mediaType, Payload: payload})
	if errs.KindOf(err) == errs.NotFound {
		return nil
	}
	return err
}
