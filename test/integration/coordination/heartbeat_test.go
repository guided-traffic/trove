//go:build integration

package coordination

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// bufRows is a pgx.Rows over buffered (id, state) rows of HeartbeatJobs, so a
// test can hold the answer back after the statement ran on the server.
type bufRows struct {
	ids    []pgtype.UUID
	states []string
	i      int
}

func (b *bufRows) Close()                                       {}
func (b *bufRows) Err() error                                   { return nil }
func (b *bufRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (b *bufRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (b *bufRows) Next() bool                                   { b.i++; return b.i <= len(b.ids) }
func (b *bufRows) Scan(dest ...any) error {
	*dest[0].(*pgtype.UUID) = b.ids[b.i-1]
	*dest[1].(*string) = b.states[b.i-1]
	return nil
}
func (b *bufRows) Values() ([]any, error) { return nil, errors.New("unused") }
func (b *bufRows) RawValues() [][]byte    { return nil }
func (b *bufRows) Conn() *pgx.Conn        { return nil }
func (b *bufRows) TypeMap() *pgtype.Map   { return nil }

// slowHeartbeat runs HeartbeatJobs to completion on the server, then runs
// onAfter (once) and holds the result back for hold before returning it.
type slowHeartbeat struct {
	q.DBTX
	armed   atomic.Bool
	hold    time.Duration
	onAfter func()
}

func (s *slowHeartbeat) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if !strings.Contains(sql, "RETURNING j.id, j.state") || !s.armed.Load() {
		return s.DBTX.Query(ctx, sql, args...)
	}
	rows, err := s.DBTX.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	b := &bufRows{}
	for rows.Next() {
		var id pgtype.UUID
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			rows.Close()
			return nil, err
		}
		b.ids, b.states = append(b.ids, id), append(b.states, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if s.armed.CompareAndSwap(true, false) {
		s.onAfter()
		time.Sleep(s.hold)
	}
	return b, nil
}

// heartbeatWorker builds a worker whose heartbeat goes through hb, with a long
// job's handler and a victim kind the test controls.
type hbFixture struct {
	rep   *replica
	w     *jobs.Worker
	hb    *slowHeartbeat
	gateA chan struct{}
	cfg   config.Jobs
}

func newHBFixture(t *testing.T, e *env, name string, victim func(ctx context.Context, run *jobs.Run) error) *hbFixture {
	t.Helper()
	f := &hbFixture{gateA: make(chan struct{}), cfg: defaultJobs()}
	f.rep = e.newReplica(name, replicaOpts{role: "worker", noWorker: true, kinds: func(r *jobs.Registry, rep *replica) {
		r.RegisterKind("rv.long", jobs.KindSpec{Handler: func(ctx context.Context, run *jobs.Run) error {
			select {
			case <-f.gateA:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
		r.RegisterKind("rv.victim", jobs.KindSpec{Handler: victim})
	}})
	f.rep.jobsReg.Freeze()
	f.hb = &slowHeartbeat{DBTX: f.rep.db.ControlPool(), hold: 1500 * time.Millisecond}
	w, err := jobs.NewWorker(jobs.WorkerOptions{
		Role: "worker", Instance: name, UID: f.rep.uid, Main: f.rep.db.Main(), Control: f.hb,
		Registry: f.rep.jobsReg, Config: f.cfg, Signals: f.rep.signals, WatchdogTimeout: 30 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.w = w
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.openGate()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		w.Quiesce(sctx)
		_ = w.Stop(sctx)
	})
	return f
}

func (f *hbFixture) openGate() {
	select {
	case <-f.gateA:
	default:
		close(f.gateA)
	}
}

// TestHeartbeatJudgesOnlyItsRuns covers the heartbeat of R-ARCH-016 and the
// recovery of R-HA-005: the statement extends and judges the jobs this worker
// runs, no others.
func TestHeartbeatJudgesOnlyItsRuns(t *testing.T) {
	t.Run("R-ARCH-016 R-HA-005 a job claimed while a heartbeat statement is in flight is not cancelled as lost", func(t *testing.T) {
		e := newEnv(t)
		var runs atomic.Int64
		cause := make(chan error, 4)
		f := newHBFixture(t, e, "hb-race", func(ctx context.Context, run *jobs.Run) error {
			runs.Add(1)
			select {
			case <-time.After(3 * time.Second):
				return nil
			case <-ctx.Done():
				cause <- context.Cause(ctx)
				return ctx.Err()
			}
		})
		var victimID atomic.Value
		f.hb.onAfter = func() {
			res, err := f.w.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: "rv.victim"})
			if err != nil {
				t.Errorf("enqueue victim: %v", err)
				return
			}
			victimID.Store(res.Job.ID)
		}
		a, err := f.w.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: "rv.long"})
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(f.rep, a.Job.ID, jobs.StateRunning, 5*time.Second)
		f.hb.armed.Store(true) // the next heartbeat statement runs, then its answer is held back while the victim is claimed

		waitFor(t, "the victim being enqueued and claimed", 10*time.Second, func() bool { return victimID.Load() != nil && runs.Load() >= 1 })
		id := victimID.Load().(string)
		j := e.waitState(f.rep, id, jobs.StateSucceeded, 15*time.Second)
		select {
		case c := <-cause:
			t.Fatalf("the victim's handler was cancelled by a heartbeat that did not cover it (cause %v)", c)
		default:
		}
		if runs.Load() != 1 || j.Attempt != 1 {
			t.Fatalf("victim ran %d time(s), attempt %d, want one run of attempt 1", runs.Load(), j.Attempt)
		}
		f.openGate()
		e.waitState(f.rep, a.Job.ID, jobs.StateSucceeded, 5*time.Second)
	})

	t.Run("R-ARCH-016 R-HA-005 a row this worker holds without running it expires and is reaped, not kept alive", func(t *testing.T) {
		e := newEnv(t)
		var runs atomic.Int64
		f := newHBFixture(t, e, "hb-abandon", func(ctx context.Context, run *jobs.Run) error {
			runs.Add(1)
			return nil
		})
		a, err := f.w.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: "rv.long"})
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(f.rep, a.Job.ID, jobs.StateRunning, 5*time.Second)

		// A claim whose answer never reached the worker: the row is running and
		// locked by this worker, but no run exists for it.
		res, err := f.w.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: "rv.victim", Delay: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE job SET state = 'running', attempt = 1, locked_by = $2::uuid, locked_until = now() + interval '6 seconds', started_at = now() WHERE id = $1::uuid`,
			res.Job.ID, f.rep.uid)

		// While the long job keeps the heartbeat busy, the abandoned row must run out
		// its lease and go back to the queue (the reaper), then run.
		j := e.waitState(f.rep, res.Job.ID, jobs.StateSucceeded, f.cfg.LeaseDuration+20*time.Second)
		if runs.Load() != 1 {
			t.Fatalf("the abandoned job ran %d times, want once after the reaper returned it", runs.Load())
		}
		if j.Attempt != 2 {
			t.Fatalf("attempt = %d, want 2: the abandoned attempt counted, the rerun is the second", j.Attempt)
		}
		if long := e.job(f.rep, a.Job.ID); long.State != jobs.StateRunning {
			t.Fatalf("the long job is %s; the abandoned row must have been reaped while it still ran", long.State)
		}
		f.openGate()
		e.waitState(f.rep, a.Job.ID, jobs.StateSucceeded, 5*time.Second)
	})
}

// TestOwnershipNamesTheAttempt: every write of a run names the attempt it
// claimed, so a run whose job has moved on to a newer attempt (the same worker
// holding the row again, or a stale run) writes nothing.
func TestOwnershipNamesTheAttempt(t *testing.T) {
	t.Run("R-ARCH-016 a run of an older attempt neither checkpoints nor completes the job", func(t *testing.T) {
		e := newEnv(t)
		bump := make(chan struct{})
		type outcome struct {
			ckpt  error
			cause error
		}
		out := make(chan outcome, 1)
		f := newHBFixture(t, e, "hb-attempt", func(ctx context.Context, run *jobs.Run) error {
			<-bump
			ck := run.SaveCheckpoint(ctx, map[string]int{"step": 1})
			<-ctx.Done()
			out <- outcome{ckpt: ck, cause: context.Cause(ctx)}
			return ctx.Err()
		})
		res, err := f.w.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: "rv.victim"})
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(f.rep, res.Job.ID, jobs.StateRunning, 5*time.Second)
		// The row moves to attempt 2 under the same owner, as after a release and a new claim.
		e.exec(`UPDATE job SET attempt = 2 WHERE id = $1::uuid`, res.Job.ID)
		close(bump)

		select {
		case o := <-out:
			if !errors.Is(o.ckpt, jobs.ErrLeaseLost) {
				t.Errorf("SaveCheckpoint of the older attempt = %v, want ErrLeaseLost", o.ckpt)
			}
			if !errors.Is(o.cause, jobs.ErrLeaseLost) {
				t.Errorf("the older run was stopped with cause %v, want ErrLeaseLost", o.cause)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the run of the older attempt was not stopped")
		}
		time.Sleep(500 * time.Millisecond)
		j := e.job(f.rep, res.Job.ID)
		if j.State != jobs.StateRunning || j.Attempt != 2 || len(j.Checkpoint) > 0 && string(j.Checkpoint) != "null" {
			t.Fatalf("the older run changed the row: state %s attempt %d checkpoint %s", j.State, j.Attempt, j.Checkpoint)
		}
	})
}
