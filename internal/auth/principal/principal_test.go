package principal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

func TestR_AUTH_016_UserNames(t *testing.T) {
	good := []string{"alice", "a", "alice.smith", "alice@example.com", "a+b", "0day", strings.Repeat("a", 128)}
	bad := []string{"", "Alice", "-alice", ".alice", "al ice", strings.Repeat("a", 129), "alice/bob"}
	for _, n := range good {
		if !ValidUserName(n, "breakglass") {
			t.Errorf("%q rejected", n)
		}
	}
	for _, n := range bad {
		if ValidUserName(n) {
			t.Errorf("%q accepted", n)
		}
	}
	for _, n := range []string{"anonymous", "authenticated", "system", "me", "breakglass"} {
		if ValidUserName(n, "breakglass") || !ReservedUserName(n, "breakglass") {
			t.Errorf("R-AUTH-007 %q is not reserved", n)
		}
	}
	if ReservedUserName("breakglass") {
		t.Error("the break-glass name is reserved only where it is configured")
	}
}

func TestR_AUTH_018_AndR_AUTH_020_Names(t *testing.T) {
	for n, want := range map[string]bool{"team-web": true, "a.b_c-d": true, "Team": false, "": false, "-x": false} {
		if ValidGroupName(n) != want {
			t.Errorf("group %q = %v", n, !want)
		}
	}
	for n, want := range map[string]bool{"ci-bot": true, "ab": true, "a": false, "ci_bot": false, "Ci-bot": false, strings.Repeat("a", 63): true, strings.Repeat("a", 64): false} {
		if ValidServiceAccountName(n) != want {
			t.Errorf("service account %q = %v", n, !want)
		}
	}
}

func TestNewIDIsUUIDv7(t *testing.T) {
	prev := ""
	for i := 0; i < 50; i++ {
		id := NewID()
		if !IsID(id) || id[14] != '7' {
			t.Fatalf("%s is not a UUIDv7", id)
		}
		if prev != "" && id[:8] < prev[:8] {
			t.Fatal("IDs do not sort by creation time")
		}
		prev = id
		time.Sleep(time.Millisecond)
	}
	if IsID("0192F0C4-7A7E-7B51-9B1E-2F0D6C9E1A10") || IsID("x") {
		t.Fatal("IsID accepts a non-canonical form")
	}
}

func TestR_AUTH_023_ChangePayloads(t *testing.T) {
	var sent []Change
	n := notifierFunc(func(area string, version int, payload any) error {
		if area != "auth" || version != ChangeVersion {
			t.Errorf("area %q version %d", area, version)
		}
		sent = append(sent, payload.(Change))
		return nil
	})
	if err := NotifyPrincipals(n, nil, ChangePrincipal, "a", "b"); err != nil {
		t.Fatal(err)
	}
	many := make([]string, maxChangeIDs+1)
	if err := NotifyPrincipals(n, nil, ChangeMembership, many...); err != nil {
		t.Fatal(err)
	}
	refs := make([]TokenRef, maxChangeIDs)
	if err := NotifyTokens(n, nil, refs...); err != nil {
		t.Fatal(err)
	}
	if err := NotifyTokens(n, nil, TokenRef{ID: "1", Hash: "ab"}); err != nil {
		t.Fatal(err)
	}
	if sent[0].Kind != ChangePrincipal || len(sent[0].IDs) != 2 {
		t.Errorf("%+v", sent[0])
	}
	if sent[1].Kind != ChangeAll || sent[2].Kind != ChangeAll {
		t.Error("a payload that would not fit becomes a flush of the whole area")
	}
	if sent[3].Kind != ChangeToken {
		t.Errorf("%+v", sent[3])
	}
	raw, _ := json.Marshal(sent[3])
	if c, ok := Decode(raw); !ok || c.Tokens[0].ID != "1" {
		t.Errorf("round trip: %+v %v", c, ok)
	}
	if _, ok := Decode(json.RawMessage(`{"x":1}`)); ok {
		t.Error("a payload without a kind decodes")
	}
	if len(raw) > 1024-96 {
		t.Errorf("a one-token payload takes %d bytes", len(raw))
	}
}

func TestR_AUTH_023_FlightsShareOnlyYoungLookups(t *testing.T) {
	var f Flights[string, int]
	var runs atomic.Int32
	gate := make(chan struct{})
	slow := func(context.Context) (int, error) {
		runs.Add(1)
		<-gate
		return 7, nil
	}
	var wg sync.WaitGroup
	results := make([]int, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := f.Do(context.Background(), "k", time.Minute, slow)
			if err != nil {
				t.Error(err)
			}
			results[i] = v
		}()
	}
	for runs.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("%d lookups for 5 concurrent callers", runs.Load())
	}
	for _, v := range results {
		if v != 7 {
			t.Fatalf("results %v", results)
		}
	}

	t.Run("R-AUTH-023 a lookup older than the bound is not joined", func(t *testing.T) {
		var g Flights[string, int]
		var n atomic.Int32
		release := make(chan struct{})
		first := func(context.Context) (int, error) { n.Add(1); <-release; return 1, nil }
		done := make(chan struct{})
		go func() { _, _ = g.Do(context.Background(), "k", 10*time.Millisecond, first); close(done) }()
		for n.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond) // the running lookup is now older than the bound
		v, err := g.Do(context.Background(), "k", 10*time.Millisecond, func(context.Context) (int, error) { n.Add(1); return 2, nil })
		close(release)
		<-done
		if err != nil || v != 2 || n.Load() != 2 {
			t.Fatalf("v=%d err=%v lookups=%d", v, err, n.Load())
		}
	})
	t.Run("a joined caller whose leader was cancelled runs again", func(t *testing.T) {
		var g Flights[string, int]
		leaderCtx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		leader := func(ctx context.Context) (int, error) { close(started); <-ctx.Done(); return 0, ctx.Err() }
		go func() { _, _ = g.Do(leaderCtx, "k", time.Minute, leader) }()
		<-started
		res := make(chan int, 1)
		go func() {
			v, _ := g.Do(context.Background(), "k", time.Minute, func(context.Context) (int, error) { return 9, nil })
			res <- v
		}()
		time.Sleep(20 * time.Millisecond)
		cancel()
		if v := <-res; v != 9 {
			t.Fatalf("got %d", v)
		}
	})
	t.Run("errors are shared too", func(t *testing.T) {
		var g Flights[string, int]
		want := errors.New("boom")
		if _, err := g.Do(context.Background(), "k", time.Second, func(context.Context) (int, error) { return 0, want }); !errors.Is(err, want) {
			t.Fatal(err)
		}
	})
}

type notifierFunc func(area string, version int, payload any) error

func (f notifierFunc) Notify(_ meta.Tx, area string, version int, payload any) error {
	return f(area, version, payload)
}
