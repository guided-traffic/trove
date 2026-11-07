package app

import (
	"context"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/ship"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The wiring of the revocation journal's shipper (R-HA-054 "Outbox and shipping"). It is not an area of its own: the shipper
// needs the object-store client, which wireBlob builds, and the queue, which the coordination step builds, so the first hands
// its client to wireJournalShipper and the second starts the trigger.
//
//   - wireJournalShipper (worker and all, from wireBlob) builds the shipper over the blob bucket under the runtime credential
//     and makes it available to the job handler (a late value, because the registry is frozen first).
//   - registerJournalJobs adds the job kind revocation-journal-ship to the registry.
//   - startJournalShipTrigger (worker and all, from the coordination step) enqueues the job every ship_interval while the
//     outbox holds a row. A change never waits for it.

// wireJournalShipper builds the shipper for a replica that claims jobs.
func (a *App) wireJournalShipper(client *s3.Client, db *postgres.DB) {
	j := a.cfg.Backup.RevocationJournal
	o := ship.Options{
		Outbox: ship.NewOutbox(db), BlobPrefix: a.cfg.Storage.S3.Prefix, Discard: !j.Enabled,
		Log:          a.log.With("component", "journal"),
		OnQuarantine: func(journal.OutboxRow, error) { a.laneMetrics().jrnl.Quarantined.Inc() },
	}
	if j.Enabled {
		o.Store = s3.NewJournalStore(client, false)
	}
	a.shipper.set(ship.New(o))
}

// registerJournalJobs registers the shipping job kind.
func (a *App) registerJournalJobs(r *jobs.Registry) {
	jobs.RegisterJournalShip(r, func() (journal.Shipper, bool) { return a.shipper.get() })
}

// journalTrigger enqueues the shipping job on a timer.
type journalTrigger struct {
	outbox   journal.Outbox
	queue    *jobs.Queue
	interval time.Duration
	log      interface{ Warn(msg string, args ...any) }

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *journalTrigger) Name() string { return "journal-ship-trigger" }

func (t *journalTrigger) Start(context.Context) error {
	// The loop lives until Stop, not until the context of the start-up ends.
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.cancel, t.done = cancel, make(chan struct{})
	t.mu.Unlock()
	go t.run(ctx)
	return nil
}

func (t *journalTrigger) run(ctx context.Context) {
	defer close(t.done)
	tick := time.NewTicker(t.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		rows, _, err := t.outbox.Pending(ctx)
		if err != nil {
			t.log.Warn("revocation journal: reading the outbox", "error", err)
			continue
		}
		if rows == 0 {
			continue
		}
		if _, err := t.queue.EnqueueNow(ctx, jobs.EnqueueSpec{Kind: jobs.KindRevocationJournalShip}); err != nil && ctx.Err() == nil {
			t.log.Warn("revocation journal: enqueueing the shipper", "error", err)
		}
	}
}

func (t *journalTrigger) Stop(context.Context) error {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

// startJournalShipTrigger starts the trigger on a replica that claims jobs.
func (a *App) startJournalShipTrigger(ctx context.Context, db *postgres.DB, queue *jobs.Queue) error {
	return a.startComponent(ctx, &journalTrigger{
		outbox: ship.NewOutbox(db), queue: queue, interval: a.cfg.Backup.RevocationJournal.ShipInterval, log: a.log.With("component", "journal"),
	})
}
