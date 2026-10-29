//go:build integration

package coordination

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
)

// flushLog is a cluster.Listener that records what the poll tells the owners.
type flushLog struct {
	mu      sync.Mutex
	flushes []string
	changes []string
}

func (f *flushLog) ApplyChange(area string, _ int, p json.RawMessage) {
	var compact bytes.Buffer // PostgreSQL's jsonb adds spaces
	if err := json.Compact(&compact, p); err != nil {
		compact.Write(p)
	}
	f.mu.Lock()
	f.changes = append(f.changes, area+":"+compact.String())
	f.mu.Unlock()
}

func (f *flushLog) FlushArea(area, reason string) {
	f.mu.Lock()
	f.flushes = append(f.flushes, area+":"+reason)
	f.mu.Unlock()
}

func (f *flushLog) flushed(reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.flushes {
		if s == "cache:"+reason {
			n++
		}
	}
	return n
}

func (f *flushLog) changed(payload string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.changes {
		if s == "cache:"+payload {
			return true
		}
	}
	return false
}

func (rep *replica) notify(payload map[string]any) {
	rep.t.Helper()
	err := rep.db.Tx(context.Background(), func(tx meta.Tx) error {
		return rep.changes.Notify(tx, "cache", 1, payload)
	})
	if err != nil {
		rep.t.Fatalf("Notify: %v", err)
	}
}

// TestChangeLog covers the change log without Valkey, so that only the poll
// decides (R-ARCH-018, R-HA-004).
func TestChangeLog(t *testing.T) {
	e := newEnv(t)
	cc := defaultCluster()
	cc.EpochPollInterval = 200 * time.Millisecond
	a := e.newReplica("log-a", replicaOpts{role: "web", clu: cc})
	b := e.newReplica("log-b", replicaOpts{role: "web", clu: cc})
	log := &flushLog{}
	b.changes.AddListener(log)
	held := cache.Register[string, string](b.caches, "it_held", cache.Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"cache"}})
	within := 3 * cc.EpochPollInterval // one poll interval plus one query, with slack for a loaded machine

	appliedIs := func(n int64) func() bool { return func() bool { return b.changes.Applied("cache") == n } }

	t.Run("R-ARCH-018 a replica started now starts from the current epoch and reads the area's changed_at", func(t *testing.T) {
		if b.changes.Applied("cache") != 0 {
			t.Fatalf("applied = %d on a fresh database", b.changes.Applied("cache"))
		}
		if _, at := b.changes.AreaState("cache"); at.IsZero() {
			t.Fatal("no changed_at")
		}
	})

	t.Run("R-ARCH-018 changes written by one replica are applied by the poll of the other, in epoch order", func(t *testing.T) {
		for i := 1; i <= 3; i++ {
			a.notify(map[string]any{"n": i})
		}
		waitFor(t, "B's applied epoch to reach 3", within*5, appliedIs(3))
		log.mu.Lock()
		got := append([]string(nil), log.changes...)
		log.mu.Unlock()
		want := []string{`cache:{"n":1}`, `cache:{"n":2}`, `cache:{"n":3}`}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("applied %v, want %v in order", got, want)
		}
		if e.scalar(`SELECT epoch FROM cluster_epoch WHERE area = 'cache'`) != 3 || e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'cache'`) != 3 {
			t.Fatal("the writer left a different state in the database")
		}
	})

	t.Run("R-ARCH-018 a rolled-back writer leaves neither a row nor an epoch", func(t *testing.T) {
		err := a.db.Tx(context.Background(), func(tx meta.Tx) error {
			if err := a.changes.Notify(tx, "cache", 1, map[string]any{"n": 99}); err != nil {
				return err
			}
			return context.Canceled
		})
		if err == nil {
			t.Fatal("no error")
		}
		if e.scalar(`SELECT epoch FROM cluster_epoch WHERE area = 'cache'`) != 3 {
			t.Fatal("a rollback raised the epoch")
		}
	})

	t.Run("R-ARCH-018 epochs of concurrent writers are gap-free and in commit order", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rep := a
				if i%2 == 1 {
					rep = b
				}
				rep.notify(map[string]any{"c": i})
			}()
		}
		wg.Wait()
		if n := e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'cache'`); n != 23 {
			t.Fatalf("%d change rows, want 23", n)
		}
		if gaps := e.scalar(`SELECT count(*) FROM generate_series(1, 23) g WHERE NOT EXISTS (SELECT 1 FROM cluster_change WHERE area = 'cache' AND epoch = g)`); gaps != 0 {
			t.Fatalf("%d gaps in the epochs", gaps)
		}
		waitFor(t, "B to apply 23", within*5, appliedIs(23))
	})

	t.Run("R-HA-004 an epoch that went backwards flushes the area, and the next change takes effect", func(t *testing.T) {
		held.Set("x", "v")
		// A failover that lost committed transactions: the area is back at epoch 5.
		// A consistent older state: changed_at is the created_at of the row at that epoch.
		e.exec(`DELETE FROM cluster_change WHERE area = 'cache' AND epoch > 5;
		        UPDATE cluster_epoch SET epoch = 5, changed_at = (SELECT created_at FROM cluster_change WHERE area = 'cache' AND epoch = 5) WHERE area = 'cache'`)
		waitFor(t, "the flush", within*5, func() bool { return log.flushed(cluster.FlushEpochRegressed) == 1 })
		if _, ok := held.Get("x"); ok {
			t.Fatal("the cache of the area kept its entry")
		}
		waitFor(t, "B taking the database's epoch as its starting point", within*5, appliedIs(5))
		a.notify(map[string]any{"after": "regression"})
		waitFor(t, "the next change to take effect on B", within*5, func() bool { return log.changed(`{"after":"regression"}`) })
		if b.changes.Applied("cache") != 6 {
			t.Fatalf("applied = %d, want 6: the change was skipped as already applied", b.changes.Applied("cache"))
		}
	})

	t.Run("R-HA-004 a reused epoch (same number, other changed_at) flushes the area", func(t *testing.T) {
		before := log.flushed(cluster.FlushEpochRegressed)
		// Later writes raised the area to the same number again: same epoch, a different transaction.
		e.exec(`UPDATE cluster_epoch SET changed_at = changed_at + interval '1 second' WHERE area = 'cache';
		        UPDATE cluster_change SET created_at = created_at + interval '1 second' WHERE area = 'cache' AND epoch = 6`)
		waitFor(t, "the flush", within*5, func() bool { return log.flushed(cluster.FlushEpochRegressed) == before+1 })
		a.notify(map[string]any{"after": "reuse"})
		waitFor(t, "the next change", within*5, func() bool { return log.changed(`{"after":"reuse"}`) })
	})

	t.Run("R-HA-004 a higher epoch whose row at the applied epoch has another created_at flushes the area", func(t *testing.T) {
		before := log.flushed(cluster.FlushEpochRegressed)
		e.exec(`UPDATE cluster_change SET created_at = created_at - interval '1 minute' WHERE area = 'cache' AND epoch = 7`)
		a.notify(map[string]any{"after": "created_at"})
		waitFor(t, "the flush", within*5, func() bool { return log.flushed(cluster.FlushEpochRegressed) == before+1 })
		waitFor(t, "B at epoch 8", within*5, appliedIs(8))
	})

	t.Run("R-ARCH-018 a missing row at the applied epoch is a change_log_gap and flushes the area", func(t *testing.T) {
		e.exec(`DELETE FROM cluster_change WHERE area = 'cache' AND epoch = 8`)
		a.notify(map[string]any{"after": "gap"})
		waitFor(t, "the flush", within*5, func() bool { return log.flushed(cluster.FlushChangeLogGap) == 1 })
		waitFor(t, "B at epoch 9", within*5, appliedIs(9))
	})

	t.Run("R-ARCH-018 a payload version this replica does not know flushes the area instead of being ignored", func(t *testing.T) {
		e.exec(`UPDATE cluster_epoch SET epoch = 10, changed_at = now() WHERE area = 'cache';
		        INSERT INTO cluster_change (area, epoch, payload) VALUES ('cache', 10, '{"v": 9, "p": {"new": "format"}}')`)
		waitFor(t, "the flush", within*5, func() bool { return log.flushed(cluster.FlushUnknownPayload) == 1 })
		waitFor(t, "B at epoch 10", within*5, appliedIs(10))
	})

	t.Run("R-ARCH-018 cluster-change-prune keeps the newest row of the area and the row a lagging replica needs", func(t *testing.T) {
		e.exec(`UPDATE cluster_change SET created_at = now() - interval '3 hours' WHERE area = 'cache' AND epoch < 10`)
		n, err := cluster.PruneChanges(context.Background(), a.db.Main(), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		// Rows below 9 have a successor older than an hour; 9 has the fresh 10 as its successor and stays, 10 is the newest.
		var left []int64
		rows, _ := e.admin.Query(context.Background(), `SELECT epoch FROM cluster_change WHERE area = 'cache' ORDER BY epoch`)
		for rows.Next() {
			var x int64
			_ = rows.Scan(&x)
			left = append(left, x)
		}
		rows.Close()
		if len(left) != 2 || left[0] != 9 || left[1] != 10 {
			t.Fatalf("after the prune of %d rows these epochs remain: %v, want [9 10]", n, left)
		}
		again, err := cluster.PruneChanges(context.Background(), a.db.Main(), time.Hour)
		if err != nil || again != 0 {
			t.Fatalf("a second prune deleted %d (%v), want 0: it is idempotent", again, err)
		}
		a.notify(map[string]any{"after": "prune"})
		waitFor(t, "the change after the prune", within*5, func() bool { return log.changed(`{"after":"prune"}`) })
	})

	t.Run("R-ARCH-018 the invalidation check follows the poll and PollNow runs one at once", func(t *testing.T) {
		if err := b.changes.Check(context.Background()); err != nil {
			t.Fatalf("Check: %v", err)
		}
		if !b.changes.FreshWithin(2 * time.Second) {
			t.Fatal("FreshWithin")
		}
		a.notify(map[string]any{"now": true})
		// Concurrent triggers share polls; each returns after a poll that started after it was called.
		var wg sync.WaitGroup
		for range 5 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := b.changes.PollNow(context.Background()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if !log.changed(`{"now":true}`) {
			t.Fatal("PollNow returned before the change was applied")
		}
	})
}

// TestChangesFreshWithPostgresCutOff shows that the invalidation check depends on PostgreSQL only.
func TestCacheFillRuleAgainstTheChangeLog(t *testing.T) {
	e := newEnv(t)
	cc := defaultCluster()
	cc.EpochPollInterval = 100 * time.Millisecond
	a := e.newReplica("fill-a", replicaOpts{role: "web", clu: cc})
	b := e.newReplica("fill-b", replicaOpts{role: "web", clu: cc})
	c := cache.Register[string, string](b.caches, "it_fill", cache.Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"cache"}})

	t.Run("R-ARCH-018 a fill whose query ran before a change and whose insert lands after the poll is not cached", func(t *testing.T) {
		v, err := c.Load(context.Background(), "k", func(ctx context.Context) (string, error) {
			old := "old-value" // the query result, read before the change committed
			a.notify(map[string]any{"k": "k"})
			if err := b.changes.PollNow(ctx); err != nil { // the poll applies the change before the insert
				return "", err
			}
			return old, nil
		})
		if err != nil || v != "old-value" {
			t.Fatalf("Load = %q, %v: the request must still be served", v, err)
		}
		if _, ok := c.Get("k"); ok {
			t.Fatal("the old value was cached after the poll applied the change that invalidates it")
		}
		v, _ = c.Load(context.Background(), "k", func(context.Context) (string, error) { return "new-value", nil })
		if got, ok := c.Get("k"); !ok || got != "new-value" || v != "new-value" {
			t.Fatalf("a later fill was not cached: %q %v", got, ok)
		}
	})
}
