package oci

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

const digestZero = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// conformance is the fixture of the skeleton: the checks that need a built protocol (the content headers of a served route, the
// revision of a push, the package view, the remote and virtual adapters) are skipped or listed as not built, each with its
// reason, and the work package that builds the operation removes the entry (the kit refuses a skip the format contradicts).
func conformance() formattest.Fixture {
	not := func(what string) string { return what + " is not built yet (T-16): its handler answers 501" }
	return formattest.Fixture{
		Rejects: []string{"app/manifests/%2e%2e", "../app/manifests/1", "app/manifests/1%00"},
		Paths: []formattest.PathCase{
			{Path: "app/_manifests/" + digestZero, Want: formats.PathInfo{Kind: formats.Content, Package: "app"}},
			{Path: "team/app/_blobs/" + digestZero, Want: formats.PathInfo{Kind: formats.Content, Package: "team/app"}},
			{Path: "team/app/_tags/1.0", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "team/app", Version: "1.0"}},
			{Path: "team/app/_taglist", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "team/app"}},
		},
		Host:      func() formattest.HostOptions { return formattest.HostOptions{Data: unbuiltStore{}} },
		Canonical: canonical,
		Protocol:  protocol,
		Inspect:   formattest.InspectJSON("team/app/_manifests/" + digestZero),
		Names:     []formattest.NameCase{{Spellings: []string{"library/nginx"}}, {Spellings: []string{"a__b/c-d"}}},
		Content: []formattest.ContentCase{
			{Name: "manifest by tag", Path: "app/manifests/1", Status: http.StatusNotImplemented},
			{Name: "manifest by digest", Path: "app/manifests/" + digestZero, Status: http.StatusNotImplemented},
			{Name: "blob", Path: "app/blobs/" + digestZero, Status: http.StatusNotImplemented},
			{Name: "tags list", Path: "app/tags/list", Status: http.StatusNotImplemented},
			{Name: "referrers", Path: "app/referrers/" + digestZero, Status: http.StatusNotImplemented},
		},
		NoContent: map[string]string{
			routeManifestGetTag: not("pulling a manifest"), routeManifestGetDgs: not("pulling a manifest"),
			routeBlobGet: not("pulling a blob"), routeTagsList: not("the tags list"), routeReferrers: not("the referrers"),
		},
		NoRevision: map[string]string{
			routeManifestPut: not("pushing a manifest"), routeManifestDelete: not("deleting a manifest"),
			routeBlobDelete: not("deleting a blob"), routeUploadStart: not("starting an upload"),
			routeUploadStatus: not("querying an upload"), routeUploadPatch: not("sending a chunk"),
			routeUploadPut: not("finishing an upload"), routeUploadCancel: not("cancelling an upload"),
			"OnCommit": not("recording a manifest"), "OnItemsRemoved": not("removing a manifest"),
		},
		Skip: map[formattest.Check]string{
			formattest.CheckRewrite: "local class only: the remote adapter joins with its work package (R-OCI-021)",
			formattest.CheckMerge:   "local class only: the virtual adapter joins with its work package (R-OCI-027)",
			formattest.CheckSetup:   "no clients yet: the setup snippets and the guide join with R-OCI-030",
			formattest.CheckView:    "no package view yet (R-OCI-031)",
		},
	}
}

// unbuiltStore is the in-memory twin of the data port of the skeleton: no handler reads it yet, and a call would panic on the nil
// interface, which a test of a built handler replaces with a real twin.
type unbuiltStore struct{ Store }

// canonical maps the example of a route to the canonical item path Classify is asked about; a route that addresses no item (an
// upload, the referrers) is held to its kind alone.
func canonical(example string) (string, bool) {
	segs := strings.Split(example, "/")
	n := len(segs)
	switch {
	case n >= 3 && segs[n-2] == wordManifests && ValidDigest(segs[n-1]):
		return ManifestPath(strings.Join(segs[:n-2], "/"), segs[n-1]), true
	case n >= 3 && segs[n-2] == wordManifests:
		return TagPath(strings.Join(segs[:n-2], "/"), segs[n-1]), true
	case n >= 3 && segs[n-2] == wordBlobs && segs[n-1] != wordUploads:
		return BlobPath(strings.Join(segs[:n-2], "/"), segs[n-1]), true
	case n >= 3 && segs[n-2] == wordTags && segs[n-1] == wordList:
		return TaglistPath(strings.Join(segs[:n-2], "/")), true
	}
	return "", false
}

// protocol is the inverse of canonical for the package document: "<name>/_taglist" is requested at "<name>/tags/list".
func protocol(canonical string) string {
	if name, ok := strings.CutSuffix(canonical, "/"+fileTaglist); ok {
		return name + "/tags/list"
	}
	return canonical
}

// TestR_ARCH_010_Conformance runs the conformance kit over the skeleton.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance())
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
