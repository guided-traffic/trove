package link_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/link"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

const baseURL = "https://packages.example.com"

// clock is a settable clock shared by the link and the fake main installation, so that the entries' times, the
// cursors and the validity rules all read the same time.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	real bool
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	if c.real {
		return time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type logSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *logSink) count(sub string) int {
	return strings.Count(s.String(), sub)
}

// recSink records what the link asks the request flow to delete.
type recSink struct {
	mu      sync.Mutex
	dropped []iface.FeedEntry
	all     int
	calls   int
}

func (r *recSink) Drop(es []iface.FeedEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped = append(r.dropped, es...)
	r.calls++
}

func (r *recSink) DropAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all++
}

func (r *recSink) entries() []iface.FeedEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]iface.FeedEntry(nil), r.dropped...)
}

type rig struct {
	t      *testing.T
	main   *maintest.Main
	m      *metrics.Metrics
	c      *mainclient.HTTPClient
	faulty *maintest.Faulty
	l      *link.Link
	clock  *clock
	logs   *logSink
	sink   *recSink
	token  string
	holder *mainclient.Holder
	states []link.State
	smu    sync.Mutex
}

type rigOpts struct {
	main    *maintest.Main
	clock   *clock
	mutate  func(*link.Config)
	timeout time.Duration
	expires time.Duration
	// realTime runs the link on the real clock, for the tests that poll in the background.
	realTime bool
	// clientBase is the base URL the client sends; the default is baseURL.
	clientBase string
}

// newRig builds a replica against a fake main installation and returns it before its first tick.
func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, clock: o.clock, logs: &logSink{}, sink: &recSink{}, faulty: &maintest.Faulty{}}
	if r.clock == nil {
		r.clock = newClock()
		if o.realTime {
			r.clock = &clock{real: true}
		}
	}
	r.main = o.main
	if r.main == nil {
		r.main = maintest.New(t)
		if !o.realTime {
			r.main.SetClock(r.clock.Now)
		}
	}
	if o.clientBase == "" {
		o.clientBase = baseURL
	}
	var err error
	if r.m, err = metrics.New(nil); err != nil {
		t.Fatal(err)
	}
	if o.expires == 0 {
		o.expires = 365 * 24 * time.Hour
	}
	r.token = r.main.NewTokenExpiring(r.clock.Now().Add(o.expires))
	tok, err := mainclient.ParseToken(r.token)
	if err != nil {
		t.Fatal(err)
	}
	r.holder = mainclient.NewHolder(tok)
	if o.timeout == 0 {
		o.timeout = 2 * time.Second
	}
	r.c, err = mainclient.New(mainclient.Options{
		URL: r.main.URL(), BaseURL: o.clientBase, Replica: "replica-" + t.Name(), Release: "1.2.3", Tokens: r.holder,
		ConnectTimeout: o.timeout, ResponseHeaderTimeout: o.timeout, CAFile: r.main.CAFile(t.TempDir()), Metrics: r.m,
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper { r.faulty.Next = rt; return r.faulty },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.c.Close)
	cfg := link.Config{
		BaseURL: baseURL, HandshakeInterval: time.Minute, PollInterval: 2 * time.Second, UnreachableAfter: 10 * time.Second,
		AnswerPeriod: 5 * time.Minute, GracePeriod: 15 * time.Minute, WarnBefore: 720 * time.Hour,
	}
	if o.mutate != nil {
		o.mutate(&cfg)
	}
	r.l = link.New(link.Options{
		Client: r.c, Config: cfg, Feed: answers.NewFeedLog(), Metrics: r.m, Log: slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Tokens: r.holder, Sink: r.sink, Now: r.clock.Now,
	})
	r.l.Watch(func(st link.State) {
		r.smu.Lock()
		defer r.smu.Unlock()
		r.states = append(r.states, st)
	})
	return r
}

func (r *rig) tick() { r.l.Tick(context.Background()) }

// tickAfter advances the clock and ticks once.
func (r *rig) tickAfter(d time.Duration) {
	r.clock.Advance(d)
	r.tick()
}

// connect ticks until the replica is reachable; it fails if one tick does not do it.
func (r *rig) connect() {
	r.t.Helper()
	r.tick()
	if !r.l.State().Reachable {
		r.t.Fatalf("not reachable after the first tick:\n%s", r.logs.String())
	}
}

func (r *rig) gauge(name string) float64 {
	r.t.Helper()
	switch name {
	case "reachable":
		return maintest.Value(r.m.MainReachable)
	case "since":
		return maintest.Value(r.m.MainUnreachableSince)
	case "refused":
		return maintest.Value(r.m.MainTokenRefused)
	case "mismatch":
		return maintest.Value(r.m.InterfaceMismatch)
	case "baseurl":
		return maintest.Value(r.m.BaseURLMismatch)
	case "lag":
		return maintest.Value(r.m.FeedLag)
	case "remaining":
		return maintest.Value(r.m.TokenRemaining)
	case "resets":
		return maintest.Value(r.m.FeedResets)
	}
	r.t.Fatalf("unknown gauge %s", name)
	return 0
}

func (r *rig) applied(kind string) float64 {
	return maintest.Value(r.m.FeedApplied.WithLabelValues(kind))
}

func (r *rig) answer(o time.Time, tokenID string) answers.Answer {
	return answers.Answer{Kind: answers.Credential, ObtainedAt: o, TokenID: tokenID}
}

// check judges an answer at the clock's time against the link's state.
func (r *rig) check(a answers.Answer) answers.Reason {
	return r.l.AnswerState(r.clock.Now()).Check(a)
}

func (r *rig) newToken() {
	tok, err := mainclient.ParseToken(r.main.NewTokenExpiring(r.clock.Now().Add(365 * 24 * time.Hour)))
	if err != nil {
		r.t.Fatal(err)
	}
	r.holder.Set(tok)
}
