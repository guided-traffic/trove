package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The task operations of R-OPS-019 and the trigger of R-OPS-020. The trigger
// runs on web replicas and only enqueues; a worker replica runs the job. A task
// is a named maintenance job, recurring or manual-only, registered in the job
// registry; the schedules are configuration and the API cannot edit them.

// resolveTasks returns the tasks of the registry with the configuration applied.
func (s *Server) resolveTasks() ([]jobs.TaskInfo, error) {
	if s.o.API.Jobs == nil {
		return nil, unavailable("The task registry")
	}
	return s.o.API.Jobs.ResolveTasks(s.o.API.Maintenance)
}

// ListTasks implements GET /api/v1/system/tasks (R-OPS-019).
func (s *Server) ListTasks(w http.ResponseWriter, r *http.Request, _ gen.ListTasksParams) {
	ctx := r.Context()
	lq, err := ParseList(r, ListSpec{}, s.o.API.Config)
	if err == nil && lq.After != nil && len(lq.After) != 1 {
		err = invalidCursor()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	all, err := s.resolveTasks()
	if err != nil || s.o.API.Store == nil {
		if err == nil {
			err = unavailable("The task list")
		}
		httpserver.Problem(w, r, err)
		return
	}
	// The registry is fixed at start-up and identical on every replica of a
	// release, so a page is the tasks after the cursor's name, by name.
	var page []jobs.TaskInfo
	for _, t := range all {
		if lq.After != nil && t.Name <= lq.After[0] {
			continue
		}
		page = append(page, t)
		if len(page) == lq.FetchLimit() {
			break
		}
	}
	items, err := s.taskViews(ctx, page)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WritePage(w, r, NewPage(lq, items, func(t gen.Task) []string { return []string{t.Name} }))
}

// taskViews adds the live state of each task: whether a run is going on, the
// last run, and the next scheduled slot on the database's clock.
func (s *Server) taskViews(ctx context.Context, infos []jobs.TaskInfo) ([]gen.Task, error) {
	kinds := make([]string, 0, len(infos))
	for _, t := range infos {
		kinds = append(kinds, t.Kind)
	}
	last := map[string]q.LastJobPerKindRow{}
	running := map[string]bool{}
	var now time.Time
	err := s.o.API.Store.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.LastJobPerKind(ctx, kinds)
		if err != nil {
			return err
		}
		for _, row := range rows {
			last[row.Kind] = row
		}
		act, err := qs.KindsRunningNow(ctx, kinds)
		if err != nil {
			return err
		}
		for _, k := range act {
			running[k] = true
		}
		t, err := qs.DatabaseNow(ctx)
		now = t.Time
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]gen.Task, 0, len(infos))
	for _, t := range infos {
		v := gen.Task{Name: t.Name, Enabled: t.Enabled, Running: running[t.Kind]}
		if t.Schedule != "" {
			sc := t.Schedule
			v.Schedule = &sc
		}
		if t.Enabled && t.Sched != nil {
			if next, ok := t.Sched.Next(now); ok {
				n := next.UTC()
				v.NextRunAt = &n
			}
		}
		if row, ok := last[t.Kind]; ok {
			lr := gen.TaskLastRun{JobId: parseUUID(row.ID.String()), Outcome: gen.JobState(row.State)}
			if row.StartedAt.Valid {
				st := row.StartedAt.Time.UTC()
				lr.StartedAt = &st
			}
			if row.FinishedAt.Valid {
				fin := row.FinishedAt.Time.UTC()
				lr.FinishedAt = &fin
			}
			switch {
			case row.ReplicaName.Valid:
				lr.Replica = &row.ReplicaName.String
			case row.RanBy.Valid:
				u := row.RanBy.String()
				lr.Replica = &u
			}
			v.LastRun = &lr
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RunTask implements POST /api/v1/system/tasks/{name}/run (R-OPS-019,
// R-OPS-020). The caller holds settings:write (declared). The run is idempotent
// under an Idempotency-Key.
func (s *Server) RunTask(w http.ResponseWriter, r *http.Request, name string, _ gen.RunTaskParams) {
	if s.idem == nil || s.o.API.Queue == nil {
		httpserver.Problem(w, r, unavailable("The task trigger"))
		return
	}
	tasks, err := s.resolveTasks()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var task *jobs.TaskInfo
	for i := range tasks {
		if tasks[i].Name == name {
			task = &tasks[i]
		}
	}
	if task == nil {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No task has this name."))
		return
	}
	body, err := readOptionalBody(w, r, int64(s.o.API.Config.MaxRequestBodyBytes))
	var args json.RawMessage
	if err == nil {
		args, err = decodeTaskParams(body, r, s.o.API.TaskArgs[name])
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	err = s.idem.Do(w, r, body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		return s.enqueueTask(ctx, r, tx, *task, args)
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// enqueueTask checks the task's precondition and enqueues the run in tx, with
// the audit event of the trigger. A run that exists already (the exclusive key
// is held) is 409 with that job.
func (s *Server) enqueueTask(ctx context.Context, r *http.Request, tx meta.Tx, t jobs.TaskInfo, args json.RawMessage) (Outcome, error) {
	if t.Name == KindSecretsReencrypt {
		// R-OPS-019: while a live replica's ring lacks a key that is active elsewhere
		// or referenced, re-sealing would seal values that replica cannot open.
		if err := s.keyRingPrecondition(ctx); err != nil {
			return Outcome{}, err
		}
	}
	if pre := s.o.API.TaskPreconditions[t.Name]; pre != nil {
		if err := pre(ctx); err != nil {
			return Outcome{}, err
		}
	}
	spec := jobs.EnqueueSpec{Kind: t.Kind, TargetKind: "task", TargetKey: t.Name, CreatedBy: callerID(ctx)}
	if args != nil {
		spec.Args = args
	} else if string(t.Args) != "{}" {
		spec.Args = json.RawMessage(t.Args)
	}
	if ks, ok := s.o.API.Jobs.Kind(t.Kind); ok && ks.DedupMode != jobs.NoDedup {
		spec.DedupKey = t.DedupKey
	}
	res, err := s.o.API.Queue.Enqueue(ctx, tx, spec)
	if err != nil {
		return Outcome{}, err
	}
	ref := jobReference(res.Job.ID, res.Job.Kind, res.Job.State)
	if !res.Created {
		return Outcome{}, errs.E(errs.Conflict, errs.CodeConflict,
			"A run of this task, or of a task that shares its exclusive key, is queued or running.",
			errs.WithExtension("job", ref))
	}
	details, _ := json.Marshal(map[string]any{"task": t.Name, "jobId": res.Job.ID, "args": json.RawMessage(orEmptyObject(args))})
	if err := s.recorder().RecordTx(ctx, tx, audit.Event{
		Type: "system.task_triggered", Actor: auditActor(ctx), Outcome: audit.Success, Source: auditSource(r),
		Subject: audit.Subject{Kind: "task", ID: t.Name, Name: t.Name}, Details: details,
	}); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Status: http.StatusAccepted,
		Header: map[string]string{"Location": jobLocation(r, res.Job.ID)},
		Body:   gen.JobReferenceEnvelope{Job: ref},
	}, nil
}

func orEmptyObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}
