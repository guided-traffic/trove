//go:build integration

package server

import (
	"encoding/json"
	"io"
	"net"
	"slices"
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

// outageGrace is health.database_outage_grace of the replica under test. It is long against what a loaded host
// stalls a check by: the grace runs from the first failed check to the first passing one, and a recovery takes a few
// check rounds (checkInterval, checkTimeout). The tests therefore time nothing but the grace: the first ends the
// outage when the replica has reported it, the second waits for the grace.
const outageGrace = 12 * time.Second

// outageSlack is what the replica may need beyond the grace to turn 503: it must see failure_threshold (2) failed
// checks plus one more round before the first failure counts, each a check interval plus a timeout. A 503 later than
// that means the grace ran too long.
const outageSlack = 3 * (checkInterval + checkTimeout)

// degradedChecks returns the checks a /readyz body lists as degraded.
func degradedChecks(body string) []string {
	var b struct {
		Degraded []string `json:"degraded"`
	}
	_ = json.Unmarshal([]byte(body), &b)
	return b.Degraded
}

func TestDatabaseOutageGrace(t *testing.T) {
	in := newInstall(t)
	if code, out, errOut := in.run(t, "migrate", "up"); code != 0 {
		t.Fatalf("migrate %d %s %s", code, out, errOut)
	}
	px := newPGProxy(t, net.JoinHostPort(testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_PORT")))
	host, port, _ := net.SplitHostPort(px.addr())
	// The grace must stay below retry.max_elapsed.
	web := in.start(t, "web", "trove-web-grace",
		"TROVE_DATABASE_HOST="+host, "TROVE_DATABASE_PORT="+port,
		"TROVE_DATABASE_RETRY_MAX_ELAPSED="+(outageGrace+time.Second).String(),
		"TROVE_HEALTH_DATABASE_OUTAGE_GRACE="+outageGrace.String())

	t.Run("R-OPS-001 an outage shorter than health.database_outage_grace keeps /readyz at 200 and lists the database as degraded", func(t *testing.T) {
		// A failed subtest must not leave PostgreSQL away for the next one.
		t.Cleanup(func() { px.setDown(false) })
		// Take PostgreSQL away until /readyz lists the database as degraded, give it back, and watch
		// /readyz every 50 ms until neither database nor schema is listed. Other checks (valkey, the
		// object store) may be degraded for reasons of their own. The outage lasts as long as the
		// replica needs to notice it, which is far below the grace; the guard only ends a replica that never does.
		codes, degraded, recovered := map[int]int{}, false, false
		began := time.Now()
		px.setDown(true)
		var restored time.Time
		guard := began.Add(3 * outageGrace)
		for ; !recovered && time.Now().Before(guard); time.Sleep(50 * time.Millisecond) {
			code, body := opsGet(web.ops, "/readyz")
			codes[code]++
			if code == 503 {
				_, verbose := opsGet(web.ops, "/readyz?verbose=1")
				t.Fatalf("readyz turned 503 after an outage of %s, shorter than the %s grace (restored: %t): %v\n%s\n%s",
					time.Since(began).Round(time.Millisecond), outageGrace, !restored.IsZero(), codes, verbose, web.logs.String())
			}
			list := degradedChecks(body)
			listed := code == 200 && slices.Contains(list, "database")
			degraded = degraded || listed
			if listed && restored.IsZero() {
				restored = time.Now()
				px.setDown(false)
			}
			recovered = degraded && code == 200 && !slices.Contains(list, "database") && !slices.Contains(list, "schema")
		}
		if !degraded {
			t.Fatalf("no /readyz answer listed the database as degraded during %s of outage: %v\n%s", time.Since(began).Round(time.Millisecond), codes, web.logs.String())
		}
		if !recovered {
			t.Fatalf("/readyz still lists the database or the schema as degraded %s after PostgreSQL returned: %v\n%s", time.Since(restored).Round(time.Millisecond), codes, web.logs.String())
		}
		if code, _ := opsGet(web.ops, "/healthz"); code != 200 {
			t.Errorf("healthz = %d", code)
		}
	})

	t.Run("R-OPS-001 an outage longer than the grace turns /readyz 503 and it returns to 200 when PostgreSQL is back", func(t *testing.T) {
		t.Cleanup(func() { px.setDown(false) })
		px.setDown(true)
		began := time.Now()
		waitFor(t, "readyz 503 after the grace", outageGrace+outageSlack, func() bool {
			code, _ := opsGet(web.ops, "/readyz")
			return code == 503
		})
		if took := time.Since(began); took < outageGrace {
			t.Errorf("readyz turned 503 after %s: the %s grace did not hold it", took, outageGrace)
		}
		px.setDown(false)
		waitFor(t, "readyz 200 after the outage", 20*time.Second, func() bool {
			code, _ := opsGet(web.ops, "/readyz")
			return code == 200
		})
	})
}
