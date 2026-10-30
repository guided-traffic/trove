package invalidation

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
)

type noopNotifier struct{}

func (noopNotifier) Notify(meta.Tx, string, int, any) error { return nil }

func TestFoldRegistration(t *testing.T) {
	t.Run("R-OPS-019 the fold is an exclusive maintenance task with a valid default schedule", func(t *testing.T) {
		r := jobs.NewRegistry()
		r.RegisterKind(FoldKind, JobSpec(func(context.Context, *jobs.Run) error { return nil }))
		r.RegisterTask(FoldKind, TaskSpec(DefaultFoldSchedule))
		spec, ok := r.Kind(FoldKind)
		if !ok || spec.Queue != "maintenance" || spec.DedupMode != jobs.Exclusive {
			t.Fatalf("spec = %+v, %v", spec, ok)
		}
		if _, err := jobs.ParseSchedule(DefaultFoldSchedule); err != nil {
			t.Errorf("the default schedule does not parse: %v", err)
		}
		if _, ok := r.Tasks()[FoldKind]; !ok {
			t.Error("the task is not registered under the kind's name")
		}
	})
	t.Run("R-REPO-045 the retention of a negative row is the largest notFoundTtlSeconds plus the allowance", func(t *testing.T) {
		if NegativeRetention != 86405*time.Second {
			t.Errorf("NegativeRetention = %s", NegativeRetention)
		}
	})
}

func TestConstructors(t *testing.T) {
	if _, err := NewSet(SetOptions{}); err == nil {
		t.Error("a set without a runner and a change log")
	}
	if _, err := NewService(ServiceOptions{}); err == nil {
		t.Error("a service without a runner and a notifier")
	}
	if _, err := NewFolder(FolderOptions{}); err == nil {
		t.Error("a folder without a runner and a notifier")
	}
	f, err := NewFolder(FolderOptions{Runner: fakeRunner{}, Notifier: noopNotifier{}})
	if err != nil || f.o.BatchSize != defaultFoldBatch || f.o.MetadataGrace != DefaultMetadataGrace || f.o.Clock == nil {
		t.Errorf("defaults = %+v, %v", f, err)
	}
}
