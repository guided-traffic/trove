package guard

import (
	"context"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_BAS_019_AnIPv6AddressCountsAsItsSlash64(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203.0.113.7", "203.0.113.7"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"::1", "::/64"},
	}
	for _, c := range cases {
		got, err := ParseAddressKey(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseAddressKey(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if k1, k2 := AddressKey(netip.MustParseAddr("2001:db8:1:2::1")), AddressKey(netip.MustParseAddr("2001:db8:1:3::1")); k1 == k2 {
		t.Errorf("two /64 networks share the key %s", k1)
	}
	if _, err := ParseAddressKey("not-an-address"); err == nil {
		t.Error("an unparsable address was accepted")
	}
}

// clock is the injectable clock of the tests: the limits are bounded by what the requirement bounds, and
// nothing waits.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	g   *Instance
	clk *clock
	m   *metrics.Metrics
	st  *shared.State
}

// defaults are the defaults of R-BAS-035 to R-BAS-037.
func defaults() Options {
	return Options{
		Limits: config.BastionLimits{
			Authenticated: config.BastionLimitAuthenticated{RequestsPerSecond: 50, Burst: 200},
			Anonymous:     config.BastionLimitAnonymous{RequestsPerSecond: 10, Burst: 40},
			Misses:        config.BastionMisses{AuthenticatedPerMinute: 300, AnonymousPerMinute: 30},
		},
		FailedAuth: config.BastionFailedAuth{MaxFailures: 20, Window: 10 * time.Minute, Timeout: 15 * time.Minute},
		Blocking: config.BastionBlocking{
			Threshold: 300, Window: time.Minute, Duration: 15 * time.Minute, MaxDuration: 24 * time.Hour,
		},
	}
}

func newRig(t *testing.T, replicas int, mutate func(*Options)) *rig {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	o := defaults()
	o.State, o.Metrics, o.Now = shared.NewLocal(replicas, clk.now), m, clk.now
	if mutate != nil {
		mutate(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return &rig{g: g, clk: clk, m: m, st: o.State}
}

var ctx = context.Background()

func TestR_BAS_035_AnonymousRequestsAreLimitedPerAddress(t *testing.T) {
	r := newRig(t, 1, nil)
	// 1,000 anonymous requests within 10 s: the burst of 40 plus 10 per second is 140 at most.
	admitted := 0
	for i := 0; i < 1000; i++ {
		if r.g.Request(ctx, "203.0.113.7", false).Allowed {
			admitted++
		}
		r.clk.advance(10 * time.Millisecond)
	}
	if admitted > 140 {
		t.Fatalf("admitted %d anonymous requests in 10 s, the bound is 140", admitted)
	}
	if admitted < 100 {
		t.Fatalf("admitted %d, the limit is not meant to refuse a client that stays within it", admitted)
	}
	if !r.g.Request(ctx, "203.0.113.8", false).Allowed {
		t.Fatal("a second address was slowed down")
	}
	v := Allow()
	for i := 0; i < 100 && v.Allowed; i++ {
		v = r.g.Request(ctx, "203.0.113.7", false)
	}
	if v.Allowed || v.Status != http.StatusTooManyRequests || v.Code != CodeRateLimited || v.Reason != metrics.RefusedRateLimited || v.RetryAfter < time.Second {
		t.Fatalf("refusal = %+v", v)
	}
}

func TestR_BAS_035_AuthenticatedRequestsHaveTheirOwnLimit(t *testing.T) {
	r := newRig(t, 1, nil)
	for i := 0; i < 200; i++ {
		if !r.g.Request(ctx, "203.0.113.7", true).Allowed {
			t.Fatalf("authenticated request %d refused inside the burst of 200", i)
		}
	}
	if r.g.Request(ctx, "203.0.113.7", true).Allowed {
		t.Fatal("the 201st authenticated request was admitted")
	}
	if !r.g.Request(ctx, "203.0.113.7", false).Allowed {
		t.Fatal("the anonymous class shares the authenticated bucket")
	}
}

func TestR_BAS_035_MissesAreLimitedPerMinute(t *testing.T) {
	r := newRig(t, 1, nil)
	// 100 random missing names within a minute, anonymous: at most 30 reach the main installation.
	admitted := 0
	for i := 0; i < 100; i++ {
		if v := r.g.Miss(ctx, "203.0.113.7", false); v.Allowed {
			admitted++
		} else if v.Code != CodeRateLimited || v.Reason != metrics.RefusedMissLimited {
			t.Fatalf("refusal = %+v", v)
		}
		r.clk.advance(600 * time.Millisecond)
	}
	if admitted > 30 {
		t.Fatalf("%d misses admitted in a minute, the bound is 30", admitted)
	}
	if admitted < 25 {
		t.Fatalf("%d misses admitted, the limit refuses too early", admitted)
	}
	// Hits are not asked about misses: another address, and the authenticated class, keep their own.
	if !r.g.Miss(ctx, "203.0.113.8", false).Allowed || !r.g.Miss(ctx, "203.0.113.7", true).Allowed {
		t.Fatal("a miss of another address or class was refused")
	}
}

func TestR_BAS_035_WithoutValkeyTheTotalOverReplicasStaysNearTheLimit(t *testing.T) {
	// Two replicas, each at half: the anonymous limit admits the configured rate in total (± one burst each).
	total := 0
	for replica := 0; replica < 2; replica++ {
		r := newRig(t, 2, nil)
		for i := 0; i < 1000; i++ {
			if r.g.Request(ctx, "203.0.113.7", false).Allowed {
				total++
			}
			r.clk.advance(10 * time.Millisecond)
		}
	}
	if total > 140+2*40 {
		t.Fatalf("two replicas admitted %d in 10 s, more than the rate and one burst each", total)
	}
	if total > 140 {
		t.Fatalf("two replicas admitted %d in 10 s, the divided values give %d", total, 140)
	}
}

func TestR_BAS_035_AnExemptAddressIsNeverLimited(t *testing.T) {
	r := newRig(t, 1, func(o *Options) { o.Blocking.ExemptCIDRs = []string{"198.51.100.0/24", "192.0.2.9", "2001:db8:5::/48"} })
	for _, a := range []string{"198.51.100.77", "192.0.2.9", "2001:db8:5:1::/64"} {
		for i := 0; i < 2000; i++ {
			if v := r.g.Request(ctx, a, false); !v.Allowed {
				t.Fatalf("exempt address %s refused at %d: %+v", a, i, v)
			}
			if v := r.g.Miss(ctx, a, false); !v.Allowed {
				t.Fatalf("exempt address %s refused a miss at %d", a, i)
			}
		}
		if !r.g.Blocked(ctx, a).Allowed {
			t.Fatalf("exempt address %s blocked", a)
		}
	}
	if _, err := New(Options{State: r.st, Metrics: r.m, Blocking: config.BastionBlocking{ExemptCIDRs: []string{"nonsense"}}}); err == nil {
		t.Fatal("an exempt entry that is no address was accepted")
	}
}

// fail makes n failed authentications from addr, each asked about first as the engine does.
func fail(r *rig, addr string, n int) {
	for i := 0; i < n; i++ {
		if v := r.g.AuthTimeout(ctx, addr); !v.Allowed {
			continue
		}
		r.g.FailedAuth(ctx, addr)
		r.clk.advance(time.Second)
	}
}

func TestR_BAS_036_TheFailedAuthenticationTimeoutRefusesTheNextUnknownCredential(t *testing.T) {
	r := newRig(t, 1, nil)
	const addr = "203.0.113.7"
	// 20 random well-formed tokens are answered by the main installation as invalid.
	for i := 1; i <= 20; i++ {
		if v := r.g.AuthTimeout(ctx, addr); !v.Allowed {
			t.Fatalf("token %d refused before the limit", i)
		}
		r.g.FailedAuth(ctx, addr)
		r.clk.advance(time.Second)
	}
	// The 21st and every later one is refused without a request to the main installation.
	for i := 21; i <= 25; i++ {
		v := r.g.AuthTimeout(ctx, addr)
		if v.Allowed || v.Status != http.StatusTooManyRequests || v.Code != CodeAuthTimeout || v.Reason != metrics.RefusedAuthTimeout {
			t.Fatalf("token %d: %+v", i, v)
		}
		if v.RetryAfter <= 0 || v.RetryAfter > 15*time.Minute {
			t.Fatalf("Retry-After = %s", v.RetryAfter)
		}
	}
	// Anonymous stays anonymous: the request limits know nothing of the timeout, and another address is not touched.
	if !r.g.Request(ctx, addr, false).Allowed || !r.g.AuthTimeout(ctx, "203.0.113.8").Allowed {
		t.Fatal("the timeout reached beyond the unverified credentials of the address")
	}
	r.clk.advance(16 * time.Minute)
	if !r.g.AuthTimeout(ctx, addr).Allowed {
		t.Fatal("the timeout did not end after 15 minutes")
	}
}

func TestR_BAS_036_FailuresOutsideTheWindowDoNotAdd(t *testing.T) {
	r := newRig(t, 1, nil)
	const addr = "203.0.113.7"
	for round := 0; round < 5; round++ {
		fail(r, addr, 10)
		r.clk.advance(30 * time.Minute)
	}
	if !r.g.AuthTimeout(ctx, addr).Allowed {
		t.Fatal("slow failures started the timeout")
	}
}

func TestR_BAS_037_AFloodIsBlockedWithDoublingDurations(t *testing.T) {
	r := newRig(t, 1, nil)
	const addr = "198.51.100.9"
	flood := func() (blockedAt int) {
		blockedAt = -1
		for i := 0; i < 2000; i++ {
			if v := r.g.Blocked(ctx, addr); !v.Allowed {
				if blockedAt < 0 {
					blockedAt = i
				}
			} else {
				r.g.Request(ctx, addr, false)
			}
			r.clk.advance(30 * time.Millisecond) // 2,000 requests in a minute
		}
		return blockedAt
	}
	if at := flood(); at < 0 {
		t.Fatal("2,000 anonymous requests within a minute did not block the address")
	}
	v := r.g.Blocked(ctx, addr)
	if v.Allowed || v.Status != http.StatusTooManyRequests || v.Code != CodeAddressBlocked || v.Reason != metrics.RefusedBlocked || !v.Quiet {
		t.Fatalf("blocked verdict = %+v", v)
	}
	if v.RetryAfter <= 0 || v.RetryAfter > 15*time.Minute {
		t.Fatalf("Retry-After = %s, want within the first 15 minutes", v.RetryAfter)
	}
	if got := value(r.m.Blocks.WithLabelValues(metrics.BlockRefusals)); got != 1 {
		t.Fatalf("trove_bastion_blocks_total = %v, want 1", got)
	}
	if got := value(r.m.Blocked); got != 1 {
		t.Fatalf("trove_bastion_blocked_addresses = %v, want 1", got)
	}
	// 15 minutes after the block began it ends; a second flood within 24 h yields 30 minutes.
	r.clk.advance(15 * time.Minute)
	if !r.g.Blocked(ctx, addr).Allowed {
		t.Fatal("still blocked after 15 minutes")
	}
	flood()
	v = r.g.Blocked(ctx, addr)
	if v.Allowed || v.RetryAfter <= 15*time.Minute || v.RetryAfter > 30*time.Minute {
		t.Fatalf("second block: %+v, want between 15 and 30 minutes remaining", v)
	}
	r.clk.advance(30 * time.Minute)
	if !r.g.Blocked(ctx, addr).Allowed {
		t.Fatal("still blocked after 30 minutes")
	}
	if !r.g.Blocked(ctx, "198.51.100.10").Allowed {
		t.Fatal("another address was blocked")
	}
}

func TestR_BAS_037_TheDurationDoublesUpToTheMaximum(t *testing.T) {
	for level, want := range map[int]time.Duration{
		1: 15 * time.Minute, 2: 30 * time.Minute, 3: time.Hour, 7: 16 * time.Hour, 8: 24 * time.Hour, 20: 24 * time.Hour, 100: 24 * time.Hour,
	} {
		if got := blockDuration(15*time.Minute, 24*time.Hour, level); got != want {
			t.Errorf("level %d: %s, want %s", level, got, want)
		}
	}
}

func TestR_BAS_037_ABlockIsLoggedOnceWithAddressReasonDurationAndCount(t *testing.T) {
	var lines []string
	r := newRig(t, 1, func(o *Options) { o.Log = captureLog(&lines) })
	for i := 0; i < 2000; i++ {
		if r.g.Blocked(ctx, "198.51.100.9").Allowed {
			r.g.Request(ctx, "198.51.100.9", false)
		}
		r.clk.advance(30 * time.Millisecond)
	}
	var blocks int
	for _, l := range lines {
		if contains(l, "bastion address blocked") {
			blocks++
			for _, want := range []string{"198.51.100.9", "rate_limited", "15m0s", "count=1"} {
				if !contains(l, want) {
					t.Errorf("log line %q lacks %q", l, want)
				}
			}
		}
	}
	if blocks != 1 {
		t.Fatalf("%d block lines for one block: %q", blocks, lines)
	}
}

func TestR_BAS_037_RefusedMissesAndTimeoutsCountTowardTheBlock(t *testing.T) {
	r := newRig(t, 1, func(o *Options) {
		o.Blocking.Threshold = 10
		o.Limits.Misses.AnonymousPerMinute = 1
	})
	const addr = "198.51.100.9"
	for i := 0; i < 20; i++ {
		r.g.Miss(ctx, addr, false)
	}
	if r.g.Blocked(ctx, addr).Allowed {
		t.Fatal("refused misses did not block the address")
	}
	r2 := newRig(t, 1, func(o *Options) { o.Blocking.Threshold = 5; o.FailedAuth.MaxFailures = 1 })
	r2.g.FailedAuth(ctx, addr)
	for i := 0; i < 10; i++ {
		r2.g.AuthTimeout(ctx, addr)
	}
	if r2.g.Blocked(ctx, addr).Allowed {
		t.Fatal("refused credentials in the timeout did not block the address")
	}
}

func TestR_BAS_019_AnIPv6HostCountsAsOneAddress(t *testing.T) {
	r := newRig(t, 1, nil)
	// Two hosts of one /64 share one bucket; a host of another /64 has its own.
	a, _ := ParseAddressKey("2001:db8:1:2::1")
	b, _ := ParseAddressKey("2001:db8:1:2::dead:beef")
	c, _ := ParseAddressKey("2001:db8:1:3::1")
	n := 0
	for i := 0; i < 100; i++ {
		k := a
		if i%2 == 1 {
			k = b
		}
		if r.g.Request(ctx, k, false).Allowed {
			n++
		}
	}
	if n != 40 {
		t.Fatalf("one /64 admitted %d at once, want the burst of 40", n)
	}
	if !r.g.Request(ctx, c, false).Allowed {
		t.Fatal("another /64 was slowed down")
	}
}

func TestR_BAS_035_ASharedStateThatFailsAdmits(t *testing.T) {
	r := newRig(t, 1, nil)
	r.g.o.State.Limiter = failing{}
	if !r.g.Request(ctx, "203.0.113.7", false).Allowed || !r.g.Miss(ctx, "203.0.113.7", false).Allowed {
		t.Fatal("the guard refused on its own fault")
	}
	r.g.FailedAuth(ctx, "203.0.113.7")
}

func TestR_BAS_037_TheGaugeFollowsTheBlocks(t *testing.T) {
	r := newRig(t, 1, func(o *Options) {
		o.Blocking.Threshold = 1
		o.Limits.Anonymous = config.BastionLimitAnonymous{RequestsPerSecond: 1, Burst: 1}
	})
	for i := 0; i < 10; i++ {
		r.g.Request(ctx, "198.51.100.9", false)
	}
	if got := value(r.m.Blocked); got != 1 {
		t.Fatalf("gauge = %v, want 1", got)
	}
	r.clk.advance(time.Hour)
	r.g.refreshGauge(ctx)
	if got := value(r.m.Blocked); got != 0 {
		t.Fatalf("gauge = %v after the block ended, want 0", got)
	}
}

func TestR_BAS_035_TheNamesOfTheLimitersAreBastionScoped(t *testing.T) {
	for _, n := range []string{limiterRequestsAuthenticated, limiterRequestsAnonymous, limiterMissesAuthenticated, limiterMissesAnonymous, limiterFailedAuth, limiterRefusals} {
		if len(n) < 9 || n[:8] != "bastion." {
			t.Errorf("limiter %q is not named bastion.<name> (R-BAS-034)", n)
		}
	}
}
