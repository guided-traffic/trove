//go:build integration

package coordination

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
)

var itFrozenLease = cluster.RegisterLease("it_frozen", 78)

// freezeRelay relays TCP between the replicas and PostgreSQL and can freeze: from Freeze on it moves no byte and closes no
// connection, in either direction, and accepts new connections without relaying them. The server then sees what it sees of
// a client whose process is stopped while its kernel keeps the connection (a paused container, a stopped process): no
// data, and every keepalive probe answered.
type freezeRelay struct {
	ln      net.Listener
	target  string
	frozen  atomic.Bool
	thawed  chan struct{} // closed at the end of the test: every blocked relay goroutine returns
	mu      sync.Mutex
	conns   []net.Conn
	stopped bool
}

func newFreezeRelay(t *testing.T, target string) *freezeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &freezeRelay{ln: ln, target: target, thawed: make(chan struct{})}
	t.Cleanup(r.stop)
	go r.accept()
	return r
}

func (r *freezeRelay) port() int { return r.ln.Addr().(*net.TCPAddr).Port }

// Freeze stops every byte and every close from now on.
func (r *freezeRelay) Freeze() { r.frozen.Store(true) }

func (r *freezeRelay) keep(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		_ = c.Close()
		return false
	}
	r.conns = append(r.conns, c)
	return true
}

func (r *freezeRelay) accept() {
	for {
		client, err := r.ln.Accept()
		if err != nil {
			return
		}
		if !r.keep(client) {
			return
		}
		go func() {
			if r.frozen.Load() {
				<-r.thawed
				return
			}
			server, err := net.DialTimeout("tcp", r.target, 5*time.Second)
			if err != nil {
				_ = client.Close()
				return
			}
			if !r.keep(server) {
				return
			}
			go r.pump(server, client)
			r.pump(client, server)
		}()
	}
}

// pump copies from src to dst until the relay freezes; then it holds what it read and returns only when the test ends.
// An end of src (the client closed its side, as a lease holder does that gave up on a ping) is not passed on while frozen.
func (r *freezeRelay) pump(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if r.frozen.Load() {
			<-r.thawed
			return
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				_ = src.Close()
				return
			}
		}
		if err != nil {
			_ = dst.Close()
			return
		}
	}
}

func (r *freezeRelay) stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	conns := r.conns
	r.mu.Unlock()
	close(r.thawed)
	_ = r.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestLeases_R_HA_006_FrozenHolder: a holder whose process stops while its kernel keeps the TCP connection sends nothing
// more, and the server's keepalive never ends its session. The lease sessions carry idle_session_timeout at the server
// keepalive's bound (idle + interval x count, Q-HA-127), so the server ends the frozen holder's session within that bound,
// the lock goes with it, and another replica takes the lease; at no instant do two sessions hold it.
func TestLeases_R_HA_006_FrozenHolder(t *testing.T) {
	e := newEnv(t)
	host, port := e.cfg.Host, e.cfg.Port
	if e.cfg.SessionHost != "" {
		host = e.cfg.SessionHost
	}
	if e.cfg.SessionPort != 0 {
		port = e.cfg.SessionPort
	}
	relay := newFreezeRelay(t, net.JoinHostPort(host, strconv.Itoa(port)))

	// a short server keepalive for both replicas: the bound of the test is idle + interval x count = 4 s
	keepalive := config.DatabaseServerKeepalive{Idle: 2 * time.Second, Interval: time.Second, Count: 2}
	bound := keepalive.Idle + time.Duration(keepalive.Count)*keepalive.Interval
	frozenCfg := e.cfg
	frozenCfg.SessionHost, frozenCfg.SessionPort = host, relay.port()
	frozenCfg.ServerKeepalive = keepalive
	liveCfg := e.cfg
	liveCfg.ServerKeepalive = keepalive

	log := &holdLog{}
	a := e.newReplica("frozen-a", replicaOpts{role: "worker", noWorker: true, db: &frozenCfg})
	a.leases.Hold(itFrozenLease, log.work("frozen-a"))
	waitFor(t, "frozen-a holding the lease", 10*time.Second, func() bool {
		v := log.valid()
		return len(v) == 1 && v[0].replica == "frozen-a"
	})
	b := e.newReplica("frozen-b", replicaOpts{role: "worker", noWorker: true, db: &liveCfg})
	b.leases.Hold(itFrozenLease, log.work("frozen-b"))

	pids := e.frozenLockSessions()
	if len(pids) != 1 {
		t.Fatalf("pg_locks shows %d holders before the freeze, want 1", len(pids))
	}
	var twoHolders atomic.Bool
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if len(e.frozenLockSessions()) > 1 {
				twoHolders.Store(true)
			}
		}
	}()

	relay.Freeze()
	frozenAt := time.Now()
	waitFor(t, "frozen-b holding the lease", bound+30*time.Second, func() bool {
		for _, h := range log.valid() {
			if h.replica == "frozen-b" {
				return true
			}
		}
		return false
	})
	took := time.Since(frozenAt)
	close(stop)
	sampler.Wait()
	t.Logf("the lease moved %s after the holder froze (server keepalive bound %s, lease_retry_interval %s)", took.Round(100*time.Millisecond), bound, defaultCluster().LeaseRetryInterval)
	if limit := bound + defaultCluster().LeaseRetryInterval + 2*time.Second; took > limit {
		t.Errorf("R-HA-006: the lease moved %s after the holder froze, want within %s", took.Round(100*time.Millisecond), limit)
	}
	if n := e.scalar(`SELECT count(*) FROM pg_stat_activity WHERE pid = $1`, pids[0]); n != 0 {
		t.Errorf("the frozen holder's session %d still exists", pids[0])
	}
	if twoHolders.Load() {
		t.Error("R-HA-006: pg_locks showed two holders of the lease at once")
	}
	if got := e.frozenLockSessions(); len(got) != 1 || got[0] == pids[0] {
		t.Errorf("holders after the move = %v, want one other than %d", got, pids[0])
	}
}

// frozenLockSessions lists the backend PIDs that hold itFrozenLease according to pg_locks.
func (e *env) frozenLockSessions() []int64 {
	e.t.Helper()
	rows, err := e.admin.Query(context.Background(), `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 2
	    AND classid = 1 AND objid = $1 AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, itFrozenLease.Number())
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
