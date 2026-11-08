package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// viewStore is the group of the data port that reads what the package screens show (R-DEB-030).
// It is a stub: the work package of the local class replaces this file and no other (apt-work-packages.md, 2.2).
type viewStore struct{ *group }

func (s viewStore) Describe(context.Context, []int64) (map[int64]rows.VersionView, error) {
	return nil, errNotBuilt("the package screens")
}

func (s viewStore) Stored(context.Context, int64, string, int) ([]string, error) {
	return nil, errNotBuilt("the list of stored versions")
}

func (s viewStore) Find(context.Context, int64, string, string) ([]rows.Binary, error) {
	return nil, errNotBuilt("the lookup of stored files")
}

func (s viewStore) Withheld(context.Context, []string) ([]int64, error) {
	return nil, errNotBuilt("the list of withheld versions")
}
