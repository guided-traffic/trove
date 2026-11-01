// Package nuget is the data port of the NuGet format (Q-ARCH-193, D-1): the statements on
// the NuGet tables (internal/meta/postgres/queries/nuget_local.sql), behind the Store
// interface that internal/formats/nuget declares. The format runs no SQL; this package
// holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values that
// cross the port are those of internal/formats/nuget/rows, a leaf both import. It owns the
// four tables of migration 00017_nuget and registers them with the backup (tables.go), so
// that the package must be linked wherever the migrated schema is checked against the
// registry (cmd/trove/registry.go).
//
// The skeleton work package (WP3.1c) binds the port so that the format registers and the
// replica starts; the methods answer errs.NotImplemented until the data port work package
// (WP3.1d) writes them. No route of the format reaches them before that.
package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
	// Recorder records the events of the writes in their transactions.
	Recorder audit.Recorder
}

// Store implements the data port of the NuGet format.
type Store struct{ o Options }

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: no transaction runner")
	}
	if o.Recorder == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "nuget: no audit recorder")
	}
	return &Store{o: o}, nil
}

// errNotBuilt is the answer of a method the data port work package has not written.
func errNotBuilt(method string) error {
	return errs.E(errs.NotImplemented, errs.CodeNuGetNotImplemented, "The NuGet data port does not implement "+method+" yet.")
}

// VersionFacts is not built yet.
func (*Store) VersionFacts(context.Context, int64, string, string) (rows.VersionFacts, bool, error) {
	return rows.VersionFacts{}, false, errNotBuilt("VersionFacts")
}

// Tombstone is not built yet.
func (*Store) Tombstone(context.Context, int64, string, string) (rows.Tombstone, bool, error) {
	return rows.Tombstone{}, false, errNotBuilt("Tombstone")
}

// InsertVersion is not built yet.
func (*Store) InsertVersion(context.Context, meta.Tx, rows.VersionInput) error {
	return errNotBuilt("InsertVersion")
}

// InsertTombstone is not built yet.
func (*Store) InsertTombstone(context.Context, meta.Tx, rows.TombstoneInput) ([]byte, error) {
	return nil, errNotBuilt("InsertTombstone")
}

// DeleteTombstone is not built yet.
func (*Store) DeleteTombstone(context.Context, meta.Tx, int64) (bool, error) {
	return false, errNotBuilt("DeleteTombstone")
}

// SetListed is not built yet.
func (*Store) SetListed(context.Context, meta.Tx, int64, bool) (bool, error) {
	return false, errNotBuilt("SetListed")
}

// InsertSymbolPackage is not built yet.
func (*Store) InsertSymbolPackage(context.Context, meta.Tx, rows.SymbolPackageInput) (bool, error) {
	return false, errNotBuilt("InsertSymbolPackage")
}

// Versions is not built yet.
func (*Store) Versions(context.Context, int64, string) ([]rows.VersionEntry, error) {
	return nil, errNotBuilt("Versions")
}

// Registration is not built yet.
func (*Store) Registration(context.Context, int64, string) (rows.RegistrationPackage, bool, error) {
	return rows.RegistrationPackage{}, false, errNotBuilt("Registration")
}

// Search is not built yet.
func (*Store) Search(context.Context, rows.SearchQuery) ([]rows.SearchHit, error) {
	return nil, errNotBuilt("Search")
}
