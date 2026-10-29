package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// TaskSchedule is one schedule that a [ScheduleSource] reports.
type TaskSchedule struct {
	// Name is the task's name; with the slot it is the job's (task,
	// scheduled_for) key.
	Name string
	// Kind is the job kind to enqueue.
	Kind string
	// Schedule is a five-field cron expression (UTC).
	Schedule string
	Args     any
	// DedupKey defaults to Name.
	DedupKey string
}

// ScheduleSource reports schedules that other pages keep in PostgreSQL, such as
// cleanup policies (R-ARCH-016). The scheduler reads it every tick.
type ScheduleSource interface {
	Schedules(ctx context.Context) ([]TaskSchedule, error)
}

type scheduledTask struct {
	name, kind, dedupKey string
	sched                *Schedule
	args                 []byte
}

// resolveTasks merges the registered tasks with maintenance.tasks.<name> of the
// configuration and parses every schedule with the one parser. A bad schedule is
// a configuration error naming the key.
func (w *Worker) resolveTasks() ([]scheduledTask, error) {
	if err := ValidateSchedules(w.o.Maintenance); err != nil {
		return nil, err
	}
	reg := w.o.Registry.Tasks()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []scheduledTask
	for _, name := range names {
		spec := reg[name]
		enabled, expr := !spec.Disabled, spec.Schedule
		if ct, ok := w.o.Maintenance.Tasks[name]; ok {
			enabled = ct.Enabled
			if ct.Schedule != "" {
				expr = ct.Schedule
			}
		}
		if !enabled {
			continue
		}
		if _, ok := w.o.Registry.Kind(spec.Kind); !ok {
			return nil, fmt.Errorf("jobs: task %q enqueues kind %q, which is not registered", name, spec.Kind)
		}
		sched, err := ParseSchedule(expr)
		if err != nil {
			return nil, fmt.Errorf("maintenance.tasks.%s.schedule: %w", name, err)
		}
		args := []byte("{}")
		if spec.Args != nil {
			if args, err = json.Marshal(spec.Args); err != nil {
				return nil, fmt.Errorf("jobs: args of task %q: %w", name, err)
			}
		}
		key := spec.DedupKey
		if key == "" {
			key = name
		}
		out = append(out, scheduledTask{name: name, kind: spec.Kind, dedupKey: key, sched: sched, args: args})
	}
	return out, nil
}

// ValidateSchedules checks every enabled schedule of maintenance.tasks with the
// one parser, so the configuration loader of every role can reject a bad one at
// start-up naming its key (Q-STOR-4).
func ValidateSchedules(m config.Maintenance) error {
	names := make([]string, 0, len(m.Tasks))
	for n := range m.Tasks {
		names = append(names, n)
	}
	sort.Strings(names)
	var errs []error
	for _, n := range names {
		t := m.Tasks[n]
		if !t.Enabled {
			continue
		}
		if _, err := ParseSchedule(t.Schedule); err != nil {
			errs = append(errs, fmt.Errorf("maintenance.tasks.%s.schedule: %w", n, err))
		}
	}
	return errors.Join(errs...)
}

// schedulerWork is the scheduler, a singleton under the `scheduler` lease
// (R-ARCH-016, R-HA-005). Every jobs.schedule_tick it inserts the latest due
// slot of each task as (task, scheduled_for) with ON CONFLICT DO NOTHING. Missed
// slots are not caught up, and a slot is never enqueued twice, also not across a
// lease hand-over: job_task_slot refuses it. The inserts run on the lease
// connection, so a replica that lost its session inserts nothing (R-HA-006).
func (w *Worker) schedulerWork(ctx context.Context, h *cluster.Held) {
	loop := cluster.WatchLoop(w.o.Watchdog, "jobs.scheduler", w.o.WatchdogTimeout)
	defer loop.Stop()
	t := time.NewTicker(w.o.Config.ScheduleTick)
	defer t.Stop()
	for {
		if w.draining.Load() || ctx.Err() != nil {
			return
		}
		err := w.Tick(ctx, h)
		loop.Beat()
		if errors.Is(err, cluster.ErrNotHeld) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one scheduler pass on the held lease.
func (w *Worker) Tick(ctx context.Context, h *cluster.Held) error {
	ctx, cancel := context.WithTimeout(ctx, w.callTimeout())
	defer cancel()
	qs := q.New(h.DB())
	now, err := qs.DatabaseNow(ctx)
	if err != nil {
		return w.tickErr("reading the database clock", err)
	}
	tasks := append([]scheduledTask(nil), w.tasks...)
	for _, src := range w.o.Sources {
		more, err := src.Schedules(ctx)
		if err != nil {
			w.log.Warn("schedule source failed", "error", err)
			continue
		}
		for _, ts := range more {
			sched, err := ParseSchedule(ts.Schedule)
			if err != nil {
				w.log.Warn("schedule source: invalid schedule skipped", "task", ts.Name, "error", err)
				continue
			}
			args := []byte("{}")
			if ts.Args != nil {
				if args, err = json.Marshal(ts.Args); err != nil {
					continue
				}
			}
			key := ts.DedupKey
			if key == "" {
				key = ts.Name
			}
			tasks = append(tasks, scheduledTask{name: ts.Name, kind: ts.Kind, dedupKey: key, sched: sched, args: args})
		}
	}
	var first error
	woke := map[string]bool{}
	for _, task := range tasks {
		slot, ok := task.sched.Prev(now.Time)
		if !ok {
			continue
		}
		spec, ok := w.o.Registry.Kind(task.kind)
		if !ok {
			continue
		}
		rows, err := qs.InsertJob(ctx, q.InsertJobParams{
			ID: newID(), Kind: task.kind, Queue: spec.Queue, Args: string(task.args), Priority: int16(spec.Priority), //nolint:gosec // small
			MaxAttempts: int32(spec.MaxAttempts), DedupKey: task.dedupKey, DedupMode: string(Exclusive), //nolint:gosec // small
			Task: task.name, ScheduledUnix: slot.Unix(),
		})
		if err != nil {
			if err = w.tickErr("scheduling "+task.name, err); first == nil {
				first = err
			}
			if errors.Is(err, cluster.ErrNotHeld) {
				return err
			}
			continue
		}
		if len(rows) == 1 {
			w.log.Info("task scheduled", "task", task.name, "slot", slot.Format(time.RFC3339), "job", rows[0].ID.String())
			woke[spec.Queue] = true
		}
	}
	if w.o.Signals != nil {
		for queue := range woke {
			w.o.Signals.Publish(cluster.AreaJobs, queue)
		}
	}
	return first
}

func (w *Worker) tickErr(what string, err error) error {
	if errors.Is(err, cluster.ErrNotHeld) || w.root.Err() != nil {
		return err
	}
	w.log.Warn("scheduler tick failed", "step", what, "error", err)
	return err
}
