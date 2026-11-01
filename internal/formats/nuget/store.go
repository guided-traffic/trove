package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// Store is the data port of the NuGet format (Q-ARCH-193, D-1): everything the
// format needs of its own tables (internal/meta/postgres/migrations/00017_nuget.sql),
// behind an interface the format declares and internal/formatdata/nuget implements.
// The format runs no SQL (R-ARCH-008); the composition root binds the implementation
// per format ID and refuses to start without it ([Format.Descriptor] names the type).
//
// The store persists what the format decided. The format holds the version order, the
// ID rules and the SemVer 2.0.0 classification, so the values that cross the port
// arrive computed ([rows]); the store never parses a version. The methods that take a
// transaction use it for every statement, and the package row of the write is locked by
// then: the writes of one package serialize on that lock, and the version and tombstone
// checks read only after it (R-NUGET-006).
//
// Every read leaves out a version whose trashed_at is set (R-NUGET-005).
type Store interface {
	// VersionFacts reads, outside a transaction, what the immutability and the
	// acknowledged-retry rules need of a stored version (R-NUGET-006). found is false
	// for a version that is not stored.
	VersionFacts(ctx context.Context, repoID int64, lowerID, lowerVersion string) (facts rows.VersionFacts, found bool, err error)
	// Tombstone reads the tombstone of a version number (R-NUGET-006); found is false
	// when there is none.
	Tombstone(ctx context.Context, repoID int64, lowerID, lowerVersion string) (t rows.Tombstone, found bool, err error)
	// InsertVersion stores the NuGet row of a new version in the transaction of a push, a
	// copy or a cache fill (R-NUGET-005 step 6), with the search document built from the
	// parts the format hands over.
	InsertVersion(ctx context.Context, tx formats.Tx, in rows.VersionInput) error
	// InsertTombstone writes the tombstone of a live version with the SHA-512 of its
	// .nupkg, in the transaction that removes the version, and returns that digest. A
	// number that is tombstoned already keeps its first tombstone (R-NUGET-006).
	InsertTombstone(ctx context.Context, tx formats.Tx, in rows.TombstoneInput) (sha512 []byte, err error)
	// DeleteTombstone removes the tombstone of a version that is stored again with the
	// SHA-512 the tombstone holds: a restore from the trash can, or the identical push
	// against a reserved number (R-NUGET-006, Q-NUGET-10). It reports whether one went.
	DeleteTombstone(ctx context.Context, tx formats.Tx, versionID int64) (bool, error)
	// SetListed unlists or relists a version (R-NUGET-007); it reports whether the
	// version has a NuGet row. The caller raises the package revision in the same
	// transaction.
	SetListed(ctx context.Context, tx formats.Tx, versionID int64, listed bool) (bool, error)
	// InsertSymbolPackage records the symbol package of a version (R-NUGET-020); it
	// reports false when the version holds one already.
	InsertSymbolPackage(ctx context.Context, tx formats.Tx, in rows.SymbolPackageInput) (bool, error)

	// >>> M3 nuget-read

	// Versions lists, outside a transaction and in one statement, every stored version
	// of an ID, listed or not, ascending by sort key: the version list of the flat
	// container (R-NUGET-008). It is empty for an ID that holds none.
	Versions(ctx context.Context, repoID int64, lowerID string) ([]rows.VersionEntry, error)
	// Registration reads, in one statement, what the registration documents of an ID
	// render from (R-NUGET-009): the package's revision and every visible version with
	// its metadata. found is false for an ID that holds no version.
	Registration(ctx context.Context, repoID int64, lowerID string) (pkg rows.RegistrationPackage, found bool, err error)
	// Search returns the packages of a repository that match a query, one hit per
	// package, in the rank order of R-NUGET-010, each with its latest visible version
	// and its listed versions (R-NUGET-010). The format keeps the hits the caller may
	// read and cuts the page.
	Search(ctx context.Context, q rows.SearchQuery) ([]rows.SearchHit, error)

	// <<< M3 nuget-read
}
