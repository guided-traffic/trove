package nugetstub

import (
	"fmt"
	"strings"
)

// ReferenceFeed is the feed address written into the reference solution's nuget.config: the
// virtual repository nuget-all of the dev profile. The e2e tests override it with --source.
const ReferenceFeed = "https://localhost:8080/api/nuget/e2e/nuget-all/v3/index.json"

// ReferenceFiles renders the reference solution of criterion E3.2 from the corpus: a solution with
// one project whose PackageReferences are chosen so that a restore pulls at least min packages
// (transitive dependencies included), all of them from the stub. The result maps a path relative to
// the solution's directory to the file's bytes. It is deterministic; perf/nuget/reference holds
// the committed copy and a test fails when the two differ.
func ReferenceFiles(c *Corpus, min int) map[string][]byte {
	roots := c.ReferenceRoots(min)
	var proj strings.Builder
	proj.WriteString("<Project Sdk=\"Microsoft.NET.Sdk\">\n\n  <PropertyGroup>\n")
	proj.WriteString("    <TargetFramework>net10.0</TargetFramework>\n")
	proj.WriteString("    <OutputType>Library</OutputType>\n")
	proj.WriteString("    <EnableDefaultCompileItems>false</EnableDefaultCompileItems>\n")
	proj.WriteString("  </PropertyGroup>\n\n  <ItemGroup>\n")
	for _, id := range roots {
		p, _ := c.Get(id)
		fmt.Fprintf(&proj, "    <PackageReference Include=%q Version=%q />\n", p.ID, p.LatestStable())
	}
	proj.WriteString("  </ItemGroup>\n\n</Project>\n")

	sln := "Microsoft Visual Studio Solution File, Format Version 12.00\n" +
		"# Visual Studio Version 17\n" +
		"Project(\"{9A19103F-16F7-4668-BE54-9A1E7A4F7556}\") = \"Reference\", \"Reference\\Reference.csproj\", \"{5B7A1C0E-2D6F-4B1A-9C3E-7F4D8A2B6E10}\"\n" +
		"EndProject\n" +
		"Global\n" +
		"\tGlobalSection(SolutionConfigurationPlatforms) = preSolution\n" +
		"\t\tDebug|Any CPU = Debug|Any CPU\n" +
		"\t\tRelease|Any CPU = Release|Any CPU\n" +
		"\tEndGlobalSection\n" +
		"\tGlobalSection(ProjectConfigurationPlatforms) = postSolution\n" +
		"\t\t{5B7A1C0E-2D6F-4B1A-9C3E-7F4D8A2B6E10}.Debug|Any CPU.ActiveCfg = Debug|Any CPU\n" +
		"\t\t{5B7A1C0E-2D6F-4B1A-9C3E-7F4D8A2B6E10}.Debug|Any CPU.Build.0 = Debug|Any CPU\n" +
		"\t\t{5B7A1C0E-2D6F-4B1A-9C3E-7F4D8A2B6E10}.Release|Any CPU.ActiveCfg = Release|Any CPU\n" +
		"\t\t{5B7A1C0E-2D6F-4B1A-9C3E-7F4D8A2B6E10}.Release|Any CPU.Build.0 = Release|Any CPU\n" +
		"\tEndGlobalSection\n" +
		"EndGlobal\n"

	cfg := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<configuration>\n  <packageSources>\n    <clear />\n" +
		"    <add key=\"trove\" value=\"" + ReferenceFeed + "\" />\n  </packageSources>\n</configuration>\n"

	return map[string][]byte{
		"Reference.sln":              []byte(sln),
		"Reference/Reference.csproj": []byte(proj.String()),
		"nuget.config":               []byte(cfg),
	}
}
