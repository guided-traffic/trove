package downloads_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

// sender is a main installation reduced to the downloads operation.
type sender struct {
	mu      sync.Mutex
	batches []iface.DownloadsRequest
	fail    func(n int) error // n is the number of the call, from 1
	calls   int
}

func (s *sender) Downloads(_ context.Context, req iface.DownloadsRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail != nil {
		if err := s.fail(s.calls); err != nil {
			return err
		}
	}
	s.batches = append(s.batches, req)
	return nil
}

func (s *sender) events() []iface.DownloadEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []iface.DownloadEvent
	for _, b := range s.batches {
		out = append(out, b.Events...)
	}
	return out
}

func (s *sender) all() []iface.DownloadsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]iface.DownloadsRequest(nil), s.batches...)
}

type env struct {
	m         *metrics.Metrics
	snd       *sender
	spool     *maintest.MemSpool
	reachable atomic.Bool
	r         *downloads.Reporter
}

func newEnv(t *testing.T, mut func(*config.BastionAudit, *downloads.ReporterOptions)) *env {
	t.Helper()
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{m: m, snd: &sender{}, spool: maintest.NewMemSpool()}
	e.reachable.Store(true)
	cfg := config.BastionAudit{BufferEvents: 1000, BatchSize: 10, FlushInterval: 20 * time.Millisecond, SpoolMaxEvents: 1000}
	o := downloads.ReporterOptions{Replica: "replica-a", Sender: e.snd, Spool: e.spool, Reachable: e.reachable.Load, Metrics: m, AdoptEvery: 20 * time.Millisecond}
	if mut != nil {
		mut(&cfg, &o)
	}
	o.Config = cfg
	e.r = downloads.NewReporter(o)
	return e
}

func (e *env) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = e.r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func ev(i int) iface.DownloadEvent {
	return iface.DownloadEvent{
		At: time.Date(2026, 11, 2, 10, 0, 0, i*1_000_000, time.UTC), TokenID: "t", ClientAddress: "203.0.113.7", Repository: "npm-all",
		Path: fmt.Sprintf("/api/npm/npm-all/pkg-%d.tgz", i), SHA256: strings.Repeat("a", 64), Replica: "replica-a",
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within 10s: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestR_BAS_039_EventsAreSentOnlyWhileRecordDownloadsIsTrue: until the handshake says so, an event is discarded
// and not counted; turning it off discards what is buffered.
func TestR_BAS_039_EventsAreSentOnlyWhileRecordDownloadsIsTrue(t *testing.T) {
	e := newEnv(t, nil)
	e.r.Add(ev(1))
	if maintest.Value(e.m.AuditBuffer) != 0 || maintest.Value(e.m.AuditDropped) != 0 {
		t.Fatal("an event was kept or counted before recordDownloads")
	}
	e.r.SetEnabled(true)
	e.r.Add(ev(2))
	e.r.Add(ev(3))
	if maintest.Value(e.m.AuditBuffer) != 2 {
		t.Fatalf("buffer = %v", maintest.Value(e.m.AuditBuffer))
	}
	e.r.SetEnabled(false)
	if maintest.Value(e.m.AuditBuffer) != 0 {
		t.Error("turning the recording off kept the buffer")
	}
	e.r.Add(ev(4))
	e.run(t)
	time.Sleep(100 * time.Millisecond)
	if n := len(e.snd.events()); n != 0 {
		t.Errorf("%d events sent with recordDownloads false", n)
	}
}

// TestR_BAS_039_ABatchGoesAtBatchSizeAndAPartialOneAtTheFlushInterval: a full batch does not wait for the
// interval; the rest goes with the next interval; the events keep their time.
func TestR_BAS_039_ABatchGoesAtBatchSizeAndAPartialOneAtTheFlushInterval(t *testing.T) {
	e := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.FlushInterval = time.Hour })
	e.r.SetEnabled(true)
	e.run(t)
	for i := 0; i < 10; i++ {
		e.r.Add(ev(i))
	}
	eventually(t, "the full batch", func() bool { return len(e.snd.events()) == 10 })
	if b := e.snd.all(); len(b) != 1 || len(b[0].Events) != 10 || b[0].Dropped != 0 || b[0].DroppedSince != nil {
		t.Errorf("batches: %+v", b)
	}
	for i := 0; i < 3; i++ {
		e.r.Add(ev(100 + i))
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.snd.events()); n != 10 {
		t.Fatalf("a partial batch was sent before the flush interval: %d", n)
	}

	e2 := newEnv(t, nil) // flush interval 20 ms
	e2.r.SetEnabled(true)
	e2.run(t)
	for i := 0; i < 3; i++ {
		e2.r.Add(ev(i))
	}
	eventually(t, "the partial batch", func() bool { return len(e2.snd.events()) == 3 })
	got := e2.snd.events()
	for i := range got {
		if !got[i].At.Equal(ev(i).At) || got[i].Path != ev(i).Path {
			t.Errorf("event %d changed: %+v", i, got[i])
		}
	}
	// The reporter counts a batch as sent when the send has returned, after the sender holds its events.
	eventually(t, "sent = 3", func() bool { return maintest.Value(e2.m.AuditEvents.WithLabelValues("sent")) == 3 })
	eventually(t, "the buffer empties", func() bool { return maintest.Value(e2.m.AuditBuffer) == 0 })
}

// TestR_BAS_039_WhileUnreachableTheBufferGrowsToItsBoundAndTheExcessIsReportedAsDropped: events wait for the
// main installation; with buffer_events 100, 150 events leave 100 and a dropped count of 50 with the time of the
// first lost event, in the next batch only; the metrics agree.
func TestR_BAS_039_WhileUnreachableTheBufferGrowsToItsBoundAndTheExcessIsReportedAsDropped(t *testing.T) {
	e := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.BufferEvents, c.BatchSize = 100, 40 })
	e.reachable.Store(false)
	e.r.SetEnabled(true)
	e.run(t)
	for i := 0; i < 150; i++ {
		e.r.Add(ev(i))
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.snd.events()); n != 0 {
		t.Fatalf("%d events sent while unreachable", n)
	}
	if maintest.Value(e.m.AuditBuffer) != 100 || maintest.Value(e.m.AuditDropped) != 50 || maintest.Value(e.m.AuditEvents.WithLabelValues("dropped")) != 50 {
		t.Fatalf("buffer %v dropped %v", maintest.Value(e.m.AuditBuffer), maintest.Value(e.m.AuditDropped))
	}
	e.reachable.Store(true)
	eventually(t, "everything sent", func() bool { return len(e.snd.events()) == 100 })
	batches := e.snd.all()
	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 40+40+20", len(batches))
	}
	if batches[0].Dropped != 50 || batches[0].DroppedSince == nil || !batches[0].DroppedSince.Equal(ev(100).At) {
		t.Errorf("first batch: dropped %d since %v, want 50 since %v", batches[0].Dropped, batches[0].DroppedSince, ev(100).At)
	}
	if batches[1].Dropped != 0 || batches[2].Dropped != 0 || batches[1].DroppedSince != nil {
		t.Errorf("the count was reported again: %+v", batches[1:])
	}
	// The order and the original times survive the outage.
	for i, got := range e.snd.events() {
		if !got.At.Equal(ev(i).At) {
			t.Fatalf("event %d: %v", i, got.At)
		}
	}
	// The counter that was reported is cleared: the next loss starts a new period.
	e.r.Add(ev(200))
	eventually(t, "the next event", func() bool { return len(e.snd.events()) == 101 })
	if last := e.snd.all(); last[len(last)-1].Dropped != 0 {
		t.Error("an old drop count was reported twice")
	}
}

// TestR_BAS_039_ADropCountThatFindsNoEventsIsReportedOnItsOwn: no event is buffered, but events were lost: the
// batch has an empty events list and the count.
func TestR_BAS_039_ADropCountThatFindsNoEventsIsReportedOnItsOwn(t *testing.T) {
	e := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.BufferEvents, c.BatchSize = 2, 2 })
	e.reachable.Store(false)
	e.r.SetEnabled(true)
	for i := 0; i < 5; i++ {
		e.r.Add(ev(i))
	}
	e.reachable.Store(true)
	e.run(t)
	eventually(t, "the batch", func() bool { return len(e.snd.all()) >= 1 })
	if b := e.snd.all()[0]; len(b.Events) != 2 || b.Dropped != 3 {
		t.Errorf("batch %+v", b)
	}
}

// TestR_BAS_039_AFailedSendKeepsTheEventsAndARefusedBatchIsDroppedAndCounted: a 5xx or a cut keeps the events for
// the next interval; a 400 for the batch itself drops its events, counts them and reports the count.
func TestR_BAS_039_AFailedSendKeepsTheEventsAndARefusedBatchIsDroppedAndCounted(t *testing.T) {
	e := newEnv(t, nil)
	e.snd.fail = func(n int) error {
		switch n {
		case 1, 2:
			return &mainclient.UnreachableError{Status: 503}
		case 3:
			return &mainclient.AnswerError{Status: http.StatusBadRequest}
		}
		return nil
	}
	e.r.SetEnabled(true)
	for i := 0; i < 4; i++ {
		e.r.Add(ev(i))
	}
	e.run(t)
	eventually(t, "the refused batch is dropped", func() bool { return maintest.Value(e.m.AuditDropped) == 4 })
	// The loss is reported on its own, at once: with nothing else buffered the batch has no events.
	eventually(t, "the loss is reported", func() bool { return len(e.snd.all()) == 1 })
	b := e.snd.all()[0]
	if b.Dropped != 4 || b.DroppedSince == nil || !b.DroppedSince.Equal(ev(0).At) || len(b.Events) != 0 {
		t.Errorf("batch %+v", b)
	}
	e.r.Add(ev(10))
	eventually(t, "the next event", func() bool { return len(e.snd.events()) == 1 })
	if last := e.snd.all(); last[len(last)-1].Dropped != 0 {
		t.Error("the count was reported twice")
	}
}

// TestR_BAS_039_ADrainSendsWhatItCanAndSpoolsTheRest: reachable, the drain sends the buffer and spools nothing;
// unreachable, it writes the events and the unreported drop count into one spool file, one batch per line.
func TestR_BAS_039_ADrainSendsWhatItCanAndSpoolsTheRest(t *testing.T) {
	ctx := context.Background()
	t.Run("reachable", func(t *testing.T) {
		e := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.FlushInterval = time.Hour })
		e.r.SetEnabled(true)
		for i := 0; i < 25; i++ {
			e.r.Add(ev(i))
		}
		if err := e.r.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		if len(e.snd.events()) != 25 || len(e.spool.Files()) != 0 {
			t.Errorf("sent %d, spool %d", len(e.snd.events()), len(e.spool.Files()))
		}
		if maintest.Value(e.m.AuditEvents.WithLabelValues("sent")) != 25 || maintest.Value(e.m.AuditBuffer) != 0 {
			t.Error("metrics")
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		e := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.BufferEvents, c.BatchSize = 25, 10 })
		e.reachable.Store(false)
		e.r.SetEnabled(true)
		for i := 0; i < 30; i++ {
			e.r.Add(ev(i)) // 25 kept, 5 dropped
		}
		if err := e.r.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		files := e.spool.Files()
		if len(files) != 1 || len(e.snd.events()) != 0 {
			t.Fatalf("spool files %d, sent %d", len(files), len(e.snd.events()))
		}
		for name, content := range files {
			if !strings.HasPrefix(name, "replica-a/") {
				t.Errorf("file %s", name)
			}
			lines := strings.Split(strings.TrimSpace(content), "\n")
			if len(lines) != 3 {
				t.Fatalf("lines = %d, want 10+10+5", len(lines))
			}
			var first iface.DownloadsRequest
			if err := iface.DecodeStrict(strings.NewReader(lines[0]), &first); err != nil {
				t.Fatal(err)
			}
			if len(first.Events) != 10 || first.Dropped != 5 || first.DroppedSince == nil || !first.DroppedSince.Equal(ev(25).At) {
				t.Errorf("first line: %+v", first)
			}
		}
		if maintest.Value(e.m.AuditEvents.WithLabelValues("spooled")) != 25 || maintest.Value(e.m.AuditBuffer) != 0 {
			t.Errorf("spooled %v", maintest.Value(e.m.AuditEvents.WithLabelValues("spooled")))
		}
		// A second drain does nothing, and an event after the drain is lost and counted.
		if err := e.r.Drain(ctx); err != nil || len(e.spool.Files()) != 1 {
			t.Errorf("second drain: %v, files %d", err, len(e.spool.Files()))
		}
		before := maintest.Value(e.m.AuditDropped)
		e.r.Add(ev(99))
		if maintest.Value(e.m.AuditDropped) != before+1 {
			t.Error("an event after the drain was not counted as dropped")
		}
	})
	t.Run("a send that fails during the drain is spooled", func(t *testing.T) {
		e := newEnv(t, nil)
		e.snd.fail = func(int) error { return &mainclient.UnreachableError{Timeout: true} }
		e.r.SetEnabled(true)
		for i := 0; i < 4; i++ {
			e.r.Add(ev(i))
		}
		if err := e.r.Drain(ctx); err != nil || len(e.spool.Files()) != 1 {
			t.Errorf("%v, files %d", err, len(e.spool.Files()))
		}
	})
}

// TestR_BAS_039_TheSpoolIsBoundedBySpoolMaxEvents: with 100 buffered and spool_max_events 30, 30 are spooled and
// 70 are lost, counted and logged; with 0, nothing is spooled.
func TestR_BAS_039_TheSpoolIsBoundedBySpoolMaxEvents(t *testing.T) {
	for _, tc := range []struct{ max, spooled, lost int }{{30, 30, 70}, {0, 0, 100}, {1000, 100, 0}} {
		e := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.SpoolMaxEvents = tc.max })
		e.reachable.Store(false)
		e.r.SetEnabled(true)
		for i := 0; i < 100; i++ {
			e.r.Add(ev(i))
		}
		if err := e.r.Drain(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := maintest.Value(e.m.AuditEvents.WithLabelValues("spooled")); got != float64(tc.spooled) {
			t.Errorf("max %d: spooled %v, want %d", tc.max, got, tc.spooled)
		}
		if got := maintest.Value(e.m.AuditEvents.WithLabelValues("dropped")); got != float64(tc.lost) {
			t.Errorf("max %d: dropped %v, want %d", tc.max, got, tc.lost)
		}
		if got := len(e.spool.Files()); (tc.spooled > 0) != (got == 1) {
			t.Errorf("max %d: files %d", tc.max, got)
		}
	}
	// A spool that cannot be written loses the events, counted.
	e := newEnv(t, nil)
	e.spool.FailWrites = true
	e.reachable.Store(false)
	e.r.SetEnabled(true)
	e.r.Add(ev(1))
	if err := e.r.Drain(context.Background()); err == nil {
		t.Error("a failing spool was not reported")
	}
	if maintest.Value(e.m.AuditEvents.WithLabelValues("dropped")) != 1 {
		t.Error("the lost event was not counted")
	}
}

// TestR_BAS_039_AnotherReplicaDeliversASpoolFileAndDeletesItAfter2xx: replica A drains during an outage; replica B,
// once the main installation is reachable, sends the events with their original times and deletes the file; a
// failed send keeps it.
func TestR_BAS_039_AnotherReplicaDeliversASpoolFileAndDeletesItAfter2xx(t *testing.T) {
	a := newEnv(t, func(c *config.BastionAudit, _ *downloads.ReporterOptions) { c.BufferEvents, c.BatchSize = 12, 5 })
	a.reachable.Store(false)
	a.r.SetEnabled(true)
	for i := 0; i < 15; i++ {
		a.r.Add(ev(i))
	}
	if err := a.r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.spool.Files()) != 1 {
		t.Fatalf("files = %d", len(a.spool.Files()))
	}

	b := newEnv(t, func(_ *config.BastionAudit, o *downloads.ReporterOptions) { o.Spool = a.spool })
	b.reachable.Store(false)
	if err := b.r.Adopt(context.Background()); err != nil || len(b.snd.events()) != 0 || len(a.spool.Files()) != 1 {
		t.Fatalf("adoption while unreachable: %v", err)
	}
	b.reachable.Store(true)
	b.snd.fail = func(n int) error {
		if n == 2 {
			return &mainclient.UnreachableError{Status: 503}
		}
		return nil
	}
	// The second line fails: the first is delivered, the file is replaced by one with the rest.
	if err := b.r.Adopt(context.Background()); err == nil {
		t.Fatal("a failed delivery was not reported")
	}
	if len(b.snd.events()) != 5 || len(a.spool.Files()) != 1 {
		t.Fatalf("after the failure: %d events sent, %d files", len(b.snd.events()), len(a.spool.Files()))
	}
	for _, content := range a.spool.Files() {
		if n := strings.Count(content, "\n"); n != 2 {
			t.Errorf("the file keeps %d lines, want the 2 undelivered ones", n)
		}
	}
	b.snd.fail = nil
	if err := b.r.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := b.snd.events()
	if len(got) != 12 || len(a.spool.Files()) != 0 {
		t.Fatalf("delivered %d, files %d", len(got), len(a.spool.Files()))
	}
	for i, e := range got {
		if !e.At.Equal(ev(i).At) {
			t.Fatalf("event %d has time %v, want its original %v", i, e.At, ev(i).At)
		}
	}
	reported := 0
	for _, batch := range b.snd.all() {
		reported += batch.Dropped
	}
	if reported != 3 {
		t.Errorf("the drop count of the spooled buffer arrived as %d, want 3", reported)
	}
	if maintest.Value(b.m.AuditEvents.WithLabelValues("sent")) != 12 {
		t.Errorf("sent = %v", maintest.Value(b.m.AuditEvents.WithLabelValues("sent")))
	}
}

// TestR_BAS_039_ASpoolFileIsDeliveredByOneReplicaWhenAClaimIsAvailable: a claim another replica holds leaves the
// file alone; a corrupt line and a refused batch are skipped, the rest is delivered, and the file goes.
func TestR_BAS_039_ASpoolFileIsDeliveredByOneReplicaWhenAClaimIsAvailable(t *testing.T) {
	spool := maintest.NewMemSpool()
	line, _ := iface.Marshal(iface.DownloadsRequest{Events: []iface.DownloadEvent{ev(1), ev(2)}})
	data := append([]byte("{not json}\n"), line...)
	data = append(data, '\n')
	if err := spool.SpoolWrite(context.Background(), "gone", data); err != nil {
		t.Fatal(err)
	}
	var held atomic.Bool
	released := 0
	claim := func(context.Context, string) (bool, func(), error) {
		if !held.Load() {
			return false, func() {}, nil
		}
		return true, func() { released++ }, nil
	}
	e := newEnv(t, func(_ *config.BastionAudit, o *downloads.ReporterOptions) { o.Spool, o.Claim = spool, claim })
	if err := e.r.Adopt(context.Background()); err != nil || len(e.snd.events()) != 0 || len(spool.Files()) != 1 {
		t.Fatalf("with the claim held elsewhere: %v, %d files", err, len(spool.Files()))
	}
	held.Store(true)
	if err := e.r.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.snd.events()) != 2 || len(spool.Files()) != 0 || released != 1 {
		t.Errorf("sent %d, files %d, released %d", len(e.snd.events()), len(spool.Files()), released)
	}
	// A claim that errors (Valkey unavailable) only means the replica delivers without it.
	_ = spool.SpoolWrite(context.Background(), "gone", line)
	e2 := newEnv(t, func(_ *config.BastionAudit, o *downloads.ReporterOptions) {
		o.Spool = spool
		o.Claim = func(context.Context, string) (bool, func(), error) { return false, nil, errors.New("valkey down") }
	})
	if err := e2.r.Adopt(context.Background()); err != nil || len(e2.snd.events()) != 2 {
		t.Errorf("without a claim: %v, %d events", err, len(e2.snd.events()))
	}
}

// TestR_BAS_039_TheRunLoopAdoptsTheSpoolOnItsOwn: a running replica picks up a file written by another.
func TestR_BAS_039_TheRunLoopAdoptsTheSpoolOnItsOwn(t *testing.T) {
	spool := maintest.NewMemSpool()
	line, _ := iface.Marshal(iface.DownloadsRequest{Events: []iface.DownloadEvent{ev(7)}})
	_ = spool.SpoolWrite(context.Background(), "other", append(line, '\n'))
	e := newEnv(t, func(_ *config.BastionAudit, o *downloads.ReporterOptions) { o.Spool = spool })
	e.run(t)
	eventually(t, "the file is delivered", func() bool { return len(e.snd.events()) == 1 && len(spool.Files()) == 0 })
}

// TestR_BAS_039_EventsArriveAtTheMainInstallationWithTheirOriginalTimeAfterACut: through the real client and the
// fake main installation: the link is cut, events pile up, the cut ends, and every event arrives in order with
// its time, in batches of at most batch_size.
func TestR_BAS_039_EventsArriveAtTheMainInstallationWithTheirOriginalTimeAfterACut(t *testing.T) {
	main := maintest.New(t)
	m, _ := metrics.New(nil)
	tok, _ := mainclient.ParseToken(main.NewToken())
	faulty := &maintest.Faulty{}
	client, err := mainclient.New(mainclient.Options{
		URL: main.URL(), BaseURL: "https://packages.example.com", Replica: "r", Release: "1", Tokens: mainclient.NewHolder(tok),
		ConnectTimeout: time.Second, ResponseHeaderTimeout: time.Second, CAFile: main.CAFile(t.TempDir()), Metrics: m,
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper { faulty.Next = rt; return faulty },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	var reachable atomic.Bool
	r := downloads.NewReporter(downloads.ReporterOptions{
		Config:  config.BastionAudit{BufferEvents: 1000, BatchSize: 50, FlushInterval: 20 * time.Millisecond, SpoolMaxEvents: 1000},
		Replica: "r", Sender: client, Reachable: reachable.Load, Metrics: m,
	})
	r.SetEnabled(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	faulty.SetDown(true)
	for i := 0; i < 120; i++ {
		r.Add(ev(i))
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(main.DownloadEvents()); n != 0 {
		t.Fatalf("%d events arrived during the cut", n)
	}
	faulty.SetDown(false)
	reachable.Store(true)
	eventually(t, "all events", func() bool { return len(main.DownloadEvents()) == 120 })
	for i, got := range main.DownloadEvents() {
		if !got.At.Equal(ev(i).At) || got.Path != ev(i).Path {
			t.Fatalf("event %d: %+v", i, got)
		}
	}
	for _, b := range main.Downloads() {
		if len(b.Events) > 50 {
			t.Errorf("a batch of %d", len(b.Events))
		}
	}
	// A cut that the link has not noticed yet: the send fails, the events stay, the next interval sends them.
	faulty.FailNext(2)
	r.Add(ev(500))
	eventually(t, "the event after failed sends", func() bool { return len(main.DownloadEvents()) == 121 })
}
