package sync

import (
	"context"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

const (
	// loadTimeout bounds the one query that fills the cache after a change; the state is a handful of rows.
	loadTimeout = 3 * time.Second
	// refreshWait is how long a caller waits for the refill when the replica already holds a state: after that it answers
	// with the last state and the refill goes on in the background, so that no download waits for the database.
	refreshWait = 250 * time.Millisecond
	// retryAfter is the pause after a failed read, during which callers answer with the last state without a new query.
	retryAfter = time.Second
)

// States is the replica's view of the state of each ecosystem's advisory data (R-VULN-027): the per-replica cache
// vuln_source_state on the change area vuln_data with a safety TTL of 60 s. A request never queries on a hit; a miss starts
// one refill per ecosystem (single-flight) that every concurrent caller joins. A caller waits for it at most [refreshWait]
// when the replica already holds a state, and answers with that last state otherwise; a replica that cannot reach the
// database keeps answering with the last state it had, whose staleness shows through its synced time (R-ARCH-032).
type States struct {
	// list reads every row of the source; the database in production, a function in the unit tests.
	list       func(ctx context.Context) ([]store.StateSummary, error)
	c          *cache.Cache[string, store.StateSummary]
	staleAfter time.Duration
	now        func() time.Time
	log        *slog.Logger

	mu       stdsync.Mutex
	loaded   bool
	last     map[string]store.StateSummary
	flights  map[string]*flight
	failedAt time.Time
}

// flight is one refill of one ecosystem's entry.
type flight struct {
	done chan struct{}
	sum  store.StateSummary
	err  error
}

var _ vuln.States = (*States)(nil)

// NewStates builds the replica's view of the advisory data state over the database and the cache registry. Every role
// that serves reads it; only the worker's sync service needs more.
func NewStates(db Database, reg *cache.Registry, staleAfter time.Duration, now func() time.Time, log *slog.Logger) vuln.States {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return newStates(db, reg, staleAfter, now, log)
}

func newStates(db Database, reg *cache.Registry, staleAfter time.Duration, now func() time.Time, log *slog.Logger) *States {
	s := &States{staleAfter: staleAfter, now: now, log: log, last: map[string]store.StateSummary{}, flights: map[string]*flight{}}
	s.list = func(ctx context.Context) (rows []store.StateSummary, err error) {
		err = db.Read(ctx, func(c q.DBTX) (err error) {
			rows, err = store.Sync{}.List(ctx, c, store.SourceOSV)
			return err
		})
		return rows, err
	}
	s.c = cache.Register[string, store.StateSummary](reg, vuln.CacheSourceState, cache.Options[string, store.StateSummary]{
		MaxEntries: 256, SafetyTTL: vuln.SourceStateSafetyTTL, Areas: []string{cluster.AreaVulnData},
	})
	return s
}

// State implements [vuln.States]: the zero DataState, which is not complete, for an ecosystem that was never imported.
func (s *States) State(ecosystem string) advisory.DataState {
	if sum, ok := s.c.Get(ecosystem); ok {
		return s.dataState(sum)
	}
	f, wait := s.join(ecosystem)
	if f != nil {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-f.done:
			if f.err == nil {
				return s.dataState(f.sum)
			}
		case <-t.C:
		}
	}
	// A failed or slow read is no statement about the data: keep the last state this replica saw.
	s.mu.Lock()
	sum := s.last[ecosystem]
	s.mu.Unlock()
	return s.dataState(sum)
}

// join returns the refill of the ecosystem, started by this call when none runs, and how long the caller waits for it. It
// returns no flight within [retryAfter] of a failed read.
func (s *States) join(ecosystem string) (*flight, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wait := loadTimeout
	if s.loaded {
		wait = refreshWait
	}
	if f, ok := s.flights[ecosystem]; ok {
		return f, wait
	}
	if !s.failedAt.IsZero() && s.now().Sub(s.failedAt) < retryAfter {
		return nil, 0
	}
	f := &flight{done: make(chan struct{})}
	s.flights[ecosystem] = f
	go s.refill(ecosystem, f)
	return f, wait
}

// refill runs one flight on a context of its own, so that a caller that gave up does not cancel the fill the others wait for.
func (s *States) refill(ecosystem string, f *flight) {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	f.sum, f.err = s.c.Load(ctx, ecosystem, func(ctx context.Context) (store.StateSummary, error) { return s.load(ctx, ecosystem) })
	s.mu.Lock()
	delete(s.flights, ecosystem)
	if f.err != nil {
		s.failedAt = s.now()
	} else {
		s.failedAt = time.Time{}
	}
	s.mu.Unlock()
	if f.err != nil {
		s.log.Warn("the advisory data state could not be read; using the last known state", "ecosystem", ecosystem, "error", f.err)
	}
	close(f.done)
}

// load reads every row of the source once and keeps the whole set as the last known state.
func (s *States) load(ctx context.Context, ecosystem string) (store.StateSummary, error) {
	rows, err := s.list(ctx)
	if err != nil {
		return store.StateSummary{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.last)
	s.loaded = true
	var out store.StateSummary
	for _, r := range rows {
		s.last[r.Ecosystem] = r
		if r.Ecosystem == ecosystem {
			out = r
		}
	}
	return out, nil
}

func (s *States) dataState(sum store.StateSummary) advisory.DataState {
	if !sum.Complete && sum.SyncedAt.IsZero() && sum.DataEpoch == 0 {
		return advisory.DataState{}
	}
	d := advisory.DataState{Complete: sum.Complete, SyncedAt: sum.SyncedAt, Epoch: sum.DataEpoch, NotSyncedSinceRestore: sum.NotSyncedSinceRestore}
	d.Stale = s.staleAfter > 0 && d.Complete && (sum.SyncedAt.IsZero() || s.now().Sub(sum.SyncedAt) > s.staleAfter)
	return d
}
