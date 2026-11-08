//go:build integration

package proxy

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
)

// The shutdown cancels a fetch wherever its flight stands (F-79): a request that waited for another replica's holder, or that was
// in the flight's checks, answers shutting_down, never the raw context error that a mirror pass records as a failure of the item
// with a retry a minute later.

// enteredFlights signals the first wait of the flight lock before it delegates.
type enteredFlights struct {
	cluster.FlightLock
	entered chan struct{}
	once    sync.Once
}

func (f *enteredFlights) Wait(ctx context.Context, k cluster.FlightKey) (cluster.Outcome, error) {
	f.once.Do(func() { close(f.entered) })
	return f.FlightLock.Wait(ctx, k)
}

type markKey struct{}

// blockingRunner blocks every transaction whose context carries the mark of a flight's fetch until that context ends.
type blockingRunner struct {
	meta.Runner
	entered chan struct{}
	once    sync.Once
}

func (r *blockingRunner) InTx(ctx context.Context, fn func(tx meta.Tx) error) error {
	if ctx.Value(markKey{}) != nil {
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		return ctx.Err()
	}
	return r.Runner.InTx(ctx, fn)
}

func isShuttingDownErr(err error) bool {
	ee, ok := errs.As(err)
	return ok && ee.Code == errs.CodeShuttingDown
}

// quiescedWaiter starts replica a's slow fetch of the path, then the request fn on replica b, which waits for a's flight; it
// returns once b has entered the wait, with b quiesced.
func quiescedWaiter(t *testing.T, name string, fn func(b *replica, rem proxy.Remote, path string)) (up *proxytest.Upstream, path string) {
	t.Helper()
	path = "pkg/q/-/q-1.tgz"
	e := newEnv(t)
	up = e.upstream()
	up.Set("/"+path, proxytest.Route{Body: bytes.Repeat([]byte("x"), 400_000), Type: "application/octet-stream", Chunk: 20_000, ChunkDelay: 300 * time.Millisecond})
	rem := e.remote(name, up)
	fl := &enteredFlights{entered: make(chan struct{})}
	a := e.newReplica("a", replicaOpts{})
	b := e.newReplica("b", replicaOpts{flights: func(l cluster.FlightLock) cluster.FlightLock { fl.FlightLock = l; return fl }})
	go a.artifact(rem, path)
	waitFor(t, "a's fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/"+path) == 1 })
	done := make(chan struct{})
	go func() { defer close(done); fn(b, rem, path) }()
	select {
	case <-fl.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("b did not wait for the holder's flight")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	b.engine.Quiesce(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("b's request did not end at Quiesce")
	}
	return up, path
}

func TestR_REPO_053_AFillThatWaitsForAnotherReplicasFlightAnswersShuttingDownAtQuiesce(t *testing.T) {
	var fillErr error
	up, path := quiescedWaiter(t, "quiesce-wait-fill", func(b *replica, rem proxy.Remote, path string) {
		_, fillErr = b.engine.Fill(context.Background(), b.bg(rem, path, proxy.Artifact, 2, "job-1"))
	})
	if !isShuttingDownErr(fillErr) {
		t.Fatalf("the fill answered %v, want code %s", fillErr, errs.CodeShuttingDown)
	}
	if n := up.Count("GET", "/"+path); n != 1 {
		t.Fatalf("the upstream saw %d GETs, want only the holder's", n)
	}
}

func TestR_REPO_026_AClientThatWaitsForAnotherReplicasFlightAnswers503AtQuiesce(t *testing.T) {
	var got served
	up, path := quiescedWaiter(t, "quiesce-wait-client", func(b *replica, rem proxy.Remote, path string) {
		got = b.artifact(rem, path)
	})
	if got.status != http.StatusServiceUnavailable || got.header.Get("Retry-After") != "1" {
		t.Fatalf("the client got %d with Retry-After %q (%v), want 503 with 1", got.status, got.header.Get("Retry-After"), got.err)
	}
	if n := up.Count("GET", "/"+path); n != 1 {
		t.Fatalf("the upstream saw %d GETs, want only the holder's", n)
	}
}

func TestR_REPO_053_AFillInTheChecksOfItsFlightAnswersShuttingDownAtQuiesce(t *testing.T) {
	path := "pkg/q/-/q-2.tgz"
	e := newEnv(t)
	up := e.upstream()
	up.Set("/"+path, proxytest.Route{Body: []byte("body"), Type: "application/octet-stream"})
	rem := e.remote("quiesce-checks", up)
	br := &blockingRunner{entered: make(chan struct{})}
	r := e.newReplica("a", replicaOpts{
		runner: func(m meta.Runner) meta.Runner { br.Runner = m; return br },
		detach: func(ctx context.Context) context.Context { return context.WithValue(ctx, markKey{}, true) },
	})
	errc := make(chan error, 1)
	go func() {
		_, err := r.engine.Fill(context.Background(), r.bg(rem, path, proxy.Artifact, 2, "job-1"))
		errc <- err
	}()
	select {
	case <-br.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the fill did not reach the checks of its flight")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	r.engine.Quiesce(ctx)
	cancel()
	select {
	case err := <-errc:
		if !isShuttingDownErr(err) {
			t.Fatalf("the fill answered %v, want code %s", err, errs.CodeShuttingDown)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the fill did not end at Quiesce")
	}
	if n := up.Count("GET", "/"+path); n != 0 {
		t.Fatalf("the upstream saw %d GETs, want none", n)
	}
}
