package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// LocalStore is the part of the data port that the local class writes and reads (R-DEB-006, R-DEB-008, R-DEB-009, R-DEB-013). Built
// by the work package of the local class.
type LocalStore interface {
	// RecordUpload records a package file in the transaction that writes its item: the row of the file, its coordinates (the suites
	// are created as they are first named) and the raise of change_seq of every suite the file is listed in, so the next build
	// reflects it. The identical bytes stored already add the coordinates and change nothing else (UploadResult.Created false);
	// other bytes for a stored package, version and architecture are refused with deb_version_exists by the caller, which asks
	// Binary first, and a unique violation here is the same refusal (Q-DEB-4). It enqueues nothing: the build is enqueued by the
	// caller through the job queue in the same transaction.
	RecordUpload(ctx context.Context, tx formats.Tx, in rows.UploadInput) (rows.UploadResult, error)
	// SetCoordinates replaces the coordinates of a stored file (R-DEB-009) and raises change_seq of every suite that gained or
	// lost it. It returns what was added and removed, so the caller can decide `deploy` and `delete` per side, journal the removal
	// (Q-DEB-34) and report the suites. The caller has authorized both sides before it calls (the fine check of R-DEB-004).
	SetCoordinates(ctx context.Context, tx formats.Tx, in rows.CoordinatesChange) (rows.CoordinatesChanged, error)
	// Removed runs for the removal of an item: it deletes the file's row and coordinates and raises change_seq of its suites (a
	// removal narrows what clients get). found is false for an item that is no package file. The rows of a file in the trash stay
	// until the item is removed for good, as NuGet's do.
	Removed(ctx context.Context, tx formats.Tx, repoID, itemID int64) (rows.RemovedFile, bool, error)
	// Restored runs for an item restored from the trash: the file's row is live again and change_seq of its suites is raised. found
	// is false for an item that is no package file.
	Restored(ctx context.Context, tx formats.Tx, repoID, itemID int64) (rows.RemovedFile, bool, error)
	// Binary reads the row of a stored file by its canonical path, with its coordinates; found is false for a path that is not
	// a stored package file of the repository.
	Binary(ctx context.Context, repoID int64, path string) (b rows.Binary, found bool, err error)
	// BinaryOf reads the row of the file stored for a package, version and architecture; found is false when there is none. The
	// immutability check of an upload (Q-DEB-4) asks it.
	BinaryOf(ctx context.Context, tx formats.Tx, repoID int64, pkg, version, architecture string) (b rows.Binary, found bool, err error)
}
