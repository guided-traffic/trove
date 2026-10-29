package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestR_AUTH_023_ASharedLookupDoesNotHandOneRequestsCancellationToTheOthers: the
// request that started a shared lookup is cancelled while another one waits for
// it; the waiting request gets the entry, not the cancellation (R-AUTH-023).
func TestR_AUTH_023_ASharedLookupDoesNotHandOneRequestsCancellationToTheOthers(t *testing.T) {
	s := &Store{flights: map[string]*flight{}, o: Options{RevocationBound: time.Minute}}
	started, release := make(chan struct{}), make(chan struct{})
	fill := func(ctx context.Context, _ string) (*entry, error) {
		close(started)
		<-release
		return &entry{id: "s"}, ctx.Err()
	}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var follower error
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = s.sharedFill(leaderCtx, "k", fill) }()
	<-started
	go func() {
		defer wg.Done()
		_, follower = s.sharedFill(context.Background(), "k", func(context.Context, string) (*entry, error) {
			return &entry{id: "own"}, nil // only if the flight had ended before it joined
		})
	}()
	time.Sleep(50 * time.Millisecond) // the follower is waiting on the flight
	cancelLeader()
	close(release)
	wg.Wait()
	if follower != nil {
		t.Fatalf("the follower failed with the leader's cancellation: %v", follower)
	}
}
