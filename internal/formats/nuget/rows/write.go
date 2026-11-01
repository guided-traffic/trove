package rows

import "time"

// The values below are what the writes of the data port beyond the insert of a version
// carry: a removal, a change of the listing, and the lookup that decides them. Like every
// value of this leaf they arrive computed (R-NUGET-005, R-NUGET-006, R-NUGET-007).

// TombstoneInput is the tombstone of a live version that a removal writes
// (R-NUGET-006), in the transaction that removes the version. The hook that learns of the
// removal runs after the item rows are gone, so the store reads the SHA-512 of the
// removed .nupkg from the blob row by its SHA-256, which the removal event carries; the
// blob outlives the transaction because its collection is queued.
type TombstoneInput struct {
	RepoID int64
	// LowerID is the normalized ID and LowerVersion the URL form of the version.
	LowerID, LowerVersion string
	// SHA256 is the digest of the removed .nupkg.
	SHA256 []byte
	// At is the time of the removal; By the principal id of the actor, the nil UUID
	// for the system principal.
	At time.Time
	By string
	// Audit says who removed and where; it names the repository, the actor is completed
	// from the request when it is empty.
	Audit Audit
}

// ReservedInput turns the tombstone of kind reserved of a version into one of kind
// removed (R-NUGET-007: a protocol delete in delete mode of a version that a restore's
// replay reserved).
type ReservedInput struct {
	RepoID                int64
	LowerID, LowerVersion string
	By                    string
	Audit                 Audit
}

// VersionRef is a stored version as a change of its listing finds it.
type VersionRef struct {
	VersionID int64
	Listed    bool
}

// ListingInput is an unlist or a relist (R-NUGET-007).
type ListingInput struct {
	VersionID             int64
	LowerID, LowerVersion string
	Listed                bool
	Audit                 Audit
}

// ServeState is what a GET of a stored file of a version decides on (R-NUGET-007):
// whether the version is stored, listed or not, and whether its number is tombstoned.
type ServeState struct {
	// Stored is true when the repository holds a live version row.
	Stored bool
	Listed bool
	// Tombstoned is true when the number has a tombstone.
	Tombstoned bool
}
