package qname

import (
	"net/url"
	"strings"
)

// Family is the path family of an address.
type Family string

// The families that address a repository: the protocol routes /api/<format>/<project>/<key>
// and the raw-file routes /files/<project>/<key>.
const (
	FamilyAPI   Family = "api"
	FamilyFiles Family = "files"
)

// Address is a repository address read from a request path.
type Address struct {
	Family Family
	// Format is the format ID of an /api/<format>/… address, empty for /files.
	Format string
	Repo   Repo
	// CacheView is true when the repository segment carried the cache suffix.
	CacheView bool
	// Rest is what follows the repository segment and its slash, still escaped: the
	// path is decoded once, after it was split (R-ARCH-005). Empty when there is none.
	Rest string
}

// DotSegments reports whether Rest has a "." or ".." segment as received. The router
// answers such a path with a redirect, and the Bastion does not serve it (R-BAS-003).
func (a Address) DotSegments() bool {
	for _, seg := range strings.Split(a.Rest, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// ParseAddress is the one parser of protocol and raw-file paths:
//
//	/api/<format>/<project>/<key>[~cache][/<rest>]
//	/files/<project>/<key>[~cache][/<rest>]
//
// The path is the escaped path as received. A segment that names the family, the format,
// the project or the key is refused when it holds a "%", whatever it decodes to: the
// router decodes before it compares, so such a spelling is not one every component
// classifies the same (R-BAS-018). One segment after the format (/api/npm/web) is no
// address, and a "~cache" suffix is read on the repository key only. The format shape
// excludes /api/v1. Whether the project or the repository exists is not asked here.
func ParseAddress(escapedPath string) (Address, bool) {
	tail, ok := strings.CutPrefix(escapedPath, "/")
	if !ok {
		return Address{}, false
	}
	family, tail, _ := strings.Cut(tail, "/")
	var a Address
	switch Family(family) {
	case FamilyAPI:
		var format string
		format, tail, ok = strings.Cut(tail, "/")
		if !ok || !ValidFormat(format) {
			return Address{}, false
		}
		a.Family, a.Format = FamilyAPI, format
	case FamilyFiles:
		a.Family = FamilyFiles
	default:
		return Address{}, false
	}
	project, tail, ok := strings.Cut(tail, "/")
	if !ok || !ValidProjectKey(project) {
		return Address{}, false
	}
	seg, rest, _ := strings.Cut(tail, "/")
	key, cache := strings.CutSuffix(seg, CacheSuffix)
	if !ValidKey(key) {
		return Address{}, false
	}
	a.Repo = Repo{Project: project, Key: key}
	a.CacheView = cache
	a.Rest = rest
	return a, true
}

// ProtocolBase renders a protocol base path template of a format ("/api/npm/{project}/{key}/",
// "/files/{project}/{key}/") for a repository. The values are escaped as one path
// segment each, so a slash never joins them.
func ProtocolBase(tmpl string, r Repo) string {
	return strings.NewReplacer("{project}", url.PathEscape(r.Project), "{key}", url.PathEscape(r.Key)).Replace(tmpl)
}

// FilesBase is the base path of the raw files of a repository, "/files/<project>/<key>/".
func FilesBase(r Repo) string { return ProtocolBase("/files/{project}/{key}/", r) }
