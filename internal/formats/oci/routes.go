package oci

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The names of the routes (R-OCI-009 to R-OCI-018): "oci.<resource>" and, after a dot, the operation. The request log and the
// metrics name the handler of a request by it.
const (
	routeManifestGetTag = "oci.manifest.get.tag"
	routeManifestGetDgs = "oci.manifest.get.digest"
	routeManifestPut    = "oci.manifest.put"
	routeManifestDelete = "oci.manifest.delete"
	routeBlobGet        = "oci.blob.get"
	routeBlobDelete     = "oci.blob.delete"
	routeUploadStart    = "oci.upload.start"
	routeUploadStatus   = "oci.upload.status"
	routeUploadPatch    = "oci.upload.patch"
	routeUploadPut      = "oci.upload.put"
	routeUploadCancel   = "oci.upload.cancel"
	routeTagsList       = "oci.tags.list"
	routeReferrers      = "oci.referrers"
)

// The words of the protocol path that follow the image name (R-OCI-001).
const (
	wordManifests = "manifests"
	wordBlobs     = "blobs"
	wordUploads   = "uploads"
	wordTags      = "tags"
	wordList      = "list"
	wordReferrers = "referrers"
)

// segments splits an escaped repository-relative path into its segments, as received: nothing is decoded. A '%' in an image name,
// a tag or a digest is no character of their grammars (R-OCI-002), so a name that arrives encoded stays encoded and is refused with
// 400 NAME_INVALID by [Format.named] before any lookup; decoding it here would turn "a%2Fb" into the two components "a/b". A trailing
// slash is reported separately (the upload start is "<name>/blobs/uploads/"); any other empty segment is no match, so that the
// dispatcher's 404 answers it.
func segments(escaped string) (segs []string, trailing, ok bool) {
	p := strings.TrimPrefix(escaped, "/")
	if strings.HasSuffix(p, "/") {
		trailing = true
		p = strings.TrimSuffix(p, "/")
	}
	if p == "" {
		return nil, false, false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			return nil, false, false
		}
		segs = append(segs, s)
	}
	return segs, trailing, true
}

// A route's name is everything before the protocol words, joined with '/': it is not checked against the grammar here, so
// that a name outside it answers 400 NAME_INVALID from the handler (R-OCI-002) and not a 404.
func imageName(segs []string) string { return strings.Join(segs, "/") }

// matchWord matches "<name>/<word>/<last>" with the word second to last.
func matchWord(word, param string) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		segs, trailing, ok := segments(escaped)
		n := len(segs)
		if !ok || trailing || n < 3 || segs[n-2] != word {
			return nil, false
		}
		return formats.Params{"name": imageName(segs[:n-2]), param: segs[n-1]}, true
	}
}

// matchBlob matches "<name>/blobs/<digest>"; the upload start has a trailing slash and is not a blob.
func matchBlob(escaped string) (formats.Params, bool) {
	return matchWord(wordBlobs, "digest")(escaped)
}

// matchUploadStart matches "<name>/blobs/uploads/" (the trailing slash is required, as the specification writes it).
func matchUploadStart(escaped string) (formats.Params, bool) {
	segs, trailing, ok := segments(escaped)
	n := len(segs)
	if !ok || !trailing || n < 3 || segs[n-1] != wordUploads || segs[n-2] != wordBlobs {
		return nil, false
	}
	return formats.Params{"name": imageName(segs[:n-2])}, true
}

// matchUploadSession matches "<name>/blobs/uploads/<session>".
func matchUploadSession(escaped string) (formats.Params, bool) {
	segs, trailing, ok := segments(escaped)
	n := len(segs)
	if !ok || trailing || n < 4 || segs[n-2] != wordUploads || segs[n-3] != wordBlobs {
		return nil, false
	}
	return formats.Params{"name": imageName(segs[:n-3]), "session": segs[n-1]}, true
}

// matchTagsList matches "<name>/tags/list".
func matchTagsList(escaped string) (formats.Params, bool) {
	segs, trailing, ok := segments(escaped)
	n := len(segs)
	if !ok || trailing || n < 3 || segs[n-2] != wordTags || segs[n-1] != wordList {
		return nil, false
	}
	return formats.Params{"name": imageName(segs[:n-2])}, true
}

// matchManifest matches "<name>/manifests/<reference>"; the reference is a tag or a digest, checked by the handler.
func matchManifest(escaped string) (formats.Params, bool) {
	return matchWord(wordManifests, "reference")(escaped)
}

// matchManifestByTag and matchManifestByDigest split the pull of a manifest in two routes by the reference: a tag cannot hold ':',
// a digest always does, so the split is by syntax and a malformed digest answers DIGEST_INVALID from the handler. The two have
// different path kinds (a tag is metadata, a manifest by digest is immutable content, R-OCI-007).
func matchManifestByTag(escaped string) (formats.Params, bool) {
	p, ok := matchManifest(escaped)
	if !ok || strings.Contains(p["reference"], ":") {
		return nil, false
	}
	return p, true
}

func matchManifestByDigest(escaped string) (formats.Params, bool) {
	p, ok := matchManifest(escaped)
	if !ok || !strings.Contains(p["reference"], ":") {
		return nil, false
	}
	return p, true
}

// matchReferrers matches "<name>/referrers/<digest>".
func matchReferrers(escaped string) (formats.Params, bool) {
	return matchWord(wordReferrers, "digest")(escaped)
}

// buildRoutes is the whole endpoint map of R-OCI-009 to R-OCI-018, written once with the name of every handler, so that a work
// package that builds an operation replaces its stub file with a file of its own and never edits this table.
//
// The writes are Classes local, so that a remote repository answers 405 and the dispatcher routes a write addressed to a
// virtual repository to its deployment target (R-OCI-028). The reads stay open to every class the format supports.
func (f *Format) buildRoutes() []formats.Route {
	const (
		read   = formats.ActionRead
		deploy = formats.ActionDeploy
		del    = formats.ActionDelete
		dgst   = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	)
	route := func(name, method, tmpl string, kind formats.RouteKind, action formats.Action,
		match func(string) (formats.Params, bool), examples []string, h formats.HandlerFunc,
	) formats.Route {
		r := formats.Route{
			Name: name, Method: method, Template: tmpl, Examples: examples, Action: action, Kind: kind,
			Match: match, Handler: f.named(h),
		}
		r.PackageParam = "name"
		if action != read {
			r.Classes = []formats.Class{formats.Local}
		}
		return r
	}
	return []formats.Route{
		route(routeManifestGetTag, http.MethodGet, "/{name...}/manifests/{tag}", formats.Metadata, read,
			matchManifestByTag, []string{"app/manifests/1", "library/nginx/manifests/latest"}, f.handleManifestGet),
		route(routeManifestGetDgs, http.MethodGet, "/{name...}/manifests/{digest}", formats.Download, read,
			matchManifestByDigest, []string{"app/manifests/" + dgst, "library/nginx/manifests/" + dgst}, f.handleManifestGet),
		route(routeManifestPut, http.MethodPut, "/{name...}/manifests/{reference}", formats.Upload, deploy,
			matchManifest, []string{"app/manifests/1", "team/app/manifests/" + dgst}, f.handleManifestPut),
		route(routeManifestDelete, http.MethodDelete, "/{name...}/manifests/{reference}", formats.Delete, del,
			matchManifest, []string{"app/manifests/1", "team/app/manifests/" + dgst}, f.handleDeleteManifest),

		route(routeBlobGet, http.MethodGet, "/{name...}/blobs/{digest}", formats.Download, read,
			matchBlob, []string{"app/blobs/" + dgst, "team/app/blobs/" + dgst}, f.handleBlobGet),
		route(routeBlobDelete, http.MethodDelete, "/{name...}/blobs/{digest}", formats.Delete, del,
			matchBlob, []string{"app/blobs/" + dgst}, f.handleDeleteBlob),

		route(routeUploadStart, http.MethodPost, "/{name...}/blobs/uploads/", formats.Upload, deploy,
			matchUploadStart, []string{"app/blobs/uploads/", "team/app/blobs/uploads/"}, f.handleUploadStart),
		route(routeUploadStatus, http.MethodGet, "/{name...}/blobs/uploads/{session}", formats.Upload, deploy,
			matchUploadSession, []string{"app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11"}, f.handleUploadStatus),
		route(routeUploadPatch, http.MethodPatch, "/{name...}/blobs/uploads/{session}", formats.Upload, deploy,
			matchUploadSession, []string{"app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11"}, f.handleUploadPatch),
		route(routeUploadPut, http.MethodPut, "/{name...}/blobs/uploads/{session}", formats.Upload, deploy,
			matchUploadSession, []string{"app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11"}, f.handleUploadPut),
		route(routeUploadCancel, http.MethodDelete, "/{name...}/blobs/uploads/{session}", formats.Upload, deploy,
			matchUploadSession, []string{"app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11"}, f.handleUploadCancel),

		route(routeTagsList, http.MethodGet, "/{name...}/tags/list", formats.Metadata, read,
			matchTagsList, []string{"app/tags/list", "team/app/tags/list"}, f.handleTagsList),
		route(routeReferrers, http.MethodGet, "/{name...}/referrers/{digest}", formats.Volatile, read,
			matchReferrers, []string{"app/referrers/" + dgst}, f.handleReferrers),
	}
}

// named refuses a request whose image name is outside the grammar of R-OCI-002, or whose whole name as the client sends it is above
// 255 bytes, with 400 NAME_INVALID before the handler runs: upper-case letters are refused and never folded, and an encoded character
// is refused as it is. The repository part of the whole name is the addressed repository's project and key.
func (f *Format) named(h formats.HandlerFunc) formats.HandlerFunc {
	return func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		name := rq.Params.Get("name")
		if !ValidName(name) || !ValidFullName(rq.Repo.Name.Project, rq.Repo.Name.Key, name) {
			return errs.E(errs.Invalid, errs.CodeOCINameInvalid, "The image name is outside the grammar of the distribution specification or longer than 255 bytes with the repository.")
		}
		return h(ctx, rq, w)
	}
}
