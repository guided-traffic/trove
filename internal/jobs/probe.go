package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// KindSystemProbe is a diagnostic job: it counts steps, persists its cursor
// after every step and records which workers carried it. It exists so the
// end-to-end drain test (E0.14) can prove that a job released at shutdown
// resumes on another worker from its cursor with its attempt count unchanged,
// before any job kind that does real work ships. It touches no table but its own
// job row, and nothing enqueues it except a caller with write access to the job
// table.
const KindSystemProbe = "system.probe"

// ProbeArgs are the args of a [KindSystemProbe] job.
type ProbeArgs struct {
	// Steps is the number of steps to run; default 30.
	Steps int `json:"steps"`
	// IntervalMS is the time of one step in milliseconds; default 1000.
	IntervalMS int `json:"interval_ms"`
}

// ProbeCheckpoint is the cursor of a [KindSystemProbe] job. Hops lists the
// lease holders (worker UIDs) that carried the job, in order.
type ProbeCheckpoint struct {
	Step int      `json:"step"`
	Hops []string `json:"hops"`
	// Resumes holds, per hop after the first, the step the hop resumed from.
	Resumes []int `json:"resumes,omitempty"`
}

// RegisterProbeKind registers [KindSystemProbe] in the default queue with three
// attempts. The composition root calls it on every role, like the other kinds.
func RegisterProbeKind(r *Registry) {
	r.RegisterKind(KindSystemProbe, KindSpec{MaxAttempts: 3, Handler: probeHandler})
}

func probeHandler(ctx context.Context, run *Run) error {
	args := ProbeArgs{Steps: 30, IntervalMS: 1000}
	if len(run.Args) > 0 {
		if err := json.Unmarshal(run.Args, &args); err != nil {
			return Permanent(fmt.Errorf("system.probe: args: %w", err))
		}
	}
	if args.Steps < 1 || args.IntervalMS < 1 {
		return Permanent(fmt.Errorf("system.probe: steps and interval_ms must be positive"))
	}
	var cp ProbeCheckpoint
	if _, err := run.LoadCheckpoint(&cp); err != nil {
		return err
	}
	if n := len(cp.Hops); n == 0 || cp.Hops[n-1] != run.LockedBy {
		if len(cp.Hops) > 0 {
			cp.Resumes = append(cp.Resumes, cp.Step)
		}
		cp.Hops = append(cp.Hops, run.LockedBy)
	}
	tick := time.NewTicker(time.Duration(args.IntervalMS) * time.Millisecond)
	defer tick.Stop()
	for cp.Step < args.Steps {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
		cp.Step++
		if err := run.SaveCheckpoint(ctx, cp); err != nil {
			return err
		}
		if err := run.SaveProgress(ctx, int64(cp.Step), int64(args.Steps), "steps"); err != nil {
			return err
		}
	}
	return run.SetResult(map[string]any{"steps": cp.Step, "hops": cp.Hops, "resumes": cp.Resumes})
}
