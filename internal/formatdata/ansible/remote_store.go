package ansible

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// remoteStore is the group of the data port that holds the digests a remote's version documents announce (R-ANS-018). It is a stub:
// the remote class replaces this file, adds the migration of ansible_upstream_versions and registers the table with its export
// class in a file of its own (remote_tables.go).
type remoteStore struct{ *group }

func (remoteStore) RecordAnnounced(context.Context, meta.Tx, int64, []rows.Announced) ([]rows.Announced, error) {
	return nil, errNotBuilt("the recording of announced digests")
}

func (remoteStore) Announced(context.Context, int64, string, string) (rows.Announced, bool, error) {
	return rows.Announced{}, false, errNotBuilt("the read of an announced digest")
}

func (remoteStore) AnnouncedNames(context.Context, int64, string) ([]rows.Announced, error) {
	return nil, errNotBuilt("the list of announced versions")
}
