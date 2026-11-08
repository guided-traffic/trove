package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// JournalStore is the part of the data port that writes the journaled events of files and applies their replay (R-DEB-032, Q-DEB-22,
// Q-DEB-34). Built by the work package of the local class. The replay only restricts: it withholds a file and removes coordinates,
// and deletes no content. The journaled events of the signing keys are in [KeyStore] (WithholdKey, PromoteByJournal).
type JournalStore interface {
	// RecordPackageDeleted writes the audit event deb.package.deleted with the journal entry a restore's replay withholds the file by.
	RecordPackageDeleted(ctx context.Context, tx formats.Tx, in rows.PackageDeleted) error
	// RecordCoordinatesRemoved writes deb.coordinates.removed with its journal entry, beside deb.coordinates.changed.
	RecordCoordinatesRemoved(ctx context.Context, tx formats.Tx, in rows.CoordinatesRemoved) error
	// Withhold writes the mark of a file the replay withheld (R-DEB-032) and raises change_seq of its suites; a mark that is there
	// stays (already in effect), a file the repository does not hold with that digest is subject unknown.
	Withhold(ctx context.Context, tx formats.Tx, in rows.Tombstone) (rows.ReplayResult, error)
	// ReinstateFile removes the mark of a withheld file, only while the file is stored with that digest, and writes deb.reinstate; it
	// reports whether a mark was there (`trove admin deb reinstate`).
	ReinstateFile(ctx context.Context, tx formats.Tx, repoID int64, path string, sha256 [32]byte) (bool, error)
	// Tombstone reads the mark of a path; found is false when there is none. The serve check reads the same fact with the item
	// (servefacts.DebLocal) and asks the port only when the lookup did not.
	Tombstone(ctx context.Context, repoID int64, path string) (t rows.Tombstone, found bool, err error)
	// RemoveCoordinatesByJournal is the replay of deb.coordinates.removed: when the restored repository holds the file at that path
	// with that digest the coordinates are removed from it, and a file left without coordinates is withheld by a mark, never deleted.
	RemoveCoordinatesByJournal(ctx context.Context, tx formats.Tx, in rows.CoordinatesRemoved) (rows.ReplayResult, error)
}
