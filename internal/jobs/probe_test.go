package jobs

import (
	"context"
	"testing"
)

func TestProbeKind(t *testing.T) {
	t.Run("R-ARCH-016 system.probe refuses args it cannot run as a permanent failure", func(t *testing.T) {
		for _, args := range []string{`{"steps":0}`, `{"interval_ms":-1}`, `not json`} {
			run := &Run{Job: Job{Args: []byte(args)}}
			if err := probeHandler(context.Background(), run); !IsPermanent(err) {
				t.Errorf("args %s: got %v, want a permanent error", args, err)
			}
		}
	})
	t.Run("R-ARCH-016 system.probe is registered with three attempts on the default queue", func(t *testing.T) {
		r := NewRegistry()
		RegisterProbeKind(r)
		spec, ok := r.Kind(KindSystemProbe)
		if !ok || spec.MaxAttempts != 3 || spec.Queue != "" && spec.Queue != "default" {
			t.Fatalf("got %+v, %v", spec, ok)
		}
	})
}
