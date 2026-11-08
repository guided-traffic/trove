package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// journalStore is the group of the data port that writes the journaled events of files and applies their replay (R-DEB-032).
// It is a stub: the work package of the local class replaces this file and no other (apt-work-packages.md, 2.2).
type journalStore struct{ *group }

func (s journalStore) RecordPackageDeleted(context.Context, meta.Tx, rows.PackageDeleted) error {
	return errNotBuilt("the event of a deletion")
}

func (s journalStore) RecordCoordinatesRemoved(context.Context, meta.Tx, rows.CoordinatesRemoved) error {
	return errNotBuilt("the event of removed coordinates")
}

func (s journalStore) Withhold(context.Context, meta.Tx, rows.Tombstone) (rows.ReplayResult, error) {
	return "", errNotBuilt("the withholding of a file")
}

func (s journalStore) ReinstateFile(context.Context, meta.Tx, int64, string, [32]byte) (bool, error) {
	return false, errNotBuilt("the reinstatement of a file")
}

func (s journalStore) Tombstone(context.Context, int64, string) (rows.Tombstone, bool, error) {
	return rows.Tombstone{}, false, errNotBuilt("the read of a mark")
}

func (s journalStore) RemoveCoordinatesByJournal(context.Context, meta.Tx, rows.CoordinatesRemoved) (rows.ReplayResult, error) {
	return "", errNotBuilt("the replay of removed coordinates")
}
