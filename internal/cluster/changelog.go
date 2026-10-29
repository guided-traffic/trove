package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Flush reasons of R-ARCH-018 step 5, the values of the reason label of
// trove_cache_flushes_total.
const (
	FlushChangeLogGap    = "change_log_gap"
	FlushUnknownPayload  = "unknown_payload"
	FlushEpochRegressed  = "epoch_regressed"
	sourcePoll           = "poll"
	sourceMessage        = "message"
	sourceLocal          = "local"
	maxMessageBacklog    = 512
	defaultPollTimeoutMS = 10000
)

// Listener receives the changes of the change areas. *cache.Registry implements
// it; so can any owner that keeps state outside the cache framework (the grant
// index of R-AUTHZ-028).
type Listener interface {
	// ApplyChange applies one change: from the poll in epoch order, from a
	// Valkey message as an early eviction, or from this replica's own commit.
	// It must be idempotent.
	ApplyChange(area string, version int, payload json.RawMessage)
	// FlushArea drops everything the area's changes keep fresh. Called with the
	// reasons above, and for a Valkey message of an unknown payload version.
	FlushArea(area, reason string)
}

// ChangesOptions configure [NewChanges].
type ChangesOptions struct {
	// DB runs the poll: the replica's control connection (DB.ControlPool), so a
	// request pool exhausted under load cannot stall it (R-ARCH-018).
	DB q.DBTX
	// Bus is the Valkey fast path; nil means none.
	Bus Bus
	// Config carries epoch_poll_interval and epoch_max_staleness.
	Config config.Cluster
	// WatchdogTimeout is health.watchdog_timeout; every call of the poll carries
	// a deadline below it.
	WatchdogTimeout time.Duration
	Watchdog        Watchdog
	Log             *slog.Logger
	Metrics         prometheus.Registerer
}

// Changes is the change log of one replica (R-ARCH-018): the writer side
// (Notify), the poll that applies every committed change from PostgreSQL, and
// the Valkey messages that only evict early.
type Changes struct {
	o   ChangesOptions
	log *slog.Logger
	m   *changeMetrics

	areas     map[string]*areaState // change areas this replica follows; fixed at Start
	listeners []Listener
	lmu       sync.RWMutex

	kick   chan struct{}
	wmu    sync.Mutex
	waits  []chan error
	msgq   chan Message
	pubq   chan pubItem
	cancel context.CancelFunc
	lctx   context.Context
	wg     sync.WaitGroup
	closed atomic.Bool

	smu        sync.Mutex
	subs       []func()
	subscribed bool
	warnedSub  bool

	fmu       sync.Mutex
	lastStart time.Time // start of the last successful poll
	lastEnd   time.Time // end of the last successful poll

	pollMu sync.Mutex // one poll at a time

	sql changeSQL // the statements of Notify; see changelog_notify.go
}

type areaState struct {
	meta Area
	// applied is the last applied epoch; only the poll advances it.
	applied atomic.Int64
	mu      sync.Mutex // guards at, and makes (applied, at) one pair for AreaState; never held across a query or a listener call
	at      time.Time  // changed_at of the epoch applied last
	known   bool       // poll goroutine only: false until the first poll set the starting point
	missing bool       // poll goroutine only: logged once, no cluster_epoch row
}

// set records the epoch and changed_at the poll applied last.
func (st *areaState) set(epoch int64, at time.Time) {
	st.mu.Lock()
	st.applied.Store(epoch)
	st.at = at
	st.mu.Unlock()
}

type changeMetrics struct {
	applied   *prometheus.CounterVec
	lastOK    prometheus.Gauge
	pollErrs  prometheus.Counter
	published *prometheus.CounterVec
	dropped   *prometheus.CounterVec
}

// NewChanges creates the change log. It follows the change areas registered at
// the time of the call.
func NewChanges(o ChangesOptions) (*Changes, error) {
	if o.DB == nil {
		return nil, errors.New("cluster: the change log needs the control connection")
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	m := &changeMetrics{
		applied:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_cluster_changes_applied_total", Help: "Changes applied by area and source (poll, message, local)."}, []string{"area", "source"}),
		lastOK:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "trove_cluster_poll_last_success_timestamp_seconds", Help: "End of the last successful change-log poll."}),
		pollErrs:  prometheus.NewCounter(prometheus.CounterOpts{Name: "trove_cluster_poll_errors_total", Help: "Change-log polls that failed."}),
		published: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_cluster_changes_published_total", Help: "Change messages published to Valkey by area."}, []string{"area"}),
		dropped:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_cluster_changes_dropped_total", Help: "Change messages not published because Valkey was unavailable, by area."}, []string{"area"}),
	}
	if err := register(o.Metrics, m.applied, m.lastOK, m.pollErrs, m.published, m.dropped); err != nil {
		return nil, err
	}
	c := &Changes{
		o: o, log: log, m: m, areas: map[string]*areaState{},
		kick: make(chan struct{}, 1), msgq: make(chan Message, maxMessageBacklog), pubq: make(chan pubItem, maxMessageBacklog),
	}
	c.sql = pgChangeSQL{}
	for _, a := range Areas(ChangeArea) {
		c.areas[a.Name] = &areaState{meta: a}
	}
	return c, nil
}

// AddListener registers a listener. Call it before Start.
func (c *Changes) AddListener(l Listener) {
	c.lmu.Lock()
	c.listeners = append(c.listeners, l)
	c.lmu.Unlock()
}

// Applied is the last epoch of the area that the poll applied on this replica,
// 0 for an area it does not follow. A cache fill reads it before its query and
// inserts only if it is unchanged afterwards (R-ARCH-018, "a fill never
// re-inserts an invalidated value"). It implements cache.Epochs.
func (c *Changes) Applied(area string) int64 {
	if st := c.areas[area]; st != nil {
		return st.applied.Load()
	}
	return 0
}

// AreaState exposes the epoch and the changed_at the poll applied last, for the
// owners that compare them with an index label (R-AUTHZ-028).
func (c *Changes) AreaState(area string) (epoch int64, changedAt time.Time) {
	st := c.areas[area]
	if st == nil {
		return 0, time.Time{}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.applied.Load(), st.at
}

// Fresh reports whether the last successful poll is younger than
// cluster.epoch_max_staleness: the condition of the `invalidation` readiness
// check. It depends on PostgreSQL only.
func (c *Changes) Fresh() bool {
	c.fmu.Lock()
	defer c.fmu.Unlock()
	return !c.lastEnd.IsZero() && time.Since(c.lastEnd) < c.o.Config.EpochMaxStaleness
}

// FreshWithin reports whether the last completed poll started less than d ago on
// the replica's monotonic clock. A poll reads every change committed before it
// started, so true proves that every change older than d is applied.
func (c *Changes) FreshWithin(d time.Duration) bool {
	c.fmu.Lock()
	defer c.fmu.Unlock()
	return !c.lastStart.IsZero() && time.Since(c.lastStart) < d
}

// Check is the `invalidation` readiness check (R-ARCH-018).
func (c *Changes) Check(context.Context) error {
	if c.Fresh() {
		return nil
	}
	return fmt.Errorf("the last successful change-log poll is older than cluster.epoch_max_staleness (%s)", c.o.Config.EpochMaxStaleness)
}

// Name implements the composition root's Component.
func (c *Changes) Name() string { return "changes" }

// Start runs the first poll, which sets every area's starting point to the
// current epoch (a replica started after a restore starts from the current
// epochs anyway), subscribes the Valkey channel and starts the loops. The
// first poll retries until PostgreSQL answers or ctx ends (R-ARCH-024 step 7).
func (c *Changes) Start(ctx context.Context) error {
	for {
		err := c.pollOnce(ctx)
		if err == nil {
			break
		}
		c.log.Warn("first change-log poll failed; retrying", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.o.Config.EpochPollInterval):
		}
	}
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.cancel = cancel
	c.lctx = lctx
	if c.o.Bus != nil {
		c.trySubscribe()
		// A reconnect of the subscriber may have lost messages: one poll at
		// once (R-ARCH-018). The first subscription needs none.
		c.o.Bus.OnSubscribed(func(reconnect bool) {
			if reconnect && !c.closed.Load() {
				c.wake()
			}
		})
	}
	c.wg.Add(3)
	go c.pollLoop(lctx)
	go c.messageLoop(lctx)
	go c.publishLoop(lctx)
	return nil
}

// Stop ends the loops. The poll runs through the drain and ends here, after
// the last request (R-ARCH-024 step 6).
func (c *Changes) Stop(context.Context) error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.smu.Lock()
	subs := c.subs
	c.smu.Unlock()
	for _, s := range subs {
		s()
	}
	c.wg.Wait()
	c.wmu.Lock()
	for _, w := range c.waits {
		w <- errors.New("the change log is stopped")
	}
	c.waits = nil
	c.wmu.Unlock()
	return nil
}

// trySubscribe subscribes the change channel once. It fails while the keyspace
// ID is unknown, and the poll loop tries again: without the subscription only
// the early eviction is missing, never a change.
func (c *Changes) trySubscribe() {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.o.Bus == nil || c.subscribed || c.closed.Load() {
		return
	}
	ch, err := c.o.Bus.channel(invChannels)
	if err == nil {
		var cancel func()
		if cancel, err = c.o.Bus.subscribe(c.lctx, ch, c.onMessage); err == nil {
			c.subs = append(c.subs, cancel)
			c.subscribed = true
			return
		}
	}
	if !c.warnedSub {
		c.warnedSub = true
		c.log.Warn("change messages not subscribed; changes propagate with the poll until they are", "error", err)
	}
}

func (c *Changes) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// PollNow triggers one immediate poll and waits for it. Concurrent triggers,
// and a trigger that meets a running poll, share the next poll (R-ARCH-018).
// It returns when the poll has applied every change committed before PollNow
// was called, or with its error.
func (c *Changes) PollNow(ctx context.Context) error {
	done := make(chan error, 1)
	c.wmu.Lock()
	c.waits = append(c.waits, done)
	c.wmu.Unlock()
	c.wake()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Changes) pollLoop(ctx context.Context) {
	defer c.wg.Done()
	loop := WatchLoop(c.o.Watchdog, "cluster.changes", c.o.WatchdogTimeout)
	defer loop.Stop()
	t := time.NewTicker(c.o.Config.EpochPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.kick:
		}
		c.wmu.Lock()
		waiters := c.waits
		c.waits = nil
		c.wmu.Unlock()
		err := c.pollOnce(ctx)
		if err != nil && ctx.Err() == nil {
			c.log.Warn("change-log poll failed", "error", err)
		}
		c.trySubscribe()
		for _, w := range waiters {
			w <- err
		}
		loop.Beat()
	}
}

// pollTimeout is the deadline of one poll: below the watchdog bound.
func (c *Changes) pollTimeout() time.Duration {
	return callTimeout(c.o.WatchdogTimeout)
}

// onMessage is the Pub/Sub handler. It runs on the reader goroutine of the
// subscription, so it only queues.
func (c *Changes) onMessage(m Message) {
	select {
	case c.msgq <- m:
	default: // the poll covers a dropped message
	}
}

type wireChange struct {
	A string          `json:"a"`
	E int64           `json:"e"`
	V int             `json:"v"`
	P json.RawMessage `json:"p"`
}

func (c *Changes) messageLoop(ctx context.Context) {
	defer c.wg.Done()
	loop := WatchLoop(c.o.Watchdog, "cluster.signals", c.o.WatchdogTimeout)
	defer loop.Stop()
	t := time.NewTicker(max(c.o.WatchdogTimeout/4, time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C: // the subscriber records progress while Valkey is unreachable
			loop.Beat()
		case m := <-c.msgq:
			c.handleMessage(m)
			loop.Beat()
		}
	}
}

// handleMessage applies a Valkey message as an early eviction. It never
// advances the applied epoch, so a lost, late, duplicated or forged message can
// neither suppress nor reorder a change: the poll applies every change again.
func (c *Changes) handleMessage(m Message) {
	var w wireChange
	if err := json.Unmarshal([]byte(m.Payload), &w); err != nil {
		return
	}
	st := c.areas[w.A]
	if st == nil {
		return
	}
	c.m.applied.WithLabelValues(w.A, sourceMessage).Inc()
	if w.V > st.meta.PayloadVersion || w.V < 1 {
		c.flushArea(w.A, FlushUnknownPayload)
		return
	}
	c.applyChange(w.A, w.V, w.P)
}

func (c *Changes) applyChange(area string, version int, payload json.RawMessage) {
	c.lmu.RLock()
	ls := c.listeners
	c.lmu.RUnlock()
	for _, l := range ls {
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.log.Error("change listener panicked", "area", area, "panic", fmt.Sprint(r))
				}
			}()
			l.ApplyChange(area, version, payload)
		}()
	}
}

func (c *Changes) flushArea(area, reason string) {
	c.lmu.RLock()
	ls := c.listeners
	c.lmu.RUnlock()
	for _, l := range ls {
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.log.Error("change listener panicked", "area", area, "panic", fmt.Sprint(r))
				}
			}()
			l.FlushArea(area, reason)
		}()
	}
}
