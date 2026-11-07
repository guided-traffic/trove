package fixup

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta"
)

func init() {
	Register(Fixup{Name: "storage-usage", Order: OrderStorageUsage, Apply: recountStorageUsage})
}

// recountStorageUsage recomputes repository_usage and storage_usage_totals from items and blobs and empties the deltas
// (R-STOR-047): the blobs class filter drops rows that the exported physical totals still count.
func recountStorageUsage(ctx context.Context, tx meta.Tx, _ Env) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	return qs.RestoreRecountStorageUsage(ctx)
}
