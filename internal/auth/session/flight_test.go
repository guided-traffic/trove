package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
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

type freshGate struct{}

func (freshGate) FreshWithin(time.Duration) bool { return true }

// TestR_AUTH_010_PeekChangesNothing: Peek answers from the cache like Validate but
// never touches the session or ends one. The store has no database, so a touch or
// an end would panic.
func TestR_AUTH_010_PeekChangesNothing(t *testing.T) {
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{flights: map[string]*flight{}, o: Options{RevocationBound: time.Minute, Fresh: freshGate{}}}
	s.cache = registerCache(reg, time.Minute, 10)
	secret := NewSecret()
	now := time.Now()
	live := func(breakGlass bool) *entry {
		return &entry{
			id: "s", principalID: "u", principalKind: "user", principalStatus: "active", breakGlass: breakGlass,
			absDeadline: now.Add(time.Hour), idle: time.Hour, lastSeen: now.Add(-30 * time.Minute), // far past touchEvery
		}
	}
	s.cache.Set(string(Hash(secret)), live(false))
	if info, err := s.Peek(context.Background(), secret); err != nil || info.ID != "s" {
		t.Fatalf("a live session: %+v %v", info, err)
	}
	if got := s.cache.Len(); got != 1 {
		t.Fatalf("cache entries = %d", got)
	}
	// A break-glass session on a replica with the account disabled is invalid, and nothing is ended.
	s.cache.Set(string(Hash(secret)), live(true))
	if _, err := s.Peek(context.Background(), secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a break-glass session of a disabled account: %v", err)
	}
	if _, err := s.Peek(context.Background(), "not-a-secret"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a malformed cookie: %v", err)
	}
}

// TestR_AUTH_023_TheStarterOfASharedLookupAlsoRespectsItsOwnContext: the request
// that starts the lookup stops waiting when its context ends, as the requests
// that join it do; the query keeps its own bound and finishes for the others.
func TestR_AUTH_023_TheStarterOfASharedLookupAlsoRespectsItsOwnContext(t *testing.T) {
	s := &Store{flights: map[string]*flight{}, o: Options{RevocationBound: time.Minute}}
	release := make(chan struct{})
	defer close(release)
	fill := func(context.Context, string) (*entry, error) {
		<-release
		return &entry{id: "s"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.sharedFill(ctx, "k", fill); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the starter was answered although the lookup had not finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the starter of a shared lookup waits for the query although its own context ended")
	}
}
