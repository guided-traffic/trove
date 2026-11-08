package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// remoteStore is the group of the data port that holds the release documents and parsed index rows of the remote repositories (R-DEB-019 to R-DEB-023).
// It is a stub: the work package of the remote class replaces this file and no other (apt-work-packages.md, 2.2).
type remoteStore struct{ *group }

func (s remoteStore) RecordRelease(context.Context, meta.Tx, rows.ReleaseRecord) (rows.ReleaseDecision, error) {
	return rows.ReleaseDecision{}, errNotBuilt("the record of a release")
}

func (s remoteStore) Release(context.Context, int64, string) (rows.Release, bool, error) {
	return rows.Release{}, false, errNotBuilt("the read of a release")
}

func (s remoteStore) EntryDigest(context.Context, int64, string) (rows.Entry, bool, error) {
	return rows.Entry{}, false, errNotBuilt("the read of a parsed entry")
}

func (s remoteStore) ServesName(context.Context, rows.NameScope) (bool, bool, error) {
	return false, false, errNotBuilt("the name check")
}

func (s remoteStore) EnqueueParse(context.Context, meta.Tx, int64, string, string) error {
	return errNotBuilt("the enqueue of a parse")
}

func (s remoteStore) EnsureParsed(context.Context, rows.NameScope) (bool, error) {
	return false, errNotBuilt("the settling of a scope")
}

func (s remoteStore) ClearVerifiedBy(context.Context, meta.Tx, int64, []string) (int, error) {
	return 0, errNotBuilt("the clearing of verified_by")
}
