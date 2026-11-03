package guard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
)

// The names of the limiters (R-BAS-034: bastion.<name>). With Valkey they are the limiters of R-ARCH-034 and
// the labels of trove_ratelimit_fallback.
const (
	limiterRequestsAuthenticated = "bastion.requests_authenticated"
	limiterRequestsAnonymous     = "bastion.requests_anonymous"
	limiterMissesAuthenticated   = "bastion.misses_authenticated"
	limiterMissesAnonymous       = "bastion.misses_anonymous"
	limiterFailedAuth            = "bastion.failed_auth"
	limiterRefusals              = "bastion.refusals"
)

// missWindow is the period of the miss limits: they are per minute (R-BAS-035).
const missWindow = time.Minute

// missRetryAfter is the Retry-After of a refused miss: a quarter of the window lets the estimate of the
// admitted misses fall by a quarter, which a client that waits sees. A refused miss takes nothing from the
// budget (Q-BAS-127), so a retry that comes too early costs the client nothing but the refusal.
const missRetryAfter = missWindow / 4

// gaugeInterval is how often the number of blocked addresses is read again, so that a block that ends
// leaves the gauge while no request arrives.
const gaugeInterval = 5 * time.Second

// Options configure [New].
type Options struct {
	Limits     config.BastionLimits
	FailedAuth config.BastionFailedAuth
	Blocking   config.BastionBlocking
	// State is the shared state: local, or in Valkey (internal/bastion/shared).
	State   *shared.State
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Now is the clock; nil means time.Now. Tests inject one.
	Now func() time.Time
}

// Instance is the guard of a replica. It implements [Guard]; Close stops the loop that keeps
// trove_bastion_blocked_addresses current.
type Instance struct {
	o      Options
	log    *slog.Logger
	now    func() time.Time
	exempt []netip.Prefix

	// blocking serializes the decision to block an address within the process, per address, so that the
	// refusals of a burst that cross the threshold together set one block and not several levels at once, and
	// the round trips of one address's decision hold no other address back (Q-BAS-128).
	blocking keyedLock

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

var _ Guard = (*Instance)(nil)

// New builds the guard. An entry of bastion.blocking.exempt_cidrs that is neither an address nor a CIDR is an
// error.
func New(o Options) (*Instance, error) {
	if o.State == nil || o.Metrics == nil {
		return nil, errors.New("guard: State and Metrics are required")
	}
	g := &Instance{o: o, log: o.Log, now: o.Now, stop: make(chan struct{}), done: make(chan struct{})}
	if g.log == nil {
		g.log = slog.New(slog.DiscardHandler)
	}
	if g.now == nil {
		g.now = time.Now
	}
	for _, c := range o.Blocking.ExemptCIDRs {
		p, err := parseExempt(c)
		if err != nil {
			return nil, err
		}
		g.exempt = append(g.exempt, p)
	}
	go g.loop()
	return g, nil
}

func parseExempt(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("guard: bastion.blocking.exempt_cidrs: %q is neither an address nor a CIDR", s)
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// Close stops the background loop. It may be called again.
func (g *Instance) Close() error {
	g.once.Do(func() { close(g.stop) })
	<-g.done
	return nil
}

func (g *Instance) loop() {
	defer close(g.done)
	t := time.NewTicker(gaugeInterval)
	defer t.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-t.C:
			g.refreshGauge(context.Background())
		}
	}
}

func (g *Instance) refreshGauge(ctx context.Context) {
	if n, err := g.o.State.Blocks.Size(ctx); err == nil {
		g.o.Metrics.Blocked.Set(float64(n))
	}
}

// isExempt reports whether the address key (an IPv4 address, or the /64 of an IPv6 one) is in an exempt
// network. An IPv6 key counts by the first address of its /64.
func (g *Instance) isExempt(key string) bool {
	if len(g.exempt) == 0 {
		return false
	}
	var a netip.Addr
	if p, err := netip.ParsePrefix(key); err == nil {
		a = p.Addr()
	} else if a, err = netip.ParseAddr(key); err != nil {
		return false
	}
	for _, p := range g.exempt {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// retryAfter rounds a wait up to whole seconds, at least one (Retry-After is whole seconds).
func retryAfter(d time.Duration) time.Duration {
	return max((d+time.Second-1)/time.Second, 1) * time.Second
}

func refuse(code, reason string, after time.Duration) Verdict {
	return Verdict{Status: http.StatusTooManyRequests, Code: code, RetryAfter: retryAfter(after), Reason: reason}
}

// Blocked implements [Guard]. A failure of the shared state admits: the guard never refuses on its own fault.
func (g *Instance) Blocked(ctx context.Context, addr string) Verdict {
	if g.isExempt(addr) {
		return Allow()
	}
	b, ok, err := g.o.State.Blocks.Blocked(ctx, addr)
	if err != nil || !ok {
		return Allow()
	}
	v := refuse(CodeAddressBlocked, metrics.RefusedBlocked, b.Until.Sub(g.now()))
	v.Quiet = true
	return v
}

// Request implements [Guard].
func (g *Instance) Request(ctx context.Context, addr string, c Credentialed) Verdict {
	if g.isExempt(addr) {
		return Allow()
	}
	name, rate := limiterRequestsAnonymous, rateOf(g.o.Limits.Anonymous.RequestsPerSecond, g.o.Limits.Anonymous.Burst)
	if c {
		name, rate = limiterRequestsAuthenticated, rateOf(g.o.Limits.Authenticated.RequestsPerSecond, g.o.Limits.Authenticated.Burst)
	}
	ok, wait, err := g.o.State.Limiter.Allow(ctx, name, addr, rate)
	if err != nil || ok {
		return Allow()
	}
	g.refused(ctx, addr, metrics.RefusedRateLimited)
	return refuse(CodeRateLimited, metrics.RefusedRateLimited, wait)
}

func rateOf(perSecond, burst int) shared.Rate {
	return shared.Rate{PerSecond: float64(perSecond), Burst: burst}
}

// Miss implements [Guard]. The limit is a count per minute: the admitted events of the address inside the
// sliding minute, this one included, may not exceed it. A refused miss is not counted (Q-BAS-127).
func (g *Instance) Miss(ctx context.Context, addr string, c Credentialed) Verdict {
	if g.isExempt(addr) {
		return Allow()
	}
	name, limit := limiterMissesAnonymous, g.o.Limits.Misses.AnonymousPerMinute
	if c {
		name, limit = limiterMissesAuthenticated, g.o.Limits.Misses.AuthenticatedPerMinute
	}
	ok, _, err := g.o.State.Limiter.AddIfWithin(ctx, name, addr, missWindow, limit)
	if err != nil || ok {
		return Allow()
	}
	g.refused(ctx, addr, metrics.RefusedMissLimited)
	return refuse(CodeRateLimited, metrics.RefusedMissLimited, missRetryAfter)
}

// AuthTimeout implements [Guard].
func (g *Instance) AuthTimeout(ctx context.Context, addr string) Verdict {
	if g.isExempt(addr) {
		return Allow()
	}
	b, ok, err := g.o.State.Timeouts.Blocked(ctx, addr)
	if err != nil || !ok {
		return Allow()
	}
	g.refused(ctx, addr, metrics.RefusedAuthTimeout)
	return refuse(CodeAuthTimeout, metrics.RefusedAuthTimeout, b.Until.Sub(g.now()))
}

// FailedAuth implements [Guard]. The failure that brings the count of the window to max_failures starts the
// timeout, so the next credential without a usable answer is the first one refused: max_failures failures are
// answered by the main installation, the following one is not (R-BAS-036).
func (g *Instance) FailedAuth(ctx context.Context, addr string) {
	if g.isExempt(addr) {
		return
	}
	n, err := g.o.State.Limiter.Add(ctx, limiterFailedAuth, addr, g.o.FailedAuth.Window)
	if err != nil || n < g.o.FailedAuth.MaxFailures {
		return
	}
	if _, in, _ := g.o.State.Timeouts.Blocked(ctx, addr); in {
		return
	}
	_ = g.o.State.Timeouts.Set(ctx, addr, g.o.FailedAuth.Timeout, 1)
	g.log.Info("bastion failed-authentication timeout", "address", addr, "failures", n, "duration", g.o.FailedAuth.Timeout.String())
}

// refused counts a refusal of R-BAS-035 or R-BAS-036 toward the block threshold and blocks the address that
// collects more than bastion.blocking.threshold of them within bastion.blocking.window (R-BAS-037). The block
// lasts bastion.blocking.duration, doubled for each earlier block within 24 h up to max_duration.
func (g *Instance) refused(ctx context.Context, addr, reason string) {
	b := g.o.Blocking
	n, err := g.o.State.Limiter.Add(ctx, limiterRefusals, addr, b.Window)
	if err != nil || n <= b.Threshold {
		return
	}
	unlock := g.blocking.lock(addr)
	defer unlock()
	if _, already, _ := g.o.State.Blocks.Blocked(ctx, addr); already {
		return
	}
	prev, _ := g.o.State.Blocks.Level(ctx, addr)
	level := prev + 1
	d := blockDuration(b.Duration, b.MaxDuration, level)
	_ = g.o.State.Blocks.Set(ctx, addr, d, level)
	g.o.Metrics.Blocks.WithLabelValues(metrics.BlockRefusals).Inc()
	g.refreshGauge(ctx)
	g.log.Info("bastion address blocked", "address", addr, "reason", reason, "duration", d.String(), "count", level, "refusals", n)
}

// blockDuration is the duration doubled level-1 times, held to max.
func blockDuration(base, ceiling time.Duration, level int) time.Duration {
	d := base
	for i := 1; i < level; i++ {
		if d >= ceiling {
			break
		}
		d *= 2
	}
	return min(d, ceiling)
}
