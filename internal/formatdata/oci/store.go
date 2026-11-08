// Package oci is the data port of the OCI format (Q-ARCH-193, D-1): the statements on the OCI tables
// (internal/meta/postgres/queries/oci.sql and oci_lists.sql), behind the Store interface that internal/formats/oci declares. The
// format runs no SQL; this package holds it, and the composition root binds one value per replica.
//
// It is a domain package: it imports neither the format nor the edge, so the values that cross the port are those of
// internal/formats/oci/rows, a leaf both import. It owns the three tables of migration 00055_oci and registers them with the
// backup (tables.go), so that the package must be linked wherever the migrated schema is checked against the registry
// (cmd/trove/registry.go).
//
// The listings (store_lists.go), the sweep of unreferenced blobs (store_sweep.go, sweep.go), the journal replay and the
// reinstate command (journal_replay.go, reinstate.go) are built; a method that is not built yet answers not_implemented, so
// nothing is silently empty.
package oci

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// Options configure [New].
type Options struct {
	// DB reads outside a transaction.
	DB meta.Runner
	// Recorder records the events of the writes in their transactions (oci.manifest.deleted, oci.reinstate).
	Recorder audit.Recorder
	// Source gives the source of an audit event for the request in ctx; nil gives none.
	Source func(ctx context.Context) audit.Source
	// Actor gives the actor of the request in ctx. A hook that learns of a removal from the item store knows the principal's id
	// and nothing else; the actor of the request completes the event. nil keeps what the hook passed.
	Actor func(ctx context.Context) audit.Actor
}

// Store implements the data port of the format.
type Store struct{ o Options }

// New creates the store.
func New(o Options) (*Store, error) {
	if o.DB == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "oci: the data port needs a database")
	}
	if o.Recorder == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "oci: the data port needs an audit recorder")
	}
	return &Store{o: o}, nil
}

// stamp renders a time for a statement that casts it to timestamptz.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// digestOf copies a stored digest into the array the port speaks; ok is false for a value that is not 32 bytes.
func digestOf(b []byte) (d [32]byte, ok bool) {
	if len(b) != len(d) {
		return d, false
	}
	copy(d[:], b)
	return d, true
}
