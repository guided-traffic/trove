package ansible

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// Two kinds of path meet in this format and must not be mixed up.
//
// A protocol path is what a client requests below /api/ansible/<projectKey>/<repoKey>/: "v3/collections/acme/tools/versions/1.0.0/".
// Routes match protocol paths (match.go).
//
// A canonical path is where the item lives in the repository, the path of /files/<projectKey>/<repoKey>/ and of the remote cache:
// "collections/acme/tools/acme-tools-1.0.0.tar.gz" (R-ANS-002). Classify, PackageDocumentPath, VersionPath and the helpers below
// speak canonical paths.

// The directories and files of the canonical layout (R-ANS-002).
const (
	dirCollections = "collections"
	dirRoles       = "roles"
	// RootDocument is the stored discovery document of a remote's upstream (R-ANS-016).
	RootDocument = "api-root.json"
	// CollectionDocument, VersionsDocument and the "<version>.json" documents are the stored documents of a collection.
	CollectionDocument = "collection.json"
	VersionsDocument   = "versions.json"
	jsonSuffix         = ".json"
)

// maxRoleIDDigits bounds the upstream's numeric role id (R-ANS-020).
const maxRoleIDDigits = 12

// CollectionDir is the canonical directory of a collection, "collections/<ns>/<name>".
func CollectionDir(namespace, name string) string {
	return dirCollections + "/" + namespace + "/" + name
}

// ArtifactPath is the canonical path of the archive of a version, "collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz".
func ArtifactPath(namespace, name, ver string) string {
	return CollectionDir(namespace, name) + "/" + collection.FileName(namespace, name, ver)
}

// CollectionDocumentPath is the canonical path of the collection document of a collection.
func CollectionDocumentPath(namespace, name string) string {
	return CollectionDir(namespace, name) + "/" + CollectionDocument
}

// VersionsDocumentPath is the canonical path of the stored version-list page of a collection (the variant is "offset=<n>").
func VersionsDocumentPath(namespace, name string) string {
	return CollectionDir(namespace, name) + "/" + VersionsDocument
}

// VersionDocumentPath is the canonical path of the document of one version.
func VersionDocumentPath(namespace, name, ver string) string {
	return CollectionDir(namespace, name) + "/" + ver + jsonSuffix
}

// PackageDocumentPath implements the contract: the canonical path of the document that stands for a normalized package name
// upstream, the collection document (R-REPO-029, R-REPO-048). ok is false for a string that is no collection name.
func (f *Format) PackageDocumentPath(name string) (formats.Path, bool) {
	if id, ok := strings.CutPrefix(name, RolePrefix); ok && digits(id, maxRoleIDDigits) {
		return formats.Path{Path: RoleVersionsDocumentPath(id)}, true
	}
	ns, n, ok := collection.SplitName(name)
	if !ok {
		return formats.Path{}, false
	}
	return formats.Path{Path: CollectionDocumentPath(ns, n)}, true
}

// VersionPath implements [formats.VersionPather] (Q-AUTHZ-255): the canonical path of the archive of a version, the file a path pattern
// that hides it hides the version by. ok is false for a pair that names no version.
func (f *Format) VersionPath(name, ver string) (formats.Path, bool) {
	ns, n, ok := collection.SplitName(name)
	if !ok || !version.Valid(ver) {
		return formats.Path{}, false
	}
	return formats.Path{Path: ArtifactPath(ns, n, ver)}, true
}

// Classify is the request descriptor of R-REPO-003 for a canonical path (R-ANS-002). A path it cannot classify is metadata, which
// costs a refetch and never freezes a package list: a name or a version that does not parse is metadata of no version.
func (f *Format) Classify(p formats.Path) formats.PathInfo {
	if p.Path == "" {
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	segs := strings.Split(p.Path, "/")
	switch segs[0] {
	case dirCollections:
		return classifyCollection(segs[1:])
	case dirRoles:
		return classifyRole(segs[1:])
	}
	return formats.PathInfo{Kind: formats.PathMetadata}
}

// classifyCollection classifies a path below "collections/": [<ns> [<name> [<file>]]].
func classifyCollection(rest []string) formats.PathInfo {
	meta := formats.PathInfo{Kind: formats.PathMetadata}
	if len(rest) < 2 {
		return meta
	}
	ns, name := rest[0], rest[1]
	if !collection.ValidPart(ns) || !collection.ValidPart(name) {
		return meta
	}
	meta.Package = ns + "." + name
	if len(rest) != 3 {
		return meta
	}
	file := rest[2]
	if _, ok := strings.CutSuffix(file, collection.ArchiveSuffix); ok {
		fns, fname, ver, ok := collection.ParseFileName(file)
		if !ok || fns != ns || fname != name || !version.Valid(ver) {
			return meta
		}
		return formats.PathInfo{Kind: formats.Content, Package: meta.Package, Version: ver}
	}
	if v, ok := strings.CutSuffix(file, jsonSuffix); ok && file != CollectionDocument && file != VersionsDocument && version.Valid(v) {
		meta.Version = v
	}
	return meta
}

// RolePrefix is the first segment of the package name a role has: "role/<id>" (Q-ANS-36). No collection can have it, because a
// collection name has no slash.
const RolePrefix = "role/"

// RolePackage is the package name of the role with the upstream's numeric id.
func RolePackage(id string) string { return RolePrefix + id }

// RoleLookupDocument is the canonical path of the stored role lookup (variant "owner=<o>&name=<n>"); the version pages of a role are
// roles/<id>/versions.json (variant "page=<n>") and its archives roles/<id>/<ref>.tar.gz (R-ANS-020).
const (
	RoleLookupDocument   = dirRoles + "/lookup" + jsonSuffix
	roleVersionsDocument = "versions" + jsonSuffix
)

// RoleVersionsDocumentPath is the canonical path of the stored version-list page of a role.
func RoleVersionsDocumentPath(id string) string {
	return dirRoles + "/" + id + "/" + roleVersionsDocument
}

// RoleArchivePath is the canonical path of the archive of a role version.
func RoleArchivePath(id, ref string) string {
	return dirRoles + "/" + id + "/" + ref + collection.ArchiveSuffix
}

// classifyRole classifies a path below "roles/" (R-ANS-020, Q-ANS-36). The lookup document names no role. The version documents and
// the archive of the role with the upstream's id belong to the package "role/<id>", which no collection can have: the core's name
// machinery takes it (names.go), so the URL map, the include and exclude patterns and the grants decide a role like any package. A
// role has no version a grant could name, so the paths carry none.
func classifyRole(rest []string) formats.PathInfo {
	meta := formats.PathInfo{Kind: formats.PathMetadata}
	if len(rest) != 2 || !digits(rest[0], maxRoleIDDigits) {
		return meta
	}
	meta.Package = RolePackage(rest[0])
	ref, isArchive := strings.CutSuffix(rest[1], collection.ArchiveSuffix)
	if isArchive && roleRefOK(ref) {
		return formats.PathInfo{Kind: formats.Content, Package: meta.Package}
	}
	return meta
}

// roleRefOK reports whether s is the name of a role version, the grammar [A-Za-z0-9._+-]{1,128} (R-ANS-020).
func roleRefOK(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '+', c == '-':
		default:
			return false
		}
	}
	return true
}

// digits reports whether s is one to max decimal digits.
func digits(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// CanonicalPath returns the canonical path of the stored thing a matched read route addresses: the collection document, the version
// list's stored page document, the version document or the archive. ok is false for a route that addresses no stored path of a
// single collection (the root, an import task, the publish, the role routes). Params are the decoded and normalized parameters of
// the match.
func CanonicalPath(route string, p formats.Params) (string, bool) {
	switch route {
	case routeCollection:
		return CollectionDocumentPath(p[paramNamespace], p[paramName]), true
	case routeVersions:
		return VersionsDocumentPath(p[paramNamespace], p[paramName]), true
	case routeVersion:
		return VersionDocumentPath(p[paramNamespace], p[paramName], p[paramVersion]), true
	case routeArtifact:
		ns, name, ver, ok := collection.ParseFileName(p[paramFile])
		if !ok {
			return "", false
		}
		return ArtifactPath(ns, name, ver), true
	case routeRoleVersions:
		return RoleVersionsDocumentPath(p[paramID]), true
	case routeRoleArchive:
		ref, ok := strings.CutSuffix(p[paramFile], collection.ArchiveSuffix)
		if !ok {
			return "", false
		}
		return RoleArchivePath(p[paramID], ref), true
	}
	return "", false
}
