package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
)

// KindRevocationJournalShip writes the rows of revocation_outbox to the journal in object storage (R-HA-054 "Outbox and
// shipping"). It is not a periodic task: a replica with job loops enqueues it every backup.revocation_journal.ship_interval
// while the outbox holds a row, which the scheduler's one-minute cron resolution cannot do.
const KindRevocationJournalShip = "revocation-journal-ship"

// journalShipTimeout bounds one run. A run that meets an unreachable store fails after its attempts, and the next enqueue
// starts a fresh job; the outbox holds the rows meanwhile.
const journalShipTimeout = 5 * time.Minute

// RegisterJournalShip registers the kind. Coalesce lets one more job wait while another runs, so a change that commits during
// a run is shipped by the next one, and two runs never overlap. shipper is resolved when the job runs, because the registry
// is frozen before the area that owns the shipper is wired (the late values of the composition root).
func RegisterJournalShip(r *Registry, shipper func() (journal.Shipper, bool)) {
	r.RegisterKind(KindRevocationJournalShip, KindSpec{
		Queue: "maintenance", DedupMode: Coalesce, MaxAttempts: 3, Timeout: journalShipTimeout,
		Handler: func(ctx context.Context, run *Run) error {
			s, ok := shipper()
			if !ok {
				return fmt.Errorf("revocation journal shipper: this replica has not wired it yet")
			}
			n, err := s.Ship(ctx)
			if rerr := run.SetResult(map[string]int{"shipped": n}); err == nil {
				err = rerr
			}
			return err
		},
	})
}
