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
		Names:    exactNames{},
		Exposure: catalog.Exposure{Exposable: true, Packages: true, Versions: true, Tags: true, Versioning: semverish{}},
	})
	r.Register(catalog.Entry{
		Facts: catalog.Facts{
			ID: "nuget", Classes: []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}, Layout: "nuget",
			PatternSubject: catalog.SubjectName, PackageNames: true,
		},
		Names: lowerNames{},
	})
	return r
}
