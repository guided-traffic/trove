//go:build integration

package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
)

// TestR_REPO_055_AClientNeverWaitsUnderTheBackgroundRulesOfAFlightItJoined: a background request that may not run yet (the yield window of
// the signal is open) holds neither the flight nor the cross-replica lock, so a client that asks for the same item fetches for itself
// at once (Q-REPO-179).
func TestR_REPO_055_AClientNeverWaitsUnderTheBackgroundRulesOfAFlightItJoined(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/p/-/p-1.tgz", proxytest.Route{Body: []byte("p-body"), Type: "application/octet-stream"})
	rem := e.remote("joinq", up)
	r := e.newReplica("a", replicaOpts{signals: true})
	r.signals.Publish(cluster.AreaMirrorYield, strconv.FormatInt(rem.ID(), 10)) // the window of 30 s is open on this replica

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bg := make(chan error, 1)
	go func() {
		_, err := r.engine.Fill(ctx, r.bg(rem, "pkg/p/-/p-1.tgz", proxy.Artifact, 2, "job-1"))
		bg <- err
	}()
	time.Sleep(300 * time.Millisecond) // the background fetch waits for the window to end

	got := make(chan served, 1)
	go func() { got <- r.artifact(rem, "pkg/p/-/p-1.tgz") }()
	select {
	case s := <-got:
		if s.status != 200 || string(s.body) != "p-body" {
			t.Fatalf("%d %s", s.status, s.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a client that asked for the item of a waiting background request waited under the background rules")
	}
	if n := up.Count("GET", "/pkg/p/-/p-1.tgz"); n != 1 {
		t.Fatalf("the item was fetched %d times", n)
	}
	cancel()
	select {
	case err := <-bg:
		if err == nil {
			t.Fatal("the background request ran inside the window")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the background request did not end with the context of its pass")
	}
	if n := up.Count("GET", "/pkg/p/-/p-1.tgz"); n != 1 {
		t.Fatalf("the cancelled pass fetched on: %d requests", n)
	}
}

// TestR_REPO_054_ABackgroundRequestThatJoinedAClientsFlightReadsAThrottlingAnswerAsAWait: the client's 429 opens the circuit (R-REPO-029)
// and the pass gets a wait, or its own fetch's rate limit, and not a failure of the item with the client's classification
// (Q-REPO-180).
func TestR_REPO_054_ABackgroundRequestThatJoinedAClientsFlightReadsAThrottlingAnswerAsAWait(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/hot/-/hot-1.tgz", proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}})
	rem := e.remote("joinhot", up)
	r := e.newReplica("a", replicaOpts{})

	client := make(chan served, 1)
	go func() { client <- r.artifact(rem, "pkg/hot/-/hot-1.tgz") }()
	waitFor(t, "the client's fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/hot/-/hot-1.tgz") == 1 })

	_, err := r.fill(rem, "pkg/hot/-/hot-1.tgz", 2) // joins the client's flight
	var wait *proxy.BackgroundWait
	_, limited := discover.AsRateLimited(err)
	if !errors.As(err, &wait) && !limited {
		t.Fatalf("a throttling answer of a client's flight was read as %v, want a wait or a rate limit", err)
	}
	if s := <-client; s.status != 503 {
		t.Fatalf("the client: %d", s.status)
	}
}
