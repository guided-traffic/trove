// Package servefacts holds what the serve-time check of a format decides on, so that the item lookup of a download can read it in
// its own statement (R-ARCH-027: a download costs one PostgreSQL statement).
//
// A format whose check needs rows besides the item (the tombstone of an npm or NuGet version, the NuGet version row, the
// facts of an npm remote version) names them with a [Key]; the lookup of the item reads the rows of that key in the same
// statement and returns them as [Facts]. The facts carry the key they were read for, so a check uses them only when the key
// is the one it would query itself ([Facts.For]) and reads the rows itself otherwise. Nothing here keeps state: the facts live
// as long as the item of the request that read them (R-ARCH-032).
//
// The package imports nothing; the item store, the format contract and the remote engine all spell their types.
package servefacts

// Kind names the rows a lookup reads for a key; each value has its own part of the lookup's statement.
type Kind string

const (
	// None reads nothing.
	None Kind = ""
	// NPMLocal is the tombstone of a version in a local npm repository (R-NPM-047).
	NPMLocal Kind = "npm_local"
	// NPMRemote is what the serving of a tarball of a remote npm repository decides on (R-NPM-025, R-NPM-026).
	NPMRemote Kind = "npm_remote"
	// NuGetLocal is the version row and the tombstone of a version in a local NuGet repository (R-NUGET-007).
	NuGetLocal Kind = "nuget_local"
)

// Key names the rows to read: a kind and the package and version, in the spelling the format's own queries use.
type Key struct {
	Kind    Kind
	Package string
	Version string
}

// Zero reports a key that reads nothing.
func (k Key) Zero() bool { return k.Kind == None }

// NPMRemoteFacts are the columns of the npm remote kind: the record of the version, the state of the package document's cache
// entry and the repository's serveRemovedVersions.
type NPMRemoteFacts struct {
	Known, Listed bool
	Integrity     string
	Shasum        string
	// DocStored, DocCurrent: the package document's cache entry exists, and is not expired and validated after the last restore.
	DocStored, DocCurrent bool
	// DocGoneUs is the instant the upstream withdrew the package in microseconds since the epoch, 0 when it did not.
	DocGoneUs    int64
	ServeRemoved bool
}

// Facts are the rows read for a key. Key is zero when nothing was read.
type Facts struct {
	Key Key
	// Tombstoned: the version has a tombstone (NPMLocal, NuGetLocal).
	Tombstoned bool
	// Stored and Listed: the repository holds a live NuGet row for the version, and it is listed (NuGetLocal).
	Stored, Listed bool
	NPMRemote      NPMRemoteFacts
	// Refreshed is set by an artifact gate that asked the upstream after the item was looked up (a revalidation of the package
	// document): everything read with the item, the item, its cache entry and the scanning's verdict, is older than the answer
	// and the caller reads it again before it serves.
	Refreshed bool
}

// For reports whether the facts were read for the key, so that a check may decide on them.
func (f Facts) For(k Key) bool { return !k.Zero() && f.Key == k }
