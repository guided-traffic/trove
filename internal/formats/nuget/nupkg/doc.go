// Package nupkg reads a spooled .nupkg for the NuGet ingestion pipeline
// (R-NUGET-005 steps 1 to 3, R-ARCH-028): the ZIP structure through the central
// directory only, the name rules, the one .nuspec at the archive root and its
// metadata.
//
// The archive arrives as an io.ReaderAt and its size, the shape of the spool.
// Nothing is unpacked and no entry name is used as a file-system path. What is
// held in memory is the central directory (bounded by the entry limit), one
// local header at a time and the entries the caller reads through
// [Archive.Read], each within its own bound; the bytes of the other entries are
// never inflated. The package is a leaf: standard library, internal/errs and
// its sibling internal/formats/nuget/nuspec.
//
// A refusal is a 400 *errs.Error with the code [nuspec.CodeInvalidPackage] and
// one of the Err reasons of this package or of nuspec as its cause (spec:
// "400 on violation", R-NUGET-005). Only a cancelled or expired context returns
// its own error.
package nupkg
