package deb

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/jobs"
)

// R-DEB-010: the periodic task runs the remote part of its own registry only. A process that builds several replicas one after the other
// (the integration tests, each with a database of its own) must not run the part of a replica that has stopped, whose pools are closed.
func TestR_DEB_010_TheRefreshTaskRunsTheRemotePartOfItsOwnRegistryOnly(t *testing.T) {
	var stale, own int
	stopped := errors.New("closed pool")
	first := jobs.NewRegistry()
	RegisterJobs(first, RemoteHandlers{Refresh: func(context.Context, *jobs.Run) error { stale++; return stopped }})
	second := jobs.NewRegistry()
	RegisterJobs(second, RemoteHandlers{Refresh: func(context.Context, *jobs.Run) error { own++; return nil }})

	spec, ok := second.Kind(RefreshTaskName)
	if !ok {
		t.Fatal("the task is not registered")
	}
	if err := spec.Handler(context.Background(), &jobs.Run{}); err != nil {
		t.Fatalf("the task = %v, want nil", err)
	}
	if own != 1 || stale != 0 {
		t.Errorf("remote parts run: own %d, stale %d, want 1 and 0", own, stale)
	}
}
