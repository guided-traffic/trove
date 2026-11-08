package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// mirrorStore is the group of the data port that lists what the parsed indexes offer a mirror pass (R-DEB-024).
// It is a stub: the work package of the mirror replaces this file and no other (apt-work-packages.md, 2.2).
type mirrorStore struct{ *group }

func (s mirrorStore) MirrorNames(context.Context, rows.MirrorScope, string, int) ([]string, error) {
	return nil, errNotBuilt("the names of a mirror pass")
}

func (s mirrorStore) MirrorEntries(context.Context, rows.MirrorScope, string) ([]rows.Entry, error) {
	return nil, errNotBuilt("the entries of a mirror pass")
}
