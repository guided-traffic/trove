package oci

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// The storage layout of R-OCI-007. Segments that start with '_' cannot be name components (R-OCI-002), so the layout never
// collides with an image name.
const (
	dirManifests = "_manifests"
	dirTags      = "_tags"
	dirBlobs     = "_blobs"
	fileTaglist  = "_taglist"
)

// ManifestPath is the item path of a manifest by digest, "<name>/_manifests/sha256:<hex>".
func ManifestPath(name, digest string) string { return name + "/" + dirManifests + "/" + digest }

// TagPath is the item path of a tag, "<name>/_tags/<tag>": the bytes of the manifest the tag points at.
func TagPath(name, tag string) string { return name + "/" + dirTags + "/" + tag }

// TaglistPath is the item path of a remote repository's tags list document, "<name>/_taglist" (R-OCI-024).
func TaglistPath(name string) string { return name + "/" + fileTaglist }

// BlobPath is the item path of a blob of an image name, "<name>/_blobs/sha256:<hex>".
func BlobPath(name, digest string) string { return name + "/" + dirBlobs + "/" + digest }

// splitLayout splits a canonical path into the image name and the layout part after it: kind is one of the directory names
// and tail what follows it ("" for the tags list). ok is false for a path outside the layout.
func splitLayout(p string) (name, kind, tail string, ok bool) {
	segs := strings.Split(p, "/")
	n := len(segs)
	if n >= 2 && segs[n-1] == fileTaglist {
		return strings.Join(segs[:n-1], "/"), fileTaglist, "", true
	}
	if n >= 3 {
		switch segs[n-2] {
		case dirManifests, dirTags, dirBlobs:
			return strings.Join(segs[:n-2], "/"), segs[n-2], segs[n-1], true
		}
	}
	return "", "", "", false
}

// PackageDocumentPath is the package document of an image name: its tags list, "<name>/_taglist" (R-OCI-001).
func (f *Format) PackageDocumentPath(name string) (formats.Path, bool) {
	n, ok := names{}.Normalize(name)
	if !ok {
		return formats.Path{}, false
	}
	return formats.Path{Path: TaglistPath(n)}, true
}

var _ formats.VersionPather = (*Format)(nil)

// VersionPath is the path of a tag, "<name>/_tags/<tag>": a version of an image is a tag (the package rows record tags), and the
// tag's item holds the bytes of the manifest it points at, which is what a version shows. The decision point decides a tag on it
// as well as on the tags list, so a path pattern that hides a tag hides it from the package screens as the tags list hides it
// (Q-AUTHZ-258); a finding of an image names its manifest's digest, so no vulnerability report lists a tag. A name or a tag the grammar refuses
// names no version.
func (f *Format) VersionPath(name, tag string) (formats.Path, bool) {
	n, ok := names{}.Normalize(name)
	if !ok || !ValidTag(tag) {
		return formats.Path{}, false
	}
	return formats.Path{Path: TagPath(n, tag)}, true
}

// Classify says how a canonical path of the layout behaves (R-OCI-007): manifests and blobs are immutable content, a tag and
// the tags list are metadata that expires on a remote repository. A path outside the layout is metadata of no package.
func (f *Format) Classify(p formats.Path) formats.PathInfo {
	name, kind, tail, ok := splitLayout(p.Path)
	if !ok || !ValidName(name) {
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	info := formats.PathInfo{Package: name}
	switch kind {
	case dirManifests, dirBlobs:
		info.Kind = formats.Content
	case dirTags:
		info.Kind, info.Version = formats.PathMetadata, tail
		if !ValidTag(tail) {
			info.Version = ""
		}
	default:
		info.Kind = formats.PathMetadata
	}
	return info
}
