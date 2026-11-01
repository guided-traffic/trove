{{if .ExposureRules}}You are using an endpoint with exposure rules, such as an endpoint for customers or partners. It serves only the packages and versions its rules expose:

- A hidden version is missing from the version list, from all three registration hives and from search, and its `.nupkg`, `.nuspec` and embedded files answer `404` exactly like a version that does not exist. `dotnet add package <id> --version <hidden>` and a restore of a project that names it fail with `NU1102`, as for a version that was never published; `NU1101` means that no version of the ID is exposed at all.
- Pre-releases are hidden when the rules say so, whatever `--prerelease` says. Exposure does not change whether a version is listed.
- Dependencies are not filtered. A package whose dependency is an ID the endpoint does not expose fails the restore with NuGet's "unable to find package" for the dependency.
- Writes are refused: push, unlist, relist and delete answer `403` `endpoint_read_only`, and the service index lists no publish resource.

The addresses in this guide and in every answer of this endpoint name its own host. Use the token that was issued for this endpoint.
{{end}}
