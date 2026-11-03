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

// loadTimeout bounds the one query that fills the cache after a change; the state is a handful of rows.
const loadTimeout = 3 * time.Second

// States is the replica's view of the state of each ecosystem's advisory data (R-VULN-027): the per-replica cache
// vuln_source_state on the change area vuln_data with a safety TTL of 60 s. A request never queries: the cache is filled once
// per change of the area, or after the safety TTL, and a replica that cannot reach the database keeps answering with the last
// state it had.
type States struct {
	// list reads every row of the source; the database in production, a function in the unit tests.
	list       func(ctx context.Context) ([]store.StateSummary, error)
	c          *cache.Cache[string, store.StateSummary]
	staleAfter time.Duration
	now        func() time.Time
	log        *slog.Logger

	mu   stdsync.Mutex
	last map[string]store.StateSummary
}

var _ vuln.States = (*States)(nil)

func newStates(db Database, reg *cache.Registry, staleAfter time.Duration, now func() time.Time, log *slog.Logger) *States {
	s := &States{staleAfter: staleAfter, now: now, log: log, last: map[string]store.StateSummary{}}
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
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	sum, err := s.c.Load(ctx, ecosystem, func(ctx context.Context) (store.StateSummary, error) { return s.load(ctx, ecosystem) })
	if err != nil {
		// A failed read is no statement about the data: keep the last state this replica saw.
		s.log.Warn("the advisory data state could not be read; using the last known state", "ecosystem", ecosystem, "error", err)
		s.mu.Lock()
		sum = s.last[ecosystem]
		s.mu.Unlock()
	}
	return s.dataState(sum)
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
	d := advisory.DataState{Complete: sum.Complete, SyncedAt: sum.SyncedAt, Epoch: sum.DataEpoch}
	d.Stale = s.staleAfter > 0 && d.Complete && (sum.SyncedAt.IsZero() || s.now().Sub(sum.SyncedAt) > s.staleAfter)
	return d
}
