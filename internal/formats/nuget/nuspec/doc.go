// Package nuspec reads the .nuspec of a NuGet package (R-NUGET-005 step 3): XML
// in UTF-8 or UTF-16 with a byte order mark, no document type declaration, no
// entity expansion, a bounded size and nesting, and the metadata of the
// R-NUGET-005 table as a typed result.
//
// It reads bytes and nothing else: no file, no network, no schema. It validates
// structure (one package, one metadata, one id and one version, no mixed
// dependency forms) and leaves the rules for IDs, versions and ranges to the
// packages that own them (R-NUGET-003, R-NUGET-004); ids, versions and ranges
// are returned as the strings the document spells. It is a leaf: standard
// library and internal/errs.
package nuspec
