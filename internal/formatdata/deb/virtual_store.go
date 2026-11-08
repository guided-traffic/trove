package deb

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// virtualStore is the group of the data port that reads and records the inputs of a virtual repository's builds (R-DEB-025, R-DEB-026).
// It is a stub: the work package of the virtual class replaces this file and no other (apt-work-packages.md, 2.2).
type virtualStore struct{ *group }

func (s virtualStore) VirtualInputs(context.Context, meta.Tx, int64) (rows.VirtualInputs, error) {
	return rows.VirtualInputs{}, errNotBuilt("the inputs of a virtual build")
}

func (s virtualStore) RecordCheck(context.Context, meta.Tx, int64, string, json.RawMessage, time.Time) error {
	return errNotBuilt("the record of a check")
}
