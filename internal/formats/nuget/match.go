package nuget

import (
	"net/url"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
)

// The matchers of the routes. They match the shape of an escaped protocol path and nothing
// else: what the segments mean (an ID, a version, a file name that must agree with both)
// is decided once, after the route is known, by [Format.Match]. A matcher never decodes
// more than one segment at a time, so a %2F stays inside its segment (R-ARCH-005).

// segments splits an escaped protocol path; ok is false when a segment is empty or does
// not decode, a trailing slash included: only the service index tolerates one, and
// matchIndex looks at the raw text.
func segments(escaped string) (raw, decoded []string, ok bool) {
	t := strings.TrimPrefix(escaped, "/")
	if t == "" {
		return nil, nil, false
	}
	raw = strings.Split(t, "/")
	decoded = make([]string, len(raw))
	for i, r := range raw {
		d, err := url.PathUnescape(r)
		if err != nil || d == "" {
			return nil, nil, false
		}
		decoded[i] = d
	}
	return raw, decoded, true
}

// feedSegments returns the decoded segments below the feed root "v3" when the path is a
// resource of the given name, with the number of segments after the resource name.
func feedSegments(escaped, resource string, tail int) ([]string, bool) {
	_, d, ok := segments(escaped)
	if !ok || len(d) != 2+tail || d[0] != FeedSegment || d[1] != resource {
		return nil, false
	}
	return d[2:], true
}

// matchIndex matches the service index at its three spellings, "v3", "v3/" and
// "v3/index.json" (R-NUGET-001). The text is compared before it is decoded: an escaped
// spelling of the same letters is not the index.
func matchIndex(escaped string) (formats.Params, bool) {
	switch strings.TrimPrefix(escaped, "/") {
	case FeedSegment, FeedSegment + "/", FeedSegment + "/" + indexFile:
		return formats.Params{}, true
	}
	return nil, false
}

// matchFlatIndex matches "v3/flatcontainer/{id}/index.json".
func matchFlatIndex(escaped string) (formats.Params, bool) {
	t, ok := feedSegments(escaped, resFlatContainer, 2)
	if !ok || t[1] != indexFile {
		return nil, false
	}
	return formats.Params{"id": t[0]}, true
}

// flatFile matches "v3/flatcontainer/{id}/{version}/{file}" when accept says the file is
// the kind of file the route serves.
func flatFile(accept func(file string) bool) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		t, ok := feedSegments(escaped, resFlatContainer, 3)
		if !ok || !accept(t[2]) {
			return nil, false
		}
		return formats.Params{"id": t[0], "version": t[1], "file": t[2]}, true
	}
}

func hasSuffixFold(suffix string) func(string) bool {
	return func(file string) bool { _, ok := cutSuffixFold(file, suffix); return ok }
}

func isEmbedded(file string) bool { return slices.Contains(embeddedFiles(), strings.ToLower(file)) }

// matchRegistrationIndex matches "v3/{hive}/{id}/index.json" in any of the three hives.
func matchRegistrationIndex(escaped string) (formats.Params, bool) {
	p, ok := hiveSegments(escaped, 2)
	if !ok || p[2] != indexFile {
		return nil, false
	}
	return formats.Params{"hive": p[0], "id": p[1]}, true
}

// matchRegistrationPage matches "v3/{hive}/{id}/page/{lower}/{upper}.json".
func matchRegistrationPage(escaped string) (formats.Params, bool) {
	p, ok := hiveSegments(escaped, 4)
	if !ok || p[2] != registrationPageSeg {
		return nil, false
	}
	upper, ok := strings.CutSuffix(p[4], jsonSuffix)
	if !ok {
		return nil, false
	}
	return formats.Params{"hive": p[0], "id": p[1], "lower": p[3], "upper": upper}, true
}

// matchRegistrationLeaf matches "v3/{hive}/{id}/{version}.json".
func matchRegistrationLeaf(escaped string) (formats.Params, bool) {
	p, ok := hiveSegments(escaped, 2)
	if !ok || p[2] == indexFile {
		return nil, false
	}
	ver, ok := strings.CutSuffix(p[2], jsonSuffix)
	if !ok {
		return nil, false
	}
	return formats.Params{"hive": p[0], "id": p[1], "version": ver}, true
}

// hiveSegments returns the segments after "v3" of a path whose first resource is a
// registration hive, with tail further segments after the ID: [hive, id, tail...].
func hiveSegments(escaped string, tail int) ([]string, bool) {
	_, d, ok := segments(escaped)
	if !ok || len(d) != 2+tail || d[0] != FeedSegment || !slices.Contains(hives(), d[1]) {
		return nil, false
	}
	return d[1:], true
}

// matchResource matches "v3/{resource}" alone: search, autocomplete, the publish
// resources.
func matchResource(resource string) func(string) (formats.Params, bool) {
	return func(escaped string) (formats.Params, bool) {
		if _, ok := feedSegments(escaped, resource, 0); !ok {
			return nil, false
		}
		return formats.Params{}, true
	}
}

// matchPublish matches "v3/{resource}" and "v3/{resource}/", the address of a publish resource. The
// clients put the trailing slash on the address the service index names before they PUT to it (the
// .NET SDK sends "PUT .../v3/package/" for the index entry ".../v3/package"), so both spellings are
// the resource (R-NUGET-006, R-NUGET-020).
func matchPublish(resource string) func(string) (formats.Params, bool) {
	exact := matchResource(resource)
	return func(escaped string) (formats.Params, bool) {
		return exact(strings.TrimSuffix(escaped, "/"))
	}
}

// matchPackageVersion matches "v3/package/{id}/{version}", the address of the unlist,
// delete and relist operations.
func matchPackageVersion(escaped string) (formats.Params, bool) {
	t, ok := feedSegments(escaped, resPackage, 2)
	if !ok {
		return nil, false
	}
	return formats.Params{"id": t[0], "version": t[1]}, true
}

// matchSymbolKey matches "v3/symbols/{key...}" for a key of at least one segment
// (R-NUGET-021); the key is the decoded segments joined by "/".
func matchSymbolKey(escaped string) (formats.Params, bool) {
	_, d, ok := segments(escaped)
	if !ok || len(d) < 3 || d[0] != FeedSegment || d[1] != resSymbols {
		return nil, false
	}
	return formats.Params{"key": strings.Join(d[2:], "/")}, true
}

// Match resolves the escaped path to a route and normalizes what the path names. A path no
// route accepts is the 404 of R-NUGET-001 with the code nuget_v2_unsupported, whatever its
// shape: the v2 request shapes ($metadata, FindPackagesById(), Packages(), api/v2/…) a client
// sends when its source URL does not end in .json, but also a mistyped v3 URL, and the
// refusal names the service index to use (WriteError).
//
// The ID of the path is validated and lowercased, the version resolved to its URL form and
// a file name checked against both (R-NUGET-003, R-NUGET-008). A name that cannot be an ID or
// a version, a file that names another package or version, answer 404: a client that probes a
// source for an ID it does not hold gets the answer of a package that does not exist, and
// never an error that would fail its restore.
func (f *Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	r, p, err := formats.MatchRoutes(f.routes, method, escapedPath)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.NotFound && e.Code == errs.CodeNotFound {
			return nil, nil, errs.E(errs.NotFound, errs.CodeNuGetV2Unsupported, "No NuGet v3 resource has this path.")
		}
		return nil, nil, err
	}
	if err := normalizeParams(p); err != nil {
		return nil, nil, err
	}
	return r, p, nil
}

// errNoSuchResource is the 404 of a resource that cannot exist.
func errNoSuchResource() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
}

// normalizeParams replaces the raw values a matcher found by their normalized forms.
func normalizeParams(p formats.Params) error {
	rawID, hasID := p["id"]
	if hasID {
		n, ok := lowerID(rawID)
		if !ok {
			return errNoSuchResource()
		}
		p["id"] = n
	}
	if v, ok := p["version"]; ok {
		u, ok := urlForm(v)
		if !ok {
			return errNoSuchResource()
		}
		p["version"] = u
	}
	if file, ok := p["file"]; ok {
		if err := checkFile(p, rawID, file); err != nil {
			return err
		}
	}
	for _, k := range []string{"lower", "upper"} {
		if v, ok := p[k]; ok {
			u, ok := urlForm(v)
			if !ok {
				return errNoSuchResource()
			}
			p[k] = u
		}
	}
	return nil
}

// checkFile holds the file name of a flat-container request to the ID and version of the
// directories above it: "{id}.{version}.nupkg" and "{id}.nuspec", in any casing, the version
// in any spelling that resolves to the version of the directory. The file param is replaced
// by the kind of file: "nupkg", "nuspec" or the name of an embedded file.
func checkFile(p formats.Params, rawID, file string) error {
	switch {
	case hasSuffixFold(nupkgSuffix)(file):
		v, ok := versionOfFile(rawID, file, nupkgSuffix)
		if !ok || v != p["version"] {
			return errNoSuchResource()
		}
		p["file"] = "nupkg"
	case hasSuffixFold(nuspecSuffix)(file):
		if !strings.EqualFold(file, rawID+nuspecSuffix) {
			return errNoSuchResource()
		}
		p["file"] = "nuspec"
	default:
		p["file"] = strings.ToLower(file)
	}
	return nil
}

// Names is the package-name rule of R-AUTHZ-005 for NuGet: the widest rule the format
// accepts anywhere, the upstream rule of R-NUGET-003 (a remote repository may serve IDs a
// local one would refuse), then the canonical form, the ASCII-lowercased ID. A string with a
// character of the pattern syntax is a name pattern and is folded as it is.
type names struct{}

const patternChars = `*?[]{},!\`

func (names) Normalize(s string) (string, bool) {
	if strings.ContainsAny(s, patternChars) {
		return id.Lower(s), true
	}
	n, ok := lowerID(s)
	return n, ok
}

// PackageDocumentPath is the package document of R-NUGET-001: the version list of the ID,
// "{lower_id}/index.json".
func (f *Format) PackageDocumentPath(pkg string) (formats.Path, bool) {
	n, ok := lowerID(pkg)
	if !ok {
		return formats.Path{}, false
	}
	return formats.Path{Path: PackageDocumentPath(n)}, true
}

var _ formats.VersionPather = (*Format)(nil)

// VersionPath is the path of a version's .nupkg, the file that holds its content: the decision point decides a version
// on it, so a path pattern that hides `{id}/{version}/**` or the .nupkg hides the version from the version list, the
// registrations and the search (Q-AUTHZ-255, Q-NUGET-120). Any spelling of the version resolves to its URL form.
func (f *Format) VersionPath(pkg, ver string) (formats.Path, bool) {
	n, ok := lowerID(pkg)
	if !ok {
		return formats.Path{}, false
	}
	v, ok := urlForm(ver)
	if !ok {
		return formats.Path{}, false
	}
	return formats.Path{Path: NupkgPath(n, v)}, true
}
