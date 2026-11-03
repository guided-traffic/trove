package shared

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)} }

func TestR_BAS_035_ABucketAdmitsItsBurstThenTheRate(t *testing.T) {
	c := newClock()
	s := NewLocal(1, c.now)
	rate := Rate{PerSecond: 10, Burst: 40}
	ctx := context.Background()
	admitted := 0
	for i := 0; i < 1000; i++ {
		if ok, _, _ := s.Limiter.Allow(ctx, "bastion.anonymous", "203.0.113.7", rate); ok {
			admitted++
		}
	}
	if admitted != 40 {
		t.Fatalf("admitted %d at once, want the burst of 40", admitted)
	}
	c.advance(10 * time.Second)
	admitted = 0
	for i := 0; i < 1000; i++ {
		if ok, _, _ := s.Limiter.Allow(ctx, "bastion.anonymous", "203.0.113.7", rate); ok {
			admitted++
		}
	}
	if admitted != 40 {
		t.Fatalf("after 10 s: %d, want the burst of 40 again (100 tokens, capped)", admitted)
	}
	if ok, _, _ := s.Limiter.Allow(ctx, "bastion.anonymous", "203.0.113.8", rate); !ok {
		t.Fatal("a second address was slowed down")
	}
}

func TestR_BAS_034_WithoutValkeyTheRateIsDividedByTheReplicas(t *testing.T) {
	c := newClock()
	s := NewLocal(2, c.now)
	ok := 0
	for i := 0; i < 1000; i++ {
		if a, _, _ := s.Limiter.Allow(context.Background(), "bastion.anonymous", "a", Rate{PerSecond: 10, Burst: 40}); a {
			ok++
		}
	}
	if ok != 20 {
		t.Fatalf("burst per replica = %d, want 40 / 2", ok)
	}
	if s.Limiter.Replicas() != 2 || s.Shared() {
		t.Fatal("the local state claims to be shared or to have one replica")
	}
}

func TestR_BAS_036_AWindowCountsOnlyWithinItsPeriod(t *testing.T) {
	c := newClock() // 10:00:00, the start of a 10-minute window
	s := NewLocal(1, c.now)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		n, _ := s.Limiter.Add(ctx, "bastion.failed_auth", "a", 10*time.Minute)
		if n != i {
			t.Fatalf("count after %d = %d", i, n)
		}
		c.advance(time.Minute)
	}
	if n, _ := s.Limiter.Count(ctx, "bastion.failed_auth", "a", 10*time.Minute); n != 3 {
		t.Fatalf("count inside the window = %d, want 3", n)
	}
	// Two windows later nothing counts: the sliding estimate has decayed to zero.
	c.advance(20 * time.Minute)
	if n, _ := s.Limiter.Count(ctx, "bastion.failed_auth", "a", 10*time.Minute); n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
	// Half a window into the next one the previous window still counts by half.
	s = NewLocal(1, c.now)
	c.t = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		_, _ = s.Limiter.Add(ctx, "bastion.failed_auth", "b", 10*time.Minute)
	}
	c.advance(15 * time.Minute)
	if n, _ := s.Limiter.Count(ctx, "bastion.failed_auth", "b", 10*time.Minute); n != 5 {
		t.Fatalf("count = %d, want 5 (10 events, half a window past)", n)
	}
}

func TestR_BAS_034_WithoutValkeyACountIsScaledByTheReplicas(t *testing.T) {
	c := newClock()
	s := NewLocal(3, c.now)
	ctx := context.Background()
	n, _ := s.Limiter.Add(ctx, "bastion.failed_auth", "a", time.Minute)
	if n != 3 {
		t.Fatalf("one event counted as %d on a replica of three, want 3: the caller compares with the configured value", n)
	}
	n, _ = s.Limiter.Add(ctx, "bastion.failed_auth", "a", time.Minute)
	if n != 6 {
		t.Fatalf("two events counted as %d, want 6", n)
	}
	if n, _ = s.Limiter.Count(ctx, "bastion.failed_auth", "other", time.Minute); n != 0 {
		t.Fatalf("an unseen key counts %d", n)
	}
}

func TestR_BAS_035_ALimiterKeepsItsMapsBounded(t *testing.T) {
	c := newClock()
	l := newLocal(1, c.now)
	ctx := context.Background()
	for i := 0; i < maxLocalKeys+50; i++ {
		_, _, _ = l.Allow(ctx, "bastion.anonymous", fmt.Sprintf("k%d", i), Rate{PerSecond: 1, Burst: 1})
		_, _ = l.Add(ctx, "bastion.refusals", fmt.Sprintf("k%d", i), time.Minute)
	}
	if len(l.buckets) > maxLocalKeys || len(l.windows) > maxLocalKeys {
		t.Fatalf("%d buckets and %d counters held, the bound is %d", len(l.buckets), len(l.windows), maxLocalKeys)
	}
}

func TestR_BAS_036_ATimeoutListIsSeparateFromTheBlockList(t *testing.T) {
	s := NewLocal(1, newClock().now)
	ctx := context.Background()
	_ = s.Timeouts.Set(ctx, "198.51.100.9", time.Minute, 1)
	if _, ok, _ := s.Timeouts.Blocked(ctx, "198.51.100.9"); !ok {
		t.Fatal("the timeout is not listed")
	}
	if _, ok, _ := s.Blocks.Blocked(ctx, "198.51.100.9"); ok {
		t.Fatal("a timeout blocked the address")
	}
}

func TestR_BAS_031_WithoutValkeyEveryReplicaHoldsTheSweepLease(t *testing.T) {
	s := NewLocal(2, nil)
	for i := 0; i < 2; i++ {
		if held, _, err := s.Sweep.TryAcquire(context.Background(), time.Minute); !held || err != nil {
			t.Fatalf("lease = %v, %v", held, err)
		}
	}
}

func TestR_BAS_037_ABlockExpiresAndItsLevelIsKeptForADay(t *testing.T) {
	c := newClock()
	s := NewLocal(1, c.now)
	ctx := context.Background()
	if err := s.Blocks.Set(ctx, "198.51.100.9", 15*time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Blocks.Blocked(ctx, "198.51.100.9"); !ok {
		t.Fatal("not blocked")
	}
	if n, _ := s.Blocks.Size(ctx); n != 1 {
		t.Fatalf("size = %d", n)
	}
	c.advance(16 * time.Minute)
	if _, ok, _ := s.Blocks.Blocked(ctx, "198.51.100.9"); ok {
		t.Fatal("still blocked after the duration")
	}
	if lv, _ := s.Blocks.Level(ctx, "198.51.100.9"); lv != 1 {
		t.Fatalf("level = %d, want 1 within 24 h", lv)
	}
	c.advance(24 * time.Hour)
	if lv, _ := s.Blocks.Level(ctx, "198.51.100.9"); lv != 0 {
		t.Fatalf("level = %d after 24 h", lv)
	}
}

func TestR_BAS_025_AFlightLockIsHeldByOneAndReleased(t *testing.T) {
	s := NewLocal(1, nil)
	ctx := context.Background()
	held, release, _ := s.Flights.TryAcquire(ctx, "k")
	if !held {
		t.Fatal("the first caller did not get the lock")
	}
	if again, _, _ := s.Flights.TryAcquire(ctx, "k"); again {
		t.Fatal("a second caller got the lock")
	}
	release()
	if again, _, _ := s.Flights.TryAcquire(ctx, "k"); !again {
		t.Fatal("the lock was not released")
	}
}

func TestR_BAS_034_AnswerRecordsExpire(t *testing.T) {
	c := newClock()
	s := NewLocal(1, c.now)
	ctx := context.Background()
	_ = s.Answers.Put(ctx, AnswerShape, "h", []byte("signed"), time.Minute)
	if b, ok, _ := s.Answers.Get(ctx, AnswerShape, "h"); !ok || string(b) != "signed" {
		t.Fatalf("get = %q %v", b, ok)
	}
	if _, ok, _ := s.Answers.Get(ctx, AnswerURL, "h"); ok {
		t.Fatal("a record of another kind was returned")
	}
	c.advance(2 * time.Minute)
	if _, ok, _ := s.Answers.Get(ctx, AnswerShape, "h"); ok {
		t.Fatal("an expired record was returned")
	}
}
