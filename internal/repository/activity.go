package repository

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

// DefaultActivityInterval is how often a replica writes the activity of its
// repositories (R-REPO-012): at most once a minute per repository.
const DefaultActivityInterval = time.Minute

// Activity aggregates "a repository was uploaded to or downloaded from" in
// memory and writes repositories.last_activity_at in batches, so that downloads
// do not turn into database writes (R-REPO-012). It is a write-only buffer
// (R-ARCH-032): no request reads it, the list operation reads the column, and
// the shutdown flushes it (R-ARCH-024 step 6). A crash loses at most one
// interval of this replica's activity (R-REPO-049).
type Activity struct {
	run      meta.Runner
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	pending map[string]time.Time // repository id -> latest instant

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// NewActivity creates the buffer; interval zero means DefaultActivityInterval.
func NewActivity(run meta.Runner, interval time.Duration, log *slog.Logger) *Activity {
	if interval <= 0 {
		interval = DefaultActivityInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Activity{
		run: run, interval: interval, log: log, now: time.Now,
		pending: map[string]time.Time{}, stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// Touch records activity of the repository with the given id (Repository.ID) now.
// It takes a lock for a map write and does nothing else.
func (a *Activity) Touch(repoID string) {
	if a == nil || repoID == "" {
		return
	}
	a.mu.Lock()
	a.pending[repoID] = a.now()
	a.mu.Unlock()
}

// Start runs the flush loop until Stop or ctx ends. It returns at once.
func (a *Activity) Start(ctx context.Context) {
	go func() {
		defer close(a.done)
		t := time.NewTicker(a.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.stop:
				return
			case <-t.C:
				if err := a.Flush(ctx); err != nil {
					a.log.Warn("repository activity was not written; it is kept for the next interval", "error", err)
				}
			}
		}
	}()
}

// Stop ends the loop; the caller flushes afterwards.
func (a *Activity) Stop() {
	a.once.Do(func() { close(a.stop) })
	select {
	case <-a.done:
	case <-time.After(time.Second):
	}
}

// Flush writes the pending activity in one statement. A batch that fails stays in
// the buffer (merged with what arrived meanwhile) and is retried at the next call.
func (a *Activity) Flush(ctx context.Context) error {
	a.mu.Lock()
	batch := a.pending
	a.pending = map[string]time.Time{}
	a.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	type entry struct {
		ID string `json:"id"`
		At string `json:"at"`
	}
	entries := make([]entry, 0, len(batch))
	for id, at := range batch {
		entries = append(entries, entry{ID: id, At: at.UTC().Format(time.RFC3339Nano)})
	}
	raw, err := json.Marshal(entries)
	if err == nil {
		err = a.run.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			_, err = qs.TouchRepositoryActivity(ctx, string(raw))
			return err
		})
	}
	if err != nil {
		a.mu.Lock()
		for id, at := range batch {
			if cur, ok := a.pending[id]; !ok || at.After(cur) {
				a.pending[id] = at
			}
		}
		a.mu.Unlock()
		return err
	}
	return nil
}
