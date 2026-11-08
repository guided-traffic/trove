package oci

import (
	"context"
	"mime"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// RevalidationRequest is the revalidation of an expired tag document (R-OCI-022): a HEAD on the same URL with the same Accept, whose
// Docker-Content-Digest says whether the tag still points at the stored manifest. Docker Hub counts the GET of a manifest against its
// pull limit and not the HEAD, so an unchanged tag costs no pull. The tags list has no digest header and is revalidated by the
// engine's conditional GET.
func (remoteAdapter) RevalidationRequest(ctx context.Context, _ formats.RemoteContext, p formats.Path, _ string, _ formats.RemoteDocument, get *http.Request) (*http.Request, bool, error) {
	if t, err := parseTarget(p); err == nil && t.kind == dirTags && get != nil {
		req := get.Clone(ctx)
		req.Method, req.Body, req.GetBody, req.ContentLength = http.MethodHead, nil, nil, 0
		return req, true, nil
	}
	return nil, false, nil
}

// Unchanged reads the answer of the HEAD: the digest the upstream announces equals the stored manifest's, and the type it announces,
// when it announces one, is the stored one. An answer without the header, or with another digest, is not unchanged and the engine
// fetches the manifest with a GET that carries no validator of the stored copy.
func (remoteAdapter) Unchanged(_ formats.Path, stored formats.RemoteDocument, resp *http.Response) bool {
	want, ok := announcedDigest(resp.Header)
	if !ok || want != stored.Digest {
		return false
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && stored.MediaType != "" && mediaTypeOnly(ct) != stored.MediaType {
		return false
	}
	return true
}

// mediaTypeOnly is the media type of a Content-Type value without its parameters, "" for a value that is none.
func mediaTypeOnly(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return mt
}
