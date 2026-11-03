package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// gatedBody serves its bytes up to a point, waits for the gate and serves the rest.
type gatedBody struct {
	head, tail *bytes.Reader
	gate       chan struct{}
	closed     chan struct{}
	once       sync.Once
}

func newGatedBody(b []byte, at int) *gatedBody {
	return &gatedBody{head: bytes.NewReader(b[:at]), tail: bytes.NewReader(b[at:]), gate: make(chan struct{}), closed: make(chan struct{})}
}

func (g *gatedBody) Read(p []byte) (int, error) {
	if g.head.Len() > 0 {
		return g.head.Read(p)
	}
	select {
	case <-g.gate:
	case <-g.closed:
		return 0, io.ErrClosedPipe
	}
	return g.tail.Read(p)
}

func (g *gatedBody) Close() error { g.once.Do(func() { close(g.closed) }); return nil }

func eventuallyTrue(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestR_BAS_021_R_BAS_025_TheFillOutlivesTheClientThatStartedIt: the fill reads the main installation's body
// into the spool whatever the client does. A client that goes away mid-transfer leaves the entry committed and
// the budget slot freed (the body closed); the next request is a hit (Q-BAS-123).
func TestR_BAS_021_R_BAS_025_TheFillOutlivesTheClientThatStartedIt(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(300 << 10)
	gb := newGatedBody(body, 100<<10)
	started := make(chan struct{}, 1)
	viaSite := s.main.doFn
	s.site.artifacts[tgz] = body
	var once sync.Once
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		r, err := viaSite(f)
		if err == nil && r.Marking.Class == "artifact" && f.IfNoneMatch == "" {
			once.Do(func() { r.Body = gb; started <- struct{}{} })
		}
		return r, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "https://packages.example.com"+tgz, nil).WithContext(ctx)
	req.RemoteAddr = "203.0.113.7:4711"
	req.Header.Set("Authorization", bearer(s.full))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.e.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-started
	cancel() // the client goes away with 100 KiB of 300 read
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not end with its client")
	}
	if s.cache.entryCount() != 0 {
		t.Fatal("the entry exists before the body ended")
	}
	close(gb.gate)
	eventuallyTrue(t, 10*time.Second, "the fill to commit after its client left", func() bool { return s.cache.entryCount() == 1 })
	before := s.main.count()
	w := s.get(tgz, "Authorization", bearer(s.full))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("the next request: %d, %d bytes", w.Code, w.Body.Len())
	}
	if s.main.count() != before {
		t.Fatal("the next request went to the main installation: the fill was lost with its client")
	}
	select {
	case <-gb.closed:
	default:
		t.Fatal("the body of the fill, which holds the budget slot, was not closed")
	}
}

// TestR_BAS_025_AWaiterThatWaitsTooLongForTheLeaderAnswers503WithRetryAfter: a request that joined a fetch does
// not wait for ever behind it. After collapseWait it answers 503 bastion_busy with Retry-After: 1 and makes no
// request of its own; the leader is not affected (Q-BAS-123, security finding P3).
func TestR_BAS_025_AWaiterThatWaitsTooLongForTheLeaderAnswers503WithRetryAfter(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.e.collapseWait = 200 * time.Millisecond
	body := payload(2000)
	s.site.artifacts[tgz] = body
	gate := make(chan struct{})
	viaSite := s.main.doFn
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		<-gate
		return viaSite(f)
	}
	leader := make(chan *httptest.ResponseRecorder, 1)
	go func() { leader <- s.get(tgz, "Authorization", bearer(s.full)) }()
	eventuallyTrue(t, 5*time.Second, "the leader's request", func() bool { return s.main.count() == 1 })
	w := s.get(tgz, "Authorization", bearer(s.full))
	if w.Code != http.StatusServiceUnavailable || problemCode(w) != "bastion_busy" || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("the waiter: %d %s %v", w.Code, w.Body, w.Header())
	}
	if counterValue(t, s.harness, "refused", "busy") != 1 {
		t.Fatal("the refusal is not counted as busy")
	}
	close(gate)
	if lw := <-leader; lw.Code != 200 || !strings.HasPrefix(lw.Body.String(), string(body[:16])) {
		t.Fatalf("the leader: %d", lw.Code)
	}
	if n := s.main.count(); n != 1 {
		t.Fatalf("%d requests to the main installation, want 1", n)
	}
}
