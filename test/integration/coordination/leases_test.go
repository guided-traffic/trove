//go:build integration

package coordination

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var itLease = cluster.RegisterLease("it_singleton", 77)

// holdLog records every acquisition of the test lease across replicas.
type holdLog struct {
	mu      sync.Mutex
	holders []*hold
}

type hold struct {
	replica string
	seq     int64 // acquisition order in this test, not a property of the lease
	held    *cluster.Held
}

func (l *holdLog) work(replica string) func(ctx context.Context, h *cluster.Held) {
	return func(ctx context.Context, h *cluster.Held) {
		l.mu.Lock()
		l.holders = append(l.holders, &hold{replica: replica, seq: int64(len(l.holders) + 1), held: h})
		l.mu.Unlock()
		<-ctx.Done()
	}
}

func (l *holdLog) valid() []*hold {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*hold
	for _, h := range l.holders {
		if h.held.Valid() {
			out = append(out, h)
		}
	}
	return out
}

func (l *holdLog) all() []*hold {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*hold(nil), l.holders...)
}

// lockSessions lists the backend PIDs that hold the test lease according to pg_locks.
func (e *env) lockSessions() []int64 {
	e.t.Helper()
	rows, err := e.admin.Query(context.Background(), `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 2
	    AND classid = 1 AND objid = $1 AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, itLease.Number())
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var pids []int64
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			e.t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func TestLeases(t *testing.T) {
	e := newEnv(t)
	e.exec(`CREATE TABLE trove.coord_fence (replica text, seq bigint)`)
	log := &holdLog{}
	workers := map[string]*replica{}
	for _, name := range []string{"lease-a", "lease-b"} {
		r := e.newReplica(name, replicaOpts{role: "worker", noWorker: true})
		r.leases.Hold(itLease, log.work(name))
		workers[name] = r
	}
	web := e.newReplica("lease-web", replicaOpts{role: "web"})

	// Sample the server's view for the whole test: two sessions holding the same
	// lease at one instant would be a split brain.
	var maxLocks, maxValid atomic.Int64
	sampleConn := e.st.admin(t, e.db)
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			if n := int64(len(log.valid())); n > maxValid.Load() {
				maxValid.Store(n)
			}
			var n int64
			err := sampleConn.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 2 AND classid = 1 AND objid = $1 AND granted`, itLease.Number()).Scan(&n)
			if err == nil && n > maxLocks.Load() {
				maxLocks.Store(n)
			}
		}
	}()
	defer func() { close(stop); sampler.Wait() }()

	waitFor(t, "a holder", 10*time.Second, func() bool { return len(log.valid()) == 1 })

	t.Run("R-ARCH-017 exactly one session holds the lease across two workers", func(t *testing.T) {
		time.Sleep(1500 * time.Millisecond) // both have retried a few times
		if pids := e.lockSessions(); len(pids) != 1 {
			t.Fatalf("pg_locks shows %d holders of (1, %d), want 1", len(pids), itLease.Number())
		}
		if n := len(log.valid()); n != 1 {
			t.Fatalf("%d valid holders", n)
		}
	})

	t.Run("R-ARCH-017 a web replica holds no lease connection", func(t *testing.T) {
		if web.db.LeasePool() != nil {
			t.Fatal("a web replica has a lease pool")
		}
		if _, err := cluster.NewLeases(cluster.LeaseOptions{Pool: web.db.LeasePool()}); !errors.Is(err, cluster.ErrNoLeasePool) {
			t.Fatalf("NewLeases on a web replica: %v", err)
		}
		_ = web.db.Main().Ping(context.Background())
		// The patterns match what they should: the web replica has main sessions and a worker has lease sessions.
		if e.scalar(`SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:lease-web:main'`) == 0 ||
			e.scalar(`SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:lease-a:lease' OR application_name LIKE 'trove:lease-b:lease'`) == 0 {
			t.Fatal("the application_name pattern of this test does not match the sessions it looks for")
		}
		if n := e.scalar(`SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:lease-web:lease%'`); n != 0 {
			t.Fatalf("the web replica has %d lease sessions", n)
		}
		total := e.scalar(`SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:%:lease'`)
		if limit := int64(2 * e.cfg.LeasePoolSize); total > limit {
			t.Fatalf("%d lease sessions, above 2 x lease_pool_size = %d", total, limit)
		}
	})

	t.Run("R-HA-006 a holder's session ends: it commits nothing afterwards and another replica takes the lease", func(t *testing.T) {
		first := log.valid()[0]
		write := func(h *hold) error {
			return h.held.Tx(context.Background(), func(db q.DBTX) error {
				_, err := db.Exec(context.Background(), "INSERT INTO coord_fence (replica, seq) VALUES ($1, $2)", h.replica, h.seq)
				return err
			})
		}
		if err := write(first); err != nil {
			t.Fatalf("a write under the lease: %v", err)
		}
		if err := first.held.Fence(context.Background()); err != nil {
			t.Fatalf("Fence while held: %v", err)
		}

		pids := e.lockSessions()
		if len(pids) != 1 {
			t.Fatalf("holders = %v", pids)
		}
		killed := time.Now()
		e.exec(`SELECT pg_terminate_backend($1)`, pids[0])

		waitFor(t, "the holder noticing the loss", 3*time.Second, func() bool { return !first.held.Valid() })
		t.Logf("loss detected %s after the session was terminated (lease_check_interval = %s)", time.Since(killed).Round(time.Millisecond), e.cfg.LeaseCheckInterval)
		if err := write(first); err == nil {
			t.Fatal("R-HA-006: the old holder committed a write after its session ended")
		}
		if n := e.scalar(`SELECT count(*) FROM coord_fence WHERE replica = $1 AND seq = $2`, first.replica, first.seq); n != 1 {
			t.Fatalf("the old holder's rows = %d, want only the write before the loss", n)
		}
		select {
		case <-first.held.Context().Done():
		default:
			t.Fatal("the holder's context was not cancelled")
		}

		waitFor(t, "a new holder", 5*time.Second, func() bool {
			for _, h := range log.valid() {
				if h.seq != first.seq {
					return true
				}
			}
			return false
		})
		took := time.Since(killed)
		if limit := 500*time.Millisecond + time.Second + time.Second; took > limit { // lease_retry_interval + detection + 1 s
			t.Errorf("the lease moved after %s", took)
		}
		second := log.valid()[0]
		if second.seq <= first.seq {
			t.Fatalf("holder %d is not a later acquisition than %d", second.seq, first.seq)
		}
		if err := write(second); err != nil {
			t.Fatalf("the new holder's write: %v", err)
		}
		if pids := e.lockSessions(); len(pids) != 1 {
			t.Fatalf("pg_locks shows %d holders after the move", len(pids))
		}
	})

	t.Run("R-ARCH-017 a lease released explicitly moves on without waiting for a session to end", func(t *testing.T) {
		cur := log.valid()[0]
		before := len(log.all())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		workers[cur.replica].leases.Release(ctx, itLease)
		if cur.held.Valid() {
			t.Fatal("a released lease still reports valid")
		}
		waitFor(t, "the other worker holding the lease", 5*time.Second, func() bool {
			v := log.valid()
			return len(v) == 1 && v[0].replica != cur.replica
		})
		if len(log.all()) != before+1 {
			t.Fatalf("the lease was taken %d times", len(log.all())-before)
		}
	})

	t.Run("R-ARCH-024 stopping the holder releases its lease explicitly", func(t *testing.T) {
		cur := log.valid()[0]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := workers[cur.replica].leases.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		// The replica that gave the lease up earlier does not try it again, so
		// nobody holds it once the last holder released it.
		waitFor(t, "the lock to be gone", 3*time.Second, func() bool { return len(e.lockSessions()) == 0 })
	})

	t.Run("R-HA-006 at no instant did two sessions hold the lease", func(t *testing.T) {
		if n := maxLocks.Load(); n > 1 {
			t.Fatalf("pg_locks showed %d holders at once", n)
		}
		all := log.all()
		if len(all) < 3 {
			t.Fatalf("only %d acquisitions happened", len(all))
		}
	})
}
