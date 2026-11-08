// Package oci is the data port of the OCI format (Q-ARCH-193, D-1): the statements on the OCI tables
// (internal/meta/postgres/queries/oci.sql), behind the Store interface that internal/formats/oci declares. The format runs no
// SQL; this package holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values that cross the port are those of
// internal/formats/oci/rows, a leaf both import. It owns the two tables of migration 00055_oci and registers them with the
// backup (tables.go), so that the package must be linked wherever the migrated schema is checked against the registry
// (cmd/trove/registry.go).
//
// Until the work package that fills the port lands, every method answers not_implemented: the format registers, the composition
// root binds the port, and nothing is silently empty.
package oci

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
}

// Store implements the data port of the format.
type Store struct{ o Options }

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "oci: the data port needs a database")
	}
	return &Store{o: o}, nil
}

func notBuilt() error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The OCI data port is not built yet.")
}

// PutManifest records a manifest row and its references.
func (*Store) PutManifest(context.Context, meta.Tx, rows.ManifestInput) error { return notBuilt() }

// RemoveManifest removes a manifest row and its references.
func (*Store) RemoveManifest(context.Context, meta.Tx, int64, string, [32]byte) error {
	return notBuilt()
}

// Manifest reads a manifest row.
func (*Store) Manifest(context.Context, int64, string, [32]byte) (rows.Manifest, bool, error) {
	return rows.Manifest{}, false, notBuilt()
}

// References lists what a manifest names.
func (*Store) References(context.Context, int64, string, [32]byte) ([]rows.Reference, error) {
	return nil, notBuilt()
}

// ReferencedBy lists the manifests that name a target.
func (*Store) ReferencedBy(context.Context, meta.Tx, int64, string, [32]byte) ([][32]byte, error) {
	return nil, notBuilt()
}

// Referrers lists the manifests whose subject is a digest.
func (*Store) Referrers(context.Context, int64, string, [32]byte, string, *[32]byte, int) ([]rows.Manifest, error) {
	return nil, notBuilt()
}

// Tags lists the tags of an image name.
func (*Store) Tags(context.Context, int64, string, string, int) (rows.TagPage, error) {
	return rows.TagPage{}, notBuilt()
}

// PutTombstone records the deletion of a manifest.
func (*Store) PutTombstone(context.Context, meta.Tx, rows.Tombstone) error { return notBuilt() }

// RemoveTombstone reinstates a withheld manifest.
func (*Store) RemoveTombstone(context.Context, meta.Tx, int64, string, [32]byte) (bool, error) {
	return false, notBuilt()
}

// Tombstone reads the withheld mark of a manifest digest.
func (*Store) Tombstone(context.Context, int64, string, [32]byte) (rows.Tombstone, bool, error) {
	return rows.Tombstone{}, false, notBuilt()
}

// UnreferencedBlobs lists the blob items no manifest references.
func (*Store) UnreferencedBlobs(context.Context, int64, time.Time, int) ([]rows.Blob, error) {
	return nil, notBuilt()
}
