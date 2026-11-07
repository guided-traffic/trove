package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

// fakeClock is the clock of the yield window.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)} }

func preempted(ctx context.Context) bool { return errors.Is(context.Cause(ctx), ErrPreempted) }

func TestPreemption(t *testing.T) {
	ctx := context.Background()
	t.Run("R-REPO-055 a client fetch that finds no free slot pre-empts the youngest background fetch and takes its slot at once", func(t *testing.T) {
		s := newSlots(2, nil) // maxConcurrentFetches 2: B = 1
		bctx, rel, err := s.acquireBackground(ctx, 1, 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		first, _, err := s.acquire(ctx, 1, 2, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		second, wait, err := s.acquire(ctx, 1, 2, time.Second)
		if err != nil {
			t.Fatalf("the second client fetch found no slot: %v", err)
		}
		if wait > 50*time.Millisecond || time.Since(start) > 50*time.Millisecond {
			t.Fatalf("the second client fetch waited %v", wait)
		}
		if !preempted(bctx) {
			t.Fatalf("the background fetch was not pre-empted: %v", context.Cause(bctx))
		}
		if s.background(1) != 0 || s.held() != 2 {
			t.Fatalf("background %d, held %d", s.background(1), s.held())
		}
		rel() // the pre-empted fetch unwinds: its release gives nothing back a second time
		if s.held() != 2 {
			t.Fatalf("the release of a pre-empted fetch changed the count: held %d", s.held())
		}
		first()
		second()
		if s.held() != 0 {
			t.Fatalf("held = %d", s.held())
		}
	})

	t.Run("R-REPO-055 only the youngest background fetch of the repository is pre-empted", func(t *testing.T) {
		s := newSlots(10, nil)
		old, relOld, _ := s.acquireBackground(ctx, 1, 4, 2)
		young, relYoung, _ := s.acquireBackground(ctx, 1, 4, 2)
		c1, _, _ := s.acquire(ctx, 1, 4, time.Second)
		c2, _, _ := s.acquire(ctx, 1, 4, time.Second)
		c3, _, err := s.acquire(ctx, 1, 4, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if !preempted(young) || preempted(old) {
			t.Fatalf("young pre-empted %v, old pre-empted %v", preempted(young), preempted(old))
		}
		for _, r := range []func(){relOld, relYoung, c1, c2, c3} {
			r()
		}
	})

	t.Run("R-REPO-055 a background fetch that a follower of the replica joined is not pre-empted", func(t *testing.T) {
		s := newSlots(2, nil)
		var joined atomic.Bool
		bctx, rel, _ := s.acquireBackgroundJoined(ctx, 1, 2, 1, joined.Load)
		first, _, _ := s.acquire(ctx, 1, 2, time.Second)
		joined.Store(true)
		if _, _, err := s.acquire(ctx, 1, 2, 100*time.Millisecond); err == nil {
			t.Fatal("a client fetch took the slot of a joined background fetch")
		} else if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeFetchQueueTimeout {
			t.Fatalf("%v", err)
		}
		if preempted(bctx) {
			t.Fatal("a joined background fetch was pre-empted")
		}
		first()
		rel()
	})

	t.Run("R-REPO-055 when only the replica's limit is exhausted, the youngest background fetch of any repository is pre-empted", func(t *testing.T) {
		s := newSlots(2, nil)
		bctx, rel, _ := s.acquireBackground(ctx, 2, 64, 1) // another repository's background fetch
		other, _, _ := s.acquire(ctx, 3, 64, time.Second)
		got, _, err := s.acquire(ctx, 1, 64, time.Second)
		if err != nil {
			t.Fatalf("the client fetch found no slot: %v", err)
		}
		if !preempted(bctx) {
			t.Fatal("the other repository's background fetch was not pre-empted")
		}
		rel()
		other()
		got()
	})

	t.Run("R-REPO-055 a client fetch does not pre-empt for a share another repository's background fetch does not hold", func(t *testing.T) {
		s := newSlots(10, nil)
		bctx, rel, _ := s.acquireBackground(ctx, 2, 1, 1)
		c1, _, _ := s.acquire(ctx, 1, 1, time.Second) // the repository's own share is spent by clients
		if _, _, err := s.acquire(ctx, 1, 1, 100*time.Millisecond); err == nil {
			t.Fatal("a client fetch got a slot above the repository's share")
		}
		if preempted(bctx) {
			t.Fatal("a background fetch of another repository was pre-empted for a limit that is not the replica's")
		}
		rel()
		c1()
	})

	t.Run("R-REPO-055 a background fetch starts only while no client fetch of the repository waits for a slot", func(t *testing.T) {
		s := newSlots(1, nil)
		c1, _, _ := s.acquire(ctx, 1, 8, time.Second)
		waiter := make(chan struct{})
		go func() {
			rel, _, err := s.acquire(ctx, 1, 8, 2*time.Second)
			if err == nil {
				close(waiter)
				time.Sleep(100 * time.Millisecond)
				rel()
			}
		}()
		time.Sleep(50 * time.Millisecond) // the client fetch queues
		started := make(chan struct{})
		go func() {
			_, rel, err := s.acquireBackground(ctx, 1, 8, 1)
			if err == nil {
				close(started)
				rel()
			}
		}()
		c1() // a slot is free: the waiting client fetch takes it, the background fetch must not
		<-waiter
		select {
		case <-started:
			t.Fatal("a background fetch started while a client fetch of its repository held the slot it waited for")
		case <-time.After(50 * time.Millisecond):
		}
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("the background fetch did not start after the client fetch ended")
		}
	})

	t.Run("R-REPO-055 a client fetch that has to wait tells the engine, one that pre-empts does not", func(t *testing.T) {
		s := newSlots(1, nil)
		var told []int64
		var mu sync.Mutex
		s.onWait = func(id int64) { mu.Lock(); told = append(told, id); mu.Unlock() }
		_, relBg, _ := s.acquireBackground(ctx, 7, 8, 1)
		c, _, err := s.acquire(ctx, 7, 8, time.Second) // pre-empts
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		n := len(told)
		mu.Unlock()
		if n != 0 {
			t.Fatalf("a pre-empting client fetch raised the signal: %v", told)
		}
		if _, _, err := s.acquire(ctx, 7, 8, 50*time.Millisecond); err == nil { // has to wait
			t.Fatal("a second slot appeared")
		}
		mu.Lock()
		defer mu.Unlock()
		if len(told) != 1 || told[0] != 7 {
			t.Fatalf("told = %v", told)
		}
		relBg()
		c()
	})
}

func TestYield(t *testing.T) {
	ctx := context.Background()
	t.Run("R-REPO-055 the signal pre-empts every background fetch of the repository that no client joined and starts none for 30 s", func(t *testing.T) {
		clk := newFakeClock()
		s := newSlots(10, nil)
		s.now = clk.now
		a, relA, _ := s.acquireBackground(ctx, 1, 8, 3)
		b, relB, _ := s.acquireBackground(ctx, 1, 8, 3)
		joinedCtx, relJ, _ := s.acquireBackgroundJoined(ctx, 1, 8, 3, func() bool { return true })
		other, relO, _ := s.acquireBackground(ctx, 2, 8, 3)

		if n := s.yield(1); n != 2 {
			t.Fatalf("yield pre-empted %d fetches, want 2", n)
		}
		if !preempted(a) || !preempted(b) || preempted(joinedCtx) || preempted(other) {
			t.Fatalf("a %v b %v joined %v other %v", preempted(a), preempted(b), preempted(joinedCtx), preempted(other))
		}
		relA()
		relB()

		start := func() chan struct{} {
			ch := make(chan struct{})
			go func() {
				c, rel, err := s.acquireBackground(ctx, 1, 8, 3)
				if err == nil {
					_ = c
					close(ch)
					rel()
				}
			}()
			return ch
		}
		blocked := start()
		clk.advance(29 * time.Second)
		select {
		case <-blocked:
			t.Fatal("a background fetch started 29 s after the signal")
		case <-time.After(600 * time.Millisecond): // more than two polls of the waiting fetch
		}
		// Another repository is not held back.
		if _, rel, err := s.acquireBackground(ctx, 3, 8, 1); err != nil {
			t.Fatal(err)
		} else {
			rel()
		}
		clk.advance(2 * time.Second) // 31 s after the signal
		select {
		case <-blocked:
		case <-time.After(3 * time.Second):
			t.Fatal("no background fetch started 31 s after the signal")
		}
		relJ()
		relO()
	})

	t.Run("R-REPO-055 every signal moves the window: 30 s count from the last one", func(t *testing.T) {
		clk := newFakeClock()
		s := newSlots(10, nil)
		s.now = clk.now
		s.yield(1)
		clk.advance(20 * time.Second)
		s.yield(1)
		clk.advance(20 * time.Second) // 40 s after the first, 20 s after the last
		short, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		if _, _, err := s.acquireBackground(short, 1, 8, 1); err == nil {
			t.Fatal("a background fetch started within 30 s of the last signal")
		}
		clk.advance(11 * time.Second)
		if _, rel, err := s.acquireBackground(ctx, 1, 8, 1); err != nil {
			t.Fatal(err)
		} else {
			rel()
		}
	})

	t.Run("R-REPO-055 a signal that names no repository counts for all", func(t *testing.T) {
		clk := newFakeClock()
		s := newSlots(10, nil)
		s.now = clk.now
		c, rel, _ := s.acquireBackground(ctx, 5, 8, 1)
		s.yieldAll()
		if !preempted(c) {
			t.Fatal("a coarse signal did not pre-empt")
		}
		rel()
		short, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		if _, _, err := s.acquireBackground(short, 6, 8, 1); err == nil {
			t.Fatal("a background fetch started in the window of a coarse signal")
		}
	})
}

// fakeSignals records what the engine publishes and hands its handler out.
type fakeSignals struct {
	mu        sync.Mutex
	published []string
	handler   cluster.SignalHandler
	subArea   string
}

func (f *fakeSignals) Publish(area, payload string) {
	f.mu.Lock()
	f.published = append(f.published, area+":"+payload)
	f.mu.Unlock()
}

func (f *fakeSignals) Subscribe(_ context.Context, area string, h cluster.SignalHandler) (func(), error) {
	f.mu.Lock()
	f.handler, f.subArea = h, area
	f.mu.Unlock()
	return func() {}, nil
}

func TestYieldSignal(t *testing.T) {
	clk := newFakeClock()
	sig := &fakeSignals{}
	e := &Engine{o: Options{Signals: sig, Now: clk.now}, throttled: map[string]time.Time{}, slots: newSlots(10, nil)}
	e.slots.now = clk.now
	e.slots.onWait = e.raiseYield
	e.base = context.Background()

	t.Run("R-REPO-055 a client fetch that waits raises mirror_yield with the repository id, at most once a second per repository", func(t *testing.T) {
		e.raiseYield(7)
		e.raiseYield(7)
		clk.advance(900 * time.Millisecond)
		e.raiseYield(7)
		e.raiseYield(8) // another repository has its own once a second
		clk.advance(200 * time.Millisecond)
		e.raiseYield(7)
		want := []string{"mirror_yield:7", "mirror_yield:8", "mirror_yield:7"}
		sig.mu.Lock()
		defer sig.mu.Unlock()
		if len(sig.published) != len(want) {
			t.Fatalf("published %v, want %v", sig.published, want)
		}
		for i := range want {
			if sig.published[i] != want[i] {
				t.Fatalf("published %v, want %v", sig.published, want)
			}
		}
	})

	t.Run("R-REPO-055 the engine listens to mirror_yield and pre-empts on a signal for a repository it runs background fetches of", func(t *testing.T) {
		e.listenYield()
		if sig.subArea != cluster.AreaMirrorYield || sig.handler == nil {
			t.Fatalf("subscribed to %q", sig.subArea)
		}
		bctx, rel, _ := e.slots.acquireBackground(context.Background(), 11, 8, 2)
		defer rel()
		sig.handler([]string{"12", "not a number"}, false)
		if preempted(bctx) {
			t.Fatal("a signal for another repository pre-empted the fetch")
		}
		sig.handler([]string{strconv.Itoa(11)}, false)
		if !preempted(bctx) {
			t.Fatal("the signal for the repository did not pre-empt the fetch")
		}
	})

	t.Run("R-REPO-055 a replica without signals raises none and subscribes to none", func(t *testing.T) {
		none := &Engine{o: Options{Now: clk.now}, throttled: map[string]time.Time{}}
		none.raiseYield(1)
		none.listenYield()
		if none.stopYield != nil {
			t.Fatal("subscribed without signals")
		}
	})
}

func TestBackgroundClassification(t *testing.T) {
	f := &fetcher{e: &Engine{o: Options{Now: time.Now}}}
	f.rq.Remote.Repo.Name = qr("r")
	f.rq.Priority = PriorityBackground
	resp := func(status int, body string, h ...string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}, Body: http.NoBody}
		if body != "" {
			r.Body = readCloser(body)
		}
		for i := 0; i+1 < len(h); i += 2 {
			r.Header.Set(h[i], h[i+1])
		}
		return r
	}
	slowDown := "<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>"
	rem := Remote{Repo: f.rq.Remote.Repo}
	rem.Settings.Availability.CircuitOpenSeconds = 300
	for name, tc := range map[string]struct {
		status int
		body   string
		header []string
		reason string
		wait   time.Duration
	}{
		"429 with Retry-After":                {429, "", []string{"Retry-After", "7"}, upstream.ReasonTooManyRequests, 7 * time.Second},
		"503 SlowDown without a hint":         {503, slowDown, nil, upstream.ReasonSlowDown, 0},
		"503 with Retry-After":                {503, "", []string{"Retry-After", "3"}, upstream.ReasonRetryAfter, 3 * time.Second},
		"403 with X-RateLimit-Remaining zero": {403, "", []string{"X-RateLimit-Remaining", "0"}, upstream.ReasonQuotaHeader, 0},
	} {
		t.Run("R-REPO-054 a background request's answer is a rate limit: "+name, func(t *testing.T) {
			r := resp(tc.status, tc.body, tc.header...)
			defer func() { _ = r.Body.Close() }()
			res, ok := f.rateLimited(r)
			if !ok || res.err != nil || res.usable() {
				t.Fatalf("ok %v, result %+v", ok, res)
			}
			var rl *upstream.RateLimitError
			if !errors.As(res.own, &rl) || rl.Reason != tc.reason || rl.Wait != tc.wait {
				t.Fatalf("%#v", res.own)
			}
			if rl.RateLimitReason() != tc.reason {
				t.Fatal("the error does not satisfy discover.RateLimited")
			}
			if !backgroundOutcome(res.own) {
				t.Fatal("a rate limit is not an outcome of the background request")
			}
			// The same answer for a client keeps the classification of R-REPO-029 and counts toward the circuit
			// (a 429 or a 503) or is an authentication failure (a 403).
			c := classifyStatus(rem, r)
			if c.err == nil || c.err.Code == "" {
				t.Fatalf("client classification: %+v", c)
			}
		})
	}
	t.Run("R-REPO-029 R-REPO-054 a client's 503 SlowDown stays upstream_error and counts toward the circuit", func(t *testing.T) {
		r := resp(503, slowDown)
		defer func() { _ = r.Body.Close() }()
		c := classifyStatus(rem, r)
		if c.err.Code != errs.CodeUpstreamError || !c.count {
			t.Fatalf("%s count=%v", c.err.Code, c.count)
		}
	})
	t.Run("R-REPO-054 a plain 503, a 500 and a 404 are no rate limit for a background request", func(t *testing.T) {
		for _, s := range []int{500, 503, 502, 404, 401} {
			r := resp(s, "")
			_, ok := f.rateLimited(r)
			_ = r.Body.Close()
			if ok {
				t.Errorf("%d is a rate limit", s)
			}
		}
	})
	t.Run("R-REPO-054 the outcomes of a background request are its own: a client that joined the flight finds the result empty", func(t *testing.T) {
		for _, err := range []error{ErrPreempted, circuitWait(time.Second), &BackgroundWait{Reason: WaitSpoolBusy, After: 5 * time.Second}, &upstream.RateLimitError{}} {
			r := resultOf(err)
			if r.err != nil || r.own == nil || r.usable() {
				t.Errorf("%v: %+v", err, r)
			}
		}
		if r := resultOf(errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "x")); r.err == nil || r.own != nil || !r.usable() {
			t.Errorf("an upstream error is the flight's: %+v", r)
		}
	})
	t.Run("R-REPO-054 the wait for an open circuit lasts until its next state change, at least 1 s and at most 5 s", func(t *testing.T) {
		for in, want := range map[time.Duration]time.Duration{0: time.Second, 300 * time.Millisecond: time.Second, 3 * time.Second: 3 * time.Second, 290 * time.Second: 5 * time.Second} {
			w := circuitWait(in)
			if w.After != want || w.Reason != WaitCircuitOpen {
				t.Errorf("circuitWait(%v) = %+v, want %v", in, w, want)
			}
		}
		var w *BackgroundWait
		if !errors.As(circuitOpenError(Request{Priority: PriorityBackground}, time.Minute), &w) {
			t.Error("a background request that finds the circuit open gets no BackgroundWait")
		}
		if ee, ok := errs.As(circuitOpenError(Request{}, time.Minute)); !ok || ee.Code != errs.CodeUpstreamAssumedOffline {
			t.Error("a client's answer to an open circuit changed")
		}
	})
}

func readCloser(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
