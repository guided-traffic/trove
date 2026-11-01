// Package search holds the parts of the NuGet search resource that need no host and no
// database (R-NUGET-010): the query parser, the bounds of the paging parameters, the
// tokens of an ID that search_doc is built from, and the result document.
//
// The format (internal/formats/nuget) runs the search over its data port; this package
// decides what the request means and renders what comes back. It imports the rows leaf,
// the version and ID libraries and the standard library, so a job that rebuilds search_doc
// (R-NUGET-025) can use the same ID tokens as the push.
package search
