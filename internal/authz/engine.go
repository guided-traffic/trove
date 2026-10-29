package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// Changes is the part of *cluster.Changes the engine uses (R-AUTHZ-028): the
// change log delivers epochs, the engine rebuilds its index from them.
type Changes interface {
	// AddListener registers the engine for the changes of the areas it follows.
	AddListener(l cluster.Listener)
	// AreaState is the epoch and changed_at the change-log poll applied last; the
	// zero state is an area this replica does not follow.
	AreaState(area string) (epoch int64, changedAt time.Time)
	// FreshWithin reports whether the last poll started less than d ago.
	FreshWithin(d time.Duration) bool
	// Notify attaches a change of a change area to the writing transaction.
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Options configure [New].
type Options struct {
	// Source reads the inputs of the index; see NewPostgresSource.
	Source Source
	// Changes is the change log of this replica.
	Changes Changes
	// Config is the authz section; Audit the audit section (the
	// anonymous-denial switch of R-AUTHZ-029).
	Config config.Authz
	Audit  config.Audit
	// EpochPollInterval is cluster.epoch_poll_interval; New refuses an
	// authz.cache.max_staleness below three times it.
	EpochPollInterval time.Duration
	// RetryBudget is database.retry.max_elapsed: how long a request waits for a
	// confirmation of the index besides its own deadline (R-AUTHZ-028).
	RetryBudget time.Duration
	// AnonymousAccess reports whether anonymous access is on (R-AUTH-037); nil
	// means off. It is read once per anonymous request, with the request's
	// context; an error is a 503, not a "no".
	AnonymousAccess func(ctx context.Context) (bool, error)
	// Ceiling is the endpoint exposure (R-EP-013), evaluated on the index of the
	// request; nil means none. The composition root passes an [ExposureCeiling]
	// built from the configured endpoints; its endpoint names bound the label of
	// the exposure metrics.
	Ceiling Ceiling
	// Repositories is the snapshot reader the decision point asks for a repository
	// its index does not hold yet ([Engine.Know]); nil asks nobody.
	Repositories repository.Reader
	// Recorder records the audit events; nil discards them.
	Recorder audit.Recorder
	// Replica names this replica in audit details.
	Replica string
	Log     *slog.Logger
	Metrics prometheus.Registerer
}

// Engine is the decision point of one replica: it implements [Authorizer] on a
// compiled grant index and keeps the index current through the change log.
type Engine struct {
	o   Options
	log *slog.Logger
	m   *metrics
	rec audit.Recorder

	cur atomic.Pointer[Index]

	// Build coordination: at most one build runs and at most one waits behind it.
	kick     chan struct{}
	trigMu   sync.Mutex
	trigger  string
	hintRead Label // the epochs the pending message build was asked for
	building atomic.Bool
	flush    atomic.Bool
	fails    atomic.Int32

	// installed is closed and replaced whenever an index is installed; waiters
	// select on it.
	instMu    sync.Mutex
	installed chan struct{}

	epochs *epochReader
	hint   chan struct{}

	compile *patternCache

	lctx   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

var _ Authorizer = (*Engine)(nil)

// watchInterval is how often the engine compares the change log's applied
// epochs with its index. The comparison reads memory only.
const watchInterval = 50 * time.Millisecond

// epochReadGap is the shortest distance between two epoch reads of a replica
// (R-AUTHZ-032): at most 20 per second.
const epochReadGap = 50 * time.Millisecond

// New creates the engine. It builds nothing: Start does.
func New(o Options) (*Engine, error) {
	if o.Source == nil || o.Changes == nil {
		return nil, errors.New("authz: Source and Changes are required")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	if o.RetryBudget <= 0 {
		o.RetryBudget = 30 * time.Second
	}
	if o.EpochPollInterval > 0 && o.Config.Cache.MaxStaleness < config.MinAuthzStaleness*o.EpochPollInterval {
		return nil, fmt.Errorf("authz.cache.max_staleness (%s) is below %d times cluster.epoch_poll_interval (%s)",
			o.Config.Cache.MaxStaleness, config.MinAuthzStaleness, o.EpochPollInterval)
	}
	if o.Config.Cache.MaxStaleness <= 0 {
		return nil, errors.New("authz.cache.max_staleness must be positive")
	}
	if o.Config.Cache.SafetyTTL <= 0 {
		o.Config.Cache.SafetyTTL = 10 * time.Minute
	}
	if o.Config.Audit.Reads == "" {
		o.Config.Audit.Reads = config.AuthzReadsDenied
	}
	e := &Engine{
		o: o, log: o.Log, rec: o.Recorder,
		kick: make(chan struct{}, 1), installed: make(chan struct{}),
		hint:    make(chan struct{}, 1),
		compile: newPatternCache(),
	}
	e.epochs = newEpochReader(o.Source, epochReadGap, o.RetryBudget)
	var endpoints []string
	if ec, ok := o.Ceiling.(interface{ Endpoints() []string }); ok {
		endpoints = ec.Endpoints()
	}
	m, err := newMetrics(o.Metrics, e.stalenessSeconds, endpoints)
	if err != nil {
		return nil, err
	}
	e.m = m
	return e, nil
}

// Name implements the composition root's Component.
func (e *Engine) Name() string { return "authz" }

// Start builds the first index and starts the loops that keep it current. A
// build that fails stops the start-up: the replica must not serve without an
// index (R-AUTHZ-028 "Who builds it").
func (e *Engine) Start(ctx context.Context) error {
	e.lctx, e.cancel = context.WithCancel(context.WithoutCancel(ctx))
	e.epochs.bg = e.lctx
	start := time.Now()
	snap, err := e.o.Source.Snapshot(ctx)
	if err != nil {
		e.cancel()
		return fmt.Errorf("building the grant index: %w", err)
	}
	e.install(BuildIndex(snap, e.log), false, TriggerStartup, time.Since(start))
	e.o.Changes.AddListener(e)
	e.wg.Add(3)
	go e.buildLoop()
	go e.watchLoop()
	go e.hintLoop()
	return nil
}

// Stop ends the loops. The change-log poll
// is the cluster's and runs on until its own stop (R-AUTHZ-028 "Drain").
func (e *Engine) Stop(_ context.Context) error {
	e.once.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}
	})
	e.wg.Wait()
	return nil
}

// Index returns the installed index, nil before Start.
func (e *Engine) Index() *Index { return e.cur.Load() }

// Epoch returns the authz epoch of the installed index, 0 before Start.
func (e *Engine) Epoch() int64 {
	if x := e.cur.Load(); x != nil {
		return x.label.AuthzEpoch
	}
	return 0
}

// ApplyChange implements cluster.Listener. It is called from the change-log poll,
// from a Valkey message and from this replica's own commit, so it never trusts
// its input: it starts a coalesced read of the epochs from the database and
// builds only if they differ from the index's label (R-AUTHZ-028).
func (e *Engine) ApplyChange(area string, _ int, _ json.RawMessage) {
	if area != AreaAuthz && area != AreaRepoConfig {
		return
	}
	select {
	case e.hint <- struct{}{}:
	default:
	}
}

// FlushArea implements cluster.Listener: the poll found a gap, an unknown
// payload version or an epoch that went backwards. The next build is installed
// whatever its epochs.
func (e *Engine) FlushArea(area, _ string) {
	if area != AreaAuthz && area != AreaRepoConfig {
		return
	}
	e.flush.Store(true)
	e.requestBuild(TriggerFlush)
}

// requestBuild asks for a build. Requests coalesce: one build runs and one waits.
func (e *Engine) requestBuild(trigger string) {
	e.requestBuildHint(trigger, Label{})
}

// triggerRank orders the triggers of requests that coalesce into one build: the
// pending build keeps the strongest. A poll build may be skipped when the index
// matches the applied state, a message build when it matches the epochs it read,
// the others never.
func triggerRank(trigger string) int {
	switch trigger {
	case TriggerFlush:
		return 3
	case TriggerPoll:
		return 1
	case TriggerMessage:
		return 2
	default:
		return 2
	}
}

// requestBuildHint is requestBuild with the epochs a message build was asked
// for; a later request of a stronger trigger replaces the pending one.
func (e *Engine) requestBuildHint(trigger string, read Label) {
	e.trigMu.Lock()
	if e.trigger == "" || triggerRank(trigger) > triggerRank(e.trigger) {
		e.trigger = trigger
	}
	if trigger == TriggerMessage {
		e.hintRead = read
	}
	e.trigMu.Unlock()
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

func (e *Engine) takeTrigger() (string, Label) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	t := e.trigger
	e.trigger = ""
	return t, e.hintRead
}

// buildLoop runs the builds one after another.
func (e *Engine) buildLoop() {
	defer e.wg.Done()
	for {
		select {
		case <-e.lctx.Done():
			return
		case <-e.kick:
		}
		trig, read := e.takeTrigger()
		if trig == "" {
			continue
		}
		e.buildOnce(trig, read)
	}
}

// buildOnce is one build and its installation. read is the label a message build
// was asked for.
func (e *Engine) buildOnce(trigger string, read Label) {
	e.building.Store(true)
	defer e.building.Store(false)
	switch {
	case e.flush.Load():
	case trigger == TriggerPoll && e.labelMatchesChanges(e.cur.Load()):
		return // a request for a state that an earlier build already installed
	case trigger == TriggerMessage && e.cur.Load() != nil && read.Equal(e.cur.Load().label):
		return // the index is the state the database had when the message arrived
	}
	needFlush := e.flush.Swap(false)
	start := time.Now()
	snap, err := e.o.Source.Snapshot(e.lctx)
	if err != nil {
		if needFlush {
			e.flush.Store(true)
		}
		if e.lctx.Err() != nil {
			return
		}
		n := e.fails.Add(1)
		e.log.Warn("authz: the grant index build failed", "trigger", trigger, "err", err, "consecutive", n)
		backoff := min(time.Duration(n)*250*time.Millisecond, 2*time.Second)
		time.AfterFunc(backoff, func() {
			if e.lctx.Err() == nil {
				e.requestBuild(trigger)
			}
		})
		return
	}
	e.fails.Store(0)
	e.install(BuildIndex(snap, e.log), needFlush, trigger, time.Since(start))
}

// install swaps a finished index in. A build is installed only if its epochs are
// not lower than those of the installed index, so a slow build never replaces a
// newer one; after a flush the next build is installed whatever its epochs
// (R-AUTHZ-028).
func (e *Engine) install(idx *Index, force bool, trigger string, took time.Duration) {
	cur := e.cur.Load()
	if cur != nil && !force && (idx.label.AuthzEpoch < cur.label.AuthzEpoch || idx.label.RepoEpoch < cur.label.RepoEpoch) {
		e.m.discarded.Inc()
		return
	}
	e.cur.Store(idx)
	e.m.epoch.Set(float64(idx.label.AuthzEpoch))
	e.m.setExposureRules(idx)
	e.m.rebuild.Observe(took.Seconds())
	e.m.rebuilds.WithLabelValues(trigger).Inc()
	e.instMu.Lock()
	close(e.installed)
	e.installed = make(chan struct{})
	e.instMu.Unlock()
}

func (e *Engine) installedCh() <-chan struct{} {
	e.instMu.Lock()
	defer e.instMu.Unlock()
	return e.installed
}

// appliedLabel is the state the change-log poll applied last; known is false for
// an area this replica does not follow.
func (e *Engine) appliedLabel() (l Label, authzKnown, repoKnown bool) {
	ae, aat := e.o.Changes.AreaState(AreaAuthz)
	re, rat := e.o.Changes.AreaState(AreaRepoConfig)
	return Label{AuthzEpoch: ae, AuthzAt: aat, RepoEpoch: re, RepoAt: rat}, !aat.IsZero(), !rat.IsZero()
}

// labelMatchesChanges reports whether the index is the state the poll applied
// last, for the areas this replica follows.
func (e *Engine) labelMatchesChanges(idx *Index) bool {
	if idx == nil {
		return false
	}
	l, ak, rk := e.appliedLabel()
	if ak && (l.AuthzEpoch != idx.label.AuthzEpoch || !l.AuthzAt.Equal(idx.label.AuthzAt)) {
		return false
	}
	if rk && (l.RepoEpoch != idx.label.RepoEpoch || !l.RepoAt.Equal(idx.label.RepoAt)) {
		return false
	}
	return true
}

// confirmed implements "Confirmation and staleness" of R-AUTHZ-028: the index is
// confirmed at the start of its own build, or at the start of the last poll that
// found the epochs and their changed_at equal to its label. A replica whose
// poll cannot reach PostgreSQL, or whose needed build fails, stops confirming;
// Valkey plays no part.
func (e *Engine) confirmed(idx *Index) bool {
	max := e.o.Config.Cache.MaxStaleness
	if time.Since(idx.started) < max {
		return true
	}
	return e.labelMatchesChanges(idx) && e.o.Changes.FreshWithin(max)
}

// stalenessSeconds is trove_authz_index_staleness_seconds: the time since the
// installed index was last confirmed. It is exact only up to the resolution of
// the poll, which is what the bound is stated in.
func (e *Engine) stalenessSeconds() float64 {
	idx := e.cur.Load()
	if idx == nil {
		return 0
	}
	if e.labelMatchesChanges(idx) && e.o.Changes.FreshWithin(time.Second) {
		return 0
	}
	return time.Since(idx.started).Seconds()
}

// watchLoop compares the epochs the poll applied with the index and requests a
// build when they differ, and starts the safety rebuild (R-AUTHZ-028 "When a
// replica rebuilds"). It reads memory only.
func (e *Engine) watchLoop() {
	defer e.wg.Done()
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	ttl := e.o.Config.Cache.SafetyTTL
	jittered := ttl + time.Duration(float64(ttl)*0.1*(rand.Float64()*2-1)) //nolint:gosec // jitter, not a secret
	for {
		select {
		case <-e.lctx.Done():
			return
		case <-t.C:
		}
		idx := e.cur.Load()
		if idx == nil || e.building.Load() {
			continue
		}
		switch {
		case !e.labelMatchesChanges(idx):
			e.requestBuild(TriggerPoll)
		case time.Since(idx.started) > jittered:
			e.requestBuild(TriggerSafety)
			jittered = ttl + time.Duration(float64(ttl)*0.1*(rand.Float64()*2-1)) //nolint:gosec // jitter, not a secret
		}
	}
}

// hintLoop turns the listener calls into epoch reads: whatever a change message
// claims, the database decides whether the index is behind.
func (e *Engine) hintLoop() {
	defer e.wg.Done()
	for {
		select {
		case <-e.lctx.Done():
			return
		case <-e.hint:
		}
		ctx, cancel := context.WithTimeout(e.lctx, e.o.RetryBudget)
		l, err := e.epochs.read(ctx, time.Now())
		cancel()
		if err != nil {
			continue // the watch loop and the next poll cover a failed read
		}
		if idx := e.cur.Load(); idx == nil || !l.Equal(idx.label) {
			// The build is the message's own: the poll has not applied this change yet
			// (a replica's own commit is announced at once), so a poll build would
			// find the index equal to the applied state and skip, while the index
			// still lacks the write.
			e.requestBuildHint(TriggerMessage, l)
		}
	}
}

// ErrNotStarted is returned by helpers that need an index before Start.
var ErrNotStarted = errors.New("authz: the engine has not been started")

// Refresh builds an index now and installs it, bypassing the loops. Tests and
// the commands that write and then evaluate use it.
func (e *Engine) Refresh(ctx context.Context) error {
	start := time.Now()
	snap, err := e.o.Source.Snapshot(ctx)
	if err != nil {
		return err
	}
	e.install(BuildIndex(snap, e.log), true, TriggerMinEpoch, time.Since(start))
	return nil
}

// epochReader shares the database epoch reads of a replica: concurrent callers
// join one read, a read answers only callers that arrived before it started, and
// reads start at most every gap (R-AUTHZ-032).
type epochReader struct {
	src    Source
	gap    time.Duration
	budget time.Duration
	bg     context.Context

	mu       sync.Mutex
	inflight *epochCall
	last     time.Time
}

type epochCall struct {
	start time.Time
	done  chan struct{}
	label Label
	err   error
}

func newEpochReader(src Source, gap, budget time.Duration) *epochReader {
	return &epochReader{src: src, gap: gap, budget: budget, bg: context.Background()}
}

// read returns the epochs from a read that started after arrived.
func (r *epochReader) read(ctx context.Context, arrived time.Time) (Label, error) {
	for {
		r.mu.Lock()
		c := r.inflight
		if c == nil {
			if wait := r.gap - time.Since(r.last); wait > 0 {
				r.mu.Unlock()
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return Label{}, ctx.Err()
				}
			}
			c = &epochCall{start: time.Now(), done: make(chan struct{})}
			r.inflight, r.last = c, c.start
			r.mu.Unlock()
			go r.run(c)
		} else {
			r.mu.Unlock()
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			return Label{}, ctx.Err()
		}
		if !c.start.Before(arrived) {
			return c.label, c.err
		}
		// The read had started before the caller arrived: it may predate the
		// caller's write. Wait for the next one.
	}
}

func (r *epochReader) run(c *epochCall) {
	ctx, cancel := context.WithTimeout(r.bg, r.budget)
	defer cancel()
	c.label, c.err = r.src.Epochs(ctx)
	r.mu.Lock()
	r.inflight = nil
	r.mu.Unlock()
	close(c.done)
}
