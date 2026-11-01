// Package nugettest is the in-memory twin of the NuGet data port (Q-ARCH-193), for the
// conformance kit and the unit tests of the format. The skeleton declares the port and
// writes none of the handlers that read it, so the twin holds nothing: every read finds
// nothing and every write succeeds without a trace. The data port work package gives it the
// state of the rows the handlers it writes read.
package nugettest

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// Store is the twin of nuget.Store.
type Store struct{}

// New creates an empty store.
func New() *Store { return &Store{} }

// VersionFacts finds no version.
func (*Store) VersionFacts(context.Context, int64, string, string) (rows.VersionFacts, bool, error) {
	return rows.VersionFacts{}, false, nil
}

// Tombstone finds no tombstone.
func (*Store) Tombstone(context.Context, int64, string, string) (rows.Tombstone, bool, error) {
	return rows.Tombstone{}, false, nil
}

// InsertVersion stores nothing.
func (*Store) InsertVersion(context.Context, formats.Tx, rows.VersionInput) error { return nil }

// InsertTombstone stores nothing and returns no digest.
func (*Store) InsertTombstone(context.Context, formats.Tx, rows.TombstoneInput) ([]byte, error) {
	return nil, nil
}

// DeleteTombstone removes nothing.
func (*Store) DeleteTombstone(context.Context, formats.Tx, int64) (bool, error) { return false, nil }

// SetListed changes nothing.
func (*Store) SetListed(context.Context, formats.Tx, int64, bool) (bool, error) { return false, nil }

// InsertSymbolPackage stores nothing.
func (*Store) InsertSymbolPackage(context.Context, formats.Tx, rows.SymbolPackageInput) (bool, error) {
	return false, nil
}

// Versions lists none.
func (*Store) Versions(context.Context, int64, string) ([]rows.VersionEntry, error) {
	return nil, nil
}

// Registration finds no package.
func (*Store) Registration(context.Context, int64, string) (rows.RegistrationPackage, bool, error) {
	return rows.RegistrationPackage{}, false, nil
}

// Search finds no package.
func (*Store) Search(context.Context, rows.SearchQuery) ([]rows.SearchHit, error) { return nil, nil }
