package authz

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// lowerNames is the name rule of a format whose package names compare
// case-insensitively (NuGet's): the one normaliser that folds names and patterns.
type lowerNames struct{}

func (lowerNames) Normalize(name string) (string, bool) { return strings.ToLower(name), name != "" }

// exactNames is the name rule of a format whose names compare as written (npm's).
type exactNames struct{}

func (exactNames) Normalize(name string) (string, bool) { return name, name != "" }

// testRegistry holds the formats the decision point's tests name: generic (paths
// only, exposable through paths alone), an npm-like format (names, versions and
// tags are narrowed by exposure rules), and a NuGet-like format with
// case-insensitive names that is not exposable. The decision point knows nothing
// of these formats but what the registry says.
func testRegistry() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{
		Facts:    catalog.Facts{ID: "generic", Classes: []catalog.Class{catalog.Local}, Layout: "generic", PatternSubject: catalog.SubjectPath},
		Exposure: catalog.Exposure{Exposable: true},
	})
	r.Register(catalog.Entry{
		Facts: catalog.Facts{
			ID: "npm", Classes: []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}, Layout: "npm",
			PatternSubject: catalog.SubjectName, PackageNames: true,
		},
		Names:        exactNames{},
		DocumentPath: npmDocumentPath,
		VersionPath:  npmVersionPath,
		Exposure:     catalog.Exposure{Exposable: true, Packages: true, Versions: true, Tags: true, Versioning: semverish{}},
	})
	r.Register(catalog.Entry{
		Facts: catalog.Facts{
			ID: "nuget", Classes: []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}, Layout: "nuget",
			PatternSubject: catalog.SubjectName, PackageNames: true, LowerCasePaths: true,
		},
		Names:        lowerNames{},
		DocumentPath: nugetDocumentPath,
		VersionPath:  nugetVersionPath,
	})
	return r
}

// npmDocumentPath is the package document path of the npm-like format: the name itself, as npm's (R-NPM-003).
func npmDocumentPath(name string) (string, bool) { return name, name != "" }

// nugetDocumentPath is the package document path of the NuGet-like format: "{lower_id}/index.json", as NuGet's
// (R-NUGET-001).
func nugetDocumentPath(name string) (string, bool) {
	return strings.ToLower(name) + "/index.json", name != ""
}

// npmVersionPath is the path of a version's tarball in the npm-like format: "<name>/-/<basename>-<version>.tgz", as npm's
// (R-NPM-006). A version with a slash is no version.
func npmVersionPath(name, version string) (string, bool) {
	if name == "" || version == "" || strings.Contains(version, "/") {
		return "", false
	}
	base := name[strings.LastIndexByte(name, '/')+1:]
	return name + "/-/" + base + "-" + version + ".tgz", true
}

// nugetVersionPath is the path of a version's .nupkg in the NuGet-like format, as NuGet's (R-NUGET-005).
func nugetVersionPath(name, version string) (string, bool) {
	if name == "" || version == "" {
		return "", false
	}
	id, v := strings.ToLower(name), strings.ToLower(version)
	return id + "/" + v + "/" + id + "." + v + ".nupkg", true
}
