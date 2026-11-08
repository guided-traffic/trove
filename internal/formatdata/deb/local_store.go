package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// localStore is the group of the data port that holds the uploads, coordinates, removal and stored files of a local repository (R-DEB-006, R-DEB-008, R-DEB-009, R-DEB-013).
// It is a stub: the work package of the local class replaces this file and no other (apt-work-packages.md, 2.2).
type localStore struct{ *group }

func (s localStore) RecordUpload(context.Context, meta.Tx, rows.UploadInput) (rows.UploadResult, error) {
	return rows.UploadResult{}, errNotBuilt("the recording of an upload")
}

func (s localStore) SetCoordinates(context.Context, meta.Tx, rows.CoordinatesChange) (rows.CoordinatesChanged, error) {
	return rows.CoordinatesChanged{}, errNotBuilt("the change of coordinates")
}

func (s localStore) Removed(context.Context, meta.Tx, int64, int64) (rows.RemovedFile, bool, error) {
	return rows.RemovedFile{}, false, errNotBuilt("the removal hook")
}

func (s localStore) Restored(context.Context, meta.Tx, int64, int64) (rows.RemovedFile, bool, error) {
	return rows.RemovedFile{}, false, errNotBuilt("the restore hook")
}

func (s localStore) Binary(context.Context, int64, string) (rows.Binary, bool, error) {
	return rows.Binary{}, false, errNotBuilt("the read of a stored file")
}

func (s localStore) BinaryOf(context.Context, meta.Tx, int64, string, string, string) (rows.Binary, bool, error) {
	return rows.Binary{}, false, errNotBuilt("the read of a stored file")
}
