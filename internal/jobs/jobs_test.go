package jobs

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func noop(context.Context, *Run) error { return nil }

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}

func TestNewWorkerRoles(t *testing.T) {
	for _, role := range []string{"web", "backup", "", "other"} {
		t.Run("R-ARCH-001 a "+role+" role cannot construct a claimer", func(t *testing.T) {
			_, err := NewWorker(WorkerOptions{Role: role, UID: "u", Main: nil, Control: nil})
			if !errors.Is(err, ErrNotWorker) {
				t.Fatalf("NewWorker(%q) = %v, want ErrNotWorker", role, err)
			}
		})
	}
	t.Run("R-ARCH-001 worker and all replicas may", func(t *testing.T) {
		// A nil pool fails the second check, which proves the role check passed.
		for _, role := range []string{"worker", "all"} {
			_, err := NewWorker(WorkerOptions{Role: role, UID: "u"})
			if err == nil || errors.Is(err, ErrNotWorker) {
				t.Fatalf("NewWorker(%q) = %v, want the missing-pool error", role, err)
			}
		}
	})
}

func TestRegistry(t *testing.T) {
	t.Run("R-ARCH-016 a kind is registered once with defaults", func(t *testing.T) {
		r := NewRegistry()
		r.RegisterKind("demo", KindSpec{Handler: noop})
		s, ok := r.Kind("demo")
		if !ok || s.Queue != "default" || s.MaxAttempts != 10 || s.Priority != 100 {
			t.Fatalf("Kind = %+v, %v", s, ok)
		}
		mustPanic(t, "a duplicate kind", func() { r.RegisterKind("demo", KindSpec{Handler: noop}) })
		mustPanic(t, "a kind without a handler", func() { r.RegisterKind("nohandler", KindSpec{}) })
		mustPanic(t, "an invalid name", func() { r.RegisterKind("Bad Name", KindSpec{Handler: noop}) })
		mustPanic(t, "an unknown dedup mode", func() { r.RegisterKind("mode", KindSpec{Handler: noop, DedupMode: "sometimes"}) })
	})
	t.Run("R-ARCH-016 the registry is frozen at start-up", func(t *testing.T) {
		r := NewRegistry()
		r.RegisterKind("before", KindSpec{Handler: noop})
		r.Freeze()
		mustPanic(t, "a kind after Freeze", func() { r.RegisterKind("after", KindSpec{Handler: noop}) })
		mustPanic(t, "a task after Freeze", func() { r.RegisterTask("after", TaskSpec{Schedule: "* * * * *"}) })
		if got := r.Kinds(); len(got) != 1 || got[0] != "before" {
			t.Fatalf("Kinds = %v", got)
		}
	})
	t.Run("R-ARCH-016 a task needs a valid default schedule and carries its kind's name", func(t *testing.T) {
		r := NewRegistry()
		r.RegisterTask("sweep", TaskSpec{Schedule: "0 5 * * 6"})
		if r.Tasks()["sweep"].Kind != "sweep" {
			t.Fatalf("task kind = %q", r.Tasks()["sweep"].Kind)
		}
		mustPanic(t, "a task with a quartz schedule", func() { r.RegisterTask("quartz", TaskSpec{Schedule: "0 30 3 ? * SUN"}) })
	})
}

func TestEnqueueParams(t *testing.T) {
	r := NewRegistry()
	r.RegisterKind("plain", KindSpec{Handler: noop})
	r.RegisterKind("keyed", KindSpec{Handler: noop, DedupMode: Exclusive, Queue: "maintenance", MaxAttempts: 1})
	qu := NewQueue(QueueOptions{Registry: r})

	t.Run("R-ARCH-016 an unknown kind is refused", func(t *testing.T) {
		if _, _, err := qu.params(EnqueueSpec{Kind: "nope"}); !errors.Is(err, ErrUnknownKind) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-016 an exclusive kind keys on its name unless told otherwise", func(t *testing.T) {
		p, _, err := qu.params(EnqueueSpec{Kind: "keyed"})
		if err != nil || p.DedupKey != "keyed" || p.DedupMode != "exclusive" || p.Queue != "maintenance" || p.MaxAttempts != 1 {
			t.Fatalf("params = %+v, %v", p, err)
		}
		p, _, _ = qu.params(EnqueueSpec{Kind: "keyed", DedupKey: "index:42"})
		if p.DedupKey != "index:42" {
			t.Fatalf("key = %q", p.DedupKey)
		}
	})
	t.Run("R-ARCH-016 a key on a kind without a dedup mode is refused", func(t *testing.T) {
		if _, _, err := qu.params(EnqueueSpec{Kind: "plain", DedupKey: "x"}); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("R-ARCH-016 job IDs are UUIDv7 and sort by creation", func(t *testing.T) {
		re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		prev := ""
		for range 50 {
			id := newID()
			if !re.MatchString(id) {
				t.Fatalf("%q is not a UUIDv7", id)
			}
			if id[:13] < prev {
				t.Fatalf("%q sorts before %q", id, prev)
			}
			prev = id[:13]
		}
	})
}

func TestValidateSchedules(t *testing.T) {
	t.Run("Q-STOR-4 a bad configured schedule names its key", func(t *testing.T) {
		err := ValidateSchedules(config.Maintenance{Tasks: map[string]config.MaintenanceTask{
			"gc":      {Enabled: true, Schedule: "*/30 * * * *"},
			"scrub":   {Enabled: true, Schedule: "0 30 4 ? * *"},
			"sweep":   {Enabled: false, Schedule: "garbage"},
			"cleanup": {Enabled: true, Schedule: "0 3 * * 7"},
		}})
		if err == nil {
			t.Fatal("accepted")
		}
		for _, want := range []string{"maintenance.tasks.scrub.schedule", "maintenance.tasks.cleanup.schedule"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not name %s: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), "maintenance.tasks.sweep") {
			t.Errorf("a disabled task is checked: %v", err)
		}
	})
}

func TestPermanent(t *testing.T) {
	base := errors.New("boom")
	if IsPermanent(base) || !IsPermanent(Permanent(base)) || !errors.Is(Permanent(base), base) || Permanent(nil) != nil {
		t.Fatal("Permanent / IsPermanent")
	}
}
