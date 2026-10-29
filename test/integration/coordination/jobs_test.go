//go:build integration

package coordination

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

// recorder collects what the handlers of all replicas of a test did.
type recorder struct {
	mu   sync.Mutex
	runs map[string][]run
}

type run struct {
	replica string
	attempt int
	at      time.Time
}

func newRecorder() *recorder { return &recorder{runs: map[string][]run{}} }

func (r *recorder) add(id string, x run) {
	r.mu.Lock()
	r.runs[id] = append(r.runs[id], x)
	r.mu.Unlock()
}

func (r *recorder) of(id string) []run {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]run(nil), r.runs[id]...)
}

// enqueueTx enqueues in a transaction of the replica, as a request handler does.
func (rep *replica) enqueueTx(ctx context.Context, spec jobs.EnqueueSpec) (jobs.Result, error) {
	var res jobs.Result
	err := rep.db.Tx(ctx, func(tx meta.Tx) error {
		var err error
		res, err = rep.queue.Enqueue(ctx, tx, spec)
		return err
	})
	return res, err
}

func (e *env) job(rep *replica, id string) jobs.Job {
	e.t.Helper()
	j, ok, err := rep.queue.Get(context.Background(), id)
	if err != nil || !ok {
		e.t.Fatalf("job %s: %v %v", id, ok, err)
	}
	return j
}

// waitRetry waits until the job has failed an attempt and sits in queued again.
func (e *env) waitRetry(rep *replica, id string, attempt int) jobs.Job {
	e.t.Helper()
	var j jobs.Job
	waitFor(e.t, "job "+id+" queued again after attempt "+string(rune('0'+attempt)), 10*time.Second, func() bool {
		j = e.job(rep, id)
		return j.State == jobs.StateQueued && j.Attempt == attempt
	})
	return j
}

func (e *env) waitState(rep *replica, id string, want string, within time.Duration) jobs.Job {
	e.t.Helper()
	var j jobs.Job
	waitFor(e.t, "job "+id+" reaching "+want, within, func() bool {
		j = e.job(rep, id)
		return j.State == want
	})
	return j
}

func TestJobs(t *testing.T) {
	e := newEnv(t)
	rec := newRecorder()
	gate := make(chan struct{})
	var gateOnce sync.Once
	openGate := func() { gateOnce.Do(func() { close(gate) }) }
	defer openGate()
	var coalesced, inCoalesce, maxCoalesce atomic.Int64
	causes := make(chan error, 8)

	kinds := func(r *jobs.Registry, rep *replica) {
		quick := func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			return nil
		}
		r.RegisterKind("it.quick", jobs.KindSpec{Handler: quick})
		r.RegisterKind("it.slow", jobs.KindSpec{Handler: func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}})
		r.RegisterKind("it.gated", jobs.KindSpec{DedupMode: jobs.Exclusive, Handler: func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}})
		r.RegisterKind("it.coalesce", jobs.KindSpec{DedupMode: jobs.Coalesce, Handler: func(ctx context.Context, run *jobs.Run) error {
			// A rebuild takes longer than the burst of enqueues (R-ARCH-016's criterion).
			if n := inCoalesce.Add(1); n > maxCoalesce.Load() {
				maxCoalesce.Store(n)
			}
			coalesced.Add(1)
			time.Sleep(1500 * time.Millisecond)
			inCoalesce.Add(-1)
			return nil
		}})
		r.RegisterKind("it.flaky", jobs.KindSpec{MaxAttempts: 3, Handler: func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			if run.Attempt < 2 {
				return errors.New("transient failure")
			}
			return nil
		}})
		r.RegisterKind("it.permanent", jobs.KindSpec{MaxAttempts: 5, Handler: func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			return jobs.Permanent(errors.New("cannot work"))
		}})
		r.RegisterKind("it.exhaust", jobs.KindSpec{MaxAttempts: 2, Handler: func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			return errors.New("always failing")
		}})
		r.RegisterKind("it.hang", jobs.KindSpec{Handler: func(ctx context.Context, run *jobs.Run) error {
			rec.add(run.ID, run_(rep.name, run.Attempt))
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return ctx.Err()
		}})
		r.RegisterKind("it.panic", jobs.KindSpec{MaxAttempts: 1, Handler: func(ctx context.Context, run *jobs.Run) error {
			panic("handler bug")
		}})
		r.RegisterKind("it.timeout", jobs.KindSpec{MaxAttempts: 1, Timeout: 300 * time.Millisecond, Handler: func(ctx context.Context, run *jobs.Run) error {
			<-ctx.Done()
			return ctx.Err()
		}})
	}
	var ready atomic.Bool
	ready.Store(true)
	web := e.newReplica("jobs-web", replicaOpts{role: "web", valkey: vkLive, kinds: kinds})
	a := e.newReplica("jobs-a", replicaOpts{role: "worker", valkey: vkLive, kinds: kinds, ready: ready.Load})
	b := e.newReplica("jobs-b", replicaOpts{role: "worker", valkey: vkLive, kinds: kinds})
	waitValkey(t, web)
	waitValkey(t, a)
	waitValkey(t, b)
	ctx := context.Background()

	t.Run("R-ARCH-001 a web replica cannot construct a claimer", func(t *testing.T) {
		_, err := jobs.NewWorker(jobs.WorkerOptions{Role: "web", UID: web.uid, Main: web.db.Main(), Control: web.db.ControlPool(), Registry: web.jobsReg})
		if !errors.Is(err, jobs.ErrNotWorker) {
			t.Fatalf("NewWorker(web) = %v", err)
		}
	})

	t.Run("R-ARCH-016 jobs enqueued on a web replica run on workers; locked_by never names a web replica", func(t *testing.T) {
		seen := map[string]bool{}
		var mu sync.Mutex
		stop := make(chan struct{})
		done := make(chan struct{})
		sample := e.st.admin(t, e.db)
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				case <-time.After(10 * time.Millisecond):
				}
				rows, err := sample.Query(ctx, `SELECT DISTINCT locked_by::text FROM job WHERE locked_by IS NOT NULL`)
				if err != nil {
					continue
				}
				for rows.Next() {
					var u string
					_ = rows.Scan(&u)
					mu.Lock()
					seen[u] = true
					mu.Unlock()
				}
				rows.Close()
			}
		}()
		var ids []string
		for range 24 {
			res, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.slow"})
			if err != nil || !res.Created {
				t.Fatalf("enqueue: %+v %v", res, err)
			}
			ids = append(ids, res.Job.ID)
		}
		for _, id := range ids {
			e.waitState(web, id, jobs.StateSucceeded, 30*time.Second)
		}
		close(stop)
		<-done
		mu.Lock()
		defer mu.Unlock()
		if seen[web.uid] {
			t.Fatalf("the web replica %s appears in job.locked_by", web.uid)
		}
		if len(seen) == 0 {
			t.Fatal("the sampler saw no running job")
		}
		for u := range seen {
			if u != a.uid && u != b.uid {
				t.Errorf("locked_by %s is neither worker", u)
			}
		}
		ranOn := map[string]bool{}
		for _, id := range ids {
			for _, r := range rec.of(id) {
				ranOn[r.replica] = true
			}
		}
		t.Logf("24 jobs ran on %v; locked_by values seen: %d", ranOn, len(seen))
		if ranOn["jobs-web"] {
			t.Fatal("a job ran on the web replica")
		}
	})

	t.Run("R-ARCH-016 an enqueue that is rolled back leaves no job and no wake-up", func(t *testing.T) {
		before := e.scalar(`SELECT count(*) FROM job`)
		err := web.db.Tx(ctx, func(tx meta.Tx) error {
			if _, err := web.queue.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: "it.quick"}); err != nil {
				return err
			}
			return errors.New("the change failed")
		})
		if err == nil {
			t.Fatal("no error")
		}
		if after := e.scalar(`SELECT count(*) FROM job`); after != before {
			t.Fatalf("a rolled-back enqueue left %d rows", after-before)
		}
	})

	t.Run("Q-STOR-13 an exclusive key admits one job; a conflicting enqueue returns the existing one", func(t *testing.T) {
		first, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.gated", DedupKey: "gc"})
		if err != nil || !first.Created {
			t.Fatalf("first: %+v %v", first, err)
		}
		e.waitState(web, first.Job.ID, jobs.StateRunning, 10*time.Second)
		second, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.gated", DedupKey: "gc"})
		if err != nil {
			t.Fatal(err)
		}
		if second.Created || second.Job.ID != first.Job.ID || second.Job.State != jobs.StateRunning {
			t.Fatalf("second = %+v, want the running job %s", second, first.Job.ID)
		}
		other, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.gated", DedupKey: "gc-other"})
		if err != nil || !other.Created {
			t.Fatalf("a different key was refused: %+v %v", other, err)
		}
		openGate()
		e.waitState(web, first.Job.ID, jobs.StateSucceeded, 10*time.Second)
		e.waitState(web, other.Job.ID, jobs.StateSucceeded, 10*time.Second)
		third, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.gated", DedupKey: "gc"})
		if err != nil || !third.Created || third.Job.ID == first.Job.ID {
			t.Fatalf("after the job ended a new enqueue must create one: %+v %v", third, err)
		}
		e.waitState(web, third.Job.ID, jobs.StateSucceeded, 10*time.Second)
	})

	t.Run("R-ARCH-016 100 coalesced enqueues within a second produce at most two executions", func(t *testing.T) {
		coalesced.Store(0)
		var wg sync.WaitGroup
		var created atomic.Int64
		start := time.Now()
		for i := range 100 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(time.Duration(i) * 8 * time.Millisecond)
				res, err := (map[bool]*replica{true: web, false: a})[i%2 == 0].enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.coalesce", DedupKey: "index:42"})
				if err != nil {
					t.Error(err)
					return
				}
				if res.Created {
					created.Add(1)
				}
			}()
		}
		wg.Wait()
		t.Logf("100 enqueues in %s created %d rows", time.Since(start).Round(time.Millisecond), created.Load())
		waitFor(t, "the queue to drain", 30*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM job WHERE kind = 'it.coalesce' AND state IN ('queued','running')`) == 0
		})
		if n := coalesced.Load(); n < 1 || n > 2 {
			t.Fatalf("%d executions, want 1 or 2", n)
		}
		if maxCoalesce.Load() != 1 {
			t.Fatalf("%d runs of one key at the same time, want never more than 1", maxCoalesce.Load())
		}
		if n := e.scalar(`SELECT count(*) FROM job WHERE kind = 'it.coalesce' AND state = 'succeeded'`); n != coalesced.Load() {
			t.Fatalf("%d succeeded rows for %d executions", n, coalesced.Load())
		}
	})

	t.Run("R-ARCH-016 a failing attempt returns the job to queued after a backoff and counts the attempt", func(t *testing.T) {
		res, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.flaky"})
		if err != nil {
			t.Fatal(err)
		}
		j := e.waitRetry(web, res.Job.ID, 1)
		delay := e.scalar(`SELECT extract(epoch FROM run_at - now())::bigint FROM job WHERE id = $1::uuid`, res.Job.ID)
		if delay < 1 || delay > 3 { // min(2^1 s, 1 h) +-20 %
			t.Fatalf("backoff = %d s, want about 2", delay)
		}
		if len(j.Error) == 0 {
			t.Error("the failed attempt left no error")
		}
		e.exec(`UPDATE job SET run_at = now() WHERE id = $1::uuid`, res.Job.ID)
		j = e.waitState(web, res.Job.ID, jobs.StateSucceeded, 10*time.Second)
		if j.Attempt != 2 {
			t.Fatalf("attempt = %d, want 2", j.Attempt)
		}
	})

	t.Run("R-ARCH-016 a permanent error, a used-up job, a panic and a timeout end failed", func(t *testing.T) {
		perm, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.permanent"})
		j := e.waitState(web, perm.Job.ID, jobs.StateFailed, 10*time.Second)
		if j.Attempt != 1 {
			t.Errorf("permanent: attempt = %d", j.Attempt)
		}
		ex, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.exhaust"})
		e.waitRetry(web, ex.Job.ID, 1)
		e.exec(`UPDATE job SET run_at = now() WHERE id = $1::uuid`, ex.Job.ID)
		j = e.waitState(web, ex.Job.ID, jobs.StateFailed, 10*time.Second)
		if j.Attempt != 2 || len(j.Error) == 0 {
			t.Errorf("exhausted: attempt %d error %s", j.Attempt, j.Error)
		}
		pn, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.panic"})
		j = e.waitState(web, pn.Job.ID, jobs.StateFailed, 10*time.Second)
		if len(j.Error) == 0 {
			t.Error("a panic left no error")
		}
		to, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.timeout"})
		j = e.waitState(web, to.Job.ID, jobs.StateFailed, 10*time.Second)
		if string(j.Error) == "" || !contains(string(j.Error), "job_timeout") {
			t.Errorf("timeout error = %s", j.Error)
		}
	})

	t.Run("R-ARCH-016 a job whose worker died is reaped and finished by another worker", func(t *testing.T) {
		dead := uuidV4()
		e.exec(`INSERT INTO job (id, kind, state, attempt, max_attempts, locked_by, locked_until, started_at)
		        VALUES ($1::uuid, 'it.quick', 'running', 1, 3, $2::uuid, now() - interval '1 second', now())`, "00000000-0000-7000-8000-00000000d00d", dead)
		j := e.waitState(web, "00000000-0000-7000-8000-00000000d00d", jobs.StateQueued, 10*time.Second)
		if j.Attempt != 1 || j.LockedBy != "" {
			t.Fatalf("reaped job: attempt %d locked_by %q", j.Attempt, j.LockedBy)
		}
		e.exec(`UPDATE job SET run_at = now() WHERE id = $1::uuid`, j.ID)
		j = e.waitState(web, j.ID, jobs.StateSucceeded, 10*time.Second)
		if j.Attempt != 2 {
			t.Fatalf("attempt = %d, want 2 (R-HA-005)", j.Attempt)
		}
		e.exec(`INSERT INTO job (id, kind, state, attempt, max_attempts, locked_by, locked_until, started_at)
		        VALUES ($1::uuid, 'it.quick', 'running', 3, 3, $2::uuid, now() - interval '1 second', now())`, "00000000-0000-7000-8000-00000000d00e", dead)
		j = e.waitState(web, "00000000-0000-7000-8000-00000000d00e", jobs.StateFailed, 10*time.Second)
		if !contains(string(j.Error), "attempts_exhausted") {
			t.Fatalf("error = %s", j.Error)
		}
	})

	t.Run("R-ARCH-016 a heartbeat that finds the job taken cancels the handler", func(t *testing.T) {
		res, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.hang"})
		e.waitState(web, res.Job.ID, jobs.StateRunning, 10*time.Second)
		e.exec(`UPDATE job SET locked_by = $2::uuid WHERE id = $1::uuid`, res.Job.ID, uuidV4())
		select {
		case cause := <-causes:
			if !errors.Is(cause, jobs.ErrLeaseLost) {
				t.Fatalf("cause = %v", cause)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the handler was not cancelled")
		}
		time.Sleep(500 * time.Millisecond)
		if j := e.job(web, res.Job.ID); j.State != jobs.StateRunning {
			t.Fatalf("the worker that lost the job wrote its end: state %s", j.State)
		}
	})

	t.Run("R-ARCH-016 cancel: a running job's handler is cancelled and the job ends cancelled; a queued job at once", func(t *testing.T) {
		res, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.hang"})
		e.waitState(web, res.Job.ID, jobs.StateRunning, 10*time.Second)
		if _, ok, err := web.queue.Cancel(ctx, res.Job.ID); err != nil || !ok {
			t.Fatalf("Cancel: %v %v", ok, err)
		}
		select {
		case cause := <-causes:
			if !errors.Is(cause, jobs.ErrCancelRequested) {
				t.Fatalf("cause = %v", cause)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the handler was not cancelled")
		}
		e.waitState(web, res.Job.ID, jobs.StateCancelled, 5*time.Second)

		later, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.quick", Delay: time.Hour})
		j, ok, err := web.queue.Cancel(ctx, later.Job.ID)
		if err != nil || !ok || j.State != jobs.StateCancelled {
			t.Fatalf("queued Cancel: %+v %v %v", j, ok, err)
		}
	})

	t.Run("Q-OPS-12 a worker claims only while its replica is ready", func(t *testing.T) {
		// Only worker a is gated by ready; b keeps claiming, so stop b for this
		// subtest by letting it drain.
		b.worker.Drain(ctx)
		ready.Store(false)
		time.Sleep(300 * time.Millisecond)
		res, _ := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.quick"})
		time.Sleep(2500 * time.Millisecond) // more than two poll intervals
		if j := e.job(web, res.Job.ID); j.State != jobs.StateQueued {
			t.Fatalf("an unready worker claimed the job: %s", j.State)
		}
		ready.Store(true)
		e.waitState(web, res.Job.ID, jobs.StateSucceeded, 5*time.Second)
	})

	t.Run("R-ARCH-016 a kind no worker knows is never claimed (rolling upgrade)", func(t *testing.T) {
		e.exec(`INSERT INTO job (id, kind) VALUES ('00000000-0000-7000-8000-00000000f00d', 'it.from_the_future')`)
		time.Sleep(2500 * time.Millisecond)
		if j := e.job(web, "00000000-0000-7000-8000-00000000f00d"); j.State != jobs.StateQueued || j.Attempt != 0 {
			t.Fatalf("a kind unknown to the worker was claimed: %+v", j)
		}
	})

	t.Run("R-ARCH-016 an unknown kind cannot be enqueued", func(t *testing.T) {
		if _, err := web.enqueueTx(ctx, jobs.EnqueueSpec{Kind: "it.nonexistent"}); !errors.Is(err, jobs.ErrUnknownKind) {
			t.Fatalf("err = %v", err)
		}
	})
}

func run_(replica string, attempt int) run {
	return run{replica: replica, attempt: attempt, at: time.Now()}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
