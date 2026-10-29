package cluster

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// monitorTick is how often the monitor compares the primary the client is
// bound to with the one it saw before. It is cheap (an atomic load) and bounds
// how late a primary change reaches the Pub/Sub connection and the counters.
const monitorTick = 250 * time.Millisecond

// Valkey is a replica's client of the shared Valkey (R-ARCH-031). It is safe
// for concurrent use. Create it with [NewValkey], stop it with Close.
type Valkey struct {
	cfg      config.Valkey
	instance string
	role     string
	log      *slog.Logger
	m        *valkeyMetrics
	br       *breaker
	creds    credentials

	dataTLS, sentinelTLS *tls.Config
	// sentinelResolver tags dialers that go to Sentinels: valkey-go copies the
	// data-node option for them, and the dial function tells them apart by it.
	sentinelResolver *net.Resolver

	keyspace atomic.Pointer[string]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	ps *pubsub

	mu          sync.RWMutex
	client      valkey.Client
	primary     string
	version     string
	unsupported string     // reason; empty when the server is supported or not yet known
	connectErr  string     // last failed connection attempt, for the status
	connectCode ReasonCode // its code
	startupErr  error
	probeFailed string // reason of the last failed probe, empty when it passed
	sentinels   int    // Sentinels that answered the last probe of all of them
	lastAvail   bool
	evaluated   bool
	closed      bool
	hooks       []func(PrimaryChange)
}

// PrimaryChange describes a change of the primary the client follows.
type PrimaryChange struct {
	Old, New string
}

// ValkeyStatus is a snapshot for /readyz, the system information and the
// registry (R-OPS-003).
type ValkeyStatus struct {
	// Available is true when calls reach Valkey.
	Available bool
	// Primary is the address of the primary the client is bound to; empty
	// before the first connection.
	Primary string
	// Version is the server version from HELLO; empty before the first
	// connection.
	Version string
	// DegradedReason names why Available is false, in free text that can carry
	// addresses and server replies; empty when it is true. It belongs in the
	// log and the authenticated replica list, never on the ops listener.
	DegradedReason string
	// ReasonCode is DegradedReason in the fixed vocabulary of [ReasonCodes];
	// ReasonNone when Available is true. /readyz?verbose=1 shows this.
	ReasonCode ReasonCode
	// Mode is "sentinel" or "standalone".
	Mode string
	// SentinelsConfigured and SentinelsReachable count the configured Sentinels
	// and those that answered the last probe of all of them.
	SentinelsConfigured int
	SentinelsReachable  int
	// CircuitOpen is true while the breaker refuses calls.
	CircuitOpen bool
	// Subscribed is true while the Pub/Sub connection is subscribed on the
	// current primary.
	Subscribed bool
}

// ValkeyOption configures [NewValkey].
type ValkeyOption func(*valkeyOptions)

type valkeyOptions struct {
	keyspace string
}

// WithKeyspaceID sets the keyspace from system_state.keyspace_id
// (R-ARCH-030), which the caller reads from PostgreSQL. Without it, keys cannot
// be formed until [Valkey.SetKeyspaceID] is called.
func WithKeyspaceID(id string) ValkeyOption { return func(o *valkeyOptions) { o.keyspace = id } }

// NewValkey creates the client. It returns an error only for a configuration
// the process cannot run with: an invalid setting, an unreadable credential,
// CA or certificate file, a duplicate metric registration. Everything about
// reaching Valkey is a state, not an error: with every Sentinel unreachable
// the client is constructed degraded and keeps trying (Q-HA-13), and a
// rejected credential, an unknown primary name or a rejected certificate shows
// in [Valkey.StartupError].
//
// NewValkey waits for the first connection attempt, bounded by ctx and by the
// configured timeouts, so that StartupError is meaningful when it returns. ctx
// does not bound the client's lifetime; Close does.
func NewValkey(ctx context.Context, cfg config.Valkey, instance, role string, log *slog.Logger, reg prometheus.Registerer, opts ...ValkeyOption) (*Valkey, error) {
	var o valkeyOptions
	for _, opt := range opts {
		opt(&o)
	}
	if err := checkValkeyConfig(cfg, instance); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	creds := newCredentials(cfg)
	for _, kind := range []peerKind{peerData, peerSentinel} {
		if _, _, err := creds.read(kind); err != nil {
			return nil, fmt.Errorf("valkey credentials: %w", err)
		}
	}
	dataTLS, err := tlsConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	var sentinelTLS *tls.Config
	if cfg.Mode == "sentinel" && cfg.Sentinel.TLS {
		sentinelTLS = dataTLS
	}
	m, err := newValkeyMetrics(reg)
	if err != nil {
		return nil, err
	}

	v := &Valkey{
		cfg: cfg, instance: instance, role: role, log: log.With("component", "valkey"), m: m,
		creds: creds, dataTLS: dataTLS, sentinelTLS: sentinelTLS,
		sentinelResolver: &net.Resolver{},
	}
	v.warnWeakTrust()
	v.br = newBreaker(cfg.CircuitBreaker.FailureThreshold, cfg.CircuitBreaker.OpenDuration, nil, func(open bool) {
		if open {
			v.m.circuitOpen.Set(1)
		} else {
			v.m.circuitOpen.Set(0)
		}
		v.reevaluate()
	})
	if o.keyspace != "" {
		if err := v.SetKeyspaceID(o.keyspace); err != nil {
			return nil, fmt.Errorf("valkey: %w", err)
		}
	}
	v.ctx, v.cancel = context.WithCancel(context.Background())
	v.ps = newPubsub(v)

	first := make(chan struct{})
	v.wg.Add(2)
	go v.connectLoop(first)
	go v.monitor()
	if cfg.Mode == "sentinel" {
		v.wg.Add(1)
		go v.sentinelLoop()
	}
	v.wg.Add(1)
	go v.ps.run()

	wait := cfg.ConnectTimeout + time.Duration(len(cfg.Sentinel.Addresses))*cfg.SentinelQueryTimeout*2 + time.Second
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-first:
	case <-timer.C:
	case <-ctx.Done():
	}
	return v, nil
}

func checkValkeyConfig(cfg config.Valkey, instance string) error {
	if instance == "" {
		return errors.New("valkey: instance name is empty")
	}
	switch cfg.Mode {
	case "sentinel":
		if len(cfg.Sentinel.Addresses) == 0 || cfg.Sentinel.PrimaryName == "" {
			return errors.New("valkey: sentinel mode needs valkey.sentinel.addresses and valkey.sentinel.primary_name")
		}
		if cfg.Sentinel.RefreshInterval <= 0 {
			return errors.New("valkey: valkey.sentinel.refresh_interval must be positive")
		}
	case "standalone":
		if cfg.Address == "" {
			return errors.New("valkey: standalone mode needs valkey.address")
		}
	default:
		return fmt.Errorf("valkey: mode %q is not supported", cfg.Mode)
	}
	if cfg.CommandTimeout <= 0 || cfg.ConnectTimeout <= 0 || cfg.SentinelQueryTimeout <= 0 || cfg.HealthCheckInterval <= 0 {
		return errors.New("valkey: timeouts and intervals must be positive")
	}
	if cfg.CircuitBreaker.FailureThreshold < 1 {
		return errors.New("valkey: valkey.circuit_breaker.failure_threshold must be at least 1")
	}
	return nil
}

// warnWeakTrust logs, once at start, the Sentinel-mode settings that let
// whoever reaches a Sentinel steer this replica to a server of their choosing:
// a Sentinel without authentication can be told to name another primary, and
// with the system roots that primary passes verification with any publicly
// issued certificate for the name it is given. The configuration has no switch
// that makes them errors (H-item of the Valkey security page), so they are
// warnings.
func (v *Valkey) warnWeakTrust() {
	if v.cfg.Mode != "sentinel" {
		return
	}
	if v.cfg.Sentinel.PasswordFile == "" {
		v.log.Warn("valkey sentinel authentication is not configured: anyone who reaches a Sentinel can name another primary",
			"fix", "set valkey.sentinel.password_file (and username_file) and require a password on the Sentinels")
	}
	if v.cfg.TLS.Mode != "disable" && v.cfg.TLS.CAFile == "" {
		v.log.Warn("valkey TLS verifies against the system roots: a primary named by a Sentinel passes with any publicly issued certificate",
			"fix", "set valkey.tls.ca_file to the CA of the Valkey servers")
	}
}

// Available reports whether calls reach Valkey. While it is false, every call
// returns [ErrUnavailable] and the caller runs its fallback.
func (v *Valkey) Available() bool {
	avail, _, _ := v.compute()
	return avail
}

// Status returns a snapshot of the client.
func (v *Valkey) Status() ValkeyStatus {
	avail, code, reason := v.compute()
	v.mu.RLock()
	st := ValkeyStatus{
		Available: avail, Primary: v.primary, Version: v.version, DegradedReason: reason, ReasonCode: code,
		Mode: v.cfg.Mode, SentinelsConfigured: len(v.cfg.Sentinel.Addresses), SentinelsReachable: v.sentinels,
	}
	v.mu.RUnlock()
	st.CircuitOpen = v.br.isOpen()
	st.Subscribed = v.ps.isSubscribed()
	return st
}

// StartupError is non-nil when the first connection attempts found a
// configuration error: a rejected credential (WRONGPASS, NOAUTH, NOPERM), a
// failed certificate verification, or a primary name that no reachable Sentinel
// knows (Q-HA-13). The readiness registry holds the first readiness back on it.
// An unreachable Valkey is not a startup error. The error clears when a later
// attempt connects.
func (v *Valkey) StartupError() error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.startupErr
}

// OnPrimaryChange registers fn to run after every change of the primary the
// client follows. The replica then runs one change-log poll and its flight-lock
// waiters re-check their keys (R-ARCH-031). fn runs on the monitor goroutine and
// must not block.
func (v *Valkey) OnPrimaryChange(fn func(PrimaryChange)) {
	v.mu.Lock()
	v.hooks = append(v.hooks, fn)
	v.mu.Unlock()
}

// CountDegraded records that a mechanism ran its fallback because Valkey was
// unavailable (trove_valkey_degraded_total). An unknown mechanism is ignored.
func (v *Valkey) CountDegraded(mechanism string) {
	if mechanisms[mechanism] {
		v.m.degraded.WithLabelValues(mechanism).Inc()
	}
}

// CountCacheLookup records a lookup in Valkey after an in-process miss
// (trove_valkey_cache_lookups_total). cache is a registered cache name, result
// is "hit", "miss" or "error".
func (v *Valkey) CountCacheLookup(cache, result string) {
	v.m.cacheLookups.WithLabelValues(cache, result).Inc()
}

// Close stops the background work and closes every connection. It is safe to
// call more than once.
func (v *Valkey) Close() {
	v.once.Do(func() {
		v.mu.Lock()
		v.closed = true
		c := v.client
		v.client = nil
		v.mu.Unlock()
		v.cancel()
		v.wg.Wait()
		if c != nil {
			c.Close()
		}
		v.m.available.Set(0)
		v.m.pubsubSubscribed.Set(0)
	})
}

// current returns the client, or nil while none is connected.
func (v *Valkey) current() valkey.Client {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.client
}

// compute derives availability, its reason code and its reason text from the
// state.
func (v *Valkey) compute() (bool, ReasonCode, string) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	switch {
	case v.closed:
		return false, ReasonClosed, "client closed"
	case v.client == nil:
		if v.connectErr != "" {
			return false, v.connectCode, "not connected: " + v.connectErr
		}
		return false, ReasonConnecting, "connecting"
	case v.unsupported != "":
		return false, ReasonUnsupported, v.unsupported
	case v.br.isOpen():
		return false, ReasonCircuitOpen, "circuit open"
	case v.probeFailed != "":
		return false, ReasonProbeFailed, "probe failed: " + v.probeFailed
	}
	return true, ReasonNone, ""
}

// reevaluate publishes availability: the gauge always, a log record on every
// transition (R-ARCH-031: WARN when it turns degraded, INFO when it recovers).
func (v *Valkey) reevaluate() {
	avail, code, reason := v.compute()
	v.mu.Lock()
	changed := !v.evaluated || avail != v.lastAvail
	v.evaluated, v.lastAvail = true, avail
	v.mu.Unlock()
	if avail {
		v.m.available.Set(1)
	} else {
		v.m.available.Set(0)
	}
	if !changed {
		return
	}
	if avail {
		v.log.Info("valkey available")
	} else {
		v.log.Warn("valkey degraded", "reason_code", string(code), "reason", reason)
	}
}

// clientOption builds the valkey-go options for the command client.
func (v *Valkey) clientOption() valkey.ClientOption {
	cfg := v.cfg
	// Connections are recycled so that rotated credentials apply (R-HA-056);
	// valkey-go has one lifetime per client, so the jitter is per process.
	lifetime := cfg.MaxConnLifetime + time.Duration(rand.Int64N(int64(cfg.MaxConnLifetime/10)+1)) //nolint:gosec // jitter, not a secret
	opt := valkey.ClientOption{
		InitAddress:       []string{cfg.Address},
		ClientName:        "trove:" + v.instance + ":main",
		TLSConfig:         v.dataTLS,
		AuthCredentialsFn: v.creds.fn,
		DialCtxFn:         dialFn(v.sentinelResolver),
		DisableCache:      true, // R-ARCH-031: trove's caches are its own
		SelectDB:          cfg.Database,
		Dialer:            outbound.NewDialer(cfg.ConnectTimeout, nil),
		ConnWriteTimeout:  max(2*cfg.CommandTimeout, time.Second),
		ConnLifetime:      lifetime,
	}
	if cfg.Mode == "sentinel" {
		opt.InitAddress = cfg.Sentinel.Addresses
		opt.Sentinel = valkey.SentinelOption{
			MasterSet:  cfg.Sentinel.PrimaryName,
			ClientName: "trove:" + v.instance + ":sentinel",
			TLSConfig:  v.sentinelTLS,
			Dialer:     outbound.NewDialer(cfg.SentinelQueryTimeout, v.sentinelResolver),
			// The poll that bounds failover when +switch-master is missed.
			TopologyRefreshInterval: cfg.Sentinel.RefreshInterval,
		}
	} else {
		opt.ForceSingleClient = true
	}
	return opt
}

// connectLoop builds the client until it succeeds. The first attempt closes
// first, whatever its result, so NewValkey can report the startup state.
func (v *Valkey) connectLoop(first chan<- struct{}) {
	defer v.wg.Done()
	backoff := v.cfg.Reconnect.InitialBackoff
	signalled := false
	for {
		ok := v.attempt()
		if !signalled {
			signalled = true
			close(first)
		}
		if ok {
			return
		}
		select {
		case <-v.ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
		backoff = min(backoff*2, v.cfg.Reconnect.MaxBackoff)
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d)/2+1)) //nolint:gosec // jitter, not a secret
}

// attempt makes one try at a client. It reports whether the client is installed.
func (v *Valkey) attempt() bool {
	c, err := valkey.NewClient(v.clientOption())
	if v.ctx.Err() != nil {
		if c != nil {
			c.Close()
		}
		return false
	}
	if err != nil {
		v.connectFailed(err)
		return false
	}
	// A HELLO that fails is a connection that is not usable yet.
	if err := v.checkVersion(c); err != nil {
		c.Close()
		v.connectFailed(err)
		return false
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		c.Close()
		return false
	}
	v.client = c
	v.connectErr, v.connectCode = "", ReasonNone
	v.startupErr = nil
	v.primary = primaryAddr(c)
	v.mu.Unlock()
	st := v.Status()
	v.log.Info("valkey connected", "primary", st.Primary, "version", st.Version)
	v.reevaluate()
	v.ps.poke()
	return true
}

// connectFailed records a failed attempt, classifies it and logs it once per
// distinct cause.
func (v *Valkey) connectFailed(err error) {
	var probes []sentinelProbe
	if v.cfg.Mode == "sentinel" {
		ctx, cancel := context.WithTimeout(v.ctx, v.cfg.SentinelQueryTimeout*2+v.cfg.ConnectTimeout)
		probes = v.probeSentinels(ctx)
		cancel()
	}
	isConfig, cause := classifyStartup(v.cfg.Sentinel.PrimaryName, probes, err)
	kind := classify(cause)
	v.m.errors.WithLabelValues(string(kindLabel(kind))).Inc()

	text := describeConnectError(cause, probes)
	code := connectReason(isConfig, kind, probes)
	v.mu.Lock()
	changed := v.connectErr != text
	v.connectErr, v.connectCode = text, code
	if isConfig {
		v.startupErr = fmt.Errorf("valkey configuration error: %w", cause)
	} else {
		v.startupErr = nil
	}
	v.mu.Unlock()
	if changed {
		if isConfig {
			v.log.Error("valkey configuration error", "error", cause.Error())
		} else {
			v.log.Warn("valkey connection failed", "error", text)
		}
	}
	v.reevaluate()
}

func kindLabel(k ErrorKind) ErrorKind {
	if k == kindNone || k == kindCanceled {
		return KindConnection
	}
	return k
}

// primaryAddr returns the address of the node the client sends commands to.
func primaryAddr(c valkey.Client) string {
	for addr := range c.Nodes() {
		return addr
	}
	return ""
}

// monitor watches for primary changes and probes Valkey every
// health_check_interval.
func (v *Valkey) monitor() {
	defer v.wg.Done()
	t := time.NewTicker(monitorTick)
	defer t.Stop()
	lastProbe := time.Now()
	for {
		select {
		case <-v.ctx.Done():
			return
		case <-t.C:
		}
		c := v.current()
		if c == nil {
			continue
		}
		v.checkPrimary(c)
		if time.Since(lastProbe) >= v.cfg.HealthCheckInterval {
			lastProbe = time.Now()
			v.probe()
		}
	}
}

// checkPrimary notices a change of the primary the client is bound to.
func (v *Valkey) checkPrimary(c valkey.Client) {
	addr := primaryAddr(c)
	v.mu.Lock()
	old := v.primary
	if addr == "" || addr == old {
		v.mu.Unlock()
		return
	}
	v.primary = addr
	hooks := append([]func(PrimaryChange){}, v.hooks...)
	v.mu.Unlock()

	v.m.primaryChanges.Inc()
	v.log.Info("valkey primary changed", "old", old, "new", addr)
	// The new primary may run another version, or be a node that lost data.
	if err := v.checkVersion(c); err != nil {
		v.log.Warn("valkey version check failed after a primary change", "error", err.Error())
	}
	v.ps.poke()
	for _, fn := range hooks {
		fn(PrimaryChange{Old: old, New: addr})
	}
	v.reevaluate()
}

// probe checks that the primary answers and accepts writes. It goes through
// the breaker, so after open_duration it is the probe that closes it again.
func (v *Valkey) probe() {
	if v.isUnsupported() {
		if c := v.current(); c != nil {
			_ = v.checkVersion(c) // a server upgraded in place becomes usable
		}
		v.reevaluate()
		return
	}
	var err error
	if key, kerr := v.formKey(probeFamily, v.instance); kerr == nil {
		err = v.set(v.ctx, key, "1")
	} else {
		err = v.ping(v.ctx)
	}
	v.mu.Lock()
	if err != nil {
		v.probeFailed = err.Error()
	} else {
		v.probeFailed = ""
	}
	v.mu.Unlock()
	v.reevaluate()
}

func (v *Valkey) isUnsupported() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.unsupported != ""
}

// probeFamily is the health probe key of R-ARCH-030. Every probe write sets
// its 30 s expiry.
var probeFamily = registerKeyFamily("probe", 1, 30*time.Second)

// checkVersion reads the server and version from HELLO and records the
// decision. A transport error is returned; an unsupported server is a state.
func (v *Valkey) checkVersion(c valkey.Client) error {
	ctx, cancel := context.WithTimeout(v.ctx, v.cfg.CommandTimeout)
	defer cancel()
	start := time.Now()
	reply, err := helloReply(ctx, c)
	v.m.duration.WithLabelValues("hello").Observe(time.Since(start).Seconds())
	if err != nil {
		v.m.errors.WithLabelValues(string(kindLabel(classify(err)))).Inc()
		return fmt.Errorf("HELLO: %w", err)
	}
	server, version, err := helloFields(reply)
	if err != nil {
		return err
	}
	reason := checkServer(server, version)
	v.mu.Lock()
	was := v.unsupported
	v.version, v.unsupported = version, reason
	v.mu.Unlock()
	if reason != "" && reason != was {
		v.log.Warn(reason, "version", version)
	}
	return nil
}
