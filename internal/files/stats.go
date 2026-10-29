package files

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

// Stats aggregates download counts in memory and writes them every
// storage.stats.flush_interval, one upsert per item, in item id order in one
// transaction (R-STOR-015). Requests reach replicas at random, so several
// replicas flush the same popular items in every interval; the common order keeps
// their batches from deadlocking on each other's rows. It is a write-only buffer
// (R-ARCH-032): no request reads it, the statistics operation reads PostgreSQL.
// The buffer is flushed at shutdown, so a drain loses no counts, and a crash
// loses at most one interval.
type Stats struct {
	run      meta.Runner
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	pending map[int64]*statEntry

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

type statEntry struct {
	n   int64
	at  time.Time
	who string
}

// NewStats creates the buffer; interval is storage.stats.flush_interval.
func NewStats(run meta.Runner, interval time.Duration, log *slog.Logger) *Stats {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Stats{
		run: run, interval: interval, log: log, now: time.Now,
		pending: map[int64]*statEntry{}, stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// Add counts one download of an item by a principal ("" for anonymous or a
// principal without a row).
func (s *Stats) Add(itemID int64, who string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	e := s.pending[itemID]
	if e == nil {
		e = &statEntry{}
		s.pending[itemID] = e
	}
	e.n++
	e.at, e.who = s.now(), who
	s.mu.Unlock()
}

// Start runs the flush loop until Stop or ctx ends. It returns at once.
func (s *Stats) Start(ctx context.Context) {
	go func() {
		defer close(s.done)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-t.C:
				if err := s.Flush(ctx); err != nil {
					s.log.Warn("download statistics were not written; they are kept for the next interval", "error", err)
				}
			}
		}
	}()
}

// Stop ends the loop; the caller flushes afterwards.
func (s *Stats) Stop() {
	s.once.Do(func() { close(s.stop) })
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
}

// Flush writes the pending counts. A batch that fails stays in the buffer, merged
// with what arrived meanwhile, and is retried at the next call.
func (s *Stats) Flush(ctx context.Context) error {
	s.mu.Lock()
	batch := s.pending
	s.pending = map[int64]*statEntry{}
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(batch))
	for id := range batch {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	type row struct {
		ID  string `json:"id"`
		N   int64  `json:"n"`
		At  string `json:"at"`
		Who string `json:"who"`
	}
	rows := make([]row, len(ids))
	for i, id := range ids {
		e := batch[id]
		rows[i] = row{ID: strconv.FormatInt(id, 10), N: e.n, At: e.at.UTC().Format(time.RFC3339Nano), Who: e.who}
	}
	raw, err := json.Marshal(rows)
	if err == nil {
		err = s.run.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			_, err = qs.UpsertDownloadStats(ctx, string(raw))
			return err
		})
	}
	if err != nil {
		s.mu.Lock()
		for id, e := range batch {
			cur := s.pending[id]
			if cur == nil {
				s.pending[id] = e
				continue
			}
			cur.n += e.n
			if e.at.After(cur.at) {
				cur.at, cur.who = e.at, e.who
			}
		}
		s.mu.Unlock()
		return err
	}
	return nil
}
