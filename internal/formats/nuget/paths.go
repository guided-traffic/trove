package nuget

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// Two kinds of path meet in this format and must not be mixed up (Q-NUGET-29).
//
// A protocol path is what a client requests below /api/nuget/<key>/: "v3/flatcontainer/
// acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg". Routes match protocol paths (match.go).
//
// A canonical path is where the item lives in the repository, the path of /files/<key>/
// and of the remote cache: "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg" (R-NUGET-005
// storage layout, R-NUGET-015 descriptors). Classify, PackageDocumentPath and the helpers
// below speak canonical paths.

// FeedSegment is the first segment of every protocol resource: the v3 root of a
// repository, <base>/api/nuget/<key>/v3 (R-NUGET-001).
const FeedSegment = "v3"

// The resources below the feed root, as the first segment after it (R-NUGET-001).
const (
	resFlatContainer    = "flatcontainer"
	resRegistration     = "registration"
	resRegistrationGz   = "registration-gz"
	resRegistrationSV2  = "registration-semver2"
	resSearch           = "search"
	resAutocomplete     = "autocomplete"
	resPackage          = "package"
	resSymbolPackage    = "symbolpackage"
	resSymbols          = "symbols"
	indexFile           = "index.json"
	nupkgSuffix         = ".nupkg"
	snupkgSuffix        = ".snupkg"
	nuspecSuffix        = ".nuspec"
	jsonSuffix          = ".json"
	registrationPageSeg = "page"
)

// The reserved first segments of the canonical layout that name no package (R-NUGET-005,
// R-NUGET-015). An ID cannot start with a dot, so none of them is a package.
const (
	canonDiscovery     = ".nuget"
	canonVulnerability = ".vulnerabilities"
	canonSymbols       = ".symbols"
	canonRegistration  = ".registration"
	canonEmbedded      = ".embedded"
)

// hives returns the registration hives in the order of the service index (R-NUGET-009).
func hives() []string { return []string{resRegistration, resRegistrationGz, resRegistrationSV2} }

// embeddedFiles returns the files a package may embed (R-NUGET-024).
func embeddedFiles() []string { return []string{"icon", "license", "readme"} }

// PackageDocumentPath is the canonical path of the version list of an ID, the package
// document of R-NUGET-001: "{lower_id}/index.json".
func PackageDocumentPath(lowerID string) string { return lowerID + "/" + indexFile }

// NupkgFile is the file name of a version's package, "{lower_id}.{lower_version}.nupkg".
func NupkgFile(lowerID, lowerVersion string) string {
	return lowerID + "." + lowerVersion + nupkgSuffix
}

// NupkgPath is the canonical path of the lead item of a version (R-NUGET-005):
// "{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg".
func NupkgPath(lowerID, lowerVersion string) string {
	return lowerID + "/" + lowerVersion + "/" + NupkgFile(lowerID, lowerVersion)
}

// NuspecPath is the canonical path of the generated .nuspec of a version.
func NuspecPath(lowerID, lowerVersion string) string {
	return lowerID + "/" + lowerVersion + "/" + lowerID + nuspecSuffix
}

// SnupkgFile is the file name of a version's symbol package (R-NUGET-020).
func SnupkgFile(lowerID, lowerVersion string) string {
	return lowerID + "." + lowerVersion + snupkgSuffix
}

// SnupkgPath is the canonical path of the symbol package of a version (R-NUGET-020).
func SnupkgPath(lowerID, lowerVersion string) string {
	return lowerID + "/" + lowerVersion + "/" + SnupkgFile(lowerID, lowerVersion)
}

// lowerID normalizes an ID for lookup (R-NUGET-003): the upstream rule decides whether it
// can be an ID at all, the lower-case form is the package name. Looking up accepts every ID
// a remote repository may serve, so a legacy ID outside ASCII is a valid lookup; the
// narrower rule of a local repository is checked at ingestion.
func lowerID(s string) (string, bool) {
	if id.ValidateUpstream(s) != nil {
		return "", false
	}
	return id.Lower(s), true
}

// urlForm normalizes a version of a path to its URL form (R-NUGET-003): any spelling, any
// casing and build metadata are accepted and resolved through the one identity.
func urlForm(s string) (string, bool) {
	v, err := version.Parse(s)
	if err != nil {
		return "", false
	}
	return v.URLForm(), true
}

// versionOfFile returns the URL form of the version a package file of a path names, when
// the file is "{id}.{version}{suffix}" for the ID of the path (R-NUGET-008: the file name
// must equal the ID and version of the directories above it, in any casing and spelling).
// rawID is the ID segment as the client wrote it, because the file is compared to it.
func versionOfFile(rawID, file, suffix string) (string, bool) {
	rest, ok := cutPrefixFold(file, rawID+".")
	if !ok {
		return "", false
	}
	rest, ok = cutSuffixFold(rest, suffix)
	if !ok {
		return "", false
	}
	return urlForm(rest)
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) < len(suffix) || !strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return "", false
	}
	return s[:len(s)-len(suffix)], true
}

// routingName is the routing name of a symbol key (R-NUGET-015): the first segment of the
// key, which is the file name a debugger looks for, without its last extension, lowercased.
// Every key shape gets one, so a pattern, a claim or a pin rule protects every symbol file.
func routingName(key string) string {
	first, _, _ := strings.Cut(key, "/")
	if i := strings.LastIndexByte(first, '.'); i > 0 {
		first = first[:i]
	}
	return strings.ToLower(first)
}

// Classify is the request descriptor of R-NUGET-015 for a canonical path. A path it cannot
// classify is metadata, which costs a refetch and never freezes a package list (R-REPO-003).
// The version of a path must be in its URL form: a path with another spelling names no
// stored item.
func (f *Format) Classify(p formats.Path) formats.PathInfo {
	if p.Path == "" {
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	segs := strings.Split(p.Path, "/")
	switch segs[0] {
	case canonDiscovery, canonVulnerability:
		return formats.PathInfo{Kind: formats.PathMetadata}
	case canonSymbols:
		if len(segs) == 1 {
			return formats.PathInfo{Kind: formats.PathMetadata}
		}
		return formats.PathInfo{Kind: formats.Content, RoutingName: routingName(strings.Join(segs[1:], "/"))}
	}
	name, ok := lowerID(segs[0])
	if !ok || name != segs[0] {
		return formats.PathInfo{Kind: formats.PathMetadata}
	}
	info := formats.PathInfo{Kind: formats.PathMetadata, Package: name}
	rest := segs[1:]
	switch {
	case len(rest) == 0:
	case len(rest) == 1 && rest[0] == indexFile:
	case len(rest) >= 1 && rest[0] == canonRegistration:
	case len(rest) == 1: // a version folder
		if v, ok := urlForm(rest[0]); ok && v == rest[0] {
			info.Version = v
		}
	default:
		ver, ok := urlForm(rest[0])
		if !ok || ver != rest[0] {
			return info
		}
		return classifyVersionFile(info, ver, rest[1:])
	}
	return info
}

// classifyVersionFile classifies a path below "{id}/{version}/".
func classifyVersionFile(info formats.PathInfo, ver string, rest []string) formats.PathInfo {
	switch {
	case len(rest) == 1:
		switch rest[0] {
		case NupkgFile(info.Package, ver), info.Package + nuspecSuffix, SnupkgFile(info.Package, ver):
			info.Kind, info.Version = formats.Content, ver
		default:
			// A package file of another name or version belongs to the package alone.
			for _, suffix := range []string{nupkgSuffix, snupkgSuffix, nuspecSuffix} {
				if strings.HasSuffix(rest[0], suffix) {
					info.Kind = formats.Content
				}
			}
		}
	case len(rest) >= 2 && rest[0] == canonSymbols:
		info.Kind, info.Version = formats.Content, ver
	case len(rest) == 2 && rest[0] == canonEmbedded:
		for _, name := range embeddedFiles() {
			if rest[1] == name {
				info.Kind, info.Version = formats.Content, ver
			}
		}
	}
	return info
}

// CanonicalPath returns the canonical path of the stored thing a matched read route
// addresses (R-NUGET-015): the package document for the version list, the item for a
// package, a .nuspec or an embedded file, the cache entry of the registration documents
// of an ID, the symbol file for a key. ok is false for a route that addresses no stored
// path: the service index, search, and every write.
func CanonicalPath(route string, p formats.Params) (string, bool) {
	switch route {
	case routeFlatIndex:
		return PackageDocumentPath(p["id"]), true
	case routeNupkg:
		return NupkgPath(p["id"], p["version"]), true
	case routeNuspec:
		return NuspecPath(p["id"], p["version"]), true
	case routeEmbedded:
		return p["id"] + "/" + p["version"] + "/" + canonEmbedded + "/" + p["file"], true
	case routeRegistrationIndex, routeRegistrationPage, routeRegistrationLeaf:
		return p["id"] + "/" + canonRegistration + "/" + indexFile, true
	case routeSymbols:
		return canonSymbols + "/" + p["key"], true
	}
	return "", false
}
