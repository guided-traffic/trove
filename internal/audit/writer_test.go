package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

func init() {
	Register(EventType{Name: "wtest.denied", Category: CategoryAuthz, Mode: Async, Schema: `{"type":"object"}`})
	Register(EventType{Name: "wtest.created", Category: CategorySystem, Mode: Transactional, Schema: `{"type":"object"}`})
	for _, name := range []string{typeDenied, typeRejected, typeDownloaded} {
		if _, ok := Lookup(name); !ok {
			Register(EventType{Name: name, Category: CategoryAuthz, Mode: Async, Schema: `{"type":"object"}`})
		}
	}
}

// fakeSink records what the writer stores and fails on demand.
type fakeSink struct {
	mu      sync.Mutex
	rows    []row
	calls   int
	failing error
	// refuse is the ID of a row the table refuses.
	refuse string
	block  chan struct{}
}

func (s *fakeSink) write(ctx context.Context, rows []row) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failing != nil {
		return s.failing
	}
	for _, r := range rows {
		if r.ID == s.refuse {
			return errData
		}
	}
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *fakeSink) stored() []row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]row(nil), s.rows...)
}

var errData = errors.New("data exception")

type countMetrics struct{ recorded, dropped atomic.Int64 }

func (m *countMetrics) Recorded(string, Outcome) { m.recorded.Add(1) }
func (m *countMetrics) Dropped()                 { m.dropped.Add(1) }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestWriter(t *testing.T, s *fakeSink, cfg config.Audit, clk *clock) (*Writer, *countMetrics, *bytes.Buffer) {
	t.Helper()
	m := &countMetrics{}
	var logs bytes.Buffer
	w := newWriter(Options{
		Replica: "r1", Config: cfg, Metrics: m, Now: clk.now,
		Log: slog.New(slog.NewJSONHandler(&lockedWriter{w: &logs}, nil)),
	}, s)
	w.isData = func(err error) bool { return errors.Is(err, errData) }
	return w, m, &logs
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func baseCfg() config.Audit {
	return config.Audit{AsyncBuffer: 100, DenialDedupWindow: time.Minute, TokenRejectionDedupWindow: 5 * time.Minute}
}

func denied(actor, action, repo, path string) Event {
	return Event{
		Type: typeDenied, Actor: Actor{Kind: "user", ID: actor}, Outcome: Denied, Reason: "no_grant",
		Repository: repo, Path: path, Details: json.RawMessage(`{"required_action":"` + action + `","operation":"put"}`),
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func flush(t *testing.T, w *Writer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestR_OPS_015_AsyncEventsAreWrittenInBatchesAndCopiedToTheLog(t *testing.T) {
	s := &fakeSink{}
	w, m, logs := newTestWriter(t, s, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	for i := range 3 {
		w.Record(context.Background(), Event{Type: "wtest.denied", Actor: Actor{Kind: "user", ID: "11111111-1111-4111-8111-11111111111" + string(rune('0'+i)), Name: "ann"}, Outcome: Denied, Details: json.RawMessage(`{"n":1}`)})
	}
	flush(t, w)
	rows := s.stored()
	if len(rows) != 3 || m.recorded.Load() != 3 {
		t.Fatalf("stored %d rows, counted %d", len(rows), m.recorded.Load())
	}
	if rows[0].Replica != "r1" || rows[0].Category != "authz" || rows[0].Outcome != "denied" || rows[0].ActorName != "ann" {
		t.Errorf("row = %+v", rows[0])
	}
	if n := strings.Count(logs.String(), `"msg":"audit"`); n != 3 {
		t.Errorf("log copies = %d, want 3:\n%s", n, logs.String())
	}
	var rec map[string]any
	line, _, _ := strings.Cut(logs.String(), "\n")
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "occurred_at", "action", "category", "outcome", "actor", "details"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("log copy lacks %q: %s", k, line)
		}
	}
}

func TestR_OPS_015_ARecordNeverBlocksAndAFullBufferDropsToTheLog(t *testing.T) {
	s := &fakeSink{block: make(chan struct{})} // PostgreSQL does not answer
	cfg := baseCfg()
	cfg.AsyncBuffer = 10
	w, m, logs := newTestWriter(t, s, cfg, &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 5000 {
			w.Record(context.Background(), Event{Type: "wtest.denied", Actor: Actor{Kind: "user", Name: "ann"}, Outcome: Denied, Path: string(rune('a' + i%26))})
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Record blocked while the sink did not answer")
	}
	// The writer holds at most one batch; the buffer holds 10; the rest dropped.
	if got := m.dropped.Load(); got < 5000-10-batchMax {
		t.Errorf("dropped = %d", got)
	}
	if n := strings.Count(logs.String(), `"msg":"audit"`); int64(n) != m.dropped.Load() {
		t.Errorf("log copies = %d, dropped = %d", n, m.dropped.Load())
	}
	close(s.block)
	flush(t, w)
	if got := int64(len(s.stored())) + m.dropped.Load(); got != 5000 {
		t.Errorf("stored + dropped = %d, want 5000", got)
	}
}

func TestR_OPS_015_APostgreSQLOutageKeepsTheBatchAndRetries(t *testing.T) {
	s := &fakeSink{failing: errors.New("connection refused")}
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	w, m, _ := newTestWriter(t, s, baseCfg(), clk)
	w.Start()
	w.Record(context.Background(), Event{Type: "wtest.denied", Actor: Actor{Kind: "user", Name: "ann"}, Outcome: Denied})
	waitFor(t, "the failing attempt", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.calls > 0 })
	if m.dropped.Load() != 0 || len(s.stored()) != 0 {
		t.Fatal("the event is neither stored nor dropped yet")
	}
	s.mu.Lock()
	s.failing = nil
	s.mu.Unlock()
	clk.add(time.Minute) // past the backoff
	waitFor(t, "the retry", func() bool { return len(s.stored()) == 1 })
	flush(t, w)
	if m.recorded.Load() != 1 || m.dropped.Load() != 0 {
		t.Errorf("recorded %d dropped %d", m.recorded.Load(), m.dropped.Load())
	}
}

func TestR_OPS_015_AnEventTheTableRefusesDoesNotStopTheOthers(t *testing.T) {
	s2 := &fakeSink{}
	w2, m2, _ := newTestWriter(t, s2, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	rows := make([]row, 5)
	for i := range rows {
		rows[i] = row{ID: newID(time.Unix(1_700_000_000, 0)), Action: "wtest.denied", Category: "authz", Outcome: "denied", ActorType: "user", Details: json.RawMessage(`{}`)}
	}
	s2.refuse = rows[2].ID
	rest, err := w2.writeBatch(context.Background(), rows)
	if err != nil || len(rest) != 0 {
		t.Fatalf("writeBatch = %v, %v", rest, err)
	}
	if len(s2.stored()) != 4 || m2.dropped.Load() != 1 || m2.recorded.Load() != 4 {
		t.Errorf("stored %d dropped %d recorded %d", len(s2.stored()), m2.dropped.Load(), m2.recorded.Load())
	}
}

func TestR_OPS_015_DeduplicationWritesTheFirstAtOnceAndOneSummaryOfTheRepeats(t *testing.T) {
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	s := &fakeSink{}
	w, _, _ := newTestWriter(t, s, baseCfg(), clk)
	w.Start()
	for range 10 {
		w.Record(context.Background(), denied("11111111-1111-4111-8111-111111111111", "deploy", "npm-local", "/a"))
		clk.add(time.Second)
	}
	w.Record(context.Background(), denied("22222222-2222-4222-8222-222222222222", "deploy", "npm-local", "/a")) // another principal
	waitFor(t, "the first events", func() bool { return len(s.stored()) == 2 })
	clk.add(2 * time.Minute) // the window closes
	waitFor(t, "the summary", func() bool { return len(s.stored()) == 3 })
	flush(t, w)

	rows := s.stored()
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	var first, sum map[string]any
	_ = json.Unmarshal(rows[0].Details, &first)
	_ = json.Unmarshal(rows[2].Details, &sum)
	if first["count"] != float64(1) || first["operation"] != "put" {
		t.Errorf("first details = %s", rows[0].Details)
	}
	if sum["count"] != float64(9) || sum["repeat_of"] != rows[0].ID || sum["required_action"] != "deploy" {
		t.Errorf("summary details = %s, first id %s", rows[2].Details, rows[0].ID)
	}
	if rows[2].ID == rows[0].ID {
		t.Error("the summary reuses the first event's ID")
	}
}

func TestR_OPS_015_AReplicaThatShutsDownWritesItsOpenSummaries(t *testing.T) {
	s := &fakeSink{}
	w, _, _ := newTestWriter(t, s, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	for range 4 {
		w.Record(context.Background(), denied("11111111-1111-4111-8111-111111111111", "read", "r", "/p"))
	}
	flush(t, w)
	rows := s.stored()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the first event and the open summary", len(rows))
	}
	var sum map[string]any
	_ = json.Unmarshal(rows[1].Details, &sum)
	if sum["count"] != float64(3) {
		t.Errorf("summary = %s", rows[1].Details)
	}
}

func TestR_OPS_015_AnonymousDenialsAreRecordedOnlyOnRequestAndCollapsePerAddress(t *testing.T) {
	anon := func(path string) Event {
		return Event{
			Type: typeDenied, Actor: Actor{Kind: "anonymous"}, Outcome: Denied, Path: path,
			Source: Source{ClientAddr: "203.0.113.9:4711"}, Details: json.RawMessage(`{"required_action":"read"}`),
		}
	}
	for _, enabled := range []bool{false, true} {
		s := &fakeSink{}
		cfg := baseCfg()
		cfg.RecordAnonymousDenials = enabled
		w, _, _ := newTestWriter(t, s, cfg, &clock{t: time.Unix(1_700_000_000, 0)})
		w.Start()
		for i := range 1000 {
			w.Record(context.Background(), anon("/p"+string(rune('a'+i%26))+string(rune('a'+i/26%26))))
		}
		flush(t, w)
		rows := s.stored()
		if !enabled {
			if len(rows) != 0 {
				t.Fatalf("anonymous denials stored while disabled: %d", len(rows))
			}
			continue
		}
		total := 0
		for _, r := range rows {
			var d map[string]any
			_ = json.Unmarshal(r.Details, &d)
			total += int(d["count"].(float64))
			if r.ActorType != "anonymous" || r.SourceIP != "203.0.113.9" {
				t.Errorf("row = %+v", r)
			}
		}
		if len(rows) != 2 || total != 1000 {
			t.Errorf("rows = %d, counts sum to %d; want 2 and 1000", len(rows), total)
		}
	}
}

func TestR_OPS_015_DownloadsAreRecordedOnlyOnRequest(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := &fakeSink{}
		cfg := baseCfg()
		cfg.RecordDownloads = enabled
		w, _, _ := newTestWriter(t, s, cfg, &clock{t: time.Unix(1_700_000_000, 0)})
		w.Start()
		w.Record(context.Background(), Event{Type: typeDownloaded, Actor: Actor{Kind: "user", Name: "ann"}, Details: json.RawMessage(`{"path":"/x"}`)})
		flush(t, w)
		if got := len(s.stored()); got != map[bool]int{false: 0, true: 1}[enabled] {
			t.Errorf("record_downloads=%v stored %d", enabled, got)
		}
	}
}

func TestR_OPS_015_TokenRejectionsCollapsePerTokenID(t *testing.T) {
	s := &fakeSink{}
	w, _, _ := newTestWriter(t, s, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	for range 6 {
		w.Record(context.Background(), Event{
			Type: typeRejected, Actor: Actor{Kind: "anonymous"}, Outcome: Failure, Reason: "revoked",
			Subject: Subject{Kind: "token", ID: "tok-1"}, Details: json.RawMessage(`{"token_type":"pat"}`),
		})
	}
	w.Record(context.Background(), Event{Type: typeRejected, Actor: Actor{Kind: "anonymous"}, Outcome: Failure, Subject: Subject{Kind: "token", ID: "tok-2"}})
	flush(t, w)
	if got := len(s.stored()); got != 3 { // two firsts and one summary
		t.Errorf("rows = %d, want 3", got)
	}
}

func TestR_OPS_015_RecordOfTheWrongTypeIsSkippedNotFatal(t *testing.T) {
	s := &fakeSink{}
	w, _, _ := newTestWriter(t, s, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	w.Record(context.Background(), Event{Type: "no.such.type", Actor: Actor{Kind: "user"}})
	w.Record(context.Background(), Event{Type: "wtest.created", Actor: Actor{Kind: "user"}}) // transactional
	w.Record(context.Background(), Event{Type: "wtest.denied"})                              // no actor kind
	w.Record(context.Background(), Event{Type: "wtest.denied", Actor: Actor{Kind: "user"}, Details: json.RawMessage(`[1]`)})
	flush(t, w)
	if len(s.stored()) != 0 {
		t.Errorf("stored %d", len(s.stored()))
	}
}

func TestR_OPS_015_AfterFlushEventsGoToTheLogOnly(t *testing.T) {
	s := &fakeSink{}
	w, m, logs := newTestWriter(t, s, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	flush(t, w)
	w.Record(context.Background(), Event{Type: "wtest.denied", Actor: Actor{Kind: "user", Name: "ann"}, Outcome: Denied})
	if m.dropped.Load() != 1 || !strings.Contains(logs.String(), `"msg":"audit"`) {
		t.Errorf("dropped = %d, log = %s", m.dropped.Load(), logs.String())
	}
	flush(t, w) // twice is fine
}

func TestR_OPS_015_ShutdownGivesUpAtTheDeadlineAndLogsWhatIsLeft(t *testing.T) {
	s := &fakeSink{failing: errors.New("connection refused")}
	w, m, logs := newTestWriter(t, s, baseCfg(), &clock{t: time.Unix(1_700_000_000, 0)})
	w.Start()
	for range 3 {
		w.Record(context.Background(), Event{Type: "wtest.denied", Actor: Actor{Kind: "user", Name: "ann"}, Outcome: Denied})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := w.Flush(ctx); err == nil {
		t.Fatal("Flush reported success with PostgreSQL away")
	}
	waitFor(t, "the leftovers in the log", func() bool { return m.dropped.Load() == 3 })
	if n := strings.Count(logs.String(), `"msg":"audit"`); n != 3 {
		t.Errorf("log copies = %d", n)
	}
}

func TestR_OPS_015_BackoffGrowsToItsCeiling(t *testing.T) {
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, d := range want {
		if got := backoff(i + 1); got != d {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, d)
		}
	}
}
