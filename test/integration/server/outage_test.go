//go:build integration

package server

import (
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// pgProxy forwards TCP to the stack's PostgreSQL and can be taken away: while
// down it accepts and closes at once and cuts the connections that are open, as
// a primary that is being switched over does. The real server is never touched.
type pgProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	down   bool
	conns  map[net.Conn]struct{}
}

func newPGProxy(t *testing.T, target string) *pgProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &pgProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() { _ = ln.Close(); p.setDown(true) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *pgProxy) addr() string { return p.ln.Addr().String() }

func (p *pgProxy) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
	if down {
		for c := range p.conns {
			_ = c.Close()
		}
		clear(p.conns)
	}
}

func (p *pgProxy) serve(c net.Conn) {
	p.mu.Lock()
	if p.down {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", p.target, 2*time.Second)
	if err != nil {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.conns[c], p.conns[up] = struct{}{}, struct{}{}
	p.mu.Unlock()
	go func() { _, _ = io.Copy(up, c); _ = up.Close(); _ = c.Close() }()
	_, _ = io.Copy(c, up)
	_ = c.Close()
	_ = up.Close()
}

func TestDatabaseOutageGrace(t *testing.T) {
	in := newInstall(t)
	if code, out, errOut := in.run(t, "migrate", "up"); code != 0 {
		t.Fatalf("migrate %d %s %s", code, out, errOut)
	}
	px := newPGProxy(t, net.JoinHostPort(testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_PORT")))
	host, port, _ := net.SplitHostPort(px.addr())
	// Grace 2.5 s inside retry.max_elapsed 3 s; checks every 300 ms, two failures to fail a check.
	web := in.start(t, "web", "trove-web-grace",
		"TROVE_DATABASE_HOST="+host, "TROVE_DATABASE_PORT="+port, "TROVE_HEALTH_DATABASE_OUTAGE_GRACE=2500ms")

	// sample reads /readyz every 50 ms for d and reports the codes seen and
	// whether any answer listed the database as degraded.
	sample := func(d time.Duration) (codes map[int]int, degraded bool) {
		codes = map[int]int{}
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			code, body := opsGet(web.ops, "/readyz")
			codes[code]++
			if code == 200 && strings.Contains(body, `"degraded":["database"`) {
				degraded = true
			}
		}
		return codes, degraded
	}

	t.Run("R-OPS-001 an outage shorter than health.database_outage_grace keeps /readyz at 200 and lists the database as degraded", func(t *testing.T) {
		px.setDown(true)
		time.AfterFunc(1200*time.Millisecond, func() { px.setDown(false) })
		codes, degraded := sample(3 * time.Second)
		if codes[503] != 0 || codes[200] == 0 {
			t.Fatalf("readyz answers during a 1.2 s outage: %v\n%s", codes, web.logs.String())
		}
		if !degraded {
			t.Errorf("no /readyz answer listed the database as degraded during the outage: %v", codes)
		}
		if code, _ := opsGet(web.ops, "/healthz"); code != 200 {
			t.Errorf("healthz = %d", code)
		}
	})

	t.Run("R-OPS-001 an outage longer than the grace turns /readyz 503 and it returns to 200 when PostgreSQL is back", func(t *testing.T) {
		px.setDown(true)
		began := time.Now()
		waitFor(t, "readyz 503 after the grace", 8*time.Second, func() bool {
			code, _ := opsGet(web.ops, "/readyz")
			return code == 503
		})
		if took := time.Since(began); took < 2*time.Second {
			t.Errorf("readyz turned 503 after %s: the 2.5 s grace did not hold it", took)
		}
		px.setDown(false)
		waitFor(t, "readyz 200 after the outage", 20*time.Second, func() bool {
			code, _ := opsGet(web.ops, "/readyz")
			return code == 200
		})
	})
}
