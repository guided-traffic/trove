package app

import (
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror"
)

// R-REPO-053 (Q-ARCH-321): the dispatch runs on the maintenance queue, so that passes that hold the slots of the queue mirror for
// hours cannot starve it; the passes stay on the queue mirror; both are exclusive with one attempt, and the dispatch is a task that
// runs every minute.
func TestR_REPO_053_TheDispatchRunsOnTheMaintenanceQueue(t *testing.T) {
	a := &App{cfg: &config.Config{}}
	r := jobs.NewRegistry()
	a.registerMirrorJobs(r)
	dispatch, ok := r.Kind(mirror.KindDispatch)
	if !ok || dispatch.Queue != "maintenance" || dispatch.DedupMode != jobs.Exclusive || dispatch.MaxAttempts != 1 {
		t.Errorf("dispatch: %+v", dispatch)
	}
	pass, ok := r.Kind(mirror.KindPass)
	if !ok || pass.Queue != mirror.QueueMirror || pass.DedupMode != jobs.Exclusive || pass.MaxAttempts != 1 || pass.Timeout != 0 {
		t.Errorf("pass: %+v", pass)
	}
	task, ok := r.Tasks()[mirror.KindDispatch]
	if !ok || task.Schedule != "* * * * *" {
		t.Errorf("task: %+v", task)
	}
}
