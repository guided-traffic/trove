package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// WorkerSignals is the signal path a worker needs: the wake-up subscription and
// what a [Queue] needs. *cluster.Signals implements it.
type WorkerSignals interface {
	Signals
	Subscribe(ctx context.Context, area string, h cluster.SignalHandler) (cancel func(), err error)
}

// Leaser holds the scheduler lease. *cluster.Leases implements it.
type Leaser interface {
	Hold(id cluster.LeaseID, work func(ctx context.Context, h *cluster.Held))
	Release(ctx context.Context, id cluster.LeaseID)
}

// WorkerOptions configure [NewWorker].
type WorkerOptions struct {
	// Role is the replica's role; only worker and all may claim.
	Role string
	// Instance is the replica's name, for logs.
	Instance string
	// UID is the replica's instance_uid (a UUID): the value of job.locked_by.
	UID string
	// Main is the request pool: claims, completions and handlers.
	Main q.DBTX
	// Control is the control connection: the job heartbeat (R-ARCH-016), so a
	// pool drained by busy handlers cannot make a live worker lose its jobs.
	Control q.DBTX

	Registry    *Registry
	Config      config.Jobs
	Maintenance config.Maintenance
	// Signals wakes the claim loop; nil means polling only.
	Signals WorkerSignals
	// Leases holds the scheduler lease; nil means this worker never schedules.
	Leases Leaser
	// Ready reports whether the replica is ready (every gating check of
	// R-OPS-001 passes). The claim loop claims only while it is (Q-OPS-12). Nil
	// means always.
	Ready func() bool

	Watchdog        cluster.Watchdog
	WatchdogTimeout time.Duration
	Log             *slog.Logger
	Metrics         prometheus.Registerer
	// Wrap lets the composition root put tracing and request-scoped logging
	// around a handler: it receives the job and the function that runs it.
	Wrap func(ctx context.Context, j Job, run func(context.Context) error) error
	// Sources are schedule sources besides the registered tasks, such as the
	// cleanup policies other pages keep in PostgreSQL.
	Sources []ScheduleSource
}

// Worker runs the claim loop, the heartbeat, the reaper and, while this replica
// holds the scheduler lease, the scheduler (R-ARCH-016). It implements the
// composition root's Component, Drainer, Settler and Quiescer.
type Worker struct {
	o   WorkerOptions
	q   *Queue
	log *slog.Logger
	m   *metrics

	queues []string       // queues with at least one slot, sorted
	slots  map[string]int // slots per queue
	kinds  []string

	root   context.Context
	cancel context.CancelCauseFunc
	loops  sync.WaitGroup
	runWG  sync.WaitGroup

	mu       sync.Mutex
	running  map[string]*runningJob
	perQueue map[string]int
	results  map[string]string

	draining  atomic.Bool
	wake      chan struct{}
	started   atomic.Bool
	subMu     sync.Mutex
	stopSub   func()
	subWarned bool
	tasks     []scheduledTask
}

type runningJob struct {
	job    Job
	cancel context.CancelCauseFunc
	// validUntil is a point on the replica's monotonic clock before which the
	// database's locked_until is surely in the future: the time the last
	// extension was sent plus the lease duration. Replica clocks are never
	// compared with the database's, only measured.
	validUntil time.Time
}

// NewWorker creates a worker. A role that does not run workers gets
// [ErrNotWorker]: a web replica cannot construct a claimer (R-ARCH-001).
func NewWorker(o WorkerOptions) (*Worker, error) {
	if !meta.RunsWorkers(o.Role) {
		return nil, fmt.Errorf("%w (role %q)", ErrNotWorker, o.Role)
	}
	if o.Main == nil || o.Control == nil {
		return nil, errors.New("jobs: a worker needs the main pool and the control connection")
	}
	if o.UID == "" {
		return nil, errors.New("jobs: a worker needs the replica's instance_uid")
	}
	if o.Registry == nil {
		o.Registry = Default
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	m, err := newMetrics(o.Metrics)
	if err != nil {
		return nil, err
	}
	w := &Worker{
		o: o, log: o.Log.With("component", "jobs"), m: m,
		slots: map[string]int{}, running: map[string]*runningJob{}, perQueue: map[string]int{},
		results: map[string]string{}, wake: make(chan struct{}, 1),
	}
	w.q = NewQueue(QueueOptions{DB: o.Main, Registry: o.Registry, Signals: o.Signals, Log: o.Log})
	return w, nil
}

// Queue returns the worker's queue, for enqueues from the same replica.
func (w *Worker) Queue() *Queue { return w.q }

// Name implements the composition root's Component.
func (w *Worker) Name() string { return "jobs" }

// Start validates the registered kinds and the task schedules, subscribes the
// wake-up signal and starts the loops. A kind in a queue without worker slots
// and a bad task schedule are configuration errors (a plain error return).
func (w *Worker) Start(ctx context.Context) error {
	for _, k := range w.o.Registry.Kinds() {
		spec, _ := w.o.Registry.Kind(k)
		n, known := w.o.Config.Workers.Count(spec.Queue)
		if !known {
			return fmt.Errorf("jobs: kind %q uses queue %q, which is not one of %v", k, spec.Queue, w.o.Config.Workers.Queues())
		}
		if n > 0 {
			w.kinds = append(w.kinds, k)
		}
	}
	for _, name := range w.o.Config.Workers.Queues() {
		if n, _ := w.o.Config.Workers.Count(name); n > 0 {
			w.queues = append(w.queues, name)
			w.slots[name] = n
		}
	}
	sort.Strings(w.queues)
	tasks, err := w.resolveTasks()
	if err != nil {
		return err
	}
	w.tasks = tasks

	w.root, w.cancel = context.WithCancelCause(context.WithoutCancel(ctx))
	w.trySubscribe()
	if w.o.Leases != nil && (len(w.tasks) > 0 || len(w.o.Sources) > 0) {
		w.o.Leases.Hold(cluster.LeaseScheduler, w.schedulerWork)
	}
	w.started.Store(true)
	w.loops.Add(3)
	go w.claimLoop()
	go w.heartbeatLoop()
	go w.reaperLoop()
	return nil
}

// trySubscribe subscribes the wake-up signal once. The wake-up is an
// optimisation and jobs.poll_interval bounds pick-up, so a failure (the keyspace
// ID is not known yet) is retried by the claim loop instead of being final.
func (w *Worker) trySubscribe() {
	w.subMu.Lock()
	defer w.subMu.Unlock()
	if w.o.Signals == nil || w.stopSub != nil || w.root.Err() != nil {
		return
	}
	stop, err := w.o.Signals.Subscribe(w.root, cluster.AreaJobs, func([]string, bool) { w.kick() })
	if err != nil {
		if !w.subWarned {
			w.subWarned = true
			w.log.Warn("job wake-ups not subscribed; relying on jobs.poll_interval until they are", "error", err)
		}
		return
	}
	w.stopSub = stop
}

func (w *Worker) kick() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// callTimeout is the deadline of every call a watched loop makes: below
// health.watchdog_timeout (R-ARCH-016).
func (w *Worker) callTimeout() time.Duration { return cluster.CallTimeout(w.o.WatchdogTimeout) }

// Drain stops claiming at once and gives up the scheduler lease, so another
// worker schedules from its next retry instead of after this drain
// (R-ARCH-024 step 1). It does not wait for running jobs.
func (w *Worker) Drain(ctx context.Context) {
	w.draining.Store(true)
	if w.o.Leases != nil && w.started.Load() {
		w.o.Leases.Release(ctx, cluster.LeaseScheduler)
	}
}

// Settle waits for the running jobs to finish (R-ARCH-024 step 3).
func (w *Worker) Settle(ctx context.Context) error {
	done := make(chan struct{})
	go func() { w.runWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return nil // the jobs are cancelled and released by Quiesce
	}
}

// Quiesce cancels the handlers still running (5 s before the shutdown deadline,
// R-ARCH-024 step 4). Each persists its checkpoint and returns, and its job is
// released without counting the attempt (R-ARCH-016).
func (w *Worker) Quiesce(ctx context.Context) {
	w.draining.Store(true)
	w.mu.Lock()
	for _, rj := range w.running {
		rj.cancel(ErrReleased)
	}
	w.mu.Unlock()
	done := make(chan struct{})
	go func() { w.runWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Stop ends the loops and releases whatever still runs.
func (w *Worker) Stop(ctx context.Context) error {
	if !w.started.Load() {
		return nil
	}
	w.draining.Store(true)
	w.cancel(ErrReleased)
	w.subMu.Lock()
	stopSub := w.stopSub
	w.subMu.Unlock()
	if stopSub != nil {
		stopSub()
	}
	done := make(chan struct{})
	go func() { w.loops.Wait(); w.runWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jobs: %d handlers still running at the end of the shutdown", w.runningCount())
	}
}

func (w *Worker) runningCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.running)
}

func (w *Worker) ready() bool { return w.o.Ready == nil || w.o.Ready() }

// claimLoop claims jobs while the replica is ready and not draining. It records
// watchdog progress at the end of every iteration, also while every worker is
// busy or the replica is unready, so liveness never fails because readiness does
// (R-ARCH-016, Q-OPS-12). The wait for a wake-up ends after jobs.poll_interval.
func (w *Worker) claimLoop() {
	defer w.loops.Done()
	loop := cluster.WatchLoop(w.o.Watchdog, "jobs.claim", w.o.WatchdogTimeout)
	defer loop.Stop()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		w.trySubscribe()
		if !w.draining.Load() && w.ready() {
			w.claim()
		}
		loop.Beat()
		timer.Reset(w.o.Config.PollInterval)
		select {
		case <-w.root.Done():
			return
		case <-w.wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// claim claims jobs into the free worker slots of every queue with one
// statement (the claim of R-ARCH-016, FOR UPDATE SKIP LOCKED).
func (w *Worker) claim() {
	w.mu.Lock()
	var queues []string
	var slots []int32
	for _, name := range w.queues {
		if free := w.slots[name] - w.perQueue[name]; free > 0 {
			queues = append(queues, name)
			slots = append(slots, int32(free)) //nolint:gosec // small
		}
	}
	w.mu.Unlock()
	if len(queues) == 0 || len(w.kinds) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(w.root, w.callTimeout())
	defer cancel()
	sent := time.Now()
	rows, err := q.New(w.o.Main).ClaimJobs(ctx, q.ClaimJobsParams{
		LockedBy: w.o.UID, LeaseSecs: w.o.Config.LeaseDuration.Seconds(), Queues: queues, Kinds: w.kinds, Slots: slots,
	})
	if err != nil {
		if w.root.Err() == nil {
			w.m.claimErrs.Inc()
			w.log.Warn("claim failed", "error", err)
		}
		return
	}
	for _, r := range rows {
		w.start(fromRow(r), sent)
	}
}

func (w *Worker) start(j Job, claimSent time.Time) {
	ctx, cancel := context.WithCancelCause(w.root)
	rj := &runningJob{job: j, cancel: cancel, validUntil: claimSent.Add(w.o.Config.LeaseDuration)}
	w.mu.Lock()
	w.running[j.ID] = rj
	w.perQueue[j.Queue]++
	w.mu.Unlock()
	w.runWG.Add(1)
	w.m.claimed.WithLabelValues(j.Kind).Inc()
	w.m.running.WithLabelValues(j.Kind).Inc()
	go w.execute(ctx, rj)
}

func (w *Worker) setResult(id, result string) {
	w.mu.Lock()
	w.results[id] = result
	w.mu.Unlock()
}

// execute runs one attempt and settles the job.
func (w *Worker) execute(ctx context.Context, rj *runningJob) {
	j := rj.job
	defer w.runWG.Done()
	started := time.Now()
	spec, _ := w.o.Registry.Kind(j.Kind)
	if spec.Timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeoutCause(ctx, spec.Timeout, ErrTimeout)
		defer stop()
	}
	run := &Run{Job: j, w: w}
	call := func(ctx context.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("handler panicked: %v", r)
			}
		}()
		return spec.Handler(ctx, run)
	}
	var err error
	if w.o.Wrap != nil {
		err = w.o.Wrap(ctx, j, call)
	} else {
		err = call(ctx)
	}
	cause := context.Cause(ctx)
	w.settle(j, err, cause, time.Since(started))

	w.mu.Lock()
	delete(w.running, j.ID)
	delete(w.results, j.ID)
	w.perQueue[j.Queue]--
	w.mu.Unlock()
	w.m.running.WithLabelValues(j.Kind).Dec()
	w.kick() // a freed slot may take the next queued job at once
}

// settle writes the end of an attempt with a fresh context: the handler's is
// cancelled on a release. Every write names the worker as the owner, so a worker
// that lost the job writes nothing.
func (w *Worker) settle(j Job, err error, cause error, took time.Duration) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(w.root), 15*time.Second)
	defer cancel()
	qs := q.New(w.o.Main)
	outcome := OutcomeSucceeded
	log := w.log.With("job", j.ID, "kind", j.Kind, "attempt", j.Attempt)

	switch {
	case err == nil:
		w.mu.Lock()
		result := w.results[j.ID]
		w.mu.Unlock()
		n, e := qs.CompleteJob(ctx, q.CompleteJobParams{Result: result, ID: j.ID, LockedBy: w.o.UID})
		if e != nil {
			log.Error("job completion not written; the lease will expire and the job run again", "error", e)
		} else if n == 0 {
			outcome = OutcomeLost
			log.Warn("job finished but this worker no longer owns it")
		}
	case errors.Is(cause, ErrLeaseLost):
		outcome = OutcomeLost
		log.Warn("job stopped: the lease was lost", "error", err)
	case errors.Is(cause, ErrCancelRequested):
		outcome = OutcomeCancelled
		if _, e := qs.EndJobCancelled(ctx, q.EndJobCancelledParams{Error: `{"code":"cancelled"}`, ID: j.ID, LockedBy: w.o.UID}); e != nil {
			log.Error("job cancellation not written", "error", e)
		}
	case errors.Is(cause, ErrReleased):
		outcome = OutcomeReleased
		states, e := qs.ReleaseJob(ctx, q.ReleaseJobParams{ID: j.ID, LockedBy: w.o.UID})
		if e != nil {
			log.Error("job not released; the lease will expire", "error", e)
		} else if len(states) == 1 && states[0] == StateCancelled {
			outcome = OutcomeCancelled
		}
	default:
		code, perm := "job_failed", IsPermanent(err)
		if errors.Is(cause, ErrTimeout) {
			code = "job_timeout"
		}
		body, _ := json.Marshal(map[string]any{"code": code, "detail": truncate(err.Error(), 2000), "attempt": j.Attempt})
		rows, e := qs.FailOrRetryJob(ctx, q.FailOrRetryJobParams{Error: string(body), Permanent: perm, ID: j.ID, LockedBy: w.o.UID})
		switch {
		case e != nil:
			log.Error("job failure not written; the lease will expire", "error", e)
			outcome = OutcomeFailed
		case len(rows) == 0:
			outcome = OutcomeLost
		case rows[0].State == StateQueued:
			outcome = OutcomeRetried
			log.Warn("job attempt failed; it will run again after the backoff", "error", err)
		default:
			outcome = OutcomeFailed
			log.Error("job failed", "error", err, "state", rows[0].State)
		}
	}
	w.m.completed.WithLabelValues(j.Kind, outcome).Inc()
	w.m.duration.WithLabelValues(j.Kind).Observe(took.Seconds())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// heartbeatLoop extends the leases of all running jobs with one statement per
// jobs.heartbeat_interval, on the control connection (R-ARCH-016). A job the
// statement no longer returns was lost; one in state cancelling was cancelled. A
// failing extension cancels a handler only once its lease could have run out.
func (w *Worker) heartbeatLoop() {
	defer w.loops.Done()
	t := time.NewTicker(w.o.Config.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-w.root.Done():
			return
		case <-t.C:
		}
		w.heartbeat()
	}
}

func (w *Worker) heartbeat() {
	w.mu.Lock()
	n := len(w.running)
	w.mu.Unlock()
	if n == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(w.root, w.callTimeout())
	defer cancel()
	sent := time.Now()
	rows, err := q.New(w.o.Control).HeartbeatJobs(ctx, q.HeartbeatJobsParams{
		LockedBy: w.o.UID, LeaseSecs: w.o.Config.LeaseDuration.Seconds(),
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if w.root.Err() != nil {
			return
		}
		w.log.Warn("job heartbeat failed", "error", err)
		for _, rj := range w.running {
			if time.Until(rj.validUntil) < w.o.Config.HeartbeatInterval {
				rj.cancel(ErrLeaseLost)
			}
		}
		return
	}
	owned := make(map[string]string, len(rows))
	for _, r := range rows {
		owned[r.ID.String()] = r.State
	}
	for id, rj := range w.running {
		state, ok := owned[id]
		switch {
		case !ok:
			rj.cancel(ErrLeaseLost)
		case state == StateCancelling:
			rj.cancel(ErrCancelRequested)
		default:
			rj.validUntil = sent.Add(w.o.Config.LeaseDuration)
		}
	}
}
