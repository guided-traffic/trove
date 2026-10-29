//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
)

func hit(i int) audit.Event {
	return audit.Event{
		Type: "probe.hit.logged", Actor: audit.Actor{Kind: "user", Name: "ann"}, Outcome: audit.Failure, Reason: "x",
		Path: fmt.Sprintf("/p/%d", i), Details: json.RawMessage(`{}`),
	}
}

func TestR_OPS_015_AsyncEventsAreBatchedAndFlushedAtShutdown(t *testing.T) {
	e := newEnv(t)
	w, m, logs := e.writer(e.open(), auditCfg(), time.Now)
	for i := range 1200 {
		w.Record(context.Background(), hit(i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e, "audit_event", "action = 'probe.hit.logged'"); n != 1200 {
		t.Errorf("rows = %d, want 1200", n)
	}
	if m.recorded.Load() != 1200 || m.dropped.Load() != 0 {
		t.Errorf("recorded %d dropped %d", m.recorded.Load(), m.dropped.Load())
	}
	if n := strings.Count(logs.String(), `"msg":"audit"`); n != 1200 {
		t.Errorf("log copies = %d", n)
	}
}

// R-OPS-015: a burst never blocks request handling. Here PostgreSQL is slow in
// the worst way, every insert waits on a table lock.
func TestR_OPS_015_AsyncEventsDoNotBlockWhileTheTableIsLocked(t *testing.T) {
	e := newEnv(t)
	cfg := auditCfg()
	cfg.AsyncBuffer = 100
	w, m, logs := e.writer(e.open(), cfg, time.Now)

	lock := e.st.admin(t, e.db)
	mustExec(t, lock, "BEGIN")
	mustExec(t, lock, "LOCK TABLE trove.audit_event IN ACCESS EXCLUSIVE MODE")

	const total = 20000
	var slowest time.Duration
	begin := time.Now()
	for i := range total {
		start := time.Now()
		w.Record(context.Background(), hit(i))
		slowest = max(slowest, time.Since(start))
	}
	took := time.Since(begin)
	if slowest > 100*time.Millisecond || took > 3*time.Second {
		t.Errorf("Record blocked: slowest %v, %d calls took %v", slowest, total, took)
	}
	if m.dropped.Load() == 0 {
		t.Error("nothing was dropped although the buffer holds 100 and PostgreSQL did not answer")
	}
	if n := strings.Count(logs.String(), `"msg":"audit"`); int64(n) != m.dropped.Load() {
		t.Errorf("a dropped event must be in the log stream: %d copies for %d dropped", n, m.dropped.Load())
	}

	// PostgreSQL recovers: what was buffered is written, the rest was dropped.
	mustExec(t, lock, "ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	stored := int64(count(t, e, "audit_event", "action = 'probe.hit.logged'"))
	if stored+m.dropped.Load() != total || stored != m.recorded.Load() {
		t.Errorf("stored %d + dropped %d != %d (recorded %d)", stored, m.dropped.Load(), total, m.recorded.Load())
	}
}

func TestR_OPS_015_AsyncEventsSurviveALostConnectionAndGoToTheLogAtShutdown(t *testing.T) {
	e := newEnv(t)
	db := e.open()
	w, m, logs := e.writer(db, auditCfg(), time.Now)
	db.Close() // PostgreSQL is not reachable from this replica any more
	for i := range 5 {
		w.Record(context.Background(), hit(i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	if err := w.Flush(ctx); err == nil {
		t.Error("Flush reported success without a database")
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.dropped.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.dropped.Load() != 5 || strings.Count(logs.String(), `"msg":"audit"`) != 5 {
		t.Errorf("dropped %d, log copies %d: the events are lost", m.dropped.Load(), strings.Count(logs.String(), `"msg":"audit"`))
	}
}

func TestR_OPS_015_AnEventTheTableRefusesDoesNotCostTheOthers(t *testing.T) {
	e := newEnv(t)
	w, m, _ := e.writer(e.open(), auditCfg(), time.Now)
	for i := range 6 {
		ev := hit(i)
		if i == 3 {
			ev.Details = json.RawMessage(`{"a":"\u0000"}`) // valid JSON that jsonb cannot hold
		}
		w.Record(context.Background(), ev)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e, "audit_event", "action = 'probe.hit.logged'"); n != 5 || m.dropped.Load() != 1 {
		t.Errorf("rows %d, dropped %d; want 5 and 1", n, m.dropped.Load())
	}
}

// fakeClock lets a test close the deduplication windows.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func denial(i int) audit.Event {
	return audit.Event{
		Type: "authz.denied", Actor: audit.Actor{Kind: "user", ID: "5f0e1111-2222-4333-8444-555566667777", Name: "ci-reader"},
		Outcome: audit.Denied, Reason: "no_grant", Repository: "npm-local", Path: "/@acme/ui",
		Details: json.RawMessage(`{"operation":"npm.publish","required_action":"deploy"}`),
	}
}

func TestR_OPS_015_RepeatedDenialsCollapseIntoTheFirstAndOneSummary(t *testing.T) {
	e := newEnv(t)
	clk := &fakeClock{t: time.Now()}
	w, _, _ := e.writer(e.open(), auditCfg(), clk.now)
	for i := range 10 {
		w.Record(context.Background(), denial(i))
	}
	waitRows(t, e, "action = 'authz.denied'", 1)
	clk.add(2 * time.Minute) // the window closes; the next tick writes the summary
	waitRows(t, e, "action = 'authz.denied'", 2)

	rows, err := e.admin.Query(context.Background(), "SELECT id::text, details FROM trove.audit_event WHERE action = 'authz.denied' ORDER BY occurred_at, (details ->> 'count')::int")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	var details []map[string]any
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		_ = json.Unmarshal(raw, &d)
		ids, details = append(ids, id), append(details, d)
	}
	if len(ids) != 2 || details[0]["count"] != float64(1) || details[1]["count"] != float64(9) || details[1]["repeat_of"] != ids[0] {
		t.Errorf("ids %v details %v", ids, details)
	}
	if details[1]["operation"] != "npm.publish" {
		t.Errorf("the summary lost the first event's details: %v", details[1])
	}
}

// Requests reach replicas at random: with n replicas one burst yields up to n
// first events and n summaries, and the counts still sum to the burst.
func TestR_OPS_015_DenialsOverThreeReplicasSumExactly(t *testing.T) {
	e := newEnv(t)
	clk := &fakeClock{t: time.Now()}
	var ws []*audit.Writer
	for range 3 {
		w, _, _ := e.writer(e.open(), auditCfg(), clk.now)
		ws = append(ws, w)
	}
	for i := range 10 {
		ws[i*7%3].Record(context.Background(), denial(i)) // an uneven spread
	}
	clk.add(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, w := range ws {
		if err := w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	n := count(t, e, "audit_event", "action = 'authz.denied'")
	sum := scalar[int](t, e.admin, "SELECT coalesce(sum((details ->> 'count')::int), 0) FROM trove.audit_event WHERE action = 'authz.denied'")
	if n > 6 || sum != 10 {
		t.Errorf("%d events with counts summing to %d; want at most 6 and 10", n, sum)
	}
}

func TestR_AUTHZ_029_AnonymousDenialsCollapsePerClientAddress(t *testing.T) {
	e := newEnv(t)
	cfg := auditCfg()
	cfg.RecordAnonymousDenials = true
	w, _, _ := e.writer(e.open(), cfg, time.Now)
	for i := range 1000 {
		w.Record(context.Background(), audit.Event{
			Type: "authz.denied", Actor: audit.Actor{Kind: "anonymous"}, Outcome: audit.Denied, Reason: "anonymous_not_allowed",
			Source: audit.Source{ClientAddr: "203.0.113.9"}, Path: fmt.Sprintf("/probe/%d", i),
			Details: json.RawMessage(`{"required_action":"read"}`),
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	n := count(t, e, "audit_event", "action = 'authz.denied' AND actor_type = 'anonymous' AND source_ip = '203.0.113.9'")
	sum := scalar[int](t, e.admin, "SELECT coalesce(sum((details ->> 'count')::int), 0) FROM trove.audit_event WHERE action = 'authz.denied'")
	if n < 1 || n > 2 || sum != 1000 {
		t.Errorf("%d events, counts sum to %d; want at most 2 and 1000", n, sum)
	}
}

func waitRows(t *testing.T, e *env, where string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if count(t, e, "audit_event", where) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d rows where %s (have %d)", want, where, count(t, e, "audit_event", where))
}
