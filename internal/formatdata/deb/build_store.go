package deb

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// buildStore is the group of the data port that reads and writes the suites and generations of the index builds (R-DEB-010 to R-DEB-014).
// It is a stub: the work package of the index builder replaces this file and no other (apt-work-packages.md, 2.2).
type buildStore struct{ *group }

func (s buildStore) Suites(context.Context, int64) ([]rows.Suite, error) {
	return nil, errNotBuilt("the index state")
}

func (s buildStore) WaitPublished(context.Context, int64, string, int64, time.Duration) (rows.Suite, bool, error) {
	return rows.Suite{}, false, errNotBuilt("the wait for a publication")
}

func (s buildStore) GenerationInputs(context.Context, meta.Tx, int64, bool) (rows.BuildInputs, error) {
	return rows.BuildInputs{}, errNotBuilt("the inputs of a build")
}

func (s buildStore) RecordGeneration(context.Context, meta.Tx, rows.GenerationRecord) (bool, error) {
	return false, errNotBuilt("the record of a generation")
}
