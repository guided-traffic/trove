package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror"
)

// The job kinds of the mirror mode (repositories-and-proxy.md R-REPO-053, docs/developer/mirror.md). Every process
// registers them, because a process that serves clients enqueues a pass on request (R-REPO-056); whatever process claims
// the queue mirror runs them.
//
// Contract wave: the kinds are registered with a handler that does nothing and says so in the job's result, and no task
// enqueues them. The engine's work package (builder C) replaces the handler and registers the dispatch task.
func (a *App) registerMirrorJobs(r *jobs.Registry) {
	notBuilt := func(_ context.Context, run *jobs.Run) error {
		return run.SetResult(map[string]string{"skipped": "the mirror engine is not built in this release yet"})
	}
	r.RegisterKind(mirror.KindPass, jobs.KindSpec{Queue: mirror.QueueMirror, DedupMode: jobs.Exclusive, MaxAttempts: 1, Handler: notBuilt})
	r.RegisterKind(mirror.KindDispatch, jobs.KindSpec{Queue: mirror.QueueMirror, DedupMode: jobs.Exclusive, MaxAttempts: 1, Handler: notBuilt})
}
