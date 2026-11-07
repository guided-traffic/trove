// Package retention deletes backups by policy and sweeps the pool (R-BAK-016, R-BAK-017). [Stage] is the pruning phase of a
// create run; [Runner] is `trove backup prune` and `trove backup delete`, which are target-writing runs of their own
// (R-BAK-010) and use the same stage code.
package retention

import (
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/backup/listing"
)

// Policy is backup.retention.keep_last and keep_within (R-BAK-016).
type Policy struct {
	KeepLast   int
	KeepWithin time.Duration
}

// Expired selects the complete backups the policy deletes, oldest first: a backup that is not among the KeepLast newest and
// whose snapshot is older than KeepWithin. With KeepWithin 0 only the count counts. The newest backup is never selected,
// whatever the policy says. complete may be in any order.
func (p Policy) Expired(complete []listing.Backup, now time.Time) []listing.Backup {
	sorted := slices.Clone(complete)
	slices.SortFunc(sorted, func(a, b listing.Backup) int { return strings.Compare(string(b.ID), string(a.ID)) })
	keep := max(p.KeepLast, 1)
	var out []listing.Backup
	for i := len(sorted) - 1; i >= keep; i-- {
		if p.KeepWithin > 0 && now.Sub(sorted[i].Manifest.SnapshotAt) <= p.KeepWithin {
			continue
		}
		out = append(out, sorted[i])
	}
	return out
}
