package ansible

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// localStore is the group of the data port that holds the versions, tombstones and import tasks of a local repository and the step of
// the reindex (R-ANS-008 to R-ANS-012). It is a stub: the local class replaces this file and no other.
type localStore struct{ *group }

func (localStore) PutVersion(context.Context, meta.Tx, rows.VersionRow) error {
	return errNotBuilt("the recording of a version")
}

func (localStore) RemoveVersion(context.Context, meta.Tx, int64, string, string, bool, string) error {
	return errNotBuilt("the removal of a version")
}

func (localStore) Version(context.Context, int64, string, string) (rows.VersionRow, bool, error) {
	return rows.VersionRow{}, false, errNotBuilt("the read of a version")
}

func (localStore) Versions(context.Context, int64, string, int, int) ([]rows.VersionRow, int, error) {
	return nil, 0, errNotBuilt("the version list")
}

func (localStore) Collection(context.Context, int64, string) (rows.CollectionSummary, bool, error) {
	return rows.CollectionSummary{}, false, errNotBuilt("the read of a collection")
}

func (localStore) Tombstoned(context.Context, int64, string, string) ([]byte, bool, error) {
	return nil, false, errNotBuilt("the tombstone check")
}

func (localStore) ClearTombstone(context.Context, int64, string, string, string) error {
	return errNotBuilt("the clearing of a tombstone")
}

func (localStore) Tombstones(context.Context, int64, string, int) ([]rows.Tombstone, error) {
	return nil, errNotBuilt("the tombstone list")
}

func (localStore) CreateTask(context.Context, meta.Tx, rows.Task) error {
	return errNotBuilt("the recording of an import task")
}

func (localStore) Task(context.Context, int64, string) (rows.Task, bool, error) {
	return rows.Task{}, false, errNotBuilt("the read of an import task")
}

func (localStore) Tasks(context.Context, int64, string, string, int) ([]rows.Task, error) {
	return nil, errNotBuilt("the task list")
}

func (localStore) RebuildRows(context.Context, int64, rows.ArchiveReader, string) (string, bool, error) {
	return "", false, errNotBuilt("the reindex step")
}
